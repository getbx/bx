package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"

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

// **这条常驻安全告警唯一附带的动作,必须是一条真敲得动的命令。**
//
// 它已经错过两次,而两次都没有任何测试拦下来:
//   - 一次指向不存在的 `bx direct remove`(真名是 `rm`)——照着敲得到「未知子命令」;
//   - 一次漏了 `sudo` —— 照着复制粘贴必得 permission denied,因为
//     `bx direct rm` 直接 os.WriteFile 改 /etc/bx/config.yaml,而那份文件是
//     `sudo bx setup` 建的 0600 属主 root。
//
// **一条点名了安全问题却给不出下一步的告警,比不告警更糟**:用户照着做、失败、
// 于是不再相信这条告警,而问题原样留在那里。
//
// cli 那半有同名思路的守卫(internal/cli 的 TestRiskyRuleFindingHintNeedsElevation),
// **而这一半此前完全裸奔** —— 两处各自拼一句 hint,只守一处等于只守一半。
//
// 判据只钉**性质**(带 sudo、点名真实子命令、规则原文带引号),不钉整句字面量:
// 措辞调整时不该跟着变红。
func TestRiskyRuleHintUsesARealSubcommand(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{
		Direct: []string{"*.s3.amazonaws.com"},
	}}}
	got := riskyRuleWarnings(cfg)
	if len(got) != 1 {
		t.Fatalf("告警数 = %d,想要 1 —— 这条测试要看的是它的 hint:%+v", len(got), got)
	}
	hint := got[0].Hint

	if !strings.Contains(hint, ""+elevate.Prefix+"bx direct rm") {
		t.Errorf("hint 没有 `"+elevate.Prefix+"bx direct rm`:%q —— 少了 sudo 就是 permission denied"+
			"(那份 config 是 0600 属主 root),而子命令写错就是「未知子命令」", hint)
	}
	// `remove` 是那次笔误的原文,专门钉住它不许回来。
	if strings.Contains(hint, "direct remove") {
		t.Errorf("hint 又出现了 `direct remove`:%q —— 真名是 `rm`,这条命令敲下去会失败", hint)
	}
	// 规则原文带 `*`,不加引号在 shell 里会被展开成当前目录下的文件名。
	if !strings.Contains(hint, "'*.s3.amazonaws.com'") {
		t.Errorf("hint 里的规则原文没带引号:%q —— 带 `*` 的规则不加引号会被 shell 展开", hint)
	}
	// 改完必须重连才生效(bx 不热重载),不说等于让用户以为改完就好了。
	if !strings.Contains(hint, "bx down") || !strings.Contains(hint, "bx up") {
		t.Errorf("hint 没说改完要重连:%q —— bx 不热重载,不说等于让用户以为改完就生效了", hint)
	}
}
