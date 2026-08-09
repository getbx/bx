package supervisor

import (
	"net/netip"
	"sort"

	"github.com/getbx/bx/internal/config"
)

// mergeHostOverrides 把用户配置的 hosts 覆盖并进传输服务器的静态 A 记录。
//
// **传输服务器一律优先。** serverStatic 里的条目是防环用的:bx 的隧道子进程靠
// 它们解析到服务器真 IP,再由 bypass 路由送出去。用户若把服务器域名映射到别处,
// 隧道会连到错误的地方 —— 而且是静默的,bx status 照样报健康,因为隧道自己那条
// socks 健康检查走的是同一条错路。所以冲突时忽略用户那条,并把它报出来。
//
// **冲突判定必须与 cfg.HostOverrides() 用同一套归一化规则,而不是各自实现一份。**
// serverStatic 的 key 来自 net/url.Hostname()(vless://VPS.example.com./... 这类
// link),原样保留大小写与尾点;user 的 key 来自 cfg.HostOverrides(),已经
// config.NormalizeHostName 处理过(小写 + 去全部尾点)。这两个 key 空间要在这里
// 判等,就必须共用同一个归一化函数——本函数第一版只补了小写、漏了去尾点("VPS.
// example.com." 与 "vps.example.com" 仍判不出冲突),证明"两处各自实现、靠人工
// 保持一致"这个结构本身就会漂:下一次分歧可能是 IDN/punycode、内部空白,或任何
// NormalizeHostName 未来才会长出的规则。改用 config.NormalizeHostName 本身(而
// 非在这里重新拼一遍它的逻辑),让"两处一致"从一条需要记住的约定变成同一份代码,
// 是唯一能保证不再漂的版本。若直接按原始 key 查找,大小写或尾点不同的同一域名
// 会被判定成"没有冲突"、两条都进 merged——下游 dns.Server.SetStaticA 再各自归一
// 一次并 append 进同一个桶,一次 A 查询同时应答服务器真 IP 与用户 IP,tunnel 连去
// 哪个全看客户端怎么选用应答里的第一条,而 ignored/日志/status 一个字都不会提。
// 归一化只用来判定冲突,不改 merged 里 serverStatic 那部分 key 的原始写法
// (SetStaticA 自己会再归一一次,这里没必要抢它的活)。
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
		taken[config.NormalizeHostName(host)] = struct{}{}
	}
	applied = make(map[string]netip.Addr, len(user))
	for host, addr := range user {
		if _, conflict := taken[config.NormalizeHostName(host)]; conflict {
			ignored = append(ignored, host)
			continue
		}
		merged[host] = []netip.Addr{addr}
		applied[host] = addr
	}
	sort.Strings(ignored)
	return merged, applied, ignored
}
