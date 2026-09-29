// Package explainwords 是「这条连接为什么走这条路」的措辞,一份,供 `bx explain`(Mac)与
// 手机端的 explain 共用 —— 两边对同一个判定说两句不一样的话,是这个仓库罚过的那一类
// (bx status 与 bx doctor 对 Tailscale 各说一句,internal/macnetprobe 就是那次长出来的)。
package explainwords

// SourceLabel 把判定来源(route.Source 的串,或 UDP 的几个来源)翻成用户读的一句话。
// 认不出的来源原样带出去 —— 不冒充看懂了。
func SourceLabel(source string) string {
	switch source {
	case "user_direct", "user_direct_ip":
		return "your direct rule"
	case "user_proxy", "user_proxy_ip":
		return "your proxy rule"
	case "user_egress":
		return "your named egress"
	case "china_domain":
		return "built-in china domain list"
	case "china_cidr":
		return "built-in china IP ranges"
	case "private":
		return "private network, always direct (global does not affect it)"
	case "split_dns":
		return "a real IP resolved by split-DNS or fakeip_filter (forced direct)"
	case "default":
		return "matched no list (default)"
	case "udp_proxy":
		return "UDP takes the dedicated transport"
	case "udp_proxy_fallback":
		return "the dedicated UDP transport is unhealthy, falling back to the main one"
	case "udp_direct_realtime":
		return "udp.mode=direct-realtime (direct, with your real IP)"
	case "udp_block":
		return "udp.mode=block"
	case "":
		return "(unnamed)"
	}
	return source
}
