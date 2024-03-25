package upstream

import (
	"errors"
	"fmt"
	"github.com/mroth/weightedrand/v2"
	"github.com/mylxsw/asteria/color"
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
	Rule        config.Rule
	Index       int
	Handler     Handler
	ServerIndex int
	KeyIndex    int
}

func (u *Upstream) Name() string {
	if u.Rule.Name != "" {
		return fmt.Sprintf("%s|s%d:k%d", u.Rule.Name, u.ServerIndex, u.KeyIndex)
	}

	return fmt.Sprintf("%s|%s", u.MaskedServer(), u.MaskedKey())
}

func (u *Upstream) MaskedKey() string {
	return mask(10, u.Rule.Keys[u.KeyIndex])
}

func (u *Upstream) MaskedServer() string {
	return u.Rule.Servers[u.ServerIndex]
}

type Handler interface {
	Serve(w http.ResponseWriter, r *http.Request, errorHandler func(w http.ResponseWriter, r *http.Request, err error))
}

type Upstreams struct {
	ups    []*Upstream
	policy Policy

	index     int
	indexLock sync.Mutex

	chooser *weightedrand.Chooser[*Upstream, int]
}

type Policy string

const (
	RandomPolicy     Policy = "random"
	RoundRobinPolicy Policy = "round_robin"
	WeightPolicy     Policy = "weight"
)

func NewUpstreams(policy Policy) *Upstreams {
	if policy == "" {
		policy = RoundRobinPolicy
	}

	return &Upstreams{policy: policy, ups: make([]*Upstream, 0)}
}

func (u *Upstreams) init() error {
	if len(u.ups) == 0 {
		return nil
	}

	if u.policy == WeightPolicy {
		choices := make([]weightedrand.Choice[*Upstream, int], 0)
		// 默认只对非 backup 的 upstream 进行权重计算
		for _, up := range u.ups {
			if up.Rule.Backup {
				continue
			}

			weight := ternary.If(up.Rule.Weight == 0, 1, up.Rule.Weight)
			choices = append(choices, weightedrand.NewChoice[*Upstream, int](up, weight))
		}

		// 如果没有主要 upstream，则使用 backup 的 upstream
		if len(choices) == 0 {
			for _, up := range u.ups {
				weight := ternary.If(up.Rule.Weight == 0, 1, up.Rule.Weight)
				choices = append(choices, weightedrand.NewChoice[*Upstream, int](up, weight))
			}
		}

		chooser, err := weightedrand.NewChooser(choices...)
		if err != nil {
			return err
		}

		u.chooser = chooser
	}

	return nil
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
	case WeightPolicy: // 权重策略
		item := u.chooser.Pick()
		return item, item.Index
	default:
		panic("unknown policy: " + u.policy)
	}
}

func (u *Upstreams) Print() {
	for _, up := range u.ups {
		fmt.Printf(
			"    -> %s %s %s\n",
			ternary.If(up.Rule.Backup, color.TextWrap(color.LightGrey, "[backup]"), color.TextWrap(color.Green, "[main]  ")),
			up.Name(),
			ternary.If(u.policy == WeightPolicy, color.TextWrap(color.LightYellow, fmt.Sprintf(" (weight: %d)", ternary.If(up.Rule.Weight == 0, 1, up.Rule.Weight))), ""),
		)
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

			for serverIndex, server := range rule.Servers {
				for keyIndex, key := range rule.Keys {
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
						Rule:        rule,
						Handler:     handler,
						Index:       len(ups[model].ups),
						ServerIndex: serverIndex,
						KeyIndex:    keyIndex,
					})

				}
			}
		}
	}

	for i, rule := range rules {
		if !rule.Default {
			continue
		}

		dum := array.ToMap(defaultUps.ups, func(t *Upstream, _ int) string { return t.Rule.Name })
		if _, ok := dum[rule.Name]; !ok {
			for serverIndex, server := range rule.Servers {
				for keyIndex, key := range rule.Keys {
					var handler Handler

					if rule.Azure {
						handler, err = NewAzureOpenAIUpstream(server, key, rule.AzureAPIVersion, ternary.If(rule.Proxy, dialer, nil))
					} else {
						handler, err = NewTransparentUpstream(server, key, ternary.If(rule.Proxy, dialer, nil))
					}
					if err != nil {
						return nil, nil, fmt.Errorf("创建 upstream 失败 #%d: %w", i+1, err)
					}

					defaultUps.ups = append(defaultUps.ups, &Upstream{
						Rule:        rule,
						Handler:     handler,
						Index:       len(defaultUps.ups),
						ServerIndex: serverIndex,
						KeyIndex:    keyIndex,
					})
				}
			}
		}
	}

	for _, up := range ups {
		if err := up.init(); err != nil {
			return nil, nil, err
		}
	}

	if err := defaultUps.init(); err != nil {
		return nil, nil, err
	}

	return ups, defaultUps, nil
}
