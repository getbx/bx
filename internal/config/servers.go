package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/getbx/bx/internal/tunnel"
)

// Server 是清单里的一台服务器:一对链接(主传输 + 可选 UDP 专用传输)。
//
// 为什么是一对而不是一条:reality 走 TCP、hysteria2 走 UDP,两条链接指向同一台
// 主机是本项目的常规部署(按类分流)。把它们拆成两个独立概念,用户就得手工保证
// 两边同步 —— 而「切了 TCP 忘了切 UDP」是个不报错、还能用、但出口不是你以为那台
// 的状态。
type Server struct {
	Name string `yaml:"name"`
	Link string `yaml:"link"`
	UDP  string `yaml:"udp"`
}

const maxServerNameLen = 32

var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateServerName 校验服务器名字。非法即报错,不静默修正 —— 一个被悄悄改过的
// 名字会让 `bx server use <你写的那个>` 找不到,而用户看不出为什么。
func ValidateServerName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("服务器名字不能为空")
	}
	if len(name) > maxServerNameLen {
		return fmt.Errorf("服务器名字过长(%d > %d): %q", len(name), maxServerNameLen, name)
	}
	if !serverNamePattern.MatchString(name) {
		return fmt.Errorf("服务器名字只允许字母数字与 . _ -,got %q", name)
	}
	return nil
}

// DeriveServerName 在用户没给 --name 时,从主链接取主机名当名字。
func DeriveServerName(link string) (string, error) {
	host, err := tunnel.ServerHost(link)
	if err != nil {
		return "", fmt.Errorf("从链接推导服务器名字: %w", err)
	}
	if err := ValidateServerName(host); err != nil {
		return "", err
	}
	return host, nil
}

// normalizeServerName 是比较用的形式(大小写不敏感);存储始终保留用户给的原样。
func normalizeServerName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// resolveServers 校验清单、解链接壳、定位 current,并把结果归一到 c.Server /
// c.UDP.Transport / c.Transports —— 下游(bypass、dialer、status)一行都不用改。
//
// c.Transports 刻意只放**当前那一条**:run.go 用 len(cfg.Transports)>1 决定要不要
// 启动 runFailover,而自动容灾正是用户明确要求退场的东西。
func (c *Config) resolveServers() error {
	if len(c.Servers) == 0 {
		return nil
	}
	if len(c.Transports) > 0 {
		return fmt.Errorf("config: servers 与 transports 不能同时配置 —— " +
			"transports 是自动容灾优先级表(不健康就自动切),servers 是用户手选的清单," +
			"两者都是「用哪条传输」的主人;请删掉 transports")
	}
	seen := map[string]int{}
	for i := range c.Servers {
		s := &c.Servers[i]
		if err := ValidateServerName(s.Name); err != nil {
			return fmt.Errorf("config: servers[%d]: %w", i, err)
		}
		key := normalizeServerName(s.Name)
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("config: servers[%d].name %q 与 servers[%d] 重名(名字比较不区分大小写)", i, s.Name, prev)
		}
		seen[key] = i
		if strings.TrimSpace(s.Link) == "" {
			return fmt.Errorf("config: servers[%d].link 不能为空", i)
		}
		decoded, err := decodeServerLink(s.Link)
		if err != nil {
			return fmt.Errorf("config: servers[%d].link: %w", i, err)
		}
		s.Link = decoded
		if strings.TrimSpace(s.UDP) != "" {
			decodedUDP, err := decodeServerLink(s.UDP)
			if err != nil {
				return fmt.Errorf("config: servers[%d].udp: %w", i, err)
			}
			s.UDP = decodedUDP
		} else {
			s.UDP = ""
		}
	}
	if strings.TrimSpace(c.Current) == "" {
		c.Current = c.Servers[0].Name
	}
	idx, ok := seen[normalizeServerName(c.Current)]
	if !ok {
		return fmt.Errorf("config: current %q 不在 servers 清单里", c.Current)
	}
	cur := c.Servers[idx]
	c.Current = cur.Name // 存回清单里的原样拼写
	c.Server = cur.Link
	c.Transports = []string{cur.Link}
	c.UDP.Transport = cur.UDP
	return nil
}

// CurrentServer 返回当前选中的那台;没有清单时返回 false。
func (c *Config) CurrentServer() (Server, bool) {
	for _, s := range c.Servers {
		if normalizeServerName(s.Name) == normalizeServerName(c.Current) {
			return s, true
		}
	}
	return Server{}, false
}
