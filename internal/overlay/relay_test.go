package overlay

import (
	"net/netip"
	"reflect"
	"testing"
)

// known-gaps A12(2026-09-25):ZeroTier 根节点的主机名此前从没被解析过,装上的只有写死的
// 兜底 IP —— 根节点换了 IP 之后,那几条 /32 会把发往陌生人的流量放出隧道。
func TestRelayBypassPrefersResolvedHostsOverTheHardCodedFallback(t *testing.T) {
	zt := Tenant{
		Name:               "zerotier",
		RelayHosts:         []string{"root-a.example.com", "root-b.example.com"},
		RelayFallbackCIDRs: []string{"192.0.2.10/32"},
	}
	ts := Tenant{Name: "tailscale", RelayHosts: []string{"controlplane.example.com"}}

	resolved := func(hosts []string) []netip.Addr {
		if !reflect.DeepEqual(hosts, zt.RelayHosts) {
			t.Fatalf("resolver got %v, want the tenant's whole host list at once", hosts)
		}
		return []netip.Addr{netip.MustParseAddr("203.0.113.5"), netip.MustParseAddr("203.0.113.6")}
	}
	if got := RelayBypassCIDRs([]Tenant{zt, ts}, resolved); !reflect.DeepEqual(got, []string{"203.0.113.5/32", "203.0.113.6/32"}) {
		t.Fatalf("resolved relays = %v, want the resolved /32s and not the stale fallback", got)
	}

	// 解析不出来 ⇒ 兜底(与今天一样);没给解析器 ⇒ 兜底。
	for name, resolve := range map[string]func([]string) []netip.Addr{
		"nothing resolved": func([]string) []netip.Addr { return nil },
		"no resolver":      nil,
	} {
		if got := RelayBypassCIDRs([]Tenant{zt}, resolve); !reflect.DeepEqual(got, []string{"192.0.2.10/32"}) {
			t.Fatalf("%s: relays = %v, want the fallback", name, got)
		}
	}

	// 应答里的私网 / CGNAT / fake-IP / 回环一律丢掉:一个出错或被投毒的应答不许变成
	// 把这些地址放出隧道的旁路。全被丢掉 ⇒ 当作没解析出来,用兜底。
	poisoned := func([]string) []netip.Addr {
		return []netip.Addr{
			netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("100.64.1.1"),
			netip.MustParseAddr("198.18.0.16"), netip.MustParseAddr("127.0.0.1"),
		}
	}
	if got := RelayBypassCIDRs([]Tenant{zt}, poisoned); !reflect.DeepEqual(got, []string{"192.0.2.10/32"}) {
		t.Fatalf("poisoned answer produced %v, want the fallback", got)
	}

	// 没有兜底表的租户(Tailscale 的中继旁路另有来源)这里一条都不出,也不去解析。
	called := false
	if got := RelayBypassCIDRs([]Tenant{ts}, func([]string) []netip.Addr { called = true; return nil }); len(got) != 0 || called {
		t.Fatalf("tailscale relays = %v (resolver called %v), want none and no lookup", got, called)
	}
}
