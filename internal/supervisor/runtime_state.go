package supervisor

import (
	"net/netip"
	"sync/atomic"
)

// RuntimeState is the non-secret handoff contract consumed by Guardian.
type RuntimeState struct {
	Version   string `json:"version"`
	PID       int    `json:"pid"`
	TunName   string `json:"tun_name"`
	SocksAddr string `json:"socks_addr"`
	// ServerHost 是**此刻**主传输指向的服务器主机。
	//
	// 与 ConfigPath 同一条纪律:发布运行中的值。启动时算一次的那个版本在
	// `bx server use` 热切之后会报旧服务器(2026-08-14 真机:出口已经是 166,
	// 而 status 的「节点」还写着 195)。
	ServerHost string `json:"server_host,omitempty"`
	// ConfigPath 是 Core **此刻在用**的配置文件(与 DNSUpstream 同一条纪律:
	// 发布运行中的值,不是盘上的值)。点名一条坏规则时要告诉用户去哪改,
	// 而 stats 是叶子包、不该自己猜一份路径常量。
	ConfigPath      string   `json:"config_path,omitempty"`
	ServerBypass    []string `json:"server_bypass"`
	TunnelHealthy   bool     `json:"tunnel_healthy"`
	DNSListening    bool     `json:"dns_listening"`
	RoutesInstalled bool     `json:"routes_installed"`
	UDPRequired     bool     `json:"udp_required"`
	UDPReady        bool     `json:"udp_ready"`
	// DNSUpstream 是 Core **此刻正在用**的直连解析器(config 的 dns.china)。
	//
	// **发布运行中的值而不是盘上的值,是有意的。** 有人改了 config 文件时,Core
	// 仍然跑着旧值直到重启;显示文件里的值而 Core 用着另一个,正是这个仓库一直在
	// 打的那类谎。两者不一致本身是一条发现,但那要由比较得出,不能靠这里悄悄换源。
	DNSUpstream string `json:"dns_upstream,omitempty"`
}

type routeReadiness struct {
	installed atomic.Bool
}

func (r *routeReadiness) set(installed bool) {
	r.installed.Store(installed)
}

func (r *routeReadiness) ready() bool {
	return r.installed.Load()
}

func runtimeIPv4Bypass(addrs []netip.Addr) []string {
	seen := make(map[string]struct{}, len(addrs))
	out := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		addr = addr.Unmap()
		if !addr.Is4() {
			continue
		}
		cidr := netip.PrefixFrom(addr, 32).String()
		if _, ok := seen[cidr]; ok {
			continue
		}
		seen[cidr] = struct{}{}
		out = append(out, cidr)
	}
	return out
}

func udpRuntimeReadiness(mode string, primaryHealthy, companionHealthy func() bool) (required, ready bool) {
	if mode != "proxy" {
		return false, false
	}
	if companionHealthy != nil {
		return true, companionHealthy()
	}
	return true, primaryHealthy != nil && primaryHealthy()
}
