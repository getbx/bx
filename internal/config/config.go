package config

import (
	"bytes"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/getbx/bx/internal/blink"
	"gopkg.in/yaml.v3"
)

// SplitRule:把匹配域名交给指定内网 DNS 解析(并由分流层强制直连)。
type SplitRule struct {
	Domains []string `yaml:"domains"` // 支持 *.suffix 通配
	Server  string   `yaml:"server"`  // 内网 DNS;无端口时补 :53
}

type DNS struct {
	China        string      `yaml:"china"`
	FakeipCIDR   string      `yaml:"fakeip_cidr"`
	Split        []SplitRule `yaml:"split"`
	FakeipFilter []string    `yaml:"fakeip_filter"` // 这些域名不分配 fake-IP(本地/反查域名,代理无意义)
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

type UDP struct {
	Mode string `yaml:"mode"` // block, direct-realtime, proxy
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
	Server        string   `yaml:"server"` // bx:// 链接或内部传输链接(自带凭据;故无独立 password 字段)
	Killswitch    bool     `yaml:"killswitch"`
	DNS           DNS      `yaml:"dns"`
	Rules         []Rule   `yaml:"rules"`
	Lists         Lists    `yaml:"lists"`
	UDP           UDP      `yaml:"udp"`
	Brook         string   `yaml:"brook"`          // 可选调试入口;空=用内嵌传输
	DataDir       string   `yaml:"data_dir"`       // 运行期数据目录;空=默认 /var/lib/bx
	Bypass        []string `yaml:"bypass"`         // 路由层绕过 tun 的网段(内网/管理网,保 SSH)
	Global        bool     `yaml:"global"`         // 全局模式:除 bypass/用户 direct 规则外,一切(含中国)走代理
	Mode          string   `yaml:"mode"`           // host(默认,劫持本机出站) | router(只劫持 LAN 转发流量)
	Router        Router   `yaml:"router"`         // 仅 mode=router 生效
	HTTPProxy     string   `yaml:"http_proxy"`     // 非空:额外开 HTTP 代理(如 127.0.0.1:7890),给只认 HTTP_PROXY 的应用(tailscaled 控制面)
	SingboxURL    string   `yaml:"singbox_url"`    // reality 传输:按需下载 sing-box 的地址(托管在自己 VPS)
	SingboxSHA256 string   `yaml:"singbox_sha256"` // 下载校验(强烈建议设置)
	SingboxBin    string   `yaml:"singbox_bin"`    // 可选:直接指定本地 sing-box 路径(免下载)
}

// Router 是网关模式参数:只代理「源在 lan_cidrs 内」的转发流量。
// 路由器自身流量(源是路由器 IP)永不被劫持 → tailscale/管理流量不受影响。
type Router struct {
	LANCIDRs []string `yaml:"lan_cidrs"` // 源网段;空=运行期自动探测 LAN 接口
}

// Parse 解析并校验配置字节。
func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // 未知字段直接报错,杜绝「配了但静默失效」
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if c.Server == "" {
		return nil, fmt.Errorf("config: server 不能为空")
	}
	if strings.HasPrefix(c.Server, "bx://") || strings.HasPrefix(c.Server, "blink://") {
		link, err := blink.Decode(c.Server)
		if err != nil {
			return nil, err
		}
		c.Server = link
	}
	if c.DNS.China == "" {
		c.DNS.China = "223.5.5.5"
	}
	if c.DNS.FakeipCIDR == "" {
		c.DNS.FakeipCIDR = "198.18.0.0/15"
	}
	if c.DNS.FakeipFilter == nil {
		// 本地/反查域名永不该走 fake-IP(代理它们无意义,且会破坏本地解析);
		// 与 mihomo/sing-box 的 fake-ip-filter 默认一致。
		c.DNS.FakeipFilter = []string{"*.lan", "*.local", "*.localdomain", "*.arpa"}
	}
	if c.UDP.Mode == "" {
		c.UDP.Mode = "proxy"
	}
	switch c.UDP.Mode {
	case "block", "direct-realtime", "proxy":
	default:
		return nil, fmt.Errorf("config: udp.mode 必须是 block/direct-realtime/proxy, got %q", c.UDP.Mode)
	}
	for i := range c.DNS.Split {
		r := &c.DNS.Split[i]
		if len(r.Domains) == 0 {
			return nil, fmt.Errorf("config: dns.split[%d].domains 不能为空", i)
		}
		if strings.TrimSpace(r.Server) == "" {
			return nil, fmt.Errorf("config: dns.split[%d].server 不能为空", i)
		}
		if host, port, err := net.SplitHostPort(r.Server); err != nil {
			r.Server = net.JoinHostPort(strings.Trim(r.Server, "[]"), "53") // 无端口补 :53(strip 裸 [::1] 的括号)
		} else if port == "" {
			r.Server = net.JoinHostPort(host, "53") // 形如 "10.0.13.23:" 的空端口也补 :53
		}
	}
	if c.DataDir == "" {
		c.DataDir = "/var/lib/bx"
	}
	if c.Mode == "" {
		c.Mode = "host"
	}
	switch c.Mode {
	case "host", "router":
	default:
		return nil, fmt.Errorf("config: mode 必须是 host/router, got %q", c.Mode)
	}
	for i, cidr := range c.Router.LANCIDRs {
		if _, _, err := net.ParseCIDR(strings.TrimSpace(cidr)); err != nil {
			return nil, fmt.Errorf("config: router.lan_cidrs[%d] 不是合法 CIDR: %q", i, cidr)
		}
	}
	return &c, nil
}
