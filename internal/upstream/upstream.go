package upstream

import (
	"fmt"
	"github.com/mroth/weightedrand/v2"
	"github.com/mylxsw/asteria/color"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/ternary"
	"github.com/mylxsw/openai-dispatcher/internal/config"
	"github.com/mylxsw/openai-dispatcher/internal/provider"
	"github.com/mylxsw/openai-dispatcher/internal/provider/base"
	"golang.org/x/net/proxy"
	"math/rand"
	"strings"
	"sync"
)

type Upstream struct {
	Rule        config.Rule
	Index       int
	Handler     base.Handler
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

func (u *Upstreams) Add(upstream *Upstream) {
	upstream.Index = len(u.ups)
	u.ups = append(u.ups, upstream)
}

func (u *Upstreams) Init() error {
	if len(u.ups) == 0 {
		return nil
	}

	if u.policy == WeightPolicy {
		choices := make([]weightedrand.Choice[*Upstream, int], 0)
		// By default, only the weight of non-backup upstream is calculated
		for _, up := range u.ups {
			if up.Rule.Backup {
				continue
			}

			weight := ternary.If(up.Rule.Weight == 0, 1, up.Rule.Weight)
			choices = append(choices, weightedrand.NewChoice[*Upstream, int](up, weight))
		}

		// If there is no primary upstream, the backup upstream is used
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

func (u *Upstreams) All() []*Upstream {
	return u.ups
}

func (u *Upstreams) Next(excludeIndex ...int) (*Upstream, int) {
	// A retry is indicated when an index to exclude is included, in which case a random upstream (containing the upstream marked backup) is selected.
	if len(excludeIndex) > 0 {
		candidates := array.Filter(u.ups, func(item *Upstream, _ int) bool { return !array.In(item.Index, excludeIndex) })
		if len(candidates) == 0 {
			return nil, -1
		}

		index := rand.Intn(len(candidates))
		return candidates[index], candidates[index].Index
	}

	// If there is no index to exclude, it is a normal request and only the non-backup upstream is selected
	candidates := array.Filter(u.ups, func(item *Upstream, _ int) bool { return !item.Rule.Backup })
	if len(candidates) == 0 {
		// After the backup upstreams are excluded, if no upstreams are available, all upstreams are used.
		// This is usually the case when all upstreams are backup, and if no upstreams are available, an error is returned
		candidates = u.ups
		if len(candidates) == 0 {
			return nil, -1
		}
	}

	switch u.policy {
	case RandomPolicy: // Stochastic strategy
		index := rand.Intn(len(candidates))
		return candidates[index], candidates[index].Index

	case RoundRobinPolicy: // Polling strategy
		u.indexLock.Lock()
		defer u.indexLock.Unlock()

		u.index = (u.index + 1) % len(candidates)
		return candidates[u.index], candidates[u.index].Index
	case WeightPolicy: // Weight strategy
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

type Result struct {
	Upstreams map[string]*Upstreams
	Default   *Upstreams
	ExprRules []config.Rule
}

func BuildUpstreamsFromRules(policy Policy, rules config.Rules, dialer proxy.Dialer) (*Result, error) {
	result := &Result{
		Upstreams: make(map[string]*Upstreams),
		Default:   NewUpstreams(policy),
		ExprRules: make([]config.Rule, 0),
	}

	for i, rule := range rules {
		for _, model := range rule.GetModels() {
			if _, ok := result.Upstreams[model]; !ok {
				result.Upstreams[model] = NewUpstreams(policy)
			}

			for serverIndex, server := range rule.Servers {
				for keyIndex, key := range rule.Keys {
					if handler, err := provider.CreateHandler(rule.Type, server, key, ternary.If(rule.Proxy, dialer, nil), rule.ModelReplacer); err != nil {
						return nil, fmt.Errorf("upstream failed to create #%d: %w", i+1, err)
					} else {
						result.Upstreams[model].ups = append(result.Upstreams[model].ups, &Upstream{
							Rule:        rule,
							Handler:     handler,
							Index:       len(result.Upstreams[model].ups),
							ServerIndex: serverIndex,
							KeyIndex:    keyIndex,
						})
					}
				}
			}
		}
	}

	for _, rule := range rules {
		if rule.Expr == nil || rule.Expr.Match == "" {
			continue
		}

		result.ExprRules = append(result.ExprRules, rule)
	}

	for i, rule := range rules {
		if !rule.Default {
			continue
		}

		dum := array.ToMap(result.Default.ups, func(t *Upstream, _ int) string { return t.Rule.Name })
		if _, ok := dum[rule.Name]; !ok {
			for serverIndex, server := range rule.Servers {
				for keyIndex, key := range rule.Keys {
					if handler, err := provider.CreateHandler(rule.Type, server, key, ternary.If(rule.Proxy, dialer, nil), rule.ModelReplacer); err != nil {
						return nil, fmt.Errorf("upstream failed to create #%d: %w", i+1, err)
					} else {
						result.Default.ups = append(result.Default.ups, &Upstream{
							Rule:        rule,
							Handler:     handler,
							Index:       len(result.Default.ups),
							ServerIndex: serverIndex,
							KeyIndex:    keyIndex,
						})
					}
				}
			}
		}
	}

	for _, up := range result.Upstreams {
		if err := up.Init(); err != nil {
			return nil, err
		}
	}

	if err := result.Default.Init(); err != nil {
		return nil, err
	}

	return result, nil
}
