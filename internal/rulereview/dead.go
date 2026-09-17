package rulereview

import (
	"fmt"
	"time"
)

// —— 死规则:这条从来没命中过 ——
//
// **它是这份体检里唯一一条要跨重启才敢说的话。** 单次运行的「0 次」什么也说明
// 不了:一台刚重连的机器上每条规则都是 0 次。所以判据有三道门槛,而**任何一道
// 问不出来就整类不判** —— 判错的后果是用户删掉一条天天在工作的规则。

// deadMinUptime 是门槛一:Core **累计在跑**的时长。
//
// 不是「距首次见到这条规则过了多久」—— 后者好实现得多,但它把 Core 没在跑的时间
// 也算进去,而那段时间任何规则都不可能被命中,拿它当分母等于悄悄降低门槛。
const deadMinUptime = 14 * 24 * time.Hour

// deadMinDecisions 是门槛二:全局累计判定数(**含内建列表命中**)。
//
// 它衡量「这台机器有没有真的被用过」。只数用户规则会让流量几乎全走内建列表的
// 机器永远达不到门槛,于是这一类静默地从不生效。
const deadMinDecisions int64 = 20000

// deadGate 判「这一类能不能查」。第二个返回值是不能查时的人话理由。
//
// **顺序是刻意的:先说「问不出来」,再说「还不够」。** 拿不到历史与「历史有、
// 只是还年轻」是两种不同的处境,措辞混在一起会让用户以为再等等就好 ——
// 而前者要做的是去看 Core 为什么没在跑。
func deadGate(in Input) (bool, string) {
	if in.History == nil {
		why := in.HistorySkipReason
		if why == "" {
			why = "no cross-restart per-rule counters are available"
		}
		return false, why
	}
	if in.HistoryOverflowed {
		// **这道门最要紧。** 表满之后新规则不再被记,于是「历史里没有这条」与
		// 「有这条、attempts==0」在数据上完全无法区分 —— 前者是「没被记过」,
		// 后者才是「从没命中」。不区分就会把一条正在工作的规则报成死的。
		return false, "the per-rule tracking table overflowed at some point, so \"no record\" and \"never matched\" are indistinguishable; this class is not judged"
	}
	if in.HistoryUptime < deadMinUptime {
		return false, fmt.Sprintf("%s of cumulative uptime, short of %s — not enough to conclude anything here yet",
			roundDays(in.HistoryUptime), roundDays(deadMinUptime))
	}
	if in.HistoryDecisions < deadMinDecisions {
		return false, fmt.Sprintf("%d cumulative decisions, short of %d — not enough to conclude anything here yet",
			in.HistoryDecisions, deadMinDecisions)
	}
	return true, ""
}

// roundDays 把时长说成人话。报告是给人读的,`336h0m0s` 不是。
func roundDays(d time.Duration) string {
	if d < 24*time.Hour {
		return fmt.Sprintf("%.0f hours", d.Hours())
	}
	return fmt.Sprintf("%.0f days", d.Hours()/24)
}

// deadFindings 找出累计命中为零的规则。
//
// kind 是这张表的名字(direct|proxy),source 是它在 route.Source 里的名字。
// **两张表分开查**:同一条原文可以同时在 direct 与 proxy 里、语义相反,
// 只按规则原文比对会让 proxy 那条的命中把 direct 那条也「救活」。
func deadFindings(kind, source string, rules []domainRule, in Input) []Finding {
	// **按归一化形式建索引,不按原文。** 历史里那一条来自 route 的判定记录,
	// 与 config 里的原文可能只差大小写或 `*.` 前缀 —— 对不上的后果是**假阳性**:
	// 一条天天在用的规则被报成「从来没命中过」,用户照着删掉。
	// 这是这个功能里代价最不对称的一处。
	hit := make(map[string]int64, len(in.History))
	for k, v := range in.History {
		if k.Source != source || k.Rule == "" {
			continue
		}
		n := normalizeRule(k.Rule)
		if n == "" {
			continue
		}
		hit[n] += v.Attempts
	}

	var out []Finding
	for _, r := range rules {
		if hit[r.norm] > 0 {
			continue
		}
		out = append(out, Finding{
			Kind:  kind,
			Rule:  r.raw,
			Class: ClassDead,
			// **不写 CoveredBy** —— 死规则没有「被谁盖住」这回事,它只是从没被用到。
			Summary: "never matched once, cumulatively: this rule may no longer be needed (before deleting, make sure you really no longer visit that domain)",
		})
	}
	return out
}
