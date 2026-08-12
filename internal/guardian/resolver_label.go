package guardian

import (
	"net"
	"net/netip"
	"strings"
)

// ResolverLabel 把一个解析器地址渲染成菜单/CLI 直接显示的一行。
//
// **国旗只在能证明的时候出现。** bx 不带 geoip 库,它能离线证明的只有「在不在
// 中国大陆」—— 靠自己已经内嵌的 china CIDR 列表。别的国家一律不给旗:猜一面国旗
// 出来,与给一个认不出的国家编一个时区大区是同一种错,而且更显眼、更容易被当真。
//
// **anycast 让「国家」这个问题本身没有答案**:1.1.1.1 / 8.8.8.8 在每个大洲都有
// 落地,给它挂任何一面旗都是假的。
//
// inChina 为 nil 表示**列表没问出来**,不是「不在中国」——那时同样不给旗。
func ResolverLabel(addr string, inChina func(netip.Addr) bool) string {
	trimmed := strings.TrimSpace(addr)
	if trimmed == "" {
		return ""
	}
	// 配置里允许带 :53,而它不属于「谁在解析」这个答案。
	host := trimmed
	if h, _, err := net.SplitHostPort(trimmed); err == nil {
		host = h
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		// 认不出就原样带出去,不猜。它仍然是用户配的东西,显示它比吞掉有用;
		// 而给它加任何判断都是编造。
		return trimmed
	}
	switch {
	case ip.IsLoopback():
		return host + " (bx)"
	case ip.IsPrivate() || ip.IsLinkLocalUnicast():
		// 查询交给了这台路由器,而它又交给了谁,bx 不知道 —— 这句话本身
		// 就是用户要的信息。
		return host + " (your router)"
	}
	if inChina != nil && inChina(ip) {
		return host + " 🇨🇳"
	}
	return host
}
