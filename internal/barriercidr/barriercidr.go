// Package barriercidr 单一来源地持有屏障实际装载的阻断网段。
//
// 这四条 IPv4 + 四条 IPv6 的 /2 reject 路由,比 Core 的 /1 split-default 更长,
// 按最长前缀匹配压过一切。装它的是 internal/guardian,问内核"它在不在"的是
// internal/observe —— 两边必须看同一份清单,否则观测会去问错的网段、然后信心
// 十足地报告"屏障不在"。
//
// 清单单独成一个叶子包(不 import 本仓库任何东西),是为了让这两边都能引用它
// 而不互相 import:internal/observe 原先直接 import guardian 取这份清单,而
// guardian 侧的调谐判据(reconcile.go)需要反过来 import observe 的观测类型,
// 二者构成 import 环。把清单下沉到叶子上,环从结构上就不可能再出现。
package barriercidr

var (
	ipv4Blocks = []string{"0.0.0.0/2", "64.0.0.0/2", "128.0.0.0/2", "192.0.0.0/2"}
	ipv6Blocks = []string{"::/2", "4000::/2", "8000::/2", "c000::/2"}
)

// Blocking 返回阻断网段的副本。返副本而非底层切片,免得任一调用方就地改动
// 污染另一方看到的清单。
func Blocking() (ipv4, ipv6 []string) {
	return append([]string(nil), ipv4Blocks...), append([]string(nil), ipv6Blocks...)
}
