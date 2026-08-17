package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/stats"
)

func TestRiskyRuleWarningNamesTheRule(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{Direct: []string{"*.myqcloud.com", "*.qq.com"}}}}
	ws := riskyRuleWarnings(cfg)
	if len(ws) != 1 {
		t.Fatalf("want 1 条告警,got %d: %+v", len(ws), ws)
	}
	w := ws[0]
	if !strings.Contains(w.Detail, "*.myqcloud.com") {
		t.Errorf("没点名到规则原文:%q", w.Detail)
	}
	// **必须是 warn 不是 error。** error 会把总状态降级成 Needs Attention,
	// 而这是一条配置建议,不是保护失效 —— 那正是 advisory 拉低总状态那个 bug。
	if w.Severity != "warn" {
		t.Errorf("Severity = %q, want warn", w.Severity)
	}
	if w.Hint == "" {
		t.Error("没有给出下一步 —— 一条没有附带动作的常驻告警会变成墙纸")
	}
}

// hint 里的子命令必须真实存在。`bx direct remove` 曾经是这条 hint 上一版的笔误
// (命令本身不存在,只有 ls/add/rm),用户照着敲会得到一句 usage 错误 —— 而它是
// 这条安全结论唯一附带的动作,给一个不存在的命令比不给更糟(用户会以为是自己
// 敲错了)。
//
// 不断言 hint 整句字面等于某个字符串:那种测试和缺陷本身一样脆,命令改名/措辞
// 调整照样绿。也不去 import internal/cli 取真实子命令名来比对 ——
// internal/supervisor 不该依赖 internal/cli(方向反了,cli 是消费方)。折中是
// 断言不含错的那个词、含对的那个词,两句都改错时测试仍能抓住。
func TestRiskyRuleHintUsesARealSubcommand(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{Direct: []string{"*.myqcloud.com"}}}}
	ws := riskyRuleWarnings(cfg)
	if len(ws) != 1 {
		t.Fatalf("前置断言失败:want 1 条告警,got %d", len(ws))
	}
	hint := ws[0].Hint
	if strings.Contains(hint, "direct remove") {
		t.Errorf("hint 用了不存在的子命令 `direct remove`:%q", hint)
	}
	if !strings.Contains(hint, "direct rm") {
		t.Errorf("hint 没有指向真实存在的 `direct rm`:%q", hint)
	}
}

// **嵌套括号。** internal/stats/render.go 的 warningText 把每条 Warning 的 Hint
// 套一层括号(`detail + " (" + hint + ")"`);这条 hint 曾自己也以括号收尾
// (`(改完要 bx down && bx up)`),套出来的是
// `… (bx direct rm '*.myqcloud.com'(改完要 bx down && bx up))` —— 圆括号嵌套两层,
// 人读起来数不清哪个括号对哪个。走真实的 stats.Render,不是自己拼一遍渲染逻辑。
func TestRiskyRuleHintRendersWithoutNestedParens(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{Direct: []string{"*.myqcloud.com"}}}}
	ws := riskyRuleWarnings(cfg)
	if len(ws) != 1 {
		t.Fatalf("前置断言失败:want 1 条告警,got %d", len(ws))
	}
	out := stats.Render(stats.Report{TunnelHealthy: true, Warnings: ws})
	if strings.Contains(out, "((") || strings.Contains(out, "))") {
		t.Fatalf("渲染出了嵌套括号,人读不清哪个括号对哪个:\n%s", out)
	}
}

// 干净配置一条都不发。
func TestNoRiskyRuleNoWarning(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{Direct: []string{"*.qq.com"}}}}
	if ws := riskyRuleWarnings(cfg); len(ws) != 0 {
		t.Fatalf("干净配置发了 %d 条告警:%+v", len(ws), ws)
	}
}

// **status 只发危险那一类。** 冗余是建议、对任何成熟配置都不为零,
// 放进常驻面板会变墙纸,把真正要紧的那一条一起淹掉。
func TestStatusWarningsCarryOnlyTheRiskyClass(t *testing.T) {
	cfg := &config.Config{Rules: []config.Rule{{
		Direct: []string{"*.apple.com", "ocsp.apple.com"}, // 一条同表冗余 + 无危险
	}}}
	if ws := riskyRuleWarnings(cfg); len(ws) != 0 {
		t.Fatalf("冗余那一类漏进了 status:%+v", ws)
	}
}

// global 不影响这一类 —— 危险直连与分流模式无关。
func TestRiskyRuleWarningIsModeIndependent(t *testing.T) {
	cfg := &config.Config{Global: true, Rules: []config.Rule{{Direct: []string{"*.myqcloud.com"}}}}
	if len(riskyRuleWarnings(cfg)) != 1 {
		t.Fatal("global 下危险直连告警被压掉了 —— 它与分流模式无关")
	}
}

// **前一版接线守卫已删,理由记在这里而不是悄悄消失。**
//
// 这里原有 TestConfigWarningsReachTheStatusReport,自称断言「Run 传下去的那个
// 参数确实到了 Report.Warnings 里」,但它在测试函数内部自己 `append` 了一遍
// guardWarnings 与 configWarnings,断言的是那个局部变量 `merged` —— 它一次都
// 没有调用生产代码里真正组装 stats.Report 的那段逻辑。把 control.go 里
// `append(guard.warnings(), configWarnings...)` 改回 `guard.warnings()`,
// 那条测试原样通过、绿灯,而 bx status 会静默丢失整个「危险直连规则」告警通道
// ——一个自称证明接线、实际什么都没证明的测试,比没有测试更糟。
//
// 现在的替代品是 control_reporter_test.go 的
// TestStatusReporterIncludesBothGuardAndConfigWarnings:它调用的是生产代码
// 里真正组装 report 的具名函数 newStatusReporter(从 serveControlWithPathRecovery
// 内联的匿名函数提出来的,逻辑完全相同,不是重新拼一遍),不是自己在测试里
// 重复一遍 append 逻辑。走真的 serveControlWithPathRecovery(要在 SockPath 常量
// 指向的 /var/run/bx 或 /run/bx 下建 socket)本机以非 root 身份实测会
// permission denied,这是选择提取 newStatusReporter 而不是直接调用
// serveControlWithPathRecovery 的原因。
