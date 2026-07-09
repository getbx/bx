package supervisor

// singleTableDirectCIDRs:单一路由表平台(macOS / Windows)下保持原生直连(经物理网关旁路)的
// 私网段——RFC1918 + docker 默认池。darwin 与 windows 的 Hijack 共用此单一真相源(无 build tag)。
//
// 刻意不含 loopback(127/8)/link-local(169.254/16):它们已有正确本地路由,绝不可改写。
// 刻意不含 CGNAT(100.64.0.0/10):单路由表下把它 route → 物理网关会和 tailscale 的同前缀
// overlay 路由冲突,断掉主动连 tailscale peer;tailscale 的 100.64/10 比 split-default 的 0/1
// 更具体、按最长前缀自然抢赢,无 tailscale 时本地 CGNAT 子网亦有更具体 connected 路由直连。
// 与 route.DefaultPrivateCIDRs(Linux 多表用)刻意分开:那套含 CGNAT,单表平台不能认领。
var singleTableDirectCIDRs = []string{
	"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
}
