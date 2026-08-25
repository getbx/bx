# 死规则 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `bx doctor` 敢说「这条用户规则从来没命中过」—— 而不是「本次运行没命中」。

**Architecture:** Core 自己把按规则计数跨重启累计到 `/var/lib/bx/rule-history.json`
(它已经在往那个目录写),经**控制 socket**(0666,非 root 可读)与本次运行的计数
**并列**发布;判据仍是 `internal/rulereview` 里的纯函数,门槛是「累计 Core 运行时长」
与「全局累计判定数」双门槛。

**Tech Stack:** Go 1.26、`internal/supervisor`(`atomicWriteFile`、`refreshLoop` 范式)、
`internal/stats`(`Counters`/`Report`)、`internal/rulereview`(纯判据 + AST 纯度守卫)、
`internal/cli`(doctor 渲染)。

**设计依据:** `docs/superpowers/specs/2026-08-17-dead-rules-design.md`。**先读它的
「判据」与「已知会成为问题、但本设计不解的」两节。**

## Global Constraints

- **双门槛,三个条件都要满足**才判死:该规则累计 `attempts == 0`、Core **累计运行
  ≥ 14 天**、全局**累计判定 ≥ 20,000 次**。
- **「累计运行时长」是 Core 在跑的时长**,不是「距首次见到这条规则过了多久」。
  后者好实现得多,但它把 Core 没在跑的时间也算进去 —— 那段时间任何规则都不可能被
  命中,拿它当分母**等于悄悄降低门槛**。
- **「全局累计判定数」含内建列表命中**(每一次 `RuleAttempt`)。它衡量「这台机器有没有
  真的被用过」;只数用户规则会让流量几乎全走内建列表的机器**永远达不到门槛**,于是
  功能静默从不生效。
- **「没有条目」≠「attempts == 0」。** `maxTrackedRules = 256` 满了之后新键不再被记。
  溢出过就把死规则这一类整个标为**没查**,绝不当成「没命中」。
- **历史跟着规则走,不跟着二进制走**,但**跨过几个版本要报出来**,让用户打折。
- **累计值与本次运行的计数分开发布,不合并成一个数。**
- **Core 不在跑 / 历史读不出 / schema 不认 / 溢出过 ⇒ 这一类是「没查」并说明理由**,
  与 `Report.BuiltinListChecked` 同一条纪律。
- **`DeadCount` 与既有四个计数并列,永不相加。**
- **`internal/rulereview` 的 AST 纯度守卫不许放宽**(禁 `os`/`net`/`os/exec`,internal
  依赖只许 `route` 与 `policy`)。全部 I/O 留在 `internal/supervisor` 与 `internal/cli`。
- **不改 `stats` 现有 JSON 字段**(`bx status --json` 是被消费的契约);只能新增。
- **「不给内建列表计数」与「内建那条要参与门槛 3」不矛盾,别把其中一边当 bug 修掉。**
  spec 的非目标说的是**不为内建列表产出 finding**(它没有哪一行可点名、用户也改不了);
  而内建命中**要计入全局判定总数**,因为门槛 3 衡量的是「这台机器有没有被用过」。
  所以:内建条目**存**、**参与门槛**、**永不产出 finding**。三件事分开。
- **绝不启动 bx、绝不改路由、不要 sudo、不读写 `/var/lib/bx` 与 `/etc/bx`。**
  这台是项目所有者的生产机器。测试一律 `t.TempDir()`。
- **绝不并行派两个会写盘的子代理进同一个 checkout**(2026-08-17 实测:一方 `git stash`
  把另一方五个未提交文件卷走)。
- 中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`。
  在 `master` 直接提交。
- 每个 task 收尾跑 `bash scripts/verify.sh --quick; echo "EXIT=$?"`,**前台跑**;最后一个
  task 跑全量。判据是退出码。

---

## File Structure

| 文件 | 责任 |
|---|---|
| `internal/supervisor/rulehistory.go`(新) | 持久化:信封、`load`/`save`、`merge`、按当前配置剪孤儿。**纯逻辑与 I/O 分开**,merge/prune 是纯函数 |
| `internal/supervisor/rulehistory_test.go`(新) | merge/prune/schema/原子替换 |
| `internal/stats/outcome.go`(改) | 全局判定计数 + 溢出标志 |
| `internal/stats/render.go` / `stats.go`(改) | `Report` 新增累计历史字段 |
| `internal/supervisor/control.go`(改) | `newStatusReporter` 多一个历史来源 |
| `internal/supervisor/run.go`(改) | 起累计与写盘的那一拍 |
| `internal/rulereview/input.go` / `verdict.go` / `review.go`(改) | `ClassDead`、`DeadCount`、门槛判据、没查理由 |
| `internal/cli/rulereview.go`(改) | 组装(从 Core 报告取历史)+ 渲染 |

**为什么持久化住 `internal/supervisor` 而不是新包**:它要复用那里已有的
`atomicWriteFile`(`refresh.go:52`)。本仓库已经有两份手写的原子写(`guardian/store.go`
的 `writeJSONAtomically`、`toolkeys/store.go` 的 `save`),**不要再加第三份**;
合并那三份是独立的一件事,牵动两个热路径而不改行为,不在本计划范围。

---

## Task 1: 持久化层(信封、merge、剪孤儿)

**Files:** Create `internal/supervisor/rulehistory.go`、`internal/supervisor/rulehistory_test.go`

**Interfaces:**
- Consumes: `stats.RuleOutcome{Source, Rule string; Attempts, Failures int64}`;`atomicWriteFile(path string, data []byte) error`(`refresh.go:52`)
- Produces:
  - `const ruleHistorySchema = 1`
  - `type ruleHistoryEntry struct { Source, Rule string; Attempts, Failures int64 }`
  - `type ruleHistory struct { SchemaVersion int; Entries []ruleHistoryEntry; UptimeSeconds int64; Decisions int64; Versions []string; TrackingLimit int; Overflowed bool; UpdatedAt time.Time }`
  - `func loadRuleHistory(path string) (ruleHistory, error)`
  - `func saveRuleHistory(path string, h ruleHistory) error`
  - `func mergeRuleHistory(prev ruleHistory, live []stats.RuleOutcome, uptimeDelta time.Duration, decisionsDelta int64, version string, trackingLimit int, overflowed bool, now time.Time) ruleHistory`
  - `func pruneRuleHistory(h ruleHistory, currentRules map[string]bool) ruleHistory`

- [ ] **Step 1: 写失败测试**

创建 `internal/supervisor/rulehistory_test.go`:

```go
package supervisor

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getbx/bx/internal/stats"
)

func TestMergeAccumulatesAcrossRuns(t *testing.T) {
	at := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	prev := ruleHistory{SchemaVersion: ruleHistorySchema, UptimeSeconds: 3600, Decisions: 100,
		Versions: []string{"0.3.0"},
		Entries:  []ruleHistoryEntry{{Source: "user_direct", Rule: "*.qq.com", Attempts: 5, Failures: 1}}}

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
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/supervisor/ -run 'TestMergeAccumulates|TestOverflowIsSticky|TestPruneDrops|TestLoadMissing|TestUnknownSchema|TestSaveReplaces' -v`
Expected: FAIL —— `undefined: ruleHistory` 等

- [ ] **Step 3: 实现 `rulehistory.go`**

要点(按上面的测试写,注释里记下这些理由):

- 信封带 `schema_version`,未知版本返回**空历史 + error**(不迁移)。
- `mergeRuleHistory` 按 `{Source, Rule}` 累加;**时长与判定数是累加不是覆盖**。
- **版本集合去重且保持有序**(报告要拿它说跨了几个版本,重复追加会虚高)。
- **`Overflowed` 是粘性的**:一旦为真永不转假 —— 那段缺口已经在累计值里了。
- `pruneRuleHistory` 只剪**有 `Rule` 且不在当前配置里**的条目;**`Rule == ""`
  (内建列表)不是孤儿**,门槛 3 要靠它。
- `saveRuleHistory` 走既有的 `atomicWriteFile`,**不要新写一份原子写**。
- `UpdatedAt` 每次写都盖,读侧渲染时可以报「这份历史多旧」。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/supervisor/ -run 'TestMergeAccumulates|TestOverflowIsSticky|TestPruneDrops|TestLoadMissing|TestUnknownSchema|TestSaveReplaces' -v`
Expected: PASS(6 条)

- [ ] **Step 5: 变异验证两条最要紧的**

**粘性溢出**:把 `Overflowed` 改成每次用当次的值覆盖 ⇒
`TestOverflowIsSticky` 必须转红,信息含「正是这个功能最忌讳的假阳性」。

**内建那条不是孤儿**:把 prune 改成「凡不在 currentRules 里就丢」⇒
`TestPruneDropsEntriesNotInCurrentConfig` 必须转红,信息含「被当成孤儿剪掉了」。

两处都改回来后重跑 ⇒ PASS。

- [ ] **Step 6: verify + 提交**

```bash
bash scripts/verify.sh --quick; echo "EXIT=$?"
git add internal/supervisor/rulehistory.go internal/supervisor/rulehistory_test.go
git commit -m "$(cat <<'EOF'
feat(supervisor): 按规则计数的跨重启累计历史(持久化层)

「0 次」只说明本次运行没命中 —— 死规则判据必须跨重启累计,否则会把刚重连的机器上
每一条规则都报成死的。信封形状照 guardian/throughputhistory.go(schema_version、
原子替换、带时间戳),复用 refresh.go 已有的 atomicWriteFile(本仓库已有两份手写的
原子写,不再加第三份)。

两条不变量各有一条变异验证过的测试:

· **Overflowed 是粘性的。** 跟踪表满过之后,这段历史里可能有规则从没被记过,而它们
  在表里与「记了、从没命中」一模一样;后续运行没溢出就清掉标志,会让「没有条目」被
  当成「没命中」—— 正是这个功能最忌讳的假阳性。
· **内建列表那条(Rule == "")不是孤儿。** 它没有对应的配置行,按「不在当前配置里就
  丢」的字面规则会被剪掉,而全局判定数(门槛 3)要靠它 —— 剪掉它门槛就永远达不到。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Core 侧累计 —— 全局判定数、溢出标志、写盘那一拍

**Files:** Modify `internal/stats/outcome.go`、`internal/stats/stats.go`;
Modify `internal/supervisor/run.go`;Test `internal/stats/outcome_test.go`、
`internal/supervisor/rulehistory_test.go`

**Interfaces:**
- Consumes: Task 1 的 `loadRuleHistory`/`saveRuleHistory`/`mergeRuleHistory`/`pruneRuleHistory`;
  既有 `refreshLoop(ctx, interval, healthy, doRefresh)`(`refresh.go:104`);
  `Counters.bump(source, rule string, failed bool)`(`outcome.go:40`);`maxTrackedRules`
- Produces:
  - `func (c *Counters) Decisions() int64` —— 全局累计判定数(**含内建列表命中**)
  - `func (c *Counters) RuleTrackingOverflowed() bool`
  - `func (s Snapshot) …` 不变;新增方法挂在 `Counters` 上
  - Core 起一条周期任务 + 关闭时再写一次

- [ ] **Step 1: 写失败测试(stats 侧)**

```go
// 全局判定数衡量「这台机器有没有真的被用过」,所以**内建列表命中也要数**。
// 只数用户规则会让流量几乎全走内建列表的机器永远达不到门槛,于是死规则这一类
// 静默地从不生效 —— 那是这个仓库反复出现的失效形状。
func TestDecisionsCountsBuiltinHitsToo(t *testing.T) {
	var c Counters
	c.RuleAttempt("user_direct", "*.qq.com")
	c.RuleAttempt("china_domain", "")
	c.RuleAttempt("china_domain", "")
	if got := c.Decisions(); got != 3 {
		t.Fatalf("Decisions() = %d, want 3(含两次内建命中)", got)
	}
}

// 失败不是一次新的判定 —— RuleFailure 跟在 RuleAttempt 之后,数两次会让门槛虚高。
func TestFailuresDoNotDoubleCountDecisions(t *testing.T) {
	var c Counters
	c.RuleAttempt("user_direct", "a.com")
	c.RuleFailure("user_direct", "a.com")
	if got := c.Decisions(); got != 1 {
		t.Fatalf("Decisions() = %d, want 1", got)
	}
}

// 跟踪表满过要留下痕迹 —— 之后「没有条目」不许被读成「没命中」。
func TestRuleTrackingOverflowIsObservable(t *testing.T) {
	var c Counters
	if c.RuleTrackingOverflowed() {
		t.Fatal("空计数器不该报溢出")
	}
	for i := 0; i < maxTrackedRules+5; i++ {
		c.RuleAttempt("user_direct", fmt.Sprintf("r%d.com", i))
	}
	if !c.RuleTrackingOverflowed() {
		t.Fatalf("插了 %d 条(上限 %d)却没报溢出", maxTrackedRules+5, maxTrackedRules)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/stats/ -run 'TestDecisions|TestFailuresDoNot|TestRuleTrackingOverflow' -v`
Expected: FAIL —— `c.Decisions undefined`

- [ ] **Step 3: 实现 stats 侧**

`Counters` 加两个字段(用 `atomic.Int64` / `atomic.Bool`,不要挤进 `ruleMu` 保护的临界区
之外去读写):`decisions`、`ruleOverflow`。

- `RuleAttempt` 递增 `decisions`;**`RuleFailure` 不递增**(它跟在 attempt 之后)。
- `bump` 里那条「表满则不记新键」的分支置 `ruleOverflow`。**找到那个分支再改,别猜它在
  哪一行** —— `outcome.go:52-56` 附近,但先读。
- 两个 getter。**不动任何既有 JSON 字段。**

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/stats/ -count=1`
Expected: PASS —— **既有测试一条都不许改**;有转红的先判断是「新行为正确、旧断言过期」
还是「改坏了」,前者要在报告里逐条说明。

- [ ] **Step 5: Core 起累计与写盘**

`run.go` 在起 `refreshLoop` 那一带(`run.go:743` 附近)加一条**同款**的周期任务:

- 周期:**5 分钟**。理由写进注释:它决定 SIGKILL 时最多丢多少累计,而 launchd 确实会
  SIGKILL Core(CLAUDE.md 记着这件事打断过 defer)。
- 每一拍:`live := counters.ruleSnapshot()`(**它是 unexported,同包可用**)、
  `uptimeDelta = 距上次写盘的时长`、`decisionsDelta = Decisions() - 上次写盘时的值`、
  merge、prune(按当前 config 的 rules)、save。**delta 而不是总量** —— 否则重启后
  会把上一轮的量再加一遍。
- **关闭时再写一次**(Run 的 defer 里),但**不能只靠它**。
- 路径:`filepath.Join(dataDir, "rule-history.json")`,`dataDir` 用 Run 已经拿到的那个,
  **不要新猜一份路径常量**(本仓库明令:叶子包不许自己猜路径)。
- 读盘失败 / schema 不认:**记一行日志继续**,历史当空。绝不因此让 Core 起不来。

- [ ] **Step 6: 写「delta 不是总量」的守卫**

追加到 `internal/supervisor/rulehistory_test.go`:

```go
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
```

> **注意**:上面这条测试要求 `mergeRuleHistory` 的 `live` 参数语义是 **delta**
> (自上次写盘以来新增的量),不是 Core 启动至今的总量。**Task 1 的实现如果把它当
> 总量写了,这里要改的是调用方的算法而不是放宽这条测试** —— 在报告里说明你怎么算 delta
> (最直接的做法是保存上一拍的 `ruleSnapshot()` 并相减)。

- [ ] **Step 7: 变异 + verify + 提交**

变异:把 `RuleFailure` 也改成递增 `decisions` ⇒ `TestFailuresDoNotDoubleCountDecisions`
转红。改回来。

```bash
bash scripts/verify.sh --quick; echo "EXIT=$?"
git add internal/stats/ internal/supervisor/
git commit -m "$(cat <<'EOF'
feat(stats,supervisor): 全局判定数、跟踪溢出标志,与 Core 侧的累计写盘

全局判定数**含内建列表命中**:它衡量「这台机器有没有真的被用过」,只数用户规则会让
流量几乎全走内建列表的机器永远达不到门槛,于是死规则这一类静默从不生效。
RuleFailure 不递增(它跟在 RuleAttempt 之后,数两次会让门槛虚高),有测试钉住。

写盘周期 5 分钟 + 关闭时再写一次,**不能只靠后者**:launchd 会 SIGKILL Core,
那时最后一段累计会丢,周期写保证丢的量有上界。

写盘用 **delta 不是总量**,另有一条测试钉住 —— 用总量的话累计值会随写盘频率虚高,
而写盘频率是实现细节,判据不该受它影响。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: 经控制 socket 发布累计历史

**Files:** Modify `internal/stats/render.go`(`Report`)、`internal/supervisor/control.go`
(`newStatusReporter`)、`internal/supervisor/run.go`(传参);
Test `internal/supervisor/control_reporter_test.go`

**Interfaces:**
- Consumes: Task 1/2 的 `ruleHistory`
- Produces: `stats.Report` 新增
  ```go
  	// RuleHistory 是**跨重启累计**的按规则计数,与上面 Snapshot 里 Rules[] 的
  	// 「本次运行」计数**并列发布,绝不合并** —— 「0 次」只说明本次运行没命中,
  	// 而死规则判据要的是累计值。nil = 这一版 Core 没有这个概念 / 读不出来。
  	RuleHistory *RuleHistorySnapshot `json:"rule_history,omitempty"`
  ```
  与
  ```go
  type RuleHistorySnapshot struct {
  	Rules         []RuleOutcome `json:"rules,omitempty"`
  	UptimeSeconds int64         `json:"uptime_seconds"`
  	Decisions     int64         `json:"decisions"`
  	Versions      []string      `json:"versions,omitempty"`
  	Overflowed    bool          `json:"overflowed"`
  	UpdatedAt     time.Time     `json:"updated_at"`
  	// SkipReason 非空表示这份历史不可用(读不出、schema 不认)。
  	SkipReason string `json:"skip_reason,omitempty"`
  }
  ```
- `newStatusReporter` 末尾多一形参 `history func() *stats.RuleHistorySnapshot`(**必填**)

- [ ] **Step 1: 写失败测试**

按 `internal/supervisor/control_reporter_test.go` 里既有那条
`TestStatusReporterIncludesBothGuardAndConfigWarnings` 的形状写:构造 reporter、
调它、断言 `Report.RuleHistory` 不为 nil 且带着注入的值。**先读那个文件**,复用它的
替身与构造方式,不要新造一套。

再加一条:**history 提供者返回 nil 时,`Report.RuleHistory` 必须是 nil 而不是零值结构**
—— 零值会让消费方读成「累计 0 次、跑了 0 秒」,而那与「没有这份数据」是两件事。

- [ ] **Step 2-4: 跑红 → 实现 → 跑绿**

`newStatusReporter` 里 `Report{...}` 组装处加 `RuleHistory: history()`;`run.go` 的
调用点传一个从 Task 2 那份内存历史读的闭包。

**形参必填、不认 nil provider** —— 漏传就编不过,那是接线正确的唯一硬凭据
(本仓库教训:「接线的真凭据是编译器」这句话我说过一次而它只证明实参被传了、不证明
它被用了,所以**还要一条走真 reporter 的测试**,见 Step 1)。

- [ ] **Step 5: 变异验证接线**

把 `RuleHistory: history()` 改成 `RuleHistory: nil` ⇒ Step 1 那条测试必须转红。
改回来。

- [ ] **Step 6: verify + 提交**(commit message 要写明「并列发布、绝不合并」的理由)

---

## Task 4: 判据进 `internal/rulereview`

**Files:** Modify `internal/rulereview/input.go`、`verdict.go`、`review.go`;
Test `internal/rulereview/review_test.go`

**Interfaces:**
- Produces:
  - `ClassDead`(加在 `ClassShadowedByBuiltinList` **之后**,零值仍是 `ClassRisky`)
  - `Report.DeadCount int`、`Report.DeadChecked bool`(**无 omitempty**)、`Report.DeadSkipReason string`
  - `Report.DeadVersionsSpanned int`
  - **本包自己的键与计数类型**(`rulereview` 的纯度守卫只许依赖 `route` 与 `policy`,
    **不能 import `stats`**,所以不能直接收 `stats.RuleOutcome`;调用方
    `internal/cli` 负责转换):
    ```go
    // RuleKey 与 stats 那边的 ruleKey 同形(判定层 + 规则原文),但**是本包自己的
    // 类型** —— 纯判据包不许依赖 stats。转换由 internal/cli 做。
    type RuleKey struct {
    	// Source 是做出判定的那一层(user_direct / user_proxy / …),取值与
    	// route.Source.String() 一致。
    	Source string
    	// Rule 是配置里那一行的原文;内建列表命中时为空。
    	Rule string
    }

    // RuleCounts 是一条规则的**累计**尝试与失败次数。
    type RuleCounts struct {
    	Attempts int64
    	Failures int64
    }
    ```
  - `Input` 新增:
    ```go
    	// History 是**跨重启累计**的按规则计数,键是本包的 RuleKey。
    	// nil = 拿不到(Core 没在跑 / 读不出 / schema 不认),那时死规则这一类是
    	// **没查**而不是「零条」。
    	History map[RuleKey]RuleCounts
    	// HistoryUptime / HistoryDecisions 是两个门槛的当前值。
    	HistoryUptime    time.Duration
    	HistoryDecisions int64
    	// HistoryVersions 是这段累积跨过的 bx 版本数,报告要说出来让用户打折。
    	HistoryVersions int
    	// HistoryOverflowed:跟踪表满过 ⇒ 「没有条目」不等于「没命中」⇒ 整类没查。
    	HistoryOverflowed bool
    	// HistorySkipReason 在 History 为 nil 时说明为什么。
    	HistorySkipReason string
    ```
  - `const deadMinUptime = 14 * 24 * time.Hour`、`const deadMinDecisions int64 = 20000`
  - `func deadFindings(kind string, rules []domainRule, in Input) []Finding`

- [ ] **Step 1: 写失败测试**

必须覆盖(每条都写清后果):

1. 三门槛全满足 ⇒ 报死,finding 点名规则**原文**,`CoveredBy` 为空。
2. **时长不够** ⇒ 不报(刚重连的机器一条都不许报)。
3. **判定数不够** ⇒ 不报(开着挂机的机器一条都不许报)。
4. **`History == nil`** ⇒ `DeadChecked == false`、`DeadSkipReason != ""`、`DeadCount == 0`。
5. **`HistoryOverflowed`** ⇒ 整类没查,理由里点名溢出。**这一条最要紧**:表满时
   「没有条目」与「有条目且 attempts==0」在数据上无法区分。
6. **历史里没有该规则的条目**(未溢出)⇒ **报死**(它确实从没被记过一次命中)。
   与第 5 条合起来才完整:溢出时不敢判,没溢出时敢判。
7. **`attempts > 0`** ⇒ 不报。
8. `DeadVersionsSpanned` 透传到 `Report`。
9. **计数不合并**:同时造出四类 + 死类,断言五个计数各自独立、没有任何一处求和。

- [ ] **Step 2-4: 跑红 → 实现 → 跑绿**

`Review` 在四类之后追加 `deadFindings`;门槛不满足或 `History == nil` 或
`HistoryOverflowed` 时**整类不产出并置 `DeadChecked=false` + 理由**。

**纯度守卫不许放宽** —— `time` 是标准库、允许;`os`/`net` 一律不许。
跑 `go test ./internal/rulereview/ -run TestRulereviewPackageStaysPure`。

- [ ] **Step 5: 变异验证两条**

**门槛**:把两个门槛判断去掉 ⇒ 第 2、3 条测试转红。
**溢出**:把 `HistoryOverflowed` 那道门去掉 ⇒ 第 5 条转红,信息要说清
「表满时『没有条目』与『从没命中』无法区分」。

- [ ] **Step 6: verify + 提交**

---

## Task 5: doctor 渲染

**Files:** Modify `internal/cli/rulereview.go`、`internal/cli/cli.go`;
Test `internal/cli/rulereview_test.go`

**Interfaces:**
- Consumes: Task 3 的 `stats.Report.RuleHistory`;Task 4 的 `ClassDead`/`DeadChecked`/
  `DeadSkipReason`/`DeadVersionsSpanned`;既有 `buildRuleReviewInput(cfg, china)`
- Produces: `buildRuleReviewInput` 多一个历史入参;`ruleReviewDoctorLines` 多一行死规则

- [ ] **Step 1: 写失败测试**

1. **每类一条 check、名字唯一**(这一支已经因为同名 check 静默丢过一次安全结论,
   见 `55ef8ea`);仓库级重名守卫 `internal/cli/doctor_check_names_test.go` 已经存在,
   确认新 check 名不与既有冲突。
2. **死规则那一行要说出跨了几个版本**;跨版本 > 1 时措辞要让用户知道该打折。
3. **「没查」必须说出来**(Core 没在跑 / 溢出 / 读不出),不许静默缺席。
4. **干净配置一个字都不说**(唯一例外仍是「没查」)。
5. **两条路径(文本 + `--json`)共用同一个 `ruleReviewDoctorLines`**,不许分叉。

- [ ] **Step 2-4: 跑红 → 实现 → 跑绿**

历史从 Core 报告取:doctor 已经会读控制 socket 拿状态,**沿用那一条路**,别新开。
Core 不在跑时 `History = nil` + 理由。

- [ ] **Step 5: 人眼看一遍真输出**

```bash
# 造一个临时 config,并注入一份满足门槛的假历史(测试替身,不碰真 socket)
go run ./cmd/bx doctor --config /tmp/bx-dead-demo.yaml --skip-probe
```
**`--skip-probe` 不碰网络;绝不用 `/etc/bx/config.yaml`。** 把输出逐字贴进报告,
包含「没查」那一种。

- [ ] **Step 6: 全量 verify + 提交**

```bash
bash scripts/verify.sh; echo "EXIT=$?"
```

---

## 收尾:CLAUDE.md 与真机验收

- [ ] **Step 1: CLAUDE.md**

要点:双门槛与两个数值的依据;**「累计时长是 Core 在跑的时长」与「全局判定数含内建
命中」两处「看起来等价但会悄悄改判据」的选择**;溢出粘性;内建那条不是孤儿;
历史跟规则走但要报跨版本数;doctor 经控制 socket 而不读文件(`/var/lib/bx` 是
`drwx------`);**以及这一类在真机上从未验过**。

- [ ] **Step 2: 提交文档**

- [ ] **Step 3: 真机验收(交给项目所有者)**

```bash
sudo bx doctor --skip-probe
bx status --json | jq '.rule_history'
```

**头一次装上之后,死规则这一类必然显示「没查」**(累计时长与判定数都不够)——
**那是正确的**,不是 bug。要等 14 天、且判定数过 2 万,它才会开始说话。
`bx status --json` 里的 `rule_history` 能让你随时看到离门槛还有多远。

**要盯的**:`rule_history.uptime_seconds` 与 `decisions` 是否在**跨重启后继续涨**
(而不是每次重启归零)—— 那是这整件事唯一真正要验的东西。

---

## 执行记录(2026-08-24)

Task 1-5 全部完成,收尾的 CLAUDE.md 记档已提交。**真机验收未做**(那一步交给
项目所有者,见上面 Step 3)。

**计划里被实测证伪的两处**,留着免得下一个人照着走:

1. Task 2 说 `live := counters.ruleSnapshot()`「**它是 unexported,同包可用**」——
   那是站在 `internal/stats` 里说的,而调用方 `run.go` 在 `internal/supervisor`,
   够不着。实际用的是导出的 `counters.Snapshot().Rules`。
2. Task 5 说「doctor **已经会**读控制 socket 拿状态,沿用那一条路」—— 实测 doctor
   此前根本不读控制 socket,这条路是新开的(与 `bx status` 共用
   `supervisor.FetchStatusReport`)。

**计划之外、实现时定下并各配了测试的判断**(理由都写进了 CLAUDE.md 那一节):
表满后仍计入判定数 · 写失败不推进基线 · 总量回退按重置处理 · 配置读不出退回启动
快照 · 收尾 flush 必须有人等且等待有上限 · 归一化比对 + 按 Source 分表 ·
`ClassDead` 必须有渲染路径(新守卫)。

**一个测试抓不到、肉眼看输出才抓到的 bug**:死规则那一行留了个悬空的箭头
(`*.a ← 、*.b ← `)。既有断言查「含规则原文」而它确实含,全绿。Step 5 那句
「人眼看一遍真输出」是这一轮唯一抓到它的手段。

---

## 已知缺口(写在计划里,免得被当成做完了)

- **14 天与 20,000 这两个数没有真机依据支撑「够不够」** —— 它们取自这台机器已有的
  量级,但「一条规则闲置多久算死」本质上是产品判断,要真机跑一段才知道有没有假阳性。
- **`maxTrackedRules = 256` 没动。** 溢出时整类标「没查」,而不是提高上限 —— 提高它
  要先知道真机上有没有人接近过那个数,而今天没有这个数据。
- **第 4 条(缺失规则)仍未做。** 它依赖同一套数据,但 spec 说门槛要严得多、宁可不说。
