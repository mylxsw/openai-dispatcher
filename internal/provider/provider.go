package provider

import (
	"context"
	"encoding/json"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/openai-dispatcher/internal/provider/anthropic"
	"github.com/mylxsw/openai-dispatcher/internal/provider/base"
	"github.com/mylxsw/openai-dispatcher/internal/provider/coze"
	"github.com/mylxsw/openai-dispatcher/internal/provider/transport"
	"github.com/sashabaranov/go-openai"
	"golang.org/x/net/proxy"
	"net/http"
	"strings"
)

type provider struct {
	client base.Provider
	server string
	key    string
	dialer proxy.Dialer
}

func CreateHandler(typ base.ChannelType, server string, key string, dialer proxy.Dialer) (base.Handler, error) {
	var client base.Provider
	//var err error
	switch typ {
	case base.ChannelTypeCoze:
		client = coze.New(server, key, dialer)
	case base.ChannelTypeAnthropic:
		client = anthropic.New(server, key, dialer)
	default:
		return transport.New(server, key, dialer)
	}

	return &provider{
		client: client,
		server: server,
		key:    key,
		dialer: dialer,
	}, nil
}

func (p *provider) Serve(ctx context.Context, w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error)) {
	if !array.In(base.Endpoint(strings.TrimSuffix(r.URL.Path, "/")), []base.Endpoint{base.EndpointChatCompletion}) {
		log.F(log.M{"endpoint": r.URL.Path}).Warningf("unsupported endpoint for coze: %s", r.URL.Path)
		errorHandler(w, r, base.ErrUpstreamShouldRetry)
		return
	}

	var req openai.ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		log.Errorf("decode request failed: %v", err)
		errorHandler(w, r, base.ErrUpstreamShouldRetry)
		return
	}

	if req.Stream {
		if err := p.client.CompletionStream(ctx, req, w); err != nil {
			errorHandler(w, r, err)
		}
	} else {
		if err := p.client.Completion(ctx, req, w); err != nil {
			errorHandler(w, r, err)
		}
	}
}
