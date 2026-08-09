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

// 传输服务器域名的大小写不能绕过冲突检测。
//
// serverStatic 的 key 来自 net/url.Hostname()(不做大小写归一,link 里写
// VPS.example.com 就原样留着),user 的 key 来自 cfg.HostOverrides()(已经
// normalizeHostName 全小写)。若冲突判定按原始 key 直接 map 查找,这两个
// key 空间对不上,同一个域名的大小写变体会被误判成"没有冲突"、两条都进
// merged —— 下游 dns.Server.SetStaticA 再各自小写一次,会把两条 append 进
// 同一个桶,一次查询同时应答服务器真 IP 与用户 IP,而 ignored/status 一个
// 字都不会提。
func TestMergeHostOverridesNeverOverridesTransportServerCaseInsensitive(t *testing.T) {
	server := map[string][]netip.Addr{"VPS.example.com": addrs("203.0.113.20")}
	user := map[string]netip.Addr{"vps.example.com": netip.MustParseAddr("127.0.0.1")}

	merged, applied, ignored := mergeHostOverrides(server, user)

	if got := merged["VPS.example.com"]; len(got) != 1 || got[0].String() != "203.0.113.20" {
		t.Fatalf("服务器域名必须保持真 IP,实际 %v —— 隧道会静默连错地方", got)
	}
	if got, ok := merged["vps.example.com"]; ok {
		t.Fatalf("不得在 merged 里留下大小写不同的第二份条目(会被 SetStaticA 小写后 append 进同一个桶):%v", got)
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

// 不止「不改 map 本身」,连 map 里切片的底层数组也不能共享。
//
// merged[host] = a 只拷贝切片头,不拷贝数据;若 serverStatic 的切片带预留
// 容量,对 merged[host] 的后续 append 会复用同一块底层数组、静默改写
// serverStatic 在 len 之外仍持有的那部分内存——len 不变,map 长度也不变,
// 上面那条"不改 map"的测试完全看不出问题,却是留给下一个调用方的活陷阱。
func TestMergeHostOverridesDoesNotShareBackingArrayWithServerStatic(t *testing.T) {
	backing := make([]netip.Addr, 1, 4) // cap=4:留出讓 append 复用同一数组的空间
	backing[0] = netip.MustParseAddr("203.0.113.20")
	server := map[string][]netip.Addr{"vps.example.com": backing}

	merged, _, _ := mergeHostOverrides(server, nil)
	merged["vps.example.com"] = append(merged["vps.example.com"], netip.MustParseAddr("198.51.100.1"))

	// 越过 backing 自己的 len、直接看它的满容量视图:若 merged 与它共享底层
	// 数组,上面的 append 会把新元素写进这里,尽管 backing 本身的 len 仍是 1。
	full := backing[:cap(backing)]
	if full[1].IsValid() {
		t.Fatalf("对 merged 的 append 写进了 serverStatic 切片的底层数组(len 之外仍可见):%v", full[1])
	}
}

func TestMergeHostOverridesWithNoUserEntries(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	merged, applied, ignored := mergeHostOverrides(server, nil)
	if len(merged) != 1 || len(applied) != 0 || len(ignored) != 0 {
		t.Fatalf("merged=%v applied=%v ignored=%v", merged, applied, ignored)
	}
}
