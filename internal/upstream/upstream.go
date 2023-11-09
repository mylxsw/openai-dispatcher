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
	"strings"
	"sync"
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

func (u *Upstream) MaskedKey() string {
	return mask(10, u.Key)
}

type Handler interface {
	Serve(w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error))
}

type Upstreams struct {
	ups    []*Upstream
	policy Policy

	index     int
	indexLock sync.Mutex
}

type Policy string

const (
	RandomPolicy     Policy = "random"
	RoundRobinPolicy Policy = "round_robin"
)

func NewUpstreams(policy Policy) *Upstreams {
	return &Upstreams{policy: policy, ups: make([]*Upstream, 0)}
}

func (u *Upstreams) Len() int {
	return len(u.ups)
}

func (u *Upstreams) Next(excludeIndex ...int) (*Upstream, int) {
	// 当包含要排除的 index 时，说明是重试，此时随机选择一个 upstream （包含标记为 backup 的 upstream）
	if len(excludeIndex) > 0 {
		candidates := array.Filter(u.ups, func(item *Upstream, _ int) bool { return !array.In(item.Index, excludeIndex) })
		if len(candidates) == 0 {
			return nil, -1
		}

		index := rand.Intn(len(candidates))
		return candidates[index], candidates[index].Index
	}

	// 当没有要排除的 index 时，说明是正常请求，此时只会在非 backup 的 upstream 中选择
	candidates := array.Filter(u.ups, func(item *Upstream, _ int) bool { return !item.Rule.Backup })
	if len(candidates) == 0 {
		// 排除掉 backup 的 upstream 后，如果没有 upstream 可用，则使用全部的 upstream
		// 这种情况一般是所有的 upstream 都是 backup 的情况
		// 此时，如果还是没有 upstream 可用，则返回错误
		candidates = u.ups
		if len(candidates) == 0 {
			return nil, -1
		}
	}

	switch u.policy {
	case RandomPolicy: // 随机策略
		index := rand.Intn(len(candidates))
		return candidates[index], candidates[index].Index

	case RoundRobinPolicy: // 轮询策略
		u.indexLock.Lock()
		defer u.indexLock.Unlock()

		u.index = (u.index + 1) % len(candidates)
		return candidates[u.index], candidates[u.index].Index
	default:
		panic("unknown policy: " + u.policy)
	}
}

func (u *Upstreams) Print() {
	for _, up := range u.ups {
		fmt.Printf("    %s -> %s\n", up.Server, up.MaskedKey())
	}
}

func mask(left int, content string) string {
	size := len(content)
	if size < 16 {
		return strings.Repeat("*", size)
	}

	return content[:left] + strings.Repeat("*", size-left*2) + content[size-left:]
}

func BuildUpstreamsFromRules(policy Policy, rules config.Rules, err error, dialer proxy.Dialer) (map[string]*Upstreams, *Upstreams, error) {
	ups := make(map[string]*Upstreams)
	defaultUps := NewUpstreams(policy)

	for i, rule := range rules {
		for _, model := range rule.Models {
			if _, ok := ups[model]; !ok {
				ups[model] = NewUpstreams(policy)
			}

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

					ups[model].ups = append(ups[model].ups, &Upstream{
						Rule:    rule,
						Handler: handler,
						Index:   len(ups[model].ups),
						Server:  server,
						Key:     key,
					})

					if rule.Default {
						defaultUps.ups = append(defaultUps.ups, &Upstream{
							Rule:    rule,
							Handler: handler,
							Index:   len(defaultUps.ups),
							Server:  server,
							Key:     key,
						})
					}
				}
			}
		}
	}

	return ups, defaultUps, nil
}
