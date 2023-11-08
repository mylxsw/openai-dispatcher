package config

import (
	"encoding/json"
	"fmt"
	"gopkg.in/yaml.v2"
	"os"
)

type Config struct {
	Listen string   `yaml:"listen" json:"listen"`
	Socks5 string   `yaml:"socks5" json:"socks5"`
	Keys   []string `yaml:"keys" json:"-"`
	Rules  Rules    `yaml:"rules" json:"rules"`
}

func (conf *Config) Validate() error {
	// TODO 检查配置是否正确
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
	Servers         []string       `yaml:"servers" json:"servers"`
	Keys            []string       `yaml:"keys" json:"-"`
	Models          []string       `yaml:"models" json:"models"`
	Proxy           bool           `yaml:"proxy,omitempty" json:"proxy,omitempty"`
	Azure           bool           `yaml:"azure,omitempty" json:"azure,omitempty"`
	AzureAPIVersion string         `yaml:"azure-api-version,omitempty" json:"azure-api-version,omitempty"`
	Rewrite         []ModelRewrite `yaml:"rewrite,omitempty" json:"rewrite,omitempty"`
	Default         bool           `yaml:"default,omitempty" json:"default,omitempty"`
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
