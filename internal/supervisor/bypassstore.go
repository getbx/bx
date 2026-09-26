package supervisor

import (
	"net/netip"
	"slices"
	"sync"
)

// bypassStore 是「什么必须绕开隧道」这件事的**唯一权威副本**。
//
// 它持有路由旁路(serverBypass:走物理网关,否则隧道自己连服务器的流量被劫进 TUN)与
// 传输服务器地址(servers)。静态 DNS 应答那一半**不在这里**:它由刷新器经
// setStaticA 直接发给 DNS 服务器 —— 2026-09-25 之前这里还存着一份 statics,
// 写了从来没有生产代码读(只有测试读),而 set(cidrs, statics, servers) 两个同型
// 实参写反照样编译,正是一次复审实测过的屏障开口漏洞的形状。拿掉之后「写反」没有了;
// 「把含 hosts 覆盖的那张表传进来」仍然编译得过,由
// TestBypassRefreshPublishKeepsHostOverridesOutOfBarrierCarveOut 守着。
//
// 为什么要有这么个东西:在此之前,「什么必须绕开隧道」在进程里散着**好几份
// 各自冻结的拷贝** —— liveMutator 一份、livePathRecoverer 一份、dns.Server 一份、
// Hijack 时的实参一份。切服务器只更新其中一份,其余几份继续拿启动时的旧集合干活;
// 尤其 livePathRecoverer 会在一次 Wi-Fi 切换后用**旧集合**重装旁路,把刚切过去的
// 那台服务器漏在外面 —— 静默成环。改成一处更新、所有消费者当场看见。
type bypassStore struct {
	mu     sync.Mutex
	bypass []string
	// servers 是**传输服务器**那一半的静态表:host → 这一轮解析到的地址
	// (不含 tailscale 旁路、不含用户 hosts 覆盖)。
	//
	// 它有两个消费者,两个都要求「只有传输服务器」:
	//   - serverAddrs() → RuntimeState.ServerBypass → cli/guardian.go →
	//     guardian.BarrierContext.ServerBypass:fail-closed `/2` 屏障据此给服务器
	//     IP 开字面的 permit 口子。混进无关地址 = 屏障上的洞,漏掉当前那台 =
	//     屏障生效时把它堵死。
	//   - serverEntries() 是下一轮刷新的**保留基准**。它绝不能含用户 hosts 覆盖:
	//     拿含覆盖的静态表当基准,一次解析失败就会把用户配的任意 IPv4「保留」成
	//     服务器地址,同时进静态表与屏障开口。
	//     按 host 存(而不是拍平成地址列表)正是为了让保留能按 host 查。
	servers map[string][]netip.Addr
}

// newBypassStore 两份视图都显式给出。servers 只含传输服务器(不含用户 hosts 覆盖)。
func newBypassStore(cidrs []string, servers map[string][]netip.Addr) *bypassStore {
	s := &bypassStore{}
	s.set(cidrs, servers)
	return s
}

// set 一次替换两份视图。刻意不提供只改一份的入口:半边更新正是本类型要消灭的故障。
func (s *bypassStore) set(cidrs []string, servers map[string][]netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bypass = append([]string(nil), cidrs...)
	s.servers = cloneStaticA(servers)
}

// serverEntries 返回传输服务器那一半的静态表(供下一轮刷新做保留基准)。
func (s *bypassStore) serverEntries() map[string][]netip.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStaticA(s.servers)
}

// serverAddrs 返回传输服务器地址(供 RuntimeState / Guardian 屏障用)。
// 从 servers 派生而不是另存一份:两份就会有一份先过期,而过期的那份是屏障开口。
// 排序 + 去重只为让输出稳定(status 的 JSON、路由安装顺序),不影响语义。
func (s *bypassStore) serverAddrs() []netip.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return flattenServerAddrs(s.servers)
}

func flattenServerAddrs(servers map[string][]netip.Addr) []netip.Addr {
	var out []netip.Addr
	seen := make(map[netip.Addr]struct{}, len(servers))
	for _, addrs := range servers {
		for _, a := range addrs {
			a = a.Unmap()
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			out = append(out, a)
		}
	}
	slices.SortFunc(out, func(a, b netip.Addr) int { return a.Compare(b) })
	return out
}

func (s *bypassStore) cidrs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bypass...)
}

func cloneStaticA(in map[string][]netip.Addr) map[string][]netip.Addr {
	if in == nil {
		return nil
	}
	out := make(map[string][]netip.Addr, len(in))
	for host, addrs := range in {
		out[host] = append([]netip.Addr(nil), addrs...)
	}
	return out
}
