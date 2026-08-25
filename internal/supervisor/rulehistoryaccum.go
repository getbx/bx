package supervisor

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/getbx/bx/internal/stats"
)

// ruleHistoryInterval 是累计写盘的周期。
//
// **它决定 SIGKILL 时最多丢多少累计。** launchd 确实会 SIGKILL Core(本仓库记着
// 那件事打断过 defer),所以「关闭时再写一次」不能是唯一的写盘时机 —— 周期写保证
// 丢掉的量有上界。5 分钟是权衡:再密一点,一台 24 小时开着的机器每天多写几百次而
// 换不来判据上的任何区别(门槛以 14 天计);再疏一点,一次 SIGKILL 能吃掉半小时。
const ruleHistoryInterval = 5 * time.Minute

// ruleHistoryFile 是累计历史在 DataDir 下的文件名。
//
// **路径由 Run 拼**(它手上有 cfg.DataDir),这里只出名字 —— 本仓库明令叶子不许
// 自己猜一份路径常量。
const ruleHistoryFile = "rule-history.json"

// ruleDeltas 算「自上一拍以来新增的量」。
//
// **累加进历史的必须是增量,不是总量。** 用总量的话,第二次写盘会把第一次已经
// 累进去的量再加一遍 —— 累计值随写盘频率虚高,而写盘频率是个实现细节,判据不该
// 受它影响(门槛是 20,000 次判定,虚高十倍就等于门槛降了十倍)。
//
// 增量为 0 的键**不产出条目**:Counters 里压根没有「从没命中过的规则」这种条目
// (bump 只在被调用时才建键),所以历史里也不会有 0 次的条目 —— 死规则判据要靠
// 「配置里有这条、历史里没有它」来认,而不是靠一条写着 0 的记录。
func ruleDeltas(prev, cur []stats.RuleOutcome) []stats.RuleOutcome {
	type key struct{ source, rule string }
	before := make(map[key]stats.RuleOutcome, len(prev))
	for _, o := range prev {
		before[key{o.Source, o.Rule}] = o
	}
	out := make([]stats.RuleOutcome, 0, len(cur))
	for _, o := range cur {
		d := o
		if b, ok := before[key{o.Source, o.Rule}]; ok {
			// **总量回退时按「这是一次重置」处理,用当前值。** 同一个进程里
			// Counters 只增不减,所以这一支今天不可达;写出来是因为它的替代
			// (相减得负数)会把负数累进历史,而那种坏值一旦落盘就再也纠正不回来。
			if o.Attempts >= b.Attempts && o.Failures >= b.Failures {
				d.Attempts = o.Attempts - b.Attempts
				d.Failures = o.Failures - b.Failures
			}
		}
		if d.Attempts == 0 && d.Failures == 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

// ruleHistoryAccumulator 把 Core 本次运行的计数按增量并进跨重启累计。
//
// 它自己记住上一拍的总量与时刻,故**每一拍都是增量** —— 见 ruleDeltas。
type ruleHistoryAccumulator struct {
	path     string
	counters *stats.Counters
	version  string
	// currentRules 给出「此刻配置里有哪些规则原文」,用来剪孤儿。
	// 做成函数而不是快照:配置在运行期可被 `bx direct/proxy` 热加,拿启动那一刻
	// 的快照会把新加的规则当孤儿剪掉。
	currentRules func() map[string]bool
	now          func() time.Time

	prevRules     []stats.RuleOutcome
	prevDecisions int64
	lastAt        time.Time

	// mu 只护下面这两个「发布用」的字段:控制面每次 status 都读它们,而 flush
	// 在另一条 goroutine 上写。
	mu sync.Mutex
	// published 是**最近一次合并出来的**那份历史。发布它而不是每次 status 都读盘:
	// status 是出问题时最先敲的命令,不该在它的路径上加一次磁盘 I/O。
	published ruleHistory
	// loadErr 记最近一次读盘失败的原因。非空时发布的数字是**重新开始累计之后**
	// 的,不是全部历史 —— 判据必须知道这件事,否则会拿一份年轻的累计当全部,
	// 门槛「还没到」是对的,但它给不出原因。
	loadErr string
}

func newRuleHistoryAccumulator(path string, counters *stats.Counters, version string,
	currentRules func() map[string]bool, now func() time.Time,
) *ruleHistoryAccumulator {
	return &ruleHistoryAccumulator{
		path: path, counters: counters, version: version,
		currentRules: currentRules, now: now, lastAt: now(),
	}
}

// flush 读盘、并进增量、剪孤儿、写回。
//
// **读盘失败不是致命的**:历史当空重新累计,记一行日志。让 Core 因为一份统计文件
// 读不出来而起不来,是拿一个可观测性功能去换保护本身。
func (a *ruleHistoryAccumulator) flush() error {
	now := a.now()
	prev, err := loadRuleHistory(a.path)
	loadErr := ""
	if err != nil {
		loadErr = err.Error()
		log.Printf("规则历史读不出来,当空重新累计: %v", err)
	}

	cur := a.counters.Snapshot().Rules
	decisions := a.counters.Decisions()

	next := mergeRuleHistory(prev,
		ruleDeltas(a.prevRules, cur),
		now.Sub(a.lastAt),
		decisions-a.prevDecisions,
		a.version,
		a.counters.TrackedRuleLimit(),
		a.counters.RuleTrackingOverflowed(),
		now,
	)
	next = pruneRuleHistory(next, a.currentRules())
	if err := saveRuleHistory(a.path, next); err != nil {
		// **写失败不推进 prev**:下一拍会把这一段增量再算一次,累计不丢。
		// 反过来(先推进再写)会让一次写失败静默吃掉一段流量。
		return err
	}
	a.prevRules, a.prevDecisions, a.lastAt = cur, decisions, now

	a.mu.Lock()
	a.published, a.loadErr = next, loadErr
	a.mu.Unlock()
	return nil
}

// snapshot 是发布给控制面的那一份。
//
// **从没成功 flush 过时返回 nil** —— nil 是「这台机器还没有累计历史可报」,
// 与「累计为 0」是两件事。返回一个零值结构会让消费方读成「跑了 0 秒、0 次判定」,
// 而那正好是判据用来说「门槛还没到」的形状 —— 两种完全不同的情况会给出同一句话。
func (a *ruleHistoryAccumulator) snapshot() *stats.RuleHistorySnapshot {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.published.UpdatedAt.IsZero() {
		return nil
	}
	out := &stats.RuleHistorySnapshot{
		UptimeSeconds: a.published.UptimeSeconds,
		Decisions:     a.published.Decisions,
		Overflowed:    a.published.Overflowed,
		UpdatedAt:     a.published.UpdatedAt,
		SkipReason:    a.loadErr,
	}
	// **每次都复制,不共享底层数组。** 消费方拿到之后会被 JSON 编码、也可能被
	// 别的代码改;共享的话下一次 flush 就在改一份已经发出去的东西。
	// (与 GuardianCapabilities 头上「每次调用都返回新切片」同一条纪律。)
	if len(a.published.Versions) > 0 {
		out.Versions = append([]string(nil), a.published.Versions...)
	}
	if len(a.published.Entries) > 0 {
		out.Rules = make([]stats.RuleOutcome, 0, len(a.published.Entries))
		for _, e := range a.published.Entries {
			out.Rules = append(out.Rules, stats.RuleOutcome{
				Source: e.Source, Rule: e.Rule, Attempts: e.Attempts, Failures: e.Failures,
			})
		}
	}
	return out
}

// runRuleHistoryLoop 周期 flush,ctx 取消时**再 flush 一次**,然后 close(done)。
//
// **`done` 不是讲究,是承重的。** 最后那次 flush 若只写在这条 goroutine 里而没人
// 等它,Run 返回后进程可能先退出 —— 那次写**静默不发生**,而它恰恰是最有价值的
// 一次(它带着自上一拍以来、最长可达一个周期的增量)。调用方必须等,但**只等一个
// 很短的上限**:停止路径不许因为别的事没做完而变慢或失败(本仓库为「关机慢」栽过
// 一次 71 分钟的事故)。
//
// **刻意不看隧道健康。** refreshLoop 那条要等健康是因为它要经隧道拉列表;而累计
// 时长是「Core 在跑的时长」,隧道挂着的那段时间 Core 一样在跑、规则一样在被判定,
// 把那段排除掉等于悄悄降低门槛。
func runRuleHistoryLoop(ctx context.Context, interval time.Duration, a *ruleHistoryAccumulator, done chan<- struct{}) {
	if done != nil {
		defer close(done)
	}
	// **启动即刷一次**(与 refreshLoop 同款)。两个理由:① 盘上可能已经攒了十几天,
	// 不先读进来的话,Core 起来后的头一个周期里 `bx status` 会说「没有累计历史」,
	// 而那是假话;② 路径写不了要早点在日志里显形,别等到第一个周期。
	if err := a.flush(); err != nil {
		log.Printf("首次写规则历史失败: %v", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			if err := a.flush(); err != nil {
				log.Printf("退出前写规则历史失败: %v", err)
			}
			return
		case <-t.C:
			if err := a.flush(); err != nil {
				log.Printf("写规则历史失败: %v", err)
			}
		}
	}
}

// ruleHistoryShutdownWait 是等最后那次 flush 的上限。
//
// 一次 flush 是「读一个小 JSON + 写一个小 JSON」,正常在毫秒级;给 2 秒是为了盘卡
// 一下也能写完。**等不到就走**,不报错也不重试 —— 丢一段统计远好过让用户关不掉保护。
const ruleHistoryShutdownWait = 2 * time.Second

// waitRuleHistoryFlush 等最后那次 flush,最多 ruleHistoryShutdownWait。
func waitRuleHistoryFlush(done <-chan struct{}) {
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(ruleHistoryShutdownWait):
		log.Printf("等规则历史收尾写盘超时(%s),继续关闭 —— 丢一段统计好过让关闭卡住", ruleHistoryShutdownWait)
	}
}
