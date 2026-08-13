package leakcheck

import "strings"

// judgeCarrier 回答「**谁在拿你的流量**」。
//
// 这一项**不吃浏览器**,纯本机路由观测;而它此前根本不存在 —— ClassifyTunnels 的
// 结果只躺在 WebRTC 那条的证据区里,于是最要紧的那个情形没人报:
//
//	**bx 在跑,而默认路由归别人。**
//
// 那正是「status 显绿而流量走别处」的形状,也正是这个仓库的观测层当初为之而建的
// 那件事 —— 用户以为自己受保护,流量却在走另一条隧道。
//
// **判定按「bx 该不该在管」分岔,而不是按「有没有别的隧道」。** bx 没在跑时,
// 别人的 VPN 拿着流量**不是问题** —— 那正是「拿 bx 检测别的 VPN」这个用法的常态,
// 报成 bad 会把整个用法变成一片红。
func judgeCarrier(local LocalFacts) Finding {
	f := Finding{ID: FindingCarrier, Title: "Who carries your traffic", Section: SectionPath}

	owner := WhoOwnsTheRoute(local)
	if owner == OwnerUnknown {
		f.Summary = "Not checked: it could not be determined which interface public traffic leaves through."
		if err := local.DefaultRouteV4.Err; err != "" {
			f.Evidence = append(f.Evidence, "route lookup: "+err)
		}
		return f
	}
	carrier := describeRef(local.DefaultRouteV4)
	f.Evidence = append(f.Evidence, "public traffic leaves via: "+carrier)

	// bx 在不在跑,决定同一个观测是好消息还是坏消息。
	running := bxRunningVerdict(local)

	// **过渡态先答不了,而且要早于 OwnerBX 那一支。** starting / recovering 时
	// 路由可能已经指向 TUN 而隧道还没起来,此刻打绿勾与喊泄漏一样没有依据。
	if running == runningUnknown {
		f.Verdict = NotChecked
		f.Summary = "bx is in a transitional state (" +
			strings.ToLower(strings.TrimSpace(local.BXProtection)) +
			"), so whether it should be carrying this traffic cannot be judged yet."
		return f
	}

	if owner == OwnerBX {
		f.Verdict = OK
		f.Summary = "Your public traffic is carried by bx (" + carrier + ")."
		// **第二个 claimant 说出来,但不改判定。** 此刻流量确实走 bx,不是泄漏;
		// 但内核是按 metric 挑的,对方重连或改 metric 就可能悄悄接管。值得说,
		// 不值得报红 —— 报红会与另外几条「流量确实从 VPS 出去」的结论当面打架。
		for _, claim := range competingTunnels(local) {
			if claim.Interface == strings.TrimSpace(local.BXTunInterface) {
				continue
			}
			f.Evidence = append(f.Evidence,
				claim.Interface+" also claims public address space ("+
					strings.Join(claim.Prefixes, ", ")+") — traffic goes through bx today, "+
					"but the kernel picks by metric, so that could change without warning")
		}
		return f
	}

	if running == notRunning {
		// **走到这里只意味着「没有证据表明 bx 在跑」,措辞不许说成「它没在跑」。**
		// 检测别的 VPN 那个用法里这确实是常态,但同一个空值也可能来自
		// 「Guardian 没答上话」——见 bxRunningVerdict。
		f.Verdict = Info
		if owner == OwnerNone {
			f.Summary = "No tunnel is carrying this machine's traffic — it leaves directly through " +
				carrier + "."
		} else {
			f.Summary = "Your public traffic is carried by " + carrier +
				", which bx is not managing. No sign of bx carrying traffic on this machine."
		}
		return f
	}

	// **本次要补的洞:bx 在跑,却不是它在管。**
	//
	f.Verdict = Bad
	if owner == OwnerNone {
		f.Summary = "bx is running" + bxWhere(local) + ", but public traffic is not " +
			"entering its tunnel at all — it leaves directly through " + carrier +
			". Whatever bx reports about itself, this traffic is not protected."
		return f
	}
	f.Summary = "bx is running" + bxWhere(local) + ", but your public traffic is " +
		"carried by " + carrier + " instead. Whatever bx reports about itself, it is not " +
		"the one carrying this traffic."
	f.Evidence = append(f.Evidence, tenantSuspicion(local)...)
	return f
}

// runningState 是「bx 此刻应不应该在承载公网流量」的三态。
//
// 本包刻意不引 observe.Tristate:那个包做 I/O,而 purity 守卫禁止判据依赖运行环境。
type runningState int

const (
	// 零值是「没问出来」—— 与本仓库 Verdict / Tristate 同一条纪律。
	runningUnknown runningState = iota
	isRunning
	notRunning
)

func (r runningState) String() string {
	switch r {
	case isRunning:
		return "running"
	case notRunning:
		return "not running"
	default:
		return "unknown"
	}
}

// bxRunningVerdict 判断此刻有没有证据表明 bx **应当正在承载公网流量**。
//
// 三态,不是二值 —— 与本包其它判据同一条纪律:认不出来的输入不许被替它选一边。
//
// 白名单而不是黑名单:黑名单会把将来新增的任何状态值默认算成「在跑」,而这条结论
// (「bx 在跑,但流量没走它」)误报的代价是让用户学会忽略它。
func bxRunningVerdict(local LocalFacts) runningState {
	// TUN 名读得出来 = Core 可达且报得出自己的接口,这是最硬的证据,压过状态字符串。
	if strings.TrimSpace(local.BXTunInterface) != "" {
		return isRunning
	}
	switch strings.ToLower(strings.TrimSpace(local.BXProtection)) {
	case "protected", "blocked", "needs_attention":
		// 三者都意味着「保护本该是开着的」。blocked 尤其:kill-switch 生效时
		// **更不该**有公网流量漏出去,此刻发现流量在走别的路是最严重的那种。
		return isRunning
	case "off", "":
		return notRunning
	default:
		// starting / recovering 是过渡态,几秒后自己会变;别的值是这一版没见过的。
		// 两类都不判 —— 在一个正在启动的系统上喊「流量没走隧道」是噪声。
		return runningUnknown
	}
}

// bxWhere 给出 bx 的接口名,**没有名字时给空串而不是一对空括号**。
//
// 走到这里而 TUN 名为空是常态不是边角:那正是 Core 不可达、只剩 Guardian
// 报得出保护状态的那条路。上一版在那条路上拼出的是字面的 `bx is running ()`。
func bxWhere(local LocalFacts) string {
	if name := strings.TrimSpace(local.BXTunInterface); name != "" {
		return " (" + name + ")"
	}
	return ""
}
