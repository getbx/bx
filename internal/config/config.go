package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type DNS struct {
	China      string `yaml:"china"`
	FakeipCIDR string `yaml:"fakeip_cidr"`
}

type Rule struct {
	Direct []string `yaml:"direct"`
	Proxy  []string `yaml:"proxy"`
}

type Lists struct {
	ChinaDomain string `yaml:"china_domain"`
	ChinaCIDR   string `yaml:"china_cidr"`
}

type Config struct {
	Server     string   `yaml:"server"`
	Password   string   `yaml:"password"`
	Killswitch bool     `yaml:"killswitch"`
	DNS        DNS      `yaml:"dns"`
	Rules      []Rule   `yaml:"rules"`
	Lists      Lists    `yaml:"lists"`
	Bypass     []string `yaml:"bypass"` // 路由层绕过 tun 的网段(内网/管理网,保 SSH)
	Global     bool     `yaml:"global"` // 全局模式:除 bypass/用户 direct 规则外,一切(含中国)走代理
}

// Parse 解析并校验配置字节。
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if c.Server == "" {
		return nil, fmt.Errorf("config: server 不能为空")
	}
	if c.DNS.China == "" {
		c.DNS.China = "223.5.5.5"
	}
	if c.DNS.FakeipCIDR == "" {
		c.DNS.FakeipCIDR = "198.18.0.0/15"
	}
	return &c, nil
}
