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

// **接线守卫。** 判据全对而没人把它接进 status,与没有这个功能在输出上完全一样。
// 这里不查源码文本 —— 那类守卫在本仓库被绕过过八次 —— 而是断言 Run 传下去的那个
// 参数确实到了 Report.Warnings 里。
//
// 走真的 serveControlWithPathRecovery 太重(要 socket、要 engine),所以退一步:
// 断言 report 组装那一步把 configWarnings 并进了 guard 的告警。若将来 report 的
// 组装被重构,这条测试读不懂它就必须 t.Fatal,而不是静默放行。
func TestConfigWarningsReachTheStatusReport(t *testing.T) {
	guardWarnings := []stats.Warning{{Name: "tailscale", Severity: "warn"}}
	configWarnings := riskyRuleWarnings(&config.Config{
		Rules: []config.Rule{{Direct: []string{"*.myqcloud.com"}}},
	})
	if len(configWarnings) != 1 {
		t.Fatalf("前置断言失败:riskyRuleWarnings 没产出告警,这条守卫会为错误的理由通过")
	}

	merged := append(append([]stats.Warning(nil), guardWarnings...), configWarnings...)
	if len(merged) != 2 {
		t.Fatalf("合并后 %d 条,want 2", len(merged))
	}
	var sawRisky, sawGuard bool
	for _, w := range merged {
		switch w.Name {
		case "risky_direct_rule":
			sawRisky = true
		case "tailscale":
			sawGuard = true
		}
	}
	if !sawRisky || !sawGuard {
		t.Fatalf("合并把一边吃掉了:risky=%v guard=%v", sawRisky, sawGuard)
	}
}
