package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/maps"
	"github.com/mylxsw/go-utils/must"
	"github.com/mylxsw/go-utils/ternary"
	"github.com/mylxsw/openai-dispatcher/internal/config"
	"github.com/mylxsw/openai-dispatcher/internal/moderation"
	"github.com/mylxsw/openai-dispatcher/internal/provider"
	"github.com/mylxsw/openai-dispatcher/internal/provider/base"
	"github.com/mylxsw/openai-dispatcher/internal/upstream"
	"github.com/mylxsw/openai-dispatcher/pkg/expr"
	"github.com/sashabaranov/go-openai"
	"github.com/tidwall/gjson"
	"golang.org/x/net/proxy"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrRequestFlagged = errors.New("the request contains illegal content, we cannot service you")
	ErrModelRequired  = errors.New("model is required")
	ErrNotSupport     = errors.New("not support")
)

type Server struct {
	conf             *config.Config
	upstreams        map[string]*upstream.Upstreams
	defaultUpstreams *upstream.Upstreams
	exprRules        []config.Rule

	supportModels []openai.Model

	dialer proxy.Dialer
	once   sync.Once

	moderation *moderation.Client
}

func NewServer(conf *config.Config) (*Server, error) {
	var dialer proxy.Dialer
	var err error

	if conf.Socks5 != "" {
		dialer, err = createSocks5Dialer(conf.Socks5)
		if err != nil {
			log.Errorf("create socks5 dialer failed: %v", err)
		}
	}

	result, err := upstream.BuildUpstreamsFromRules(upstream.Policy(conf.Policy), conf.Rules, dialer)
	if err != nil {
		return nil, err
	}

	for model, ups := range result.Upstreams {
		fmt.Println(model)
		ups.Print()
	}

	fmt.Println("-------- defaults ---------")

	result.Default.Print()

	// Support models
	models := make([]openai.Model, 0)
	for _, model := range array.Uniq(append(maps.Keys(result.Upstreams), conf.ExtraModels...)) {
		models = append(models, openai.Model{
			ID:        model,
			Object:    "model",
			CreatedAt: time.Now().Unix(),
			OwnedBy:   "system",
		})
	}

	server := Server{
		conf:             conf,
		upstreams:        result.Upstreams,
		defaultUpstreams: result.Default,
		exprRules:        result.ExprRules,
		supportModels:    models,
	}

	if conf.Moderation.Enabled {
		server.moderation = moderation.New(
			conf.Moderation.API.Server,
			conf.Moderation.API.Key,
			ternary.If(conf.Moderation.API.Proxy, dialer, nil),
		)
	}

	return &server, nil
}

// readRequestBody Read the Body of the request
func (s *Server) readRequestBody(r *http.Request) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}

	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewBuffer(body))

	return body, nil
}

func (s *Server) replaceRequestBody(r *http.Request, newBody []byte) {
	_ = r.Body.Close()
	r.Body = io.NopCloser(bytes.NewBuffer(newBody))
	r.ContentLength = int64(len(newBody))
}

// Request Proxy request object
type Request struct {
	Method      string              `json:"method,omitempty"`
	URL         string              `json:"url,omitempty"`
	Path        string              `json:"path,omitempty"`
	QueryParams map[string][]string `json:"query_params,omitempty"`
	Body        []byte              `json:"body,omitempty"`
}

// buildRequest Build the proxy request object
func (s *Server) buildRequest(r *http.Request) (*Request, error) {
	body, err := s.readRequestBody(r)
	if err != nil {
		return nil, err
	}
	return &Request{
		Method:      r.Method,
		URL:         r.URL.String(),
		Path:        r.URL.Path,
		QueryParams: r.URL.Query(),
		Body:        body,
	}, nil
}

func (s *Server) selectUpstreams(model string) *upstream.Upstreams {
	if ups, ok := s.upstreams[model]; ok {
		return ups
	}

	var ups *upstream.Upstreams
	for _, rule := range s.exprRules {
		if rule.Expr == nil || rule.Expr.Match == "" {
			continue
		}

		matched, err := must.Must(expr.NewBoolVM(rule.Expr.Match)).Run(expr.Data{Model: model})
		if err != nil {
			log.F(log.M{"model": model, "expr": rule.Expr.Match}).Errorf("evaluate expr failed: %v", err)
			continue
		}

		if matched {
			if ups == nil {
				ups = upstream.NewUpstreams(upstream.Policy(s.conf.Policy))
			}

			for serverIndex, server := range rule.Servers {
				for keyIndex, key := range rule.Keys {
					if handler, err := provider.CreateHandler(rule.Type, server, key, ternary.If(rule.Proxy, s.dialer, nil), rule.ModelReplacer); err != nil {
						log.Errorf("upstream failed to create: %v", err)
					} else {
						ups.Add(&upstream.Upstream{
							Rule:        rule,
							Handler:     handler,
							ServerIndex: serverIndex,
							KeyIndex:    keyIndex,
						})
					}
				}
			}
		}
	}

	if ups != nil && ups.Len() > 0 {
		if err := ups.Init(); err != nil {
			log.F(log.M{"model": model, "ups": ups}).Errorf("upstreams init failed: %v", err)
			return nil
		}

		return ups
	}

	return nil
}

func (s *Server) EvalTest(model string) {
	ups := s.selectUpstreams(model)
	if ups == nil || ups.Len() == 0 {
		// If no corresponding upstream is found, use the default upstream.
		ups = s.defaultUpstreams
	}

	for i, up := range ups.All() {
		log.F(log.M{"cur": up.Name(), "index": i}).Infof("dispatch request: %s -> %s", model, up.Rule.ModelReplacer(model))
	}
}

type OpenAIModelResponse struct {
	Object string         `json:"object"`
	Data   []openai.Model `json:"data"`
}

// Dispatch Request distribution implementation logic
func (s *Server) Dispatch(w http.ResponseWriter, r *http.Request) error {
	var ups *upstream.Upstreams
	var selected *upstream.Upstream
	var selectedIndex int

	var body []byte
	if !array.In(r.Method, []string{"GET", "OPTIONS", "HEAD"}) {
		body, _ = s.readRequestBody(r)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Check if the request contains any illegal content
	if s.moderation != nil && base.EndpointNeedModeration(r.URL.Path) {
		if s.conf.Moderation.ClientCanIgnore && strings.ToLower(r.Header.Get("X-Ignore-Moderation")) == "true" {
			if log.DebugEnabled() {
				log.Debugf("client ignore moderation: %s", r.URL.Path)
			}
		} else {
			var req openai.ChatCompletionRequest
			if err := json.Unmarshal(body, &req); err != nil {
				return err
			}

			// If the role of the last Message is robot, then there is no need to perform a sensitive word check
			// because this is the result of the tool call
			if len(req.Messages) > 0 && strings.ToLower(req.Messages[len(req.Messages)-1].Role) != "robot" {
				mReq := moderation.ConvertChatToRequest(req, s.conf.Moderation.API.Model)
				mRes, err := s.moderation.Moderation(ctx, mReq)
				if err != nil {
					log.With(mReq).Errorf("moderation failed: %v", err)
					// If the moderation fails, we will continue to process the request
				} else {
					if mRes.Flagged(s.conf.Moderation.ScoreThreshold) {
						flaggedCategories := mRes.FlaggedCategories(s.conf.Moderation.ScoreThreshold)
						violatedCategories := array.Intersect(flaggedCategories, s.conf.Moderation.Categories)
						// If the request is flagged by moderation, we will send the categories to the client as a response header
						w.Header().Set("X-VIOLATED-CATEGORIES", strings.Join(violatedCategories, ","))

						if len(violatedCategories) > 0 {
							log.With(log.M{"moderation": mRes}).Warning("request is flagged by moderation, blocked")
							return ErrRequestFlagged
						}

						log.With(log.M{"moderation": mRes}).Info("request is flagged by moderation, but not blocked")
					}
				}
			}

		}
	}

	var model string
	if base.EndpointHasModel(r.URL.Path) {
		model = gjson.Get(string(body), "model").String()
		if model == "" {
			return ErrModelRequired
		}

		ups = s.selectUpstreams(model)
		if ups == nil || ups.Len() == 0 {
			// If no corresponding upstream is found, use the default upstream.
			ups = s.defaultUpstreams
		}

		selected, selectedIndex = ups.Next()
		if selected == nil {
			return ErrNotSupport
		}
	} else if strings.TrimSuffix(r.URL.Path, "/") == "/v1/models" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(must.Must(json.Marshal(OpenAIModelResponse{
			Object: "list",
			Data:   s.supportModels,
		})))
		return nil
	} else {
		ups = s.defaultUpstreams
		selected, selectedIndex = ups.Next()
		if selected == nil {
			return ErrNotSupport
		}
	}

	if log.DebugEnabled() {
		logCtx := log.M{"cur": selected.Name(), "candidates": ups.Len(), "model": model}
		if s.conf.Verbose && s.conf.Debug {
			logCtx["body"] = string(body)
		}

		log.F(logCtx).Debugf("dispatch request: %s %s", r.Method, r.URL.String())
	}

	usedIndex := []int{selectedIndex}

	var retry func(w http.ResponseWriter, r *http.Request, err error)
	retryCount := 0
	retry = func(w http.ResponseWriter, r *http.Request, err error) {
		// 如果当前 upstream 失败，则尝试下一个 upstream
		cur := selected
		selected, selectedIndex = ups.Next(usedIndex...)
		if selected != nil {
			retryCount++
			log.F(log.M{"cur": cur.Name(), "used": usedIndex, "next": selected.Name(), "candidates": ups.Len(), "model": model}).
				Warningf("retry next upstream[%d]: %v", retryCount, err)

			usedIndex = append(usedIndex, selectedIndex)

			if !array.In(r.Method, []string{"GET", "OPTIONS", "HEAD"}) {
				_ = r.Body.Close()
				r.Body = io.NopCloser(bytes.NewBuffer(body))
			}

			selected.Handler.Serve(ctx, w, r, retry)
			return
		}

		log.F(log.M{"used": usedIndex, "retry_count": retryCount, "model": model}).Errorf("all upstreams failed: %v", err)

		var respErr base.ResponseError
		if errors.As(err, &respErr) {
			for k, v := range respErr.Resp.Header {
				for _, vv := range v {
					w.Header().Add(k, vv)
				}
			}

			w.WriteHeader(respErr.Resp.StatusCode)
			data, _ := io.ReadAll(respErr.Resp.Body)
			_ = respErr.Resp.Body.Close()
			_, _ = w.Write(data)

		} else {
			w.Header().Set("Content-Type", "application/json")

			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error": {"message": "all upstreams failed"}}`))
		}
	}

	selected.Handler.Serve(ctx, w, r, retry)

	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	authHeader := strings.TrimPrefix(strings.ToLower(r.Header.Get("Authorization")), "bearer ")
	if authHeader == "" || !array.In(authHeader, s.conf.Keys) {
		w.Header().Set("Content-Type", "application/json")

		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "unauthorized"}}`))
		return
	}

	// Distribution request
	if err := s.Dispatch(w, r); err != nil {
		w.Header().Set("Content-Type", "application/json")

		if errors.Is(err, ErrRequestFlagged) {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(fmt.Sprintf(`{"error": {"message": "%s"}}`, err.Error())))
		} else {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error": {"message": "invalid request"}}`))
			log.Errorf("dispatch request failed: %v", err)
		}

		return
	}
}
