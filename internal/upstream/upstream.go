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

func (u *Upstreams) init() error {
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

func BuildUpstreamsFromRules(policy Policy, rules config.Rules, err error, dialer proxy.Dialer) (map[string]*Upstreams, *Upstreams, error) {
	ups := make(map[string]*Upstreams)
	defaultUps := NewUpstreams(policy)

	for i, rule := range rules {
		for _, model := range rule.GetModels() {
			if _, ok := ups[model]; !ok {
				ups[model] = NewUpstreams(policy)
			}

			for serverIndex, server := range rule.Servers {
				for keyIndex, key := range rule.Keys {
					if handler, err := provider.CreateHandler(rule.Type, server, key, ternary.If(rule.Proxy, dialer, nil)); err != nil {
						return nil, nil, fmt.Errorf("upstream failed to create #%d: %w", i+1, err)
					} else {
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
	}

	for i, rule := range rules {
		if !rule.Default {
			continue
		}

		dum := array.ToMap(defaultUps.ups, func(t *Upstream, _ int) string { return t.Rule.Name })
		if _, ok := dum[rule.Name]; !ok {
			for serverIndex, server := range rule.Servers {
				for keyIndex, key := range rule.Keys {
					if handler, err := provider.CreateHandler(rule.Type, server, key, ternary.If(rule.Proxy, dialer, nil)); err != nil {
						return nil, nil, fmt.Errorf("upstream failed to create #%d: %w", i+1, err)
					} else {
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
