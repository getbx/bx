package leakcheck

import "bytes"

// cfChallengeMarkers 是 Cloudflare 人机挑战页的特征。
//
// **这两个串是 2026-09-13 从真机上 `claude.ai` 的 403 body 里取的**,不是猜的。
// 带浏览器 UA 重试仍然是同一页 —— 它不是 UA 检测,是 JS 挑战或 TLS 指纹,
// 命令行过不去。把它判成「不可达」会把一台**完全正常的机器**说成用不了。
var cfChallengeMarkers = [][]byte{
	[]byte("challenges.cloudflare.com"),
	[]byte("Just a moment"),
}

// regionRefusalMarkers 是服务自己说「你这个地区不行」的特征。
//
// **今天没有真机样本**(spec §3.3):这台机器的出口在支持区。它仍然要实现 ——
// 不实现的话地区拒绝会落进 undetermined,而那恰恰是我们最想答对的一种。
// 真机见到之后回来把真实串补进来。
var regionRefusalMarkers = [][]byte{
	[]byte("not available in your region"),
	[]byte("unsupported_country"),
	[]byte("country_not_supported"),
}

// JudgeReach 把一次探测的结果判成四态。**判据同时看状态码与 body。**
//
// 只看状态码会判反:`generativelanguage.googleapis.com` 的 403 是 API 在正常
// 应答,`chatgpt.com` 的 403 是人机挑战 —— 同一个码,相反的两件事(spec §2.3)。
//
// 判据按**先后顺序**取,前一条命中就不再往下看。
func JudgeReach(status int, body []byte, dialErr error) ReachState {
	// ① 连都没连上 —— 这时 status/body 没有意义。
	if dialErr != nil {
		return ReachUnreachable
	}
	head := body
	if len(head) > 4096 {
		head = head[:4096]
	}
	// ② 服务明说地区不行。**排在 CF 之前** —— 地区拒绝也可能由 CF 边缘发出,
	//    而「被拒」比「没问出来」信息量大得多。
	for _, m := range regionRefusalMarkers {
		if bytes.Contains(head, m) {
			return ReachRefused
		}
	}
	// ③ CF 人机挑战。
	for _, m := range cfChallengeMarkers {
		if bytes.Contains(head, m) {
			return ReachUndetermined
		}
	}
	// ④ 服务自己的 JSON 在说话 —— 最强的可达证据,与状态码无关。
	if looksLikeServiceJSON(head) {
		return ReachReachable
	}
	// ⑤ 没有 body 的 2xx/404:服务器应答了。
	//    404 是「没这个文件」,那也是应答。
	if len(bytes.TrimSpace(head)) == 0 && (status == 404 || (status >= 200 && status < 300)) {
		return ReachReachable
	}
	// ⑥ 2xx 且不是挑战页:真的拿到了东西(favicon 走这一支)。
	if status >= 200 && status < 300 {
		return ReachReachable
	}
	// ⑦ 4xx/5xx 且 body 是 HTML 而非 JSON:多半是某种拦截页,但我们认不出。
	//    **认不出就是认不出**,不猜。
	return ReachUndetermined
}

// looksLikeServiceJSON 判断 body 是不是服务自己返回的 JSON 错误。
// 判据刻意窄:去掉前导空白后以 `{` 开头,且含 `"error"` 或 `"type"`。
func looksLikeServiceJSON(head []byte) bool {
	t := bytes.TrimLeft(head, " \t\r\n")
	if len(t) == 0 || t[0] != '{' {
		return false
	}
	return bytes.Contains(t, []byte(`"error"`)) || bytes.Contains(t, []byte(`"type"`))
}
