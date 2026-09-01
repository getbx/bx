// Package udpsource 单一来源地持有 UDP 决策的**来源名**。
//
// 这几个字符串是 dialer(记账的一侧)与 stats(汇总的一侧)之间唯一按字面
// 对齐的东西,而两边漂开的后果**彻底静默**:计数照记,汇总却一条都匹配不上,
// 输出与「UDP 完全正常」逐字节相同。
//
// 此前两边**各有一份逐字重复的常量**,靠一条读 dialer.go 源码、检查那几个
// 字符串在不在的守卫盯着。守卫是对的,但它守的是「两份拷贝还一样」——
// 而正确的做法是**让它们没法不一样**。清单下沉到一个不 import 本仓库任何
// 东西的叶子包,两边共读同一份,漂移在构造上就不可能发生
// (与 internal/barriercidr 同一个先例、同一个理由:那次是屏障网段,装它的
// guardian 与问内核的 observe 必须看同一份清单)。
package udpsource

const (
	// Proxy:UDP 经代理正常转发。
	Proxy = "udp_proxy"
	// ProxyFallback:UDP 专用传输不健康,回落到主传输。**通路是好的、只是慢**,
	// 用户完全感知不到 —— 所以它必须被说出来(见 stats 的 UDPNotice)。
	ProxyFallback = "udp_proxy_fallback"
	// DirectRealtime:udp.mode=direct-realtime,以真实 IP 直连换低延迟。
	DirectRealtime = "udp_direct_realtime"
	// ModeBlock:udp.mode=block 下被丢掉的那一档。
	ModeBlock = "udp_block"
)
