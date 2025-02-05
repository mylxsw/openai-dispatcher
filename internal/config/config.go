package config

import (
	"encoding/json"
	"fmt"
	"github.com/mylxsw/asteria/log"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/must"
	"github.com/mylxsw/go-utils/ternary"
	"github.com/mylxsw/openai-dispatcher/internal/provider/base"
	"github.com/mylxsw/openai-dispatcher/pkg/expr"
	"gopkg.in/yaml.v3"
	"os"
	"strings"
)

type Config struct {
	LogPath          string     `yaml:"log-path" json:"log-path,omitempty"`
	Debug            bool       `yaml:"debug" json:"debug,omitempty"`
	Verbose          bool       `yaml:"verbose" json:"verbose,omitempty"`
	Listen           string     `yaml:"listen" json:"listen,omitempty"`
	Socks5           string     `yaml:"socks5" json:"socks5,omitempty"`
	Keys             []string   `yaml:"keys" json:"-"`
	Policy           string     `yaml:"policy" json:"policy,omitempty"`
	Rules            Rules      `yaml:"rules" json:"rules,omitempty"`
	ExtraModels      []string   `yaml:"extra-models" json:"extra-models,omitempty"`
	EnablePrometheus bool       `yaml:"enable-prometheus" json:"enable-prometheus,omitempty"`
	Moderation       Moderation `yaml:"moderation" json:"moderation,omitempty"`
}

func (conf *Config) Validate() error {
	// TODO Check whether the configuration is correct

	if conf.Policy != "" && !array.In(conf.Policy, []string{"random", "round_robin", "weight"}) {
		return fmt.Errorf("policy Only random, round_robin, and weight are supported")
	}

	for i, rule := range conf.Rules {
		if !array.In(rule.Type, []base.ChannelType{base.ChannelTypeOpenAI, base.ChannelTypeCoze}) {
			return fmt.Errorf("%s type is under development, so stay tuned #%d", rule.Type, i+1)
		}

		if rule.Expr != nil {
			if rule.Expr.Match != "" {
				if _, err := expr.NewBoolVM(rule.Expr.Match); err != nil {
					return fmt.Errorf("rule #%d, expr.match: %s", i+1, err)
				}
			}

			if rule.Expr.Replace != "" {
				if _, err := expr.NewStringVM(rule.Expr.Replace); err != nil {
					return fmt.Errorf("rule #%d, expr.replace: %s", i+1, err)
				}
			}
		}
	}

	if conf.Moderation.Enabled {
		if conf.Moderation.API.Type != "openai" {
			return fmt.Errorf("moderation api type only support openai")
		}

		if conf.Moderation.ScoreThreshold > 1 || conf.Moderation.ScoreThreshold < 0 {
			return fmt.Errorf("moderation score threshold must be between 0 and 1")
		}

		if !strings.HasPrefix(conf.Moderation.API.Server, "http://") &&
			!strings.HasPrefix(conf.Moderation.API.Server, "https://") {
			return fmt.Errorf("moderation api server must be a valid url")
		}

		if conf.Moderation.API.Key == "" {
			return fmt.Errorf("moderation api key is required")
		}
	}

	return nil
}

func (conf *Config) JSON() string {
	data, _ := json.Marshal(conf)
	return string(data)
}

type Rules []Rule

type Rule struct {
	Name            string           `yaml:"name" json:"name,omitempty"`
	Servers         []string         `yaml:"servers" json:"servers"`
	Keys            []string         `yaml:"keys" json:"-"`
	Models          []string         `yaml:"models" json:"models"`
	ModelKeys       []ModelKey       `yaml:"model-keys" json:"model-keys"`
	Proxy           bool             `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	Type            base.ChannelType `yaml:"type,omitempty" json:"type,omitempty"`
	AzureAPIVersion string           `yaml:"azure-api-version,omitempty" json:"azure-api-version,omitempty"`
	Rewrite         []ModelRewrite   `yaml:"rewrite,omitempty" json:"rewrite,omitempty"`
	// Default Default rule
	Default bool `yaml:"default,omitempty" json:"default,omitempty"`
	// Backup Alternate rule, which is not used by default and is used only when an error occurs
	Backup bool `yaml:"backup,omitempty" json:"backup,omitempty"`
	// Weight, used for the weight policy. The default value is 1. A negative value indicates that the rule is not used
	Weight int `yaml:"weight,omitempty" json:"weight,omitempty"`

	// Advanced configuration
	Expr *Expr `yaml:"expr,omitempty" json:"expr,omitempty"`
}

func (rule Rule) ModelReplacer(model string) string {
	for _, rewrite := range rule.Rewrite {
		if model == rewrite.Src {
			return rewrite.Dst
		}
	}

	if rule.Expr != nil && rule.Expr.Replace != "" {
		replacedModel, err := must.Must(expr.NewStringVM(rule.Expr.Replace)).Run(expr.Data{Model: model})
		if err != nil {
			log.F(log.M{"model": model, "rule": rule.Name}).Errorf("replace model failed: %v", err)
			return model
		}

		return replacedModel
	}

	return model
}

type Expr struct {
	// Match Expression to determine whether the model matches the current channel
	Match string `yaml:"match,omitempty" json:"match,omitempty"`
	// Replace Expression to replace the model name
	Replace string `yaml:"replace,omitempty" json:"replace,omitempty"`
}

func (rule Rule) GetModels() []string {
	return array.Uniq(append(rule.Models, array.Map(rule.Rewrite, func(item ModelRewrite, _ int) string { return item.Src })...))
}

type ModelRewrite struct {
	Src string `yaml:"src" json:"src"`
	Dst string `yaml:"dst" json:"dst"`
}

func LoadConfig(configFilePath string) (*Config, error) {
	configData, err := os.ReadFile(configFilePath)
	if err != nil {
		return nil, err
	}

	var conf Config
	if err := yaml.Unmarshal(configData, &conf); err != nil {
		return nil, err
	}

	conf.ExtraModels = array.Uniq(conf.ExtraModels)

	rules := make(Rules, 0)

	for _, rule := range conf.Rules {
		if rule.Type == "" {
			rule.Type = base.ChannelTypeOpenAI
		}

		if len(rule.ModelKeys) > 0 {
			for i, modelKey := range rule.ModelKeys {
				servers := modelKey.Servers
				if modelKey.Server != "" {
					servers = append(servers, modelKey.Server)
				}

				models := modelKey.Models
				if modelKey.Model != "" {
					models = append(models, modelKey.Model)
				}

				keys := modelKey.Keys
				if modelKey.Key != "" {
					keys = append(keys, modelKey.Key)
				}

				rules = append(rules, Rule{
					Name:            fmt.Sprintf("%s-S(%d)", rule.Name, i),
					Servers:         ternary.If(len(servers) == 0, rule.Servers, servers),
					Keys:            ternary.If(len(keys) == 0, rule.Keys, keys),
					Models:          models,
					Proxy:           rule.Proxy,
					Type:            rule.Type,
					AzureAPIVersion: rule.AzureAPIVersion,
					Rewrite:         array.Filter(rule.Rewrite, func(item ModelRewrite, _ int) bool { return array.In(item.Src, models) }),
					Default:         rule.Default,
					Backup:          rule.Backup,
					Weight:          rule.Weight,
				})
			}
		} else {
			rules = append(rules, rule)
		}
	}

	conf.Rules = rules

	if conf.Moderation.Enabled {
		if len(conf.Moderation.Categories) == 0 {
			conf.Moderation.Categories = []string{
				"sexual",
				"sexual/minors",
				"harassment",
				"harassment/threatening",
				"hate",
				"hate/threatening",
				"illicit",
				"illicit/violent",
				"self-harm",
				"self-harm/intent",
				"self-harm/instructions",
				"violence",
				"violence/graphic",
			}
		}

		if conf.Moderation.ScoreThreshold == 0 {
			conf.Moderation.ScoreThreshold = 0.7
		}

		if conf.Moderation.API.Type == "" {
			conf.Moderation.API.Type = "openai"
		}

		if conf.Moderation.API.Server == "" {
			conf.Moderation.API.Server = "https://api.openai.com"
		}

		if conf.Moderation.API.Model == "" {
			conf.Moderation.API.Model = "omni-moderation-latest"
		}
	}

	if err := conf.Validate(); err != nil {
		return nil, err
	}

	return &conf, nil
}

type ModelKey struct {
	Servers []string `json:"servers,omitempty"`
	Server  string   `json:"server,omitempty"`
	Model   string   `json:"model,omitempty"`
	Models  []string `json:"models,omitempty"`
	Key     string   `json:"key,omitempty"`
	Keys    []string `json:"keys,omitempty"`
}

type Moderation struct {
	Enabled bool `yaml:"enabled" json:"enabled"`
	// ClientCanIgnore If the client can ignore the moderation result, If client send a request with `X-Ignore-Moderation` header, the dispatcher will ignore the moderation result
	ClientCanIgnore bool          `yaml:"client-can-ignore" json:"client-can-ignore"`
	Categories      []string      `yaml:"categories" json:"categories"`
	ScoreThreshold  float64       `yaml:"score-threshold" json:"score-threshold"`
	API             ModerationAPI `yaml:"api" json:"api"`
}

type ModerationAPI struct {
	Type   string `yaml:"type" json:"type"`
	Server string `yaml:"server" json:"server"`
	Key    string `yaml:"key" json:"key"`
	Proxy  bool   `yaml:"proxy" json:"proxy"`
	Model  string `yaml:"model" json:"model"`
}
