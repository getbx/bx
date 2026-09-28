package supervisor

import (
	"os"
	"strings"
	"testing"
)

// Run() 里 pf 重置必须在 Hijack 成功之后、接管播报之前,而且拆除台账里要先登记那条
// 冲 anchor 的兜底。读源码是这里唯一够得着的办法(Run() 只在 netns 集成台里跑,而那是
// linux);锚点漂了就响亮失败,不许安静通过。
func TestRunWiresThePFResetAfterHijack(t *testing.T) {
	src, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	hijack := strings.Index(s, "plat.Hijack(tunH, serverBypass, cfg.Bypass)")
	flush := strings.Index(s, `teardowns.push("flush bx pf anchor"`)
	reset := strings.Index(s, "runPFReset(ctx, opts.PFReset, serverBypass, cfg.Bypass)")
	summary := strings.Index(s, "takeoverSummary(global, cfg.Mode, listsOverridden)")
	if hijack < 0 || flush < 0 || reset < 0 || summary < 0 {
		t.Fatalf("anchors moved: hijack=%d flush=%d reset=%d summary=%d", hijack, flush, reset, summary)
	}
	if !(hijack < flush && flush < reset && reset < summary) {
		t.Fatalf("order must be Hijack → push flush → runPFReset → summary, got hijack=%d flush=%d reset=%d summary=%d", hijack, flush, reset, summary)
	}
}
