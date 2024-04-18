package config

import (
	"encoding/json"
	"fmt"
	"github.com/mylxsw/go-utils/array"
	"github.com/mylxsw/go-utils/ternary"
	"gopkg.in/yaml.v3"
	"os"
)

type Config struct {
	LogPath          string   `yaml:"log-path" json:"log-path,omitempty"`
	Listen           string   `yaml:"listen" json:"listen,omitempty"`
	Socks5           string   `yaml:"socks5" json:"socks5,omitempty"`
	Keys             []string `yaml:"keys" json:"-"`
	Policy           string   `yaml:"policy" json:"policy,omitempty"`
	Rules            Rules    `yaml:"rules" json:"rules,omitempty"`
	EnablePrometheus bool     `yaml:"enable-prometheus" json:"enable-prometheus,omitempty"`
}

func (conf *Config) Validate() error {
	// TODO Check whether the configuration is correct

	if conf.Policy != "" && !array.In(conf.Policy, []string{"random", "round_robin", "weight"}) {
		return fmt.Errorf("policy Only random, round_robin, and weight are supported")
	}

	for i, rule := range conf.Rules {
		if rule.Azure && rule.Default {
			return fmt.Errorf("azure rules cannot be set as default rules at this time #%d", i+1)
		}

		if rule.Azure {
			return fmt.Errorf("azure mode is under development, so stay tuned #%d", i+1)
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
	Name            string         `yaml:"name" json:"name,omitempty"`
	Servers         []string       `yaml:"servers" json:"servers"`
	Keys            []string       `yaml:"keys" json:"-"`
	Models          []string       `yaml:"models" json:"models"`
	ModelKeys       []ModelKey     `yaml:"model-keys" json:"model-keys"`
	Proxy           bool           `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	Azure           bool           `yaml:"azure,omitempty" json:"azure,omitempty"`
	AzureAPIVersion string         `yaml:"azure-api-version,omitempty" json:"azure-api-version,omitempty"`
	Rewrite         []ModelRewrite `yaml:"rewrite,omitempty" json:"rewrite,omitempty"`
	// Default Default rule
	Default bool `yaml:"default,omitempty" json:"default,omitempty"`
	// Backup Alternate rule, which is not used by default and is used only when an error occurs
	Backup bool `yaml:"backup,omitempty" json:"backup,omitempty"`
	// Weight, used for the weight policy. The default value is 1. A negative value indicates that the rule is not used
	Weight int `yaml:"weight,omitempty" json:"weight,omitempty"`
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

	rules := make(Rules, 0)

	for _, rule := range conf.Rules {
		if len(rule.ModelKeys) > 0 {
			for i, modelKey := range rule.ModelKeys {
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
					Servers:         rule.Servers,
					Keys:            ternary.If(len(keys) == 0, rule.Keys, keys),
					Models:          models,
					Proxy:           rule.Proxy,
					Azure:           rule.Azure,
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

	if err := conf.Validate(); err != nil {
		return nil, err
	}

	return &conf, nil
}

type ModelKey struct {
	Model  string   `json:"model,omitempty"`
	Models []string `json:"models,omitempty"`
	Key    string   `json:"key,omitempty"`
	Keys   []string `json:"keys,omitempty"`
}
