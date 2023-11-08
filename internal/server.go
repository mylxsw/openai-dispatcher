package internal

import (
	"bytes"
	"errors"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/openai-dispatcher/internal/config"
	"github.com/mylxsw/openai-dispatcher/internal/upstream"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

	upstreams, defaultUpstreams, err := upstream.BuildUpstreamsFromRules(upstream.Policy(conf.Policy), conf.Rules, conf.Validate(), dialer)
	if err != nil {
		return nil, err
	}

	for model, ups := range upstreams {
		fmt.Println(model)
		ups.Print()
	}

	return &Server{
		conf:             conf,
		upstreams:        upstreams,
		defaultUpstreams: defaultUpstreams,
	}, nil
}

// readRequestBody 读取请求的 Body
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

// Request 代理请求对象
type Request struct {
	Method      string              `json:"method,omitempty"`
	URL         string              `json:"url,omitempty"`
	Path        string              `json:"path,omitempty"`
	QueryParams map[string][]string `json:"query_params,omitempty"`
	Body        []byte              `json:"body,omitempty"`
}

// buildRequest 构建代理请求对象
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

// Dispatch 请求分发实现逻辑
func (s *Server) Dispatch(w http.ResponseWriter, r *http.Request) error {
	var ups *upstream.Upstreams
	var selected *upstream.Upstream
	var selectedIndex int

	body, err := s.readRequestBody(r)
	if err != nil {
		return err
	}

	if array.In(r.URL.Path, []string{
		"/v1/chat/completions",
		"/v1/completions",
		"/v1/images/generations",
		"/v1/images/edits",
		"/v1/images/variations",
		"/v1/audio/speech",
		"/v1/audio/transcriptions",
		"/v1/audio/translations",
		"/v1/moderations",
		"/v1/embeddings",
	}) {
		model := gjson.Get(string(body), "model").String()
		if model == "" {
			return ErrModelRequired
		}

		ups = s.upstreams[model]
		if ups == nil || ups.Len() == 0 {
			return ErrNotSupport
		}

		selected, selectedIndex = ups.Next()
		if selected == nil {
			return ErrNotSupport
		}

		for _, rewrite := range selected.Rule.Rewrite {
			if model == rewrite.Src {
				newBody, _ := sjson.Set(string(body), "model", rewrite.Dst)
				body = []byte(newBody)

				s.replaceRequestBody(r, body)
				break
			}
		}

	} else {
		ups = s.defaultUpstreams
		selected, selectedIndex = ups.Next()
		if selected == nil {
			return ErrNotSupport
		}
	}

	log.F(log.M{"current": selectedIndex, "server": selected.Server, "key": selected.Key, "candidates": ups.Len()}).
		Debugf("dispatch request: %s %s", r.Method, r.URL.String())

	usedIndex := []int{selectedIndex}

	var retry func(w http.ResponseWriter, r *http.Request, err error)
	retryCount := 0
	retry = func(w http.ResponseWriter, r *http.Request, err error) {
		// 如果当前 upstream 失败，则尝试下一个 upstream
		selected, selectedIndex = ups.Next(usedIndex...)
		if selected != nil {
			retryCount++
			log.F(log.M{"next": selectedIndex, "used": usedIndex, "server": selected.Server, "candidates": ups.Len()}).Warningf("retry next upstream[%d]: %v", retryCount, err)

			usedIndex = append(usedIndex, selectedIndex)

			_ = r.Body.Close()
			r.Body = io.NopCloser(bytes.NewBuffer(body))

			selected.Handler.Serve(w, r, retry)
			return
		}

		log.F(log.M{"used": usedIndex, "retry_count": retryCount}).Errorf("all upstreams failed: %v", err)

		var respErr upstream.ResponseError
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

	selected.Handler.Serve(w, r, retry)

	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// CORS
	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return
	}

	authHeader := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if authHeader == "" || !array.In(authHeader, s.conf.Keys) {
		w.Header().Set("Content-Type", "application/json")

		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error": {"message": "unauthorized"}}`))
		return
	}

	// 分发请求
	if err := s.Dispatch(w, r); err != nil {
		log.Errorf("dispatch request failed: %v", err)
		w.Header().Set("Content-Type", "application/json")

		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error": {"message": "invalid request"}}`))
		return
	}
}
