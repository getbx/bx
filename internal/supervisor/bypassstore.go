package supervisor

import (
	"net/netip"
	"slices"
	"sync"
)

// bypassStore 是「什么必须绕开隧道」这件事的**唯一权威副本**。
//
// 它同时持有两半,因为它们本来就是一件事的两半、必须一起更新:
//   - serverBypass:走物理网关的路由旁路(否则隧道自己连服务器的流量被劫进 TUN);
//   - staticA:本地静态 DNS 应答(否则子进程解析自己的服务器域名会落进 fake-IP)。
//
// 为什么要有这么个东西:在此之前,「什么必须绕开隧道」在进程里散着**好几份
// 各自冻结的拷贝** —— liveMutator 一份、livePathRecoverer 一份、dns.Server 一份、
// Hijack 时的实参一份。切服务器只更新其中一份,其余几份继续拿启动时的旧集合干活;
// 尤其 livePathRecoverer 会在一次 Wi-Fi 切换后用**旧集合**重装旁路,把刚切过去的
// 那台服务器漏在外面 —— 静默成环。改成一处更新、所有消费者当场看见。
type bypassStore struct {
	mu      sync.Mutex
	bypass  []string
	statics map[string][]netip.Addr
	// servers 是**传输服务器**那一半的静态表:host → 这一轮解析到的地址
	// (不含 tailscale 旁路、不含用户 hosts 覆盖)。
	//
	// 它有两个消费者,两个都要求「只有传输服务器」:
	//   - serverAddrs() → RuntimeState.ServerBypass → cli/guardian.go →
	//     guardian.BarrierContext.ServerBypass:fail-closed `/2` 屏障据此给服务器
	//     IP 开字面的 permit 口子。混进无关地址 = 屏障上的洞,漏掉当前那台 =
	//     屏障生效时把它堵死。
	//   - serverEntries() 是下一轮刷新的**保留基准**。它必须与 statics 分开:
	//     statics 含用户 hosts 覆盖,拿它当基准就会在一次解析失败时把用户配的
	//     任意 IPv4 「保留」成服务器地址,同时进静态表与屏障开口。
	//     按 host 存(而不是拍平成地址列表)正是为了让保留能按 host 查。
	servers map[string][]netip.Addr
}

// newBypassStore 三份视图全部显式给出。**刻意不从 statics 推 servers**:
// statics 里含用户 hosts 覆盖,推出来的 servers 会把那些 IP 带进屏障开口。
func newBypassStore(cidrs []string, statics, servers map[string][]netip.Addr) *bypassStore {
	s := &bypassStore{}
	s.set(cidrs, statics, servers)
	return s
}

// set 一次替换三份视图。刻意不提供只改一份的入口:半边更新正是本类型要消灭的故障。
func (s *bypassStore) set(cidrs []string, statics, servers map[string][]netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bypass = append([]string(nil), cidrs...)
	s.statics = cloneStaticA(statics)
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

func (s *bypassStore) staticEntries() map[string][]netip.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneStaticA(s.statics)
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
