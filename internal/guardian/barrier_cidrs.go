package guardian

// BlockingBarrierCIDRs 返回屏障实际装载的阻断网段副本,供只读观测复用。
//
// 观测层需要问内核"这些 reject 路由在不在"。让它复用同一份清单,避免二者漂移
// 导致观测问错网段。
func BlockingBarrierCIDRs() (ipv4, ipv6 []string) {
	return append([]string(nil), publicIPv4Blocks...), append([]string(nil), publicIPv6Blocks...)
}
