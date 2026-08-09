package supervisor

import (
	"net/netip"
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
	// servers 只是**传输服务器**那些地址(不含 tailscale 旁路、不含用户 hosts 覆盖)。
	// 它经 RuntimeState.ServerBypass → cli/guardian.go → guardian 屏障的
	// BarrierContext.ServerBypass:屏障据此给服务器 IP 开口子。混进无关网段会
	// 把屏障开大,漏掉当前那台则会在屏障生效时把它堵死。
	servers []netip.Addr
}

// newBypassStore 三份视图全部显式给出。**刻意不从 statics 推 servers**:
// statics 里含用户 hosts 覆盖,推出来的 servers 会把那些 IP 带进屏障开口。
func newBypassStore(cidrs []string, statics map[string][]netip.Addr, servers []netip.Addr) *bypassStore {
	s := &bypassStore{}
	s.set(cidrs, statics, servers)
	return s
}

// set 一次替换三份视图。刻意不提供只改一份的入口:半边更新正是本类型要消灭的故障。
func (s *bypassStore) set(cidrs []string, statics map[string][]netip.Addr, servers []netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bypass = append([]string(nil), cidrs...)
	s.statics = cloneStaticA(statics)
	s.servers = append([]netip.Addr(nil), servers...)
}

// serverAddrs 返回传输服务器地址(供 RuntimeState / Guardian 屏障用)。
func (s *bypassStore) serverAddrs() []netip.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]netip.Addr(nil), s.servers...)
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
