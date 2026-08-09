package config

import (
	"bytes"
	"fmt"
	"net"
	"net/netip"
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
	FakeipFilter []string    `yaml:"fakeip_filter"` // 这些域名不分配 fake-IP(本地/反查/其它网络扩展控制域名)
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
	Server     string   `yaml:"server"`     // bx:// 链接或内部传输链接(自带凭据;故无独立 password 字段)。= transports 的首条
	Transports []string `yaml:"transports"` // 可选:有序多传输(优先级,reality 主在前),自动容灾。空=单 [server]
	// Servers 是用户手选的服务器清单(与 Transports 互斥)。一项 = 一对链接。
	Servers []Server `yaml:"servers"`
	// Current 是当前选中的服务器名字(意图,与 desired: on/off 同类)。
	Current    string `yaml:"current"`
	Killswitch bool   `yaml:"killswitch"`
	OwnerUID   int    `yaml:"owner_uid"` // 业主 uid(sudo bx setup 捕获);0=无业主,控制面退回 root-only
	DNS        DNS    `yaml:"dns"`
	Rules      []Rule `yaml:"rules"`
	// Hosts 把域名钉到固定 IPv4,由 bx 自己的 DNS 在 fake-IP 之前直接应答。
	//
	// 存在的理由:bx 开着时,被它接管的应用走系统 DNS(已指向 bx),/etc/hosts
	// 根本不在这条链路上 —— 用户改了却不生效,而且看不出为什么。
	//
	// 只支持「一个域名 → 一个 IPv4 字面量」。通配符/多值/CNAME 的语义需要单独
	// 想清楚,现在加进来只会造成误解。
	Hosts         map[string]string `yaml:"hosts"`
	Lists         Lists             `yaml:"lists"`
	UDP           UDP               `yaml:"udp"`
	Brook         string            `yaml:"brook"`          // 可选调试入口;空=用内嵌传输
	BrookURL      string            `yaml:"brook_url"`      // brook 传输:仅无内嵌 arch(windows/其他)兜底,下载 brook 的地址;空=按版本派生官方 release
	BrookSHA256   string            `yaml:"brook_sha256"`   // 下载兜底时的校验(设了 brook_url 才用,强烈建议)
	DataDir       string            `yaml:"data_dir"`       // 运行期数据目录;空=默认(linux/darwin /var/lib/bx、windows C:\ProgramData\bx)
	Bypass        []string          `yaml:"bypass"`         // 路由层绕过 tun 的网段(内网/管理网,保 SSH)
	Global        bool              `yaml:"global"`         // 全局模式:除 bypass/用户 direct 规则外,一切(含中国)走代理
	Mode          string            `yaml:"mode"`           // host(默认,劫持本机出站) | router(只劫持 LAN 转发流量)
	Router        Router            `yaml:"router"`         // 仅 mode=router 生效
	HTTPProxy     string            `yaml:"http_proxy"`     // 非空:额外开 HTTP 代理(如 127.0.0.1:7890),给只认 HTTP_PROXY 的应用(tailscaled 控制面)
	SingboxURL    string            `yaml:"singbox_url"`    // reality 传输:仅无内嵌 arch 兜底用,下载 sing-box 的地址(linux amd64/arm64 已内嵌,无需设)
	SingboxSHA256 string            `yaml:"singbox_sha256"` // 下载兜底时的校验(设了 singbox_url 才用,强烈建议)
	SingboxBin    string            `yaml:"singbox_bin"`    // 可选:直接指定本地 sing-box 路径(优先级最高,压过内嵌)
}

// Router 是网关模式参数:只代理「源在 lan_cidrs 内」的转发流量。
// 路由器自身流量(源是路由器 IP)永不被劫持 → tailscale/管理流量不受影响。
type Router struct {
	LANCIDRs []string `yaml:"lan_cidrs"` // 源网段;空=运行期自动探测 LAN 接口
}

var defaultFakeipFilter = []string{
	"*.lan",
	"*.local",
	"*.localdomain",
	"*.arpa",
	// Tailscale 控制面与 MagicDNS 必须拿真实解析结果,否则 bx 先接管 DNS/默认路由时,
	// Tailscale 可能无法建立自己的 100.64/10 overlay 路由。
	"tailscale.com",
	"ts.net",
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
	if err := c.resolveServers(); err != nil {
		return nil, err
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
		// Tailscale 域名也保留真实解析,让它能自建 overlay 路由并自行监管 100.x 流量。
		c.DNS.FakeipFilter = append([]string{}, defaultFakeipFilter...)
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
		c.DataDir = DefaultDataDir
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
	// hosts 覆盖:解析期校验,非法值直接拒绝启动。
	//
	// 不在运行期静默丢弃,理由与上面 KnownFields(true) 相同:staticA 是 DNS
	// 第一跳且不受 rules 影响,一个悄悄没生效的覆盖,用户会以为它生效了。
	//
	// 这里只为「拒绝启动」的效果调用 parseHostOverrides,故意不缓存结果 ——
	// HostOverrides() 每次都重新推导,而不是读一个仅由 Parse 填充的字段。
	// 原因:测试/其它代码常常绕开 Parse、直接用字面量构造 Config{Hosts: ...}
	// (internal/supervisor/router_test.go 已经这么干),那种路径下缓存字段
	// 永远是 nil —— 明明配了 Hosts,HostOverrides() 却悄悄报告"没有任何覆盖",
	// 这正是本功能要消灭的那种静默失效,只是从用户的 YAML 搬到了下一个工程师
	// 的测试代码里。
	if _, err := parseHostOverrides(c.Hosts); err != nil {
		return nil, err
	}
	return &c, nil
}

// NormalizeHostName 归一化域名:小写 + 去全部尾点。
//
// Torchfun.com. 与 torchfun.com 必须是同一条 —— 用户按其中一种写法配、DNS 按
// 另一种查,覆盖就静默不生效,而这类静默失效正是本功能要消灭的东西。
//
// 用 TrimRight 而非 TrimSuffix:后者只削一个点,"example.com.." 会被当成与
// "example.com" 不同的独立条目留下来 —— 同一类"看似归一、实则放过畸形输入"
// 的问题,只是换了个位置。
//
// 导出(而非包内私有)是刻意的:域名归一化规则只能有一份实现。这条规则曾经
// 短暂地在 internal/supervisor 里被复刻过一次(只做了小写、漏了去尾点),
// 两份各自维护、彼此靠人工保持一致的实现分道扬镳只是时间问题——出问题的
// 不是"忘了同步",是"两份实现"这个结构本身。任何要判断"两个域名是否指同
// 一条 hosts 覆盖"的调用方(config 包内的 parseHostOverrides、
// internal/supervisor 的 mergeHostOverrides)都必须调这一个函数,不得自己
// 再写一遍 ToLower/TrimRight。
func NormalizeHostName(name string) string {
	return strings.TrimRight(strings.ToLower(strings.TrimSpace(name)), ".")
}

// parseHostOverrides 校验并归一化一份 hosts 映射。Parse 用它在解析期拒绝非法
// 配置;HostOverrides 用它在每次调用时重新推导,两处共享同一套规则,不会走漂。
func parseHostOverrides(hosts map[string]string) (map[string]netip.Addr, error) {
	normalized := make(map[string]netip.Addr, len(hosts))
	for name, value := range hosts {
		host := NormalizeHostName(name)
		if host == "" {
			return nil, fmt.Errorf("config: hosts 的域名不能为空")
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("config: hosts[%s] 的值 %q 不是合法 IP 字面量(只支持 IPv4)", host, value)
		}
		if !addr.Is4() {
			return nil, fmt.Errorf("config: hosts[%s] 只支持 IPv4,得到 %q", host, value)
		}
		if addr.IsUnspecified() {
			return nil, fmt.Errorf("config: hosts[%s] 不能是 0.0.0.0", host)
		}
		if _, dup := normalized[host]; dup {
			return nil, fmt.Errorf("config: hosts 里 %s 出现多次(大小写/尾点归一后相同)", host)
		}
		normalized[host] = addr
	}
	return normalized, nil
}

// HostOverrides 返回归一化并校验过的 hosts 覆盖。
//
// 每次调用都重新推导(不读缓存字段)——见 Parse 里的注释:字面量构造的
// Config{Hosts: ...}(测试代码常见写法)不经过 Parse,若结果来自一个只由
// Parse 填充的缓存字段,那种路径会静默拿到空结果。
//
// 若 c.Hosts 本身非法(唯一途径:绕开 Parse 手造了非法 Config),返回错误
// 而不是 panic。这是调用方的编程错误,不是用户配置错误——Parse 才是校验
// 用户输入的关口,这里假装"没有任何覆盖"糊弄过去只会把同一个"看着配了、
// 其实没生效"的陷阱重新引入。但 bx Core 是被 Guardian 监管的常驻进程,
// 非预期 panic 在这里不是"响亮地失败",而是崩溃循环:Guardian 把它当异常
// 退出重新拉起,新进程带着同一份非法 Config 立刻在同一处再 panic 一次。
// 返回 error 让调用方(尤其 run.go 这条热路径)能把它变成一次干净的启动
// 失败——错误信息可读、进程不重启、不会陷入死循环。
func (c *Config) HostOverrides() (map[string]netip.Addr, error) {
	return parseHostOverrides(c.Hosts)
}
