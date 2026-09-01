package supervisor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/config"
)

// 分流脑那一相位从 Run() 里抽出来。
//
// **动机是可断言,不是好看**:Run() 是 764 行的组装根,而本仓库全部事故都在
// 接线。一个相位只要还长在那个函数体里,它的输入输出关系就只能靠读代码确认;
// 抽出来之后,「global 模式不加载 china 列表」「CLI flag 压过 config」这类
// 关系可以被直接断言 —— 而它们各自都对应过真实事故。

func writeListFor(t *testing.T, dir, name string, lines string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// **global 模式一个字节的 china 列表都不该读。**
//
// 它不只是省事:那台机器上 china 列表整个不生效,读了再丢会让日志里的
// china_domain=N 变成一句误导(而 2026-08-31 那条被证伪的接管播报,根子就是
// 有人把 split 的描述套在了 global 上)。
func TestSplitBrainSkipsTheChinaListsEntirelyInGlobalMode(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{DataDir: dir, Global: true}
	cfg.Lists.ChinaDomain = filepath.Join(dir, "does-not-exist.txt")

	out, err := buildSplitBrain(cfg, Options{})
	if err != nil {
		t.Fatalf("global 模式不该因为列表读不到而失败: %v", err)
	}
	if out.DomainCount != 0 || out.CIDRCount != 0 {
		t.Fatalf("global 模式读了 china 列表: domain=%d cidr=%d", out.DomainCount, out.CIDRCount)
	}
	if !out.Router.GlobalProxy {
		t.Fatal("router 没有被置成 global")
	}
	if out.ListsOverridden {
		t.Fatal("global 模式下不该报「列表被覆盖」—— 它压根没读列表")
	}
}

// **CLI flag 压过 config.lists,config 压过内嵌/刷新快照。**
// 这条优先级搞反的后果是安静的:用户以为自己换了参照表,而 bx 用的还是默认那份。
func TestSplitBrainPrefersTheCLIFlagOverTheConfigList(t *testing.T) {
	dir := t.TempDir()
	fromConfig := writeListFor(t, dir, "from-config.txt", "config-only.com\n")
	fromFlag := writeListFor(t, dir, "from-flag.txt", "flag-a.com\nflag-b.com\n")

	cfg := &config.Config{DataDir: dir}
	cfg.Lists.ChinaDomain = fromConfig
	out, err := buildSplitBrain(cfg, Options{ChinaDomainPath: fromFlag})
	if err != nil {
		t.Fatal(err)
	}
	if out.DomainCount != 2 {
		t.Fatalf("用的不是 CLI flag 指的那份列表(读到 %d 条,flag 那份是 2 条)", out.DomainCount)
	}
	if !out.ListsOverridden {
		t.Fatal("指定了自己的列表却没报 overridden —— 自动刷新会拿上游的表盖掉它")
	}
}

// 没有任何覆盖时不许报 overridden:那个标志会**关掉自动刷新**,
// 误报等于让一台本该每天更新列表的机器永远停在首装那份快照上。
func TestSplitBrainDoesNotClaimOverrideWhenThereIsNone(t *testing.T) {
	out, err := buildSplitBrain(&config.Config{DataDir: t.TempDir()}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if out.ListsOverridden {
		t.Fatal("没有任何覆盖却报了 overridden —— 自动刷新会被无声关掉")
	}
}

// 模式标签与 proxyMode 是同一件事的两种说法,不许各说各的 ——
// 2026-08-31 那条被证伪的接管播报就是这么来的。
func TestSplitBrainModeLabelAgreesWithProxyMode(t *testing.T) {
	dir := t.TempDir()
	for _, global := range []bool{false, true} {
		out, err := buildSplitBrain(&config.Config{DataDir: dir, Global: global}, Options{})
		if err != nil {
			t.Fatal(err)
		}
		wantGlobal := proxyMode(global, "host") == "global"
		if gotGlobal := out.Router.GlobalProxy; gotGlobal != wantGlobal {
			t.Fatalf("global=%v: router.GlobalProxy=%v 而 proxyMode 说 %q", global, gotGlobal, proxyMode(global, "host"))
		}
	}
}
