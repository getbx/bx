package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

// **常驻面板只发危险那一类,别的类一条都不许漏出来。**
//
// 冗余与失效是**建议**,对任何成熟配置都不为零,放进常驻面板会变墙纸、把真正
// 要紧的那条一起淹掉(与项目所有者否掉「Direct rules: N unreachable」常驻红字
// 同一条判断)。它们留在 `bx doctor` 里,那是诊断命令。
//
// 输入刻意挑成**同时产出两类**的:两条 s3 规则里,后一条被前一条覆盖 ⇒ Review
// 产出 3 条 finding(2 risky + 1 shadowed_by_user_rule)。**只用单条规则的输入
// 测不出这个过滤器** —— 那时 Review 只产出一条 risky,把整个过滤器删掉结果也不变
// (第一版就是这么写的,变异实测全绿:守卫钉住的是缺陷旁边的东西)。
func TestRiskyRuleWarningsPublishOnlyTheRiskyClass(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{
		Direct: []string{"*.s3.amazonaws.com", "bucket.s3.amazonaws.com"},
	}}}

	got := riskyRuleWarnings(cfg)
	if len(got) != 2 {
		t.Fatalf("告警数 = %d,想要 2(两条 risky;那条 shadowed_by_user_rule 不该"+
			"出现在常驻面板里)—— 实际 %+v", len(got), got)
	}
	for _, w := range got {
		if w.Name != "risky_direct_rule" {
			t.Errorf("常驻面板漏出了非危险类的告警:%+v", w)
		}
		if w.Severity != "warn" {
			t.Errorf("severity = %q,必须是 warn —— error 会把一台工作正常的机器"+
				"降级成 Needs Attention(11338a0 的教训)", w.Severity)
		}
	}
}

// 对照组:没有危险规则时**一条都不许有**。
//
// 少了它,一个「恒产出告警」的实现也会通过上面那条 —— 而一条恒亮的常驻告警
// 就是墙纸。这条用的输入会产出一条 shadowed_by_user_rule、零条 risky。
func TestRiskyRuleWarningsStaySilentWhenNothingIsRisky(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{
		Direct: []string{"*.example.com", "a.example.com"},
	}}}
	if got := riskyRuleWarnings(cfg); len(got) != 0 {
		t.Errorf("没有危险规则却产出了 %d 条告警:%+v —— 常驻告警一旦恒亮就是墙纸", len(got), got)
	}
}

// **一条本身永不生效的危险规则仍然会告警 —— 而原因不是「没填 Proxy」。**
//
// CLAUDE.md 曾把这条记成「`riskyRuleWarnings` 只填 `Input.Direct`,于是同名规则
// 同时在 proxy 里时仍会告警;方向是过度告警,刻意接受」。**机制说错了**(2026-08-24
// 探针实测):`ClassRisky` 是**独立判**的 —— 就算把 Proxy 也填进 Input,Review 照样
// 产出那条 risky,只是**额外**多一条 `overridden_by_opposite_kind`,而后者本来就会
// 被这里的过滤器丢掉。也就是说填不填 Proxy,这条告警**一个字都不会变**。
//
// 结论没变(过度告警、刻意接受),但**理由变了**:它不来自输入没填全,来自
// 「这条规则危不危险」与「这条规则生不生效」是两个独立的问题。要真的抑制它,
// 得让 riskyRuleWarnings 去查同一条 rule 有没有 overridden finding —— 而那正是
// 不该做的:漏报的代价是真实 IP 暴露,多报只是提醒了一条不生效的规则,不对称。
func TestRiskyClassIsJudgedIndependentlyOfWhetherTheRuleEverFires(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{
		// Router 先查 proxy 再查 direct ⇒ 这条 direct 规则从来不会生效。
		Direct: []string{"*.s3.amazonaws.com"},
		Proxy:  []string{"*.s3.amazonaws.com"},
	}}}
	got := riskyRuleWarnings(cfg)
	if len(got) != 1 {
		t.Fatalf("告警数 = %d,想要 1 —— 过度告警是刻意的方向", len(got))
	}
	if !strings.Contains(got[0].Detail, "*.s3.amazonaws.com") {
		t.Errorf("告警没点名是哪一条规则:%q —— 不点名用户无从下手", got[0].Detail)
	}
}
