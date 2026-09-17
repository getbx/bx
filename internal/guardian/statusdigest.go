package guardian

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// statusDigest 是 Status 的**投影**:除掉一批持续在变的字段之后的内容指纹。
// 代际号由它派生 —— 投影不同才 bump(见 statusPublisher)。
//
// **投影 = 整个 Status 减一张排除名单,不是一份白名单。** 方向是刻意的:
// 新加的字段默认参与,于是将来谁加一个易变字段会让 watch 疯狂触发(吵、当场
// 看得见);默认不参与则是菜单静默地不再对新信号反应(安静,只有用户抱怨时
// 才发现)。代价不对称,默认值站在「吵」那边 —— 与 Class 零值取 ClassRisky、
// leakcheck.Section 零值取 SectionPath 同一条纪律。
//
// **本函数绝不改动调用方那份 Status。** Core/Reconcile 是指针、FailingRules 是
// 切片,所以下面每一处都显式复制:在「副本」里清零会改到同一个底层数组,让
// 真正发布出去的 Status 计数变成 0 —— 一个污染它所要度量的东西的 digest,
// 比没有 digest 更糟。(GuardianCapabilities 头上那句注释说的是同一条纪律。)
//
// error 非 nil 时调用方**必须当作「变了」**:那意味着 Status 里有编不了码的
// 东西,是编程错误。当作「变了」会让 watch 每个兵底拍都触发 —— 吵、且有日志,
// 而当作「没变」会让菜单**静默冻住**。同一条不对称。
func statusDigest(s Status) (string, error) {
	// 代际号自己不进投影:进了就每次 bump 都让下一次比对不同,永久自激。
	s.StatusGeneration = 0

	if s.Core != nil {
		core := *s.Core
		// 每次健康探测都抖:390 → 412 → 388。
		core.LatencyMS = 0
		if len(core.FailingRules) != 0 {
			// **必须新分配。** copy 到一条新切片上再清零,否则改的是调用方的数组。
			rules := make([]FailingRule, len(core.FailingRules))
			copy(rules, core.FailingRules)
			for i := range rules {
				// 每条连接都在涨。**只零计数,保留 Kind/Rule** ——
				// 「这条规则开始成片失败」是真事件,「它又多失败了 3 次」不是。
				rules[i].Attempts = 0
				rules[i].Failures = 0
			}
			core.FailingRules = rules
		}
		s.Core = &core
	}

	if s.Reconcile != nil {
		report := *s.Reconcile
		// recordReconcileRound 是唯一写入口且**每轮都盖时间戳**。不排除它,
		// watch 会跟着调谐环每 30 秒到 10 分钟触发一次。
		// 只排 At 与 UnchangedRounds:Actions/Held/Unobservable/CoreScan 变了
		// 是真事件。
		report.At = time.Time{}
		// UnchangedRounds 与 At 是同一类东西的两面 —— **循环又跑了一轮的标记,
		// 不是「有什么变了」的信号**。它每轮都涨(也驱动退避本身),不排除它
		// 与不排除 At 是同一个错误:2026-08-17 真机 10 分钟 soak 抓到 4 次唤醒,
		// 三个连续代际里 protection/desired 全程未变,唯一移动投影的字段就是它
		// (状态 15→16 的 diff 只剩 at 与 unchanged_rounds),间隔精确对上调谐环
		// 30s→10min 的退避阶梯 —— watch 在「观测到没有变化」这件事本身上被
		// 重新触发了。
		report.UnchangedRounds = 0
		s.Reconcile = &report
	}

	// 恢复进行中每次轮询都换。只排它:State/Stage/Attempt/ErrorCode 变了都是
	// 真事件,而菜单那个「Connecting — N 秒」计数器由它自己的本地 toggleTicker
	// 驱动,不靠推送走字。
	s.Recovery.UpdatedAt = time.Time{}

	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("projecting the Status: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
