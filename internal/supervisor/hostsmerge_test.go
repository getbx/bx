package supervisor

import (
	"net/netip"
	"testing"
)

func addrs(ss ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func TestMergeHostOverridesAddsUserEntries(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	user := map[string]netip.Addr{"torchfun.com": netip.MustParseAddr("127.0.0.1")}

	merged, applied, ignored := mergeHostOverrides(server, user)

	if got := merged["torchfun.com"]; len(got) != 1 || got[0].String() != "127.0.0.1" {
		t.Fatalf("torchfun.com = %v", got)
	}
	if got := merged["vps.example.com"]; len(got) != 1 || got[0].String() != "203.0.113.20" {
		t.Fatalf("服务器条目不该被动过,实际 %v", got)
	}
	if len(applied) != 1 || applied["torchfun.com"].String() != "127.0.0.1" {
		t.Fatalf("applied = %v", applied)
	}
	if len(ignored) != 0 {
		t.Fatalf("没有冲突时 ignored 应为空,实际 %v", ignored)
	}
}

// 传输服务器的静态 A 不可被用户覆盖。
//
// 那些条目是防环用的:隧道子进程靠它们解析到服务器真 IP 再走 bypass 路由。
// 用户覆盖了会让隧道静默连到错误的地方 —— bx status 照样报健康,因为隧道
// 自己那条 socks 健康检查走的是同一条错路。这是本功能最危险的失败模式。
func TestMergeHostOverridesNeverOverridesTransportServer(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	user := map[string]netip.Addr{"vps.example.com": netip.MustParseAddr("127.0.0.1")}

	merged, applied, ignored := mergeHostOverrides(server, user)

	if got := merged["vps.example.com"]; len(got) != 1 || got[0].String() != "203.0.113.20" {
		t.Fatalf("服务器域名必须保持真 IP,实际 %v —— 隧道会静默连错地方", got)
	}
	if _, ok := applied["vps.example.com"]; ok {
		t.Fatal("被忽略的条目不得出现在 applied 里(否则 status 会谎报它生效了)")
	}
	if len(ignored) != 1 || ignored[0] != "vps.example.com" {
		t.Fatalf("必须报出被忽略的域名,实际 %v", ignored)
	}
}

// 不改传入的 map:调用方还要用 serverStatic 做别的事(bypass 路由)。
func TestMergeHostOverridesDoesNotMutateInputs(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	user := map[string]netip.Addr{"torchfun.com": netip.MustParseAddr("127.0.0.1")}

	mergeHostOverrides(server, user)

	if len(server) != 1 {
		t.Fatalf("serverStatic 被改动了:%v", server)
	}
	if len(user) != 1 {
		t.Fatalf("user 被改动了:%v", user)
	}
}

func TestMergeHostOverridesWithNoUserEntries(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	merged, applied, ignored := mergeHostOverrides(server, nil)
	if len(merged) != 1 || len(applied) != 0 || len(ignored) != 0 {
		t.Fatalf("merged=%v applied=%v ignored=%v", merged, applied, ignored)
	}
}
