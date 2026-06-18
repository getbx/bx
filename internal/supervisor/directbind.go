package supervisor

import (
	"net"
	"net/netip"

	"github.com/getbx/bx/internal/route"
)

// privateNoBind 是「直连时不应绑物理出口网卡」的目的段:RFC1918 私网、docker 默认池、
// link-local、CGNAT、loopback(v4),以及 v6 的 loopback/link-local/ULA/multicast。
// 这些段由 Hijack 的策略路由 carve 到主表(pref 150)原生投递——绑物理口反而到不了
// lo/docker0/内网邻居。集合刻意等于 route 包里「私网恒直连」的定义,保证两边一致。
var privateNoBind = func() *route.CIDRSet {
	lines := append(append([]string{}, route.DefaultPrivateCIDRs...), route.DefaultPrivateV6CIDRs...)
	s, err := route.NewCIDRSet(lines)
	if err != nil {
		panic("bx: 内建私网 CIDR 解析失败: " + err.Error())
	}
	return s
}()

// shouldBindToDevice 报告对该目的地址的直连是否应额外 SO_BINDTODEVICE 绑物理出口网卡。
// 仅公网目的地返回 true:绑设备让公网直连免疫宿主在 mangle OUTPUT 用 `CONNMARK --restore-mark`
// 清掉 bx 的 SO_MARK 的情况(如 QNAP QTS)——mark 被清后 fwmark 旁路规则失配,直连包会落回 tun
// 自环、公网直连全断。私网/docker/CGNAT/loopback/link-local/组播返回 false(交策略路由原生投递,
// 绑了反而连不通)。地址无法解析时保守返回 false(退化为仅 SO_MARK,与旧行为一致)。
func shouldBindToDevice(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		host = address // 可能是无端口的裸地址
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap().WithZone("") // 去 v4-in-v6 映射与 zone,便于前缀匹配
	if ip.IsUnspecified() || ip.IsMulticast() {
		return false
	}
	return !privateNoBind.Contains(ip)
}
