package supervisor

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/getbx/bx/internal/stats"
)

// ruleHistory 是按规则计数的**跨重启累计**。
//
// **为什么必须跨重启**:「这条规则 0 次命中」在单次运行里什么也说明不了 ——
// 一台刚重连的机器上每一条规则都是 0 次。死规则判据的整个前提是「累计到足够久、
// 足够多的判定之后仍然一次都没有」,而那个累计只能落盘。
//
// 信封形状照 internal/guardian/throughputhistory.go:带 schema_version、原子替换、
// 带时间戳。**未知 schema 不迁移**,当空并报错(调用方记一行日志)。
const ruleHistorySchema = 1

type ruleHistoryEntry struct {
	// Source 是做出判定的那一层;Rule 是用户规则原文,内建列表命中时为空。
	// 两者一起做键 —— 同一条规则原文可能同时出现在 direct 与 proxy 两张表里,
	// 语义相反,只按 Rule 做键会把它们的计数混成一个谁也不是的数。
	Source   string `json:"source"`
	Rule     string `json:"rule,omitempty"`
	Attempts int64  `json:"attempts"`
	Failures int64  `json:"failures"`
	// FailureKinds 把 Failures 拆成可行动的几类(见 internal/dialfail)。
	// 老文件里没有这个键 = nil,与「各类都是 0」是两件事。
	FailureKinds map[string]int64 `json:"failure_kinds,omitempty"`
}

type ruleHistory struct {
	SchemaVersion int                `json:"schema_version"`
	Entries       []ruleHistoryEntry `json:"entries,omitempty"`
	// UptimeSeconds 是 **Core 累计在跑的时长**,不是「距首次见到这条规则过了多久」。
	// 后者好实现得多,但它把 Core 没在跑的时间也算进去 —— 那段时间任何规则都不可能
	// 被命中,拿它当分母**等于悄悄降低门槛**。
	UptimeSeconds int64 `json:"uptime_seconds"`
	// Decisions 是**全局**累计判定数,**含内建列表命中**。它衡量「这台机器有没有真的
	// 被用过」;只数用户规则会让流量几乎全走内建列表的机器永远达不到门槛,于是这个
	// 功能静默地从不生效。
	Decisions int64 `json:"decisions"`
	// Versions 是这段累积跨过的版本集合(去重、有序)。历史跟着规则走、不跟着二进制
	// 走,但跨过几个版本要报出来,让用户对这份累计打折。
	Versions []string `json:"versions,omitempty"`
	// TrackingLimit 是产生这份历史时的按规则跟踪上限(maxTrackedRules)。
	TrackingLimit int `json:"tracking_limit,omitempty"`
	// Overflowed 记「跟踪表满过」。**它是粘性的**,见 mergeRuleHistory。
	Overflowed bool      `json:"overflowed,omitempty"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// loadRuleHistory 读一份历史。
//
// **文件不存在 = 空历史 + nil error**(第一次跑是常态,不是错误);
// **未知 schema = 空历史 + error** —— 不迁移,但要让调用方记得下一行日志。
// 两者刻意分开:前者什么都不必说,后者是「盘上有东西而我读不懂」,那值得留痕。
func loadRuleHistory(path string) (ruleHistory, error) {
	empty := ruleHistory{SchemaVersion: ruleHistorySchema}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return empty, nil
		}
		return empty, fmt.Errorf("reading the rule history %s: %w", path, err)
	}
	var h ruleHistory
	if err := json.Unmarshal(raw, &h); err != nil {
		return empty, fmt.Errorf("parsing the rule history %s: %w", path, err)
	}
	if h.SchemaVersion != ruleHistorySchema {
		return empty, fmt.Errorf("the rule history %s has schema %d and this version only understands %d — it is treated as empty and accumulation restarts",
			path, h.SchemaVersion, ruleHistorySchema)
	}
	return h, nil
}

// saveRuleHistory 原子替换写盘。
//
// **走既有的 atomicWriteFile,不新写一份原子写** —— 本仓库已经有两份手写的
// (guardian/store.go 的 writeJSONAtomically、toolkeys/store.go 的 save),
// 第三份只会让「原子性到底由谁保证」这个问题多一个答案。
func saveRuleHistory(path string, h ruleHistory) error {
	h.SchemaVersion = ruleHistorySchema
	data, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encoding the rule history: %w", err)
	}
	return atomicWriteFile(path, data)
}

// mergeRuleHistory 把本次运行的计数并进累计。
//
// **时长与判定数是累加,不是覆盖。** 覆盖会让每次重启都把门槛清零,而那正好是
// 这个功能要消灭的东西。
func mergeRuleHistory(prev ruleHistory, live []stats.RuleOutcome, uptimeDelta time.Duration,
	decisionsDelta int64, version string, trackingLimit int, overflowed bool, now time.Time,
) ruleHistory {
	next := ruleHistory{
		SchemaVersion: ruleHistorySchema,
		UptimeSeconds: prev.UptimeSeconds + int64(uptimeDelta.Seconds()),
		Decisions:     prev.Decisions + decisionsDelta,
		TrackingLimit: trackingLimit,
		// **Overflowed 只增不减。** 跟踪表满过之后,这段历史里可能有规则从没被记过,
		// 而它们在表里的样子与「记了、从没命中」一模一样。后续运行没再溢出就把标志
		// 清掉,会让「没有条目」被当成「没命中」—— 正是这个功能最忌讳的假阳性。
		Overflowed: prev.Overflowed || overflowed,
		UpdatedAt:  now,
	}

	type key struct{ source, rule string }
	idx := make(map[key]int, len(prev.Entries)+len(live))
	next.Entries = make([]ruleHistoryEntry, 0, len(prev.Entries)+len(live))
	add := func(source, rule string, attempts, failures int64, kinds map[string]int64) {
		k := key{source, rule}
		i, ok := idx[k]
		if !ok {
			idx[k] = len(next.Entries)
			next.Entries = append(next.Entries, ruleHistoryEntry{Source: source, Rule: rule})
			i = idx[k]
		}
		next.Entries[i].Attempts += attempts
		next.Entries[i].Failures += failures
		// **分类也要累加,而且要建一张新表。** 直接把入参那张 map 挂上去,
		// 历史就与活计数器共享同一张表 —— 落盘的那份会跟着后续失败继续变,
		// 而 delta 记账正是靠「落盘的那一刻是什么」才成立的。
		for kind, n := range kinds {
			if next.Entries[i].FailureKinds == nil {
				next.Entries[i].FailureKinds = make(map[string]int64, len(kinds))
			}
			next.Entries[i].FailureKinds[kind] += n
		}
	}
	for _, e := range prev.Entries {
		add(e.Source, e.Rule, e.Attempts, e.Failures, e.FailureKinds)
	}
	for _, o := range live {
		add(o.Source, o.Rule, o.Attempts, o.Failures, o.FailureKinds)
	}

	// **版本集合去重且有序。** 报告要拿它说「这段累积跨了几个版本」,每次启动都
	// 追加一遍会让那个数虚高 —— 而那个数正是让用户对累计打折的依据。
	seen := make(map[string]bool, len(prev.Versions)+1)
	for _, v := range prev.Versions {
		seen[v] = true
	}
	if version != "" {
		seen[version] = true
	}
	next.Versions = make([]string, 0, len(seen))
	for v := range seen {
		next.Versions = append(next.Versions, v)
	}
	sort.Strings(next.Versions)
	return next
}

// pruneRuleHistory 丢掉已经不在配置里的规则条目。
//
// 不剪的话,删掉的规则会永久占着 maxTrackedRules 的名额之一,而那个上限满了就会
// 触发 Overflowed —— 于是删几条规则本身就能把死规则这一类整个变成「没查」。
//
// **内建列表那条(Rule == "")不是孤儿。** 它没有对应的配置行,按「不在
// currentRules 里就丢」的字面规则会被剪掉,而全局判定数(门槛之一)要靠它 ——
// 剪掉它,门槛就永远达不到,这个功能会静默地从不生效。
func pruneRuleHistory(h ruleHistory, currentRules map[string]bool) ruleHistory {
	kept := make([]ruleHistoryEntry, 0, len(h.Entries))
	for _, e := range h.Entries {
		if e.Rule != "" && !currentRules[e.Rule] {
			continue
		}
		kept = append(kept, e)
	}
	h.Entries = kept
	return h
}
