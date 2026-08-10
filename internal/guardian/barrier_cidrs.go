package guardian

// BlockingBarrierCIDRs 返回屏障实际装载的阻断网段副本。
//
// 清单的规范来源现在是叶子包 internal/barriercidr —— 只读观测层直接引它,不再
// 经由本函数 import guardian(那条边会与 reconcile.go 的 guardian→observe 构成
// import 环)。本函数保留为 guardian 侧的读取入口,读的仍是同一份清单,故二者
// 结构上不可能漂移。
func BlockingBarrierCIDRs() (ipv4, ipv6 []string) {
	return append([]string(nil), publicIPv4Blocks...), append([]string(nil), publicIPv6Blocks...)
}
