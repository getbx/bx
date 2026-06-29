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
	Mode      string `yaml:"mode"`      // block, direct-realtime, proxy
	Transport string `yaml:"transport"` // 可选:UDP 专用传输链接(如 hysteria2://),仅 mode=proxy 生效。空=UDP 走主传输
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
	Server        string   `yaml:"server"`     // bx:// 链接或内部传输链接(自带凭据;故无独立 password 字段)。= transports 的首条
	Transports    []string `yaml:"transports"` // 可选:有序多传输(优先级,reality 主在前),自动容灾。空=单 [server]
	Killswitch    bool     `yaml:"killswitch"`
	OwnerUID      int      `yaml:"owner_uid"` // 业主 uid(sudo bx setup 捕获);0=无业主,控制面退回 root-only
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
	SingboxURL    string   `yaml:"singbox_url"`    // reality 传输:仅无内嵌 arch 兜底用,下载 sing-box 的地址(linux amd64/arm64 已内嵌,无需设)
	SingboxSHA256 string   `yaml:"singbox_sha256"` // 下载兜底时的校验(设了 singbox_url 才用,强烈建议)
	SingboxBin    string   `yaml:"singbox_bin"`    // 可选:直接指定本地 sing-box 路径(优先级最高,压过内嵌)
}

// Router 是网关模式参数:只代理「源在 lan_cidrs 内」的转发流量。
// 路由器自身流量(源是路由器 IP)永不被劫持 → tailscale/管理流量不受影响。
type Router struct {
	LANCIDRs []string `yaml:"lan_cidrs"` // 源网段;空=运行期自动探测 LAN 接口
}

// decodeServerLink 把 bx://blink:// 换壳链接还原为内部传输链接;裸链接(brook/vless 等)原样返回。
func decodeServerLink(link string) (string, error) {
	if strings.HasPrefix(link, "bx://") || strings.HasPrefix(link, "blink://") {
		return blink.Decode(link)
	}
	return link, nil
}

// Parse 解析并校验配置字节。
func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // 未知字段直接报错,杜绝「配了但静默失效」
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if c.OwnerUID < 0 {
		// 负数 owner_uid 是手改配置的错误:转 uint32 会成巨值、授权不到任何真实用户。
		// 显式拒绝(防 int→uint32 脚枪),正常由 bx setup 写正整数或省略(0=root-only)。
		return nil, fmt.Errorf("config: owner_uid 不能为负: %d", c.OwnerUID)
	}
	// 传输解析:优先 transports(有序多传输 + 自动容灾),否则单 server。
	// 各链接 bx://blink:// 换壳的还原为内部链接;Server 取首条(向后兼容读 cfg.Server 的代码)。
	if len(c.Transports) > 0 {
		for i, link := range c.Transports {
			if strings.TrimSpace(link) == "" {
				return nil, fmt.Errorf("config: transports[%d] 不能为空", i)
			}
			decoded, err := decodeServerLink(link)
			if err != nil {
				return nil, err
			}
			c.Transports[i] = decoded
		}
		c.Server = c.Transports[0]
	} else if c.Server != "" {
		decoded, err := decodeServerLink(c.Server)
		if err != nil {
			return nil, err
		}
		c.Server = decoded
		c.Transports = []string{c.Server}
	} else {
		return nil, fmt.Errorf("config: server 或 transports 至少配一个")
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
	if c.UDP.Transport != "" {
		if c.UDP.Mode != "proxy" {
			return nil, fmt.Errorf("config: udp.transport 仅 udp.mode=proxy 生效,当前 mode=%q", c.UDP.Mode)
		}
		decoded, err := decodeServerLink(c.UDP.Transport)
		if err != nil {
			return nil, fmt.Errorf("config: udp.transport: %w", err)
		}
		c.UDP.Transport = decoded
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
