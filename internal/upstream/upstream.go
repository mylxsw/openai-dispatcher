package upstream

import (
	"errors"
	"fmt"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/ternary"
	"github.com/mylxsw/openai-dispatcher/internal/config"
	"golang.org/x/net/proxy"
	"math/rand"
	"net/http"
)

var (
	ErrUpstreamShouldRetry = errors.New("upstream failed")
)

type Upstream struct {
	Rule    config.Rule
	Index   int
	Handler Handler
	Server  string
	Key     string
}

type Handler interface {
	Serve(w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error))
}

type Upstreams []*Upstream

func (u Upstreams) Next(excludeIndex ...int) (*Upstream, int) {
	candidates := array.Filter(u, func(item *Upstream, _ int) bool { return !array.In(item.Index, excludeIndex) })
	if len(candidates) == 0 {
		return nil, -1
	}

	index := rand.Intn(len(candidates))
	return candidates[index], candidates[index].Index
}

func BuildUpstreamsFromRules(rules config.Rules, err error, dialer proxy.Dialer) (map[string]Upstreams, Upstreams, error) {
	proxies := make(map[string]Upstreams)
	defaultProxies := make(Upstreams, 0)

	for i, rule := range rules {
		for _, model := range rule.Models {
			for _, server := range rule.Servers {
				for _, key := range rule.Keys {
					var handler Handler

					if rule.Azure {
						handler, err = NewAzureOpenAIUpstream(server, key, rule.AzureAPIVersion, ternary.If(rule.Proxy, dialer, nil))
					} else {
						handler, err = NewTransparentUpstream(server, key, ternary.If(rule.Proxy, dialer, nil))
					}
					if err != nil {
						return nil, nil, fmt.Errorf("创建 upstream 失败 #%d: %w", i+1, err)
					}

					proxies[model] = append(proxies[model], &Upstream{
						Rule:    rule,
						Handler: handler,
						Index:   len(proxies[model]),
						Server:  server,
						Key:     key,
					})

					if rule.Default {
						defaultProxies = append(defaultProxies, &Upstream{
							Rule:    rule,
							Handler: handler,
							Index:   len(defaultProxies),
							Server:  server,
							Key:     key,
						})
					}
				}
			}
		}
	}

	return proxies, defaultProxies, nil
}
