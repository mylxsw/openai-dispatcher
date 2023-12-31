package config

import (
	"encoding/json"
	"fmt"
	"github.com/mylxsw/go-utils/array"
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
	// TODO 检查配置是否正确

	if conf.Policy != "" && !array.In(conf.Policy, []string{"random", "round_robin", "weight"}) {
		return fmt.Errorf("policy 只支持 random、round_robin、weight")
	}

	for i, rule := range conf.Rules {
		if rule.Azure && rule.Default {
			return fmt.Errorf("azure 规则暂不能设置为默认规则 #%d", i+1)
		}

		if rule.Azure {
			return fmt.Errorf("azure 模式正在开发中，敬请期待 #%d", i+1)
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
	Proxy           bool           `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	Azure           bool           `yaml:"azure,omitempty" json:"azure,omitempty"`
	AzureAPIVersion string         `yaml:"azure-api-version,omitempty" json:"azure-api-version,omitempty"`
	Rewrite         []ModelRewrite `yaml:"rewrite,omitempty" json:"rewrite,omitempty"`
	// Default 默认规则
	Default bool `yaml:"default,omitempty" json:"default,omitempty"`
	// Backup 备用规则，默认不会使用，只有当出现错误时才会使用
	Backup bool `yaml:"backup,omitempty" json:"backup,omitempty"`
	// Weight 权重，用于 weight 策略，默认值为 1，设置为负数则表示不使用该规则
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

	if err := conf.Validate(); err != nil {
		return nil, err
	}

	return &conf, nil
}
