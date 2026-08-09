package supervisor

import (
	"net/netip"
	"sort"
)

// mergeHostOverrides 把用户配置的 hosts 覆盖并进传输服务器的静态 A 记录。
//
// **传输服务器一律优先。** serverStatic 里的条目是防环用的:bx 的隧道子进程靠
// 它们解析到服务器真 IP,再由 bypass 路由送出去。用户若把服务器域名映射到别处,
// 隧道会连到错误的地方 —— 而且是静默的,bx status 照样报健康,因为隧道自己那条
// socks 健康检查走的是同一条错路。所以冲突时忽略用户那条,并把它报出来。
//
// 返回三样:合并后的表、真正生效的用户覆盖(供 status 显示,不含被忽略的)、
// 被忽略的域名(已排序,供日志与 status 提示)。
func mergeHostOverrides(
	serverStatic map[string][]netip.Addr,
	user map[string]netip.Addr,
) (merged map[string][]netip.Addr, applied map[string]netip.Addr, ignored []string) {
	merged = make(map[string][]netip.Addr, len(serverStatic)+len(user))
	for host, a := range serverStatic {
		merged[host] = a
	}
	applied = make(map[string]netip.Addr, len(user))
	for host, addr := range user {
		if _, taken := serverStatic[host]; taken {
			ignored = append(ignored, host)
			continue
		}
		merged[host] = []netip.Addr{addr}
		applied[host] = addr
	}
	sort.Strings(ignored)
	return merged, applied, ignored
}
