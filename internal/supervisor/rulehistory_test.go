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
