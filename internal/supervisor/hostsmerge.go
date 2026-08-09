package supervisor

import (
	"net/netip"
	"sort"
	"strings"
)

// mergeHostOverrides 把用户配置的 hosts 覆盖并进传输服务器的静态 A 记录。
//
// **传输服务器一律优先。** serverStatic 里的条目是防环用的:bx 的隧道子进程靠
// 它们解析到服务器真 IP,再由 bypass 路由送出去。用户若把服务器域名映射到别处,
// 隧道会连到错误的地方 —— 而且是静默的,bx status 照样报健康,因为隧道自己那条
// socks 健康检查走的是同一条错路。所以冲突时忽略用户那条,并把它报出来。
//
// **冲突判定必须大小写不敏感。** serverStatic 的 key 来自 net/url.Hostname()
// (vless://VPS.example.com/... 这类 link),net/url **不做大小写归一**;user 的
// key 来自 cfg.HostOverrides(),已经过 normalizeHostName 全小写。若直接按原始
// key 做 map 查找,"VPS.example.com" 与 "vps.example.com" 会被判定成两个不同
// 域名——冲突检测整个失效,两条各自进了 merged。下游 dns.Server.SetStaticA 会把
// 两个 key 都小写再 append 进同一个桶,一次 A 查询会同时收到服务器真 IP 和用户
// 覆盖 IP:tunnel 连去哪个全看客户端如何选用应答里的第一条,而 bx status 完全
// 看不出这里出过分歧——正是本函数存在的理由要防的那种静默失败。归一化只用来
// 判定冲突,不改 merged 里 serverStatic 那部分 key 的原始大小写(SetStaticA 自己
// 会再做一次小写归一,这里没必要抢它的活)。
//
// 返回三样:合并后的表、真正生效的用户覆盖(供 status 显示,不含被忽略的)、
// 被忽略的域名(已排序,供日志与 status 提示)。
func mergeHostOverrides(
	serverStatic map[string][]netip.Addr,
	user map[string]netip.Addr,
) (merged map[string][]netip.Addr, applied map[string]netip.Addr, ignored []string) {
	merged = make(map[string][]netip.Addr, len(serverStatic)+len(user))
	taken := make(map[string]struct{}, len(serverStatic))
	for host, a := range serverStatic {
		// 深拷贝切片:调用方(run.go)后续还用 serverStatic 做 bypass 路由,不能
		// 让 merged 与它共享底层数组——对 merged[host] 的 append 一旦命中调用方
		// 切片的预留容量,就会静默改写 serverStatic 看不见(len 之外)的那部分
		// 底层内存,而这类"表面没改、内存已改"的陷阱恰恰最难在 code review 里
		// 被发现。
		merged[host] = append([]netip.Addr(nil), a...)
		taken[strings.ToLower(host)] = struct{}{}
	}
	applied = make(map[string]netip.Addr, len(user))
	for host, addr := range user {
		if _, conflict := taken[strings.ToLower(host)]; conflict {
			ignored = append(ignored, host)
			continue
		}
		merged[host] = []netip.Addr{addr}
		applied[host] = addr
	}
	sort.Strings(ignored)
	return merged, applied, ignored
}
