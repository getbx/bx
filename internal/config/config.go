package config

import (
	"fmt"
	"time"

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
	AutoUpdate  *bool  `yaml:"auto_update"` // nil=默认 true
	Interval    string `yaml:"interval"`    // 如 "24h";空=默认 24h
}

// AutoUpdateEnabled 报告是否启用列表自动刷新(默认 true)。
func (l Lists) AutoUpdateEnabled() bool {
	if l.AutoUpdate == nil {
		return true
	}
	return *l.AutoUpdate
}

// RefreshInterval 返回刷新间隔(非法/空时回退 24h)。
func (l Lists) RefreshInterval() time.Duration {
	if d, err := time.ParseDuration(l.Interval); err == nil && d > 0 {
		return d
	}
	return 24 * time.Hour
}

type Config struct {
	Server     string   `yaml:"server"` // brook 链接(自带凭据;故无独立 password 字段)
	Killswitch bool     `yaml:"killswitch"`
	DNS        DNS      `yaml:"dns"`
	Rules      []Rule   `yaml:"rules"`
	Lists      Lists    `yaml:"lists"`
	Brook      string   `yaml:"brook"`    // 可选;空=用内嵌 brook
	DataDir    string   `yaml:"data_dir"` // 运行期数据目录;空=默认 /var/lib/bx
	Bypass     []string `yaml:"bypass"`   // 路由层绕过 tun 的网段(内网/管理网,保 SSH)
	Global     bool     `yaml:"global"`   // 全局模式:除 bypass/用户 direct 规则外,一切(含中国)走代理
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
	if c.DataDir == "" {
		c.DataDir = "/var/lib/bx"
	}
	return &c, nil
}
