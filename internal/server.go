package internal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/must"
	"github.com/mylxsw/go-utils/ternary"
	"github.com/mylxsw/openai-dispatcher/internal/config"
	"github.com/mylxsw/openai-dispatcher/internal/provider"
	"github.com/mylxsw/openai-dispatcher/internal/provider/base"
	"github.com/mylxsw/openai-dispatcher/internal/upstream"
	"github.com/mylxsw/openai-dispatcher/pkg/expr"
	"github.com/tidwall/gjson"
	"golang.org/x/net/proxy"
	"io"
	"net/http"
	"strings"
	"sync"
)

type Server struct {
	conf             *config.Config
	upstreams        map[string]*upstream.Upstreams
	defaultUpstreams *upstream.Upstreams
	exprRules        []config.Rule

	dialer proxy.Dialer
	once   sync.Once
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

	return &Server{
		conf:             conf,
		upstreams:        result.Upstreams,
		defaultUpstreams: result.Default,
		exprRules:        result.ExprRules,
	}, nil
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

var (
	ErrModelRequired = errors.New("model is required")
	ErrNotSupport    = errors.New("not support")
)

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

// Dispatch Request distribution implementation logic
func (s *Server) Dispatch(w http.ResponseWriter, r *http.Request) error {
	var ups *upstream.Upstreams
	var selected *upstream.Upstream
	var selectedIndex int

	body, err := s.readRequestBody(r)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var model string
	if array.In(base.Endpoint(strings.TrimSuffix(r.URL.Path, "/")), []base.Endpoint{
		base.EndpointChatCompletion,
		base.EndpointCompletion,
		base.EndpointImageGeneration,
		base.EndpointImageEdit,
		base.EndpointImageVariation,
		base.EndpointAudioSpeech,
		base.EndpointAudioTranscript,
		base.EndpointAudioTranslate,
		base.EndpointModeration,
		base.EndpointEmbedding,
	}) {
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
	} else {
		ups = s.defaultUpstreams
		selected, selectedIndex = ups.Next()
		if selected == nil {
			return ErrNotSupport
		}
	}

	log.F(log.M{"cur": selected.Name(), "candidates": ups.Len(), "model": model}).
		Debugf("dispatch request: %s %s", r.Method, r.URL.String())

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

			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewBuffer(body))

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
		log.Errorf("dispatch request failed: %v", err)
		w.Header().Set("Content-Type", "application/json")

		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "invalid request"}}`))
		return
	}
}
