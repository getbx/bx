// Package explainwords 是「这条连接为什么走这条路」的措辞,一份,供 `bx explain`(Mac)与
// 手机端的 explain 共用 —— 两边对同一个判定说两句不一样的话,是这个仓库罚过的那一类
// (bx status 与 bx doctor 对 Tailscale 各说一句,internal/macnetprobe 就是那次长出来的)。
package explainwords

// labels 是唯一那张表:判定来源 → 用户读的一句话。SourceLabel 查它,AllLabels 列它(手机把这些
// 句子翻成中文,守卫拿 AllLabels 核对每一句都有译文 —— 同一张表,漂不开)。
var labels = []struct {
	sources []string
	label   string
}{
	{[]string{"user_direct", "user_direct_ip"}, "your direct rule"},
	{[]string{"user_proxy", "user_proxy_ip"}, "your proxy rule"},
	{[]string{"user_egress"}, "your named egress"},
	{[]string{"china_domain"}, "built-in china domain list"},
	{[]string{"china_cidr"}, "built-in china IP ranges"},
	{[]string{"private"}, "private network, always direct (global does not affect it)"},
	{[]string{"split_dns"}, "a real IP resolved by split-DNS or fakeip_filter (forced direct)"},
	{[]string{"default"}, "matched no list (default)"},
	{[]string{"udp_proxy"}, "UDP takes the dedicated transport"},
	{[]string{"udp_proxy_fallback"}, "the dedicated UDP transport is unhealthy, falling back to the main one"},
	{[]string{"udp_direct_realtime"}, "udp.mode=direct-realtime (direct, with your real IP)"},
	{[]string{"udp_block"}, "udp.mode=block"},
	{[]string{""}, "(unnamed)"},
}

// SourceLabel 把判定来源(route.Source 的串,或 UDP 的几个来源)翻成用户读的一句话。
// 认不出的来源原样带出去 —— 不冒充看懂了。
func SourceLabel(source string) string {
	for _, l := range labels {
		for _, s := range l.sources {
			if s == source {
				return l.label
			}
		}
	}
	return source
}

// AllLabels 是 SourceLabel 可能说出的每一句(不含原样带出的未知来源)。
func AllLabels() []string {
	out := make([]string, 0, len(labels))
	for _, l := range labels {
		out = append(out, l.label)
	}
	return out
}
