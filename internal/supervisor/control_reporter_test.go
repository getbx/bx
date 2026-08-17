package supervisor

import (
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tunnel"
)

// fakeReporterTunnel 是 tunnelStatser 的最小实现,只为让 newStatusReporter 能
// 跑起来——它自己不是这条测试要证明的东西。
type fakeReporterTunnel struct{}

func (fakeReporterTunnel) Stats() tunnel.Stats { return tunnel.Stats{Up: true} }
func (fakeReporterTunnel) SocksAddr() string   { return "127.0.0.1:1080" }

// **这是 Finding 2 的核心修复。** 此前唯一「证明」configWarnings 到达
// Report.Warnings 的测试(TestConfigWarningsReachTheStatusReport)在测试函数
// 内部自己 append 了一遍 guardWarnings 与 configWarnings,断言的是那个局部变量——
// 它一次都没有调用生产代码里真正组装 stats.Report 的那段逻辑。把
// control.go:569 从 `append(guard.warnings(), configWarnings...)` 改回
// `guard.warnings()`,那条测试原样通过,而 bx status 会静默丢失整个「危险直连
// 规则」告警通道。
//
// **走 serveControlWithPathRecovery 本身不可行、且已实测确认**:它要在 SockPath
// (darwin `/var/run/bx`、linux `/run/bx`)下经 secdir.Ensure 建目录再 net.Listen,
// 而 SockPath 是编译期常量,不是可注入的测试缝——把它改成可覆盖的变量是一次比这个
// finding 大得多的重构,不在这一轮范围内。本机以非 root 身份实测:
// `mkdir /var/run/bx-probe` → `mkdir: /var/run/bx-probe: Permission denied`
// (uid=501)。这不是「测试写得不够巧」能绕开的限制。
//
// 于是把 report 闭包的组装从 serveControlWithPathRecovery 内联的匿名函数提出成
// 具名的 newStatusReporter(control.go),两处逻辑完全相同、serveControlWithPathRecovery
// 现在只是调它——这条测试调用的就是生产代码在真实运行时调用的同一个函数,
// 不是重新拼一遍它的逻辑。不需要 socket、不需要 root、不需要 HTTP。
func TestStatusReporterIncludesBothGuardAndConfigWarnings(t *testing.T) {
	guard := &networkGuard{}
	guard.value.Store([]stats.Warning{{Name: "tailscale", Severity: "warn", Detail: "overlay present"}})

	configWarnings := riskyRuleWarnings(&config.Config{
		Rules: []config.Rule{{Direct: []string{"*.myqcloud.com"}}},
	})
	if len(configWarnings) != 1 {
		t.Fatalf("前置断言失败:riskyRuleWarnings 没产出告警,这条测试会为错误的理由通过")
	}

	reporter := newStatusReporter(&stats.Counters{}, fakeReporterTunnel{}, "vless://host:443", "split", "proxy",
		nil, nil, guard, &stats.RateMeter{}, configWarnings)

	rep := reporter()

	var sawRisky, sawGuard bool
	for _, w := range rep.Warnings {
		switch w.Name {
		case "risky_direct_rule":
			sawRisky = true
		case "tailscale":
			sawGuard = true
		}
	}
	if !sawRisky {
		t.Errorf("configWarnings 没有到达 Report.Warnings —— 这正是 Finding 2 描述的回归:%+v", rep.Warnings)
	}
	if !sawGuard {
		t.Errorf("guard 的告警被 configWarnings 挤掉了(顺序/覆盖写错):%+v", rep.Warnings)
	}
	if !rep.TunnelHealthy {
		t.Errorf("同一份 Report 里其余字段也该来自真实的 report 逻辑(隧道健康位),got %+v", rep)
	}
}

// **零 configWarnings 时不产出多余的东西**——nil 而不是长度为 0 的空 slice
// 都行,但 guard 自己的告警不能被空 configWarnings 的 append 吞掉或复制走样。
func TestStatusReporterWithNoConfigWarningsKeepsGuardWarnings(t *testing.T) {
	guard := &networkGuard{}
	guard.value.Store([]stats.Warning{{Name: "tailscale", Severity: "warn"}})

	reporter := newStatusReporter(&stats.Counters{}, fakeReporterTunnel{}, "vless://host:443", "split", "proxy",
		nil, nil, guard, &stats.RateMeter{}, nil)

	rep := reporter()
	if len(rep.Warnings) != 1 || rep.Warnings[0].Name != "tailscale" {
		t.Fatalf("Warnings = %+v, want 仅 guard 的一条", rep.Warnings)
	}
}
