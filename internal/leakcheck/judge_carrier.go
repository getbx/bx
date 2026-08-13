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

	// bx 有没有 TUN,决定同一个观测是好消息还是坏消息。
	bxRunning := strings.TrimSpace(local.BXTunInterface) != ""

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

	if !bxRunning {
		// bx 没在跑 —— 如实说谁在拿,不评判。这是检测别的 VPN 那个用法的常态。
		f.Verdict = Info
		if owner == OwnerNone {
			f.Summary = "No tunnel is carrying this machine's traffic — it leaves directly through " +
				carrier + ". bx is not running."
		} else {
			f.Summary = "Your public traffic is carried by " + carrier +
				", which bx is not managing. bx is not running, so this is expected."
		}
		return f
	}

	// **本次要补的洞:bx 在跑,却不是它在管。**
	f.Verdict = Bad
	if owner == OwnerNone {
		f.Summary = "bx is running (" + local.BXTunInterface + "), but public traffic is not " +
			"entering its tunnel at all — it leaves directly through " + carrier +
			". Whatever bx reports about itself, this traffic is not protected."
		return f
	}
	f.Summary = "bx is running (" + local.BXTunInterface + "), but your public traffic is " +
		"carried by " + carrier + " instead. Whatever bx reports about itself, it is not " +
		"the one carrying this traffic."
	f.Evidence = append(f.Evidence, tenantSuspicion(local)...)
	return f
}
