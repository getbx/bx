package supervisor

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getbx/bx/internal/stats"
)

func TestMergeAccumulatesAcrossRuns(t *testing.T) {
	at := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	prev := ruleHistory{
		SchemaVersion: ruleHistorySchema, UptimeSeconds: 3600, Decisions: 100,
		Versions: []string{"0.3.0"},
		Entries:  []ruleHistoryEntry{{Source: "user_direct", Rule: "*.qq.com", Attempts: 5, Failures: 1}},
	}

	live := []stats.RuleOutcome{{Source: "user_direct", Rule: "*.qq.com", Attempts: 7, Failures: 2}}
	got := mergeRuleHistory(prev, live, 30*time.Minute, 50, "0.4.0", 256, false, at)

	if got.UptimeSeconds != 3600+1800 {
		t.Errorf("UptimeSeconds = %d, want %d(累计,不是覆盖)", got.UptimeSeconds, 3600+1800)
	}
	if got.Decisions != 150 {
		t.Errorf("Decisions = %d, want 150", got.Decisions)
	}
	if len(got.Entries) != 1 || got.Entries[0].Attempts != 12 || got.Entries[0].Failures != 3 {
		t.Errorf("条目没有累加:%+v", got.Entries)
	}
	// **版本集合去重且有序** —— 报告要拿它说「这段累积跨了几个版本」,
	// 每次启动都追加一遍会让那个数虚高。
	if len(got.Versions) != 2 || got.Versions[0] != "0.3.0" || got.Versions[1] != "0.4.0" {
		t.Errorf("Versions = %v, want [0.3.0 0.4.0]", got.Versions)
	}
	got2 := mergeRuleHistory(got, live, time.Minute, 1, "0.4.0", 256, false, at)
	if len(got2.Versions) != 2 {
		t.Errorf("同一版本被重复计入:%v", got2.Versions)
	}
}

// **溢出是粘性的。** 一旦某次运行里跟踪表满过,这段历史里就可能有规则从没被记过 ——
// 而它们在表里的样子与「记了、从没命中」一模一样。后续运行没再溢出**不能**把这个
// 标志清掉:那段缺口已经在累计值里了。
func TestOverflowIsSticky(t *testing.T) {
	at := time.Now()
	h := mergeRuleHistory(ruleHistory{SchemaVersion: ruleHistorySchema}, nil, time.Minute, 1, "v", 256, true, at)
	if !h.Overflowed {
		t.Fatal("溢出没被记下")
	}
	h2 := mergeRuleHistory(h, nil, time.Minute, 1, "v", 256, false, at)
	if !h2.Overflowed {
		t.Fatal("后续运行没溢出就把标志清掉了 —— 那段缺口已经在累计值里,清掉它" +
			"会让「没有条目」被当成「没命中」,正是这个功能最忌讳的假阳性")
	}
}

// 孤儿:不在当前配置里的条目加载时丢掉,否则 256 上限会被它们吃掉。
func TestPruneDropsEntriesNotInCurrentConfig(t *testing.T) {
	h := ruleHistory{SchemaVersion: ruleHistorySchema, Entries: []ruleHistoryEntry{
		{Source: "user_direct", Rule: "*.qq.com", Attempts: 3},
		{Source: "user_direct", Rule: "*.gone.com", Attempts: 9},
		{Source: "china_domain", Rule: "", Attempts: 100},
	}}
	got := pruneRuleHistory(h, map[string]bool{"*.qq.com": true})
	if len(got.Entries) != 2 {
		t.Fatalf("剪完剩 %d 条,want 2(留下 *.qq.com 与内建那条):%+v", len(got.Entries), got.Entries)
	}
	for _, e := range got.Entries {
		if e.Rule == "*.gone.com" {
			t.Error("已从配置里删掉的规则仍留在历史里 —— 它会永久占着 256 个名额之一")
		}
	}
	// **内建列表那条(Rule == "")不是孤儿。** 它没有对应的配置行,但全局判定数要靠
	// 它;按「不在 currentRules 里就丢」的字面规则会把它剪掉,而那会让门槛 3 永远达不到。
	var sawBuiltin bool
	for _, e := range got.Entries {
		if e.Rule == "" {
			sawBuiltin = true
		}
	}
	if !sawBuiltin {
		t.Error("内建列表那条被当成孤儿剪掉了")
	}
}

func TestLoadMissingFileIsEmptyNotError(t *testing.T) {
	h, err := loadRuleHistory(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("文件不存在应当是空历史 + nil error,got %v", err)
	}
	if h.SchemaVersion != ruleHistorySchema {
		t.Errorf("SchemaVersion = %d,空历史也该盖上当前 schema", h.SchemaVersion)
	}
}

// 未知 schema 一律当空**并报错** —— 不做迁移,但调用方要能记一行日志。
// 与 throughputhistory 同一处置。
func TestUnknownSchemaIsEmptyAndReported(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	if err := os.WriteFile(p, []byte(`{"schema_version":999,"decisions":5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h, err := loadRuleHistory(p)
	if err == nil {
		t.Error("未知 schema 应当报错,好让调用方记一行日志")
	}
	if h.Decisions != 0 {
		t.Errorf("未知 schema 的内容被采用了:Decisions=%d", h.Decisions)
	}
}

func TestSaveReplacesAtomically(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.json")
	write := func() error { return saveRuleHistory(p, ruleHistory{SchemaVersion: ruleHistorySchema, Decisions: 1}) }
	if err := write(); err != nil {
		t.Fatal(err)
	}
	first, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := write(); err != nil {
		t.Fatal(err)
	}
	second, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(first, second) {
		t.Error("两次写是原地截断而不是 rename —— 崩在中间会留下半个文件")
	}
}

// **写盘用的是 delta,不是总量。** 用总量的话,第二次写盘会把第一次已经累进去的量
// 再加一遍,累计值随写盘频率而虚高 —— 而写盘频率是个实现细节,判据不该受它影响。
func TestAccumulatorUsesDeltasNotTotals(t *testing.T) {
	at := time.Now()
	h := ruleHistory{SchemaVersion: ruleHistorySchema}
	live := []stats.RuleOutcome{{Source: "user_direct", Rule: "a.com", Attempts: 10}}
	// 两拍之间 live 的总量没变(没有新流量),delta 应当为 0。
	h = mergeRuleHistory(h, live, time.Minute, 10, "v", 256, false, at)
	h = mergeRuleHistory(h, nil, time.Minute, 0, "v", 256, false, at)
	if got := h.Entries[0].Attempts; got != 10 {
		t.Fatalf("Attempts = %d, want 10 —— 第二拍没有新流量却又加了一遍", got)
	}
	if h.Decisions != 10 {
		t.Fatalf("Decisions = %d, want 10", h.Decisions)
	}
}

// ruleDeltas 是「总量 → 增量」那一步。上面那条测试要求调用方传增量,这条钉住
// 真正把总量换成增量的那段代码。
func TestRuleDeltasSubtractsThePreviousTotals(t *testing.T) {
	prev := []stats.RuleOutcome{{Source: "user_direct", Rule: "a.com", Attempts: 10, Failures: 3}}
	cur := []stats.RuleOutcome{
		{Source: "user_direct", Rule: "a.com", Attempts: 14, Failures: 5},
		{Source: "user_proxy", Rule: "b.com", Attempts: 2},
	}
	got := ruleDeltas(prev, cur)
	if len(got) != 2 {
		t.Fatalf("增量 = %+v, want 两条", got)
	}
	byRule := map[string]stats.RuleOutcome{}
	for _, o := range got {
		byRule[o.Rule] = o
	}
	if a := byRule["a.com"]; a.Attempts != 4 || a.Failures != 2 {
		t.Errorf("a.com 的增量 = %+v, want attempts 4 / failures 2(14-10 / 5-3)", a)
	}
	// 上一拍没有的键:整个当增量(它是新出现的)。
	if b := byRule["b.com"]; b.Attempts != 2 {
		t.Errorf("新出现的键 b.com 增量 = %+v, want attempts 2", b)
	}
}

// **零增量的键不产出条目。** Counters 里压根没有「从没命中过的规则」这种条目
// (bump 只在被调用时才建键),所以历史里也不该有一条写着 0 的记录 —— 死规则判据
// 要靠「配置里有这条、历史里没有它」来认。多写一条 0 的记录会让这两种情况看起来
// 一样,而它们是这个功能唯一要区分的东西。
func TestRuleDeltasDropsUnchangedEntries(t *testing.T) {
	same := []stats.RuleOutcome{{Source: "user_direct", Rule: "a.com", Attempts: 10, Failures: 3}}
	if got := ruleDeltas(same, same); len(got) != 0 {
		t.Fatalf("没有新流量却产出了 %+v —— 零增量的条目不该进历史", got)
	}
}

// **总量回退时用当前值,不产出负数。** 同一个进程里 Counters 只增不减,所以这一支
// 今天不可达;但相减得负数一旦落盘就再也纠正不回来,而负的累计值会让门槛永远
// 达不到 —— 一个坏值把功能静默关掉,没有任何一处会报错。
func TestRuleDeltasNeverProducesNegatives(t *testing.T) {
	prev := []stats.RuleOutcome{{Source: "user_direct", Rule: "a.com", Attempts: 100, Failures: 50}}
	cur := []stats.RuleOutcome{{Source: "user_direct", Rule: "a.com", Attempts: 3, Failures: 1}}
	got := ruleDeltas(prev, cur)
	if len(got) != 1 || got[0].Attempts != 3 || got[0].Failures != 1 {
		t.Fatalf("计数回退时的增量 = %+v, want 按重置处理、取当前值 3/1", got)
	}
}

// **写盘失败不许推进「上一拍」。** 推进了就等于宣布这段增量已经落盘,而它没有 ——
// 下一拍会从新的基线算起,那段流量被静默吃掉。累计值少算的后果是门槛更难达到,
// 功能悄悄不生效。
func TestAccumulatorKeepsTheDeltaWhenTheWriteFails(t *testing.T) {
	dir := t.TempDir()
	// 用一个**目录**当写入路径:原子替换的 rename 必然失败。
	bad := filepath.Join(dir, "taken")
	if err := os.Mkdir(bad, 0o700); err != nil {
		t.Fatal(err)
	}
	c := &stats.Counters{}
	c.RuleAttempt("user_direct", "a.com")
	at := time.Now()
	a := newRuleHistoryAccumulator(bad, c, "v", func() map[string]bool { return map[string]bool{"a.com": true} },
		func() time.Time { return at })

	if err := a.flush(); err == nil {
		t.Fatal("往一个目录里写居然成功了 —— 这条测试的前提不成立")
	}
	if len(a.prevRules) != 0 || a.prevDecisions != 0 {
		t.Fatalf("写失败之后仍然推进了基线(prevRules=%+v prevDecisions=%d)—— "+
			"那段增量会被静默吃掉", a.prevRules, a.prevDecisions)
	}
}

// 正常一拍:落盘、推进基线,再一拍没有新流量时累计不变。
func TestAccumulatorFlushThenIdleTickKeepsTheTotal(t *testing.T) {
	p := filepath.Join(t.TempDir(), ruleHistoryFile)
	c := &stats.Counters{}
	c.RuleAttempt("user_direct", "a.com")
	c.RuleAttempt("china_domain", "")
	at := time.Now()
	now := func() time.Time { return at }
	a := newRuleHistoryAccumulator(p, c, "0.4.0",
		func() map[string]bool { return map[string]bool{"a.com": true} }, now)

	if err := a.flush(); err != nil {
		t.Fatalf("第一拍: %v", err)
	}
	at = at.Add(5 * time.Minute)
	if err := a.flush(); err != nil {
		t.Fatalf("第二拍: %v", err)
	}
	h, err := loadRuleHistory(p)
	if err != nil {
		t.Fatal(err)
	}
	if h.Decisions != 2 {
		t.Errorf("Decisions = %d, want 2 —— 第二拍没有新判定却又加了一遍", h.Decisions)
	}
	// 内建那条(Rule == "")必须留着:全局判定门槛要靠它。
	var sawBuiltin bool
	for _, e := range h.Entries {
		if e.Rule == "" {
			sawBuiltin = true
		}
	}
	if !sawBuiltin {
		t.Error("内建列表那条被剪掉了 —— 它不在 currentRules 里,但它不是孤儿")
	}
	if h.UptimeSeconds != int64((5 * time.Minute).Seconds()) {
		t.Errorf("UptimeSeconds = %d, want 300(第一拍 now 与构造时刻相同,增量为 0)", h.UptimeSeconds)
	}
}

// **「读不出配置」不许被当成「配置里一条规则都没有」。**
//
// pruneRuleHistory 按「不在当前规则集里就剪」工作,所以一张空表会把**所有**带
// Rule 的条目当孤儿剪光 —— 而累计一旦剪掉就再也回不来。配置读不出来是暂时的
// (文件正被改写、权限抖动),把它读成「用户删光了所有规则」是把一次瞬时故障
// 变成永久的数据丢失。
//
// 这条钉的是 pruneRuleHistory 的行为本身;Run 那一侧的兜底(读不出就用启动快照)
// 是同一条纪律的另一半。
func TestPruneWithAnEmptyRuleSetWipesEverything(t *testing.T) {
	h := ruleHistory{SchemaVersion: ruleHistorySchema, Entries: []ruleHistoryEntry{
		{Source: "user_direct", Rule: "*.qq.com", Attempts: 3},
		{Source: "china_domain", Rule: "", Attempts: 100},
	}}
	got := pruneRuleHistory(h, map[string]bool{})
	// 这就是它的行为 —— **不是理想行为,是它为什么不能被喂空表的理由**。
	if len(got.Entries) != 1 || got.Entries[0].Rule != "" {
		t.Fatalf("剪完 = %+v —— 这条测试记录的是「空规则集会剪光带 Rule 的条目」"+
			"这个事实;它变了的话,Run 那一侧的兜底理由也要跟着重新想一遍", got.Entries)
	}
}

// **ctx 取消之后那次收尾写盘必须真的发生,而且调用方等得到它。**
//
// 它带着自上一拍以来、最长可达一个完整周期(5 分钟)的增量 —— 是所有写盘里最有
// 价值的一次。把它只写在 goroutine 里而没人等,Run 返回后进程可能先退出,那次写
// **静默不发生**:累计少一段,没有任何一处报错。(第一版就是这么写的。)
func TestRuleHistoryLoopFlushesOnceMoreWhenCancelled(t *testing.T) {
	p := filepath.Join(t.TempDir(), ruleHistoryFile)
	c := &stats.Counters{}
	c.RuleAttempt("user_direct", "a.com")
	at := time.Now()
	a := newRuleHistoryAccumulator(p, c, "v",
		func() map[string]bool { return map[string]bool{"a.com": true} },
		func() time.Time { return at })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	// 周期给得很长:这条测试要的是**取消**触发的那一次,不是周期触发的。
	go runRuleHistoryLoop(ctx, time.Hour, a, done)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消之后循环没有收尾 —— done 永远不关,调用方会一直等到超时")
	}

	h, err := loadRuleHistory(p)
	if err != nil {
		t.Fatalf("收尾那次写盘没落下东西: %v", err)
	}
	if h.Decisions != 1 {
		t.Fatalf("Decisions = %d, want 1 —— 取消时那次 flush 没有发生", h.Decisions)
	}
}

// 等待有上限:done 永远不关时也必须返回。
//
// **停止路径不许因为别的事没做完而变慢或失败** —— 本仓库为「关机慢」栽过一次
// 71 分钟的事故。丢一段统计远好过让用户关不掉保护。
func TestWaitingForTheFinalFlushIsBounded(t *testing.T) {
	start := time.Now()
	waitRuleHistoryFlush(make(chan struct{})) // 永不关闭
	if elapsed := time.Since(start); elapsed > ruleHistoryShutdownWait*3 {
		t.Fatalf("等了 %s —— 收尾写盘卡住时,关闭必须照样走下去", elapsed)
	}
}
