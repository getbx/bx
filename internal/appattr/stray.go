package appattr

import "net/netip"

// StrayConnections 找出**绕过 bx 的连接**:此刻仍从物理网卡以真实 IP 收发、又不是
// 任何人有意这么做的那些。
//
// **为什么需要它(2026-09-25 真机)**:用户 `bx down` 之后 14 秒又 `bx up`,Chrome 在
// 那 14 秒里开的一条连接,36 分钟后(中间还经过一次完整的 Guardian 切换)仍在 en0 上
// 以真实 IP 收发。macOS 不会因为路由表变了就把一条已建立的连接挪进 TUN。bx 的全部
// 保护都建立在路由上,这一类连接它从来看不见 —— 而它们正是泄漏。
//
// 判据四条同时成立:
//   - 本地地址是物理网卡的地址(physical)—— 从 TUN 或别的隧道出去的不算;
//   - 远端是公网单播地址 —— 私网、链路本地、组播、CGNAT 恒直连,本来就不走隧道;
//   - socket **没有**用 IP_BOUND_IF 绑网卡 —— 绑了的是有意为之(Tailscale 这类
//     overlay、bx Core 自己的直连规则拨号器);
//   - 不是 bx 自己的进程(ours)—— 隧道子进程到服务器的连接正是这样出去的。
//
// 纯函数:取数据(sysctl、网卡地址、进程树)是调用方的事。
func StrayConnections(pcbs []PCB, physical []netip.Addr, ours func(pid int32) bool) []PCB {
	local := make(map[netip.Addr]bool, len(physical))
	for _, a := range physical {
		local[a.Unmap()] = true
	}
	var out []PCB
	for _, p := range pcbs {
		if p.BoundToInterface || !local[p.LocalAddr] || !publicUnicast(p.RemoteAddr) {
			continue
		}
		if ours != nil && ours(p.LastPID) {
			continue
		}
		out = append(out, p)
	}
	return out
}

var (
	cgnat = netip.MustParsePrefix("100.64.0.0/10")
	// benchmarking 是 RFC 2544 保留段,也是 bx 的 fake-IP 池(198.18/15)。发往它的
	// 连接到不了任何真实主机,不是泄漏。真机 2026-09-25:trustd 一条绑在物理网卡源地址
	// 上、发往 fake IP 198.18.0.16 的 SYN 卡在 SYN_SENT,被当成了「绕过 bx」。
	benchmarking = netip.MustParsePrefix("198.18.0.0/15")
)

func publicUnicast(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && a.IsGlobalUnicast() && !a.IsPrivate() && !cgnat.Contains(a) && !benchmarking.Contains(a)
}
