package leakcheck

import (
	"bytes"
	"strconv"
	"strings"
)

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

// JudgeReach 把一次探测的结果判成五态。**判据同时看状态码与 body。**
//
// 只看状态码会判反:`generativelanguage.googleapis.com` 的 403 是 API 在正常
// 应答,`chatgpt.com` 的 403 是人机挑战 —— 同一个码,相反的两件事(spec §2.3)。
//
// 判据按**先后顺序**取,前一条命中就不再往下看。
//
// **CF 挑战判 `ReachChallenged`,不是 `ReachUndetermined`**(2026-09-14 review
// 加,见 ReachState 的注释)——两者虽然都是「没问出来」,但 Challenged 有真凭据
// 支撑「不是你的出口有问题」这句话,而其余「认不出」的形状(步骤⑦)没有凭据,
// 不许替它猜同一句话。
func JudgeReach(status int, body []byte, dialErr error) ReachState {
	// ① 连都没连上 —— 这时 status/body 没有意义。
	//
	// **⚠️ 这一支今天安全,靠的是一个本包看不见的事实**:`bx leakcheck` 跑在
	// `app.Run(os.Args)` 交下来的 `context.Background()` 上,`c.Context` 永不取消,
	// 所以走到这里的 dialErr 只可能是「这条路不通」。哪天有人给 CLI 接上
	// `signal.NotifyContext`(Ctrl-C),「我们自己被取消了」会经同一个 err 落进
	// 这里、被报成 `ReachUnreachable` —— 而那正是这一段最不该产生的答案:用户按
	// 了一下 Ctrl-C,报告说 Anthropic 连不上。那一天要在**产地**(leakserve 的
	// probeOne)先把 `ctx.Err()` 那一类分出来,不是在这里猜 —— 本包拿到的只有一个
	// error,它分不出「对方不理我」与「我自己走了」。
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
	// ③ CF 人机挑战 —— 有特征串这份真凭据,判 Challenged 而不是 Undetermined。
	for _, m := range cfChallengeMarkers {
		if bytes.Contains(head, m) {
			return ReachChallenged
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
	//
	// **一个 200 的 HTML 拦截页(酒店门户、企业代理的「访问被阻止」页)会落进
	// 这里判 Reachable。这是刻意的边界,不是漏判** —— spec §3.1 无条件把 2xx
	// 收进可达,两条理由:① 四个端点全是 https,拦截方插不进这一页 —— TLS 会
	// 先失败,那一轮落进步骤①;② 措辞救了它:这一段说的是「bx can reach X」
	// 而不是「你可以用 X」,而「这条路到得了那个地址」在拿到 200 时确实成立。
	// 要改成「认出拦截页」就得维护一份拦截页特征库,而那是一个没有语料库的
	// 分母(与「指纹那条问有没有在防、不问指纹是什么」同一条)。**别当 bug 修。**
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

// FindingReachPrefix 是可达性结论 ID 的前缀。**每个端点一条结论**,ID 由前缀拼
// 端点 ID 得来 —— 拼接而不是手抄一份清单,加端点时 Outline() 的骨架自动跟上。
const FindingReachPrefix = "reach_"

// judgeReach 把这一轮的 AI 站可达性探测变成一组 Finding,**每个端点一条**,
// 与 ReachTargets() 的顺序一致(Outline() 的骨架按同一个顺序追加)。
//
// 一个端点即使两条路径(current + bypass)都探过,也只产出一条结论 —— 这一段
// 回答的是「这条路通不通」,不是「每条子路径各自怎样」;两条路径的探测结果都
// 进 Evidence,供人核对,但极性只由 current 那条决定(bypass 是诊断用的对照,
// 不是这一段的主线 —— §5 的比较逻辑留给渲染层)。
func judgeReach(local LocalFacts) []Finding {
	targets := ReachTargets()
	out := make([]Finding, 0, len(targets))
	for _, tgt := range targets {
		out = append(out, judgeReachTarget(tgt, local.ReachProbes))
	}
	return out
}

func judgeReachTarget(tgt ReachTarget, probes []ReachProbe) Finding {
	f := Finding{ID: FindingReachPrefix + tgt.ID, Title: tgt.Title, Section: SectionReach}
	host := reachHost(tgt.URL)

	if tgt.OnChinaDirectList != nil {
		// **点名 host 而不是内部的 tgt.ID**:这一行是给用户读的证据,而
		// `anthropic_api` 这种内部标识对他是黑话 —— 他手里能对照的东西是域名。
		f.Evidence = append(f.Evidence, host+" on built-in china direct list: "+
			strconv.FormatBool(*tgt.OnChinaDirectList))
	}

	current, hasCurrent := findReachProbe(probes, tgt.ID, ReachPathCurrent)
	if bypass, hasBypass := findReachProbe(probes, tgt.ID, ReachPathBypass); hasBypass {
		// **并排出示,不参与判定。** current 缺席时仍然把它列出来 —— 它是一条真实
		// 观测,即便这一轮没能给出主线结论。
		f.Evidence = append(f.Evidence, "bypass path: "+describeReachProbe(bypass))
	}

	if !hasCurrent {
		// **与「探过了、认不出」是两件不同的事**:这里连探测记录都没有。
		// **这条路 2026-09-14 之后不再是常态**(接线已经做完:internal/cli 的
		// collectLeakCheckFacts → leakserve.CollectReach 会把结果填进 LocalFacts)——
		// 今天它只剩三种来源:`bx leakcheck --no-reach`、两个拨号器都没供货的 deps、
		// 以及不经 CLI 直接调 Judge 的调用方。
		// 零值 ReachUndetermined 之下 Verdict 仍是 NotChecked,但措辞必须诚实地
		// 说「没检查」,不能借用「像是人机挑战」那句 —— 那句话断言了一次并没有
		// 发生的观测。
		f.Reach = ReachUndetermined
		f.Verdict = NotChecked
		f.Summary = "Reachability to " + host + " was not checked in this run."
		return f
	}

	f.Reach = current.State
	f.Evidence = append(f.Evidence, "current path: "+describeReachProbe(current))

	switch current.State {
	case ReachReachable:
		f.Verdict = OK
		// **绝不说「你可以用 X」**(spec §3.4):地区限制可能发生在登录或 API 调用层,
		// 这里只观测到了边缘——bx 无权替对方的产品说话。
		f.Summary = "bx can reach " + host + "."
	case ReachRefused:
		f.Verdict = Bad
		f.Summary = host + " has refused connections from the region this exit is in."
	case ReachUnreachable:
		f.Verdict = Bad
		// **不断言对方服务的状态**(与 core_tunnel_unreachable 同一条纪律):
		// 本机自己没网时同样拨不通,这句话只说 bx 观测到了什么。
		f.Summary = "This path could not reach " + host + "."
	case ReachChallenged:
		f.Verdict = NotChecked
		// **主动否掉用户会自己脑补的坏消息**(spec §3.4):这不是「你的出口有问题」。
		// 这句话**只对这一态成立** —— 判据手里有 CF 的特征串这份真凭据,才敢替
		// 用户否掉那句坏消息;下面的 ReachUndetermined 没有这份凭据,不许借用它。
		f.Summary = "This looks like Cloudflare's bot-verification challenge, not a " +
			"problem with your exit — a browser can get through this even though the " +
			"command line cannot."
	default: // ReachUndetermined:探过了,但认不出是哪一种(未知状态码/重定向)。
		f.Verdict = NotChecked
		// **不猜原因**(2026-09-14 review 修正)——此前这一格与 ReachChallenged
		// 共用同一句 CF 措辞,而一个真实的地区封禁页(HTML,不含 CF 那几个特征串)
		// 会落在这里,读到的却是一句主动否掉坏消息的假话。判据自己都说「认不出
		// 就是认不出,不猜」(JudgeReach 步骤⑦),这里的措辞必须同样诚实。
		f.Summary = "bx could not determine whether " + host + " is reachable — " +
			"this path returned something that wasn't recognized as either reachable or blocked."
	}
	return f
}

// findReachProbe 在这一轮的探测记录里找一个端点在一条路径上的结果。
// **按 TargetID + Path 找,不假设顺序或数量** —— 上游 leakserve.ProbeReach
// 串行跑完全部目标,但这里不依赖它的实现细节。找到多条时用第一条:重复记录
// 不该发生,而发生时偏向较早的那一条不比偏向较晚的那一条更错。
func findReachProbe(probes []ReachProbe, targetID, path string) (ReachProbe, bool) {
	for _, p := range probes {
		if p.TargetID == targetID && p.Path == path {
			return p, true
		}
	}
	return ReachProbe{}, false
}

// describeReachProbe 把一次探测渲染成一行证据。
func describeReachProbe(p ReachProbe) string {
	s := p.State.String()
	if p.Detail != "" {
		s += " (" + p.Detail + ")"
	}
	return s
}

// reachHost 从探测目标的 URL 里取出主机名,**只用字符串操作,不 import net/url**
// (本包的纯度守卫挡住整棵 net 子树,只留 net/netip 一个例外——net/url 会拨号/
// 解析名字不在其列)。够用:ReachTargets() 里的 URL 都是形如
// `https://host/path` 的字面量,不需要处理端口、用户信息或 IDN。
func reachHost(rawURL string) string {
	s := rawURL
	if i := strings.Index(s, "://"); i >= 0 {
		s = s[i+3:]
	}
	if i := strings.IndexAny(s, "/?#"); i >= 0 {
		s = s[:i]
	}
	return s
}
