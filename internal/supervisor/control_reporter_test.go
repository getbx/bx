package supervisor

import (
	"testing"
	"time"

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
		nil, nil, guard, &stats.RateMeter{}, configWarnings, nil,
		// 这条测试看的是告警那条路;累计历史给一个「没有」的提供者即可 ——
		// **但它必须传**,漏传编不过,那正是这个必填形参存在的理由。
		func() *stats.RuleHistorySnapshot { return nil })

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
		nil, nil, guard, &stats.RateMeter{}, nil, nil,
		func() *stats.RuleHistorySnapshot { return nil })

	rep := reporter()
	if len(rep.Warnings) != 1 || rep.Warnings[0].Name != "tailscale" {
		t.Fatalf("Warnings = %+v, want 仅 guard 的一条", rep.Warnings)
	}
}

// —— 跨重启累计历史必须真的到达 Report(2026-08-24)——
//
// 形状与上面那条一样:调**生产代码真正调用的那个** newStatusReporter,不重拼一遍
// 它的逻辑。这条钉的是「接线接上了」,而接线正是本仓库全部事故的所在地。
func TestStatusReporterPublishesRuleHistory(t *testing.T) {
	at := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	want := &stats.RuleHistorySnapshot{
		UptimeSeconds: 1_209_600, Decisions: 25_000,
		Versions:  []string{"0.3.0", "0.4.0"},
		UpdatedAt: at,
		Rules:     []stats.RuleOutcome{{Source: "user_direct", Rule: "*.qq.com", Attempts: 12}},
	}
	reporter := newStatusReporter(&stats.Counters{}, fakeReporterTunnel{}, "vless://host:443", "split", "proxy",
		nil, nil, &networkGuard{}, &stats.RateMeter{}, nil, nil,
		func() *stats.RuleHistorySnapshot { return want })

	rep := reporter()
	if rep.RuleHistory == nil {
		t.Fatal("累计历史没到达 Report —— 死规则判据拿不到它要的那个数,整条功能静默失效")
	}
	if rep.RuleHistory.Decisions != 25_000 || rep.RuleHistory.UptimeSeconds != 1_209_600 {
		t.Errorf("发布出去的不是注入的那份:%+v", rep.RuleHistory)
	}
	if len(rep.RuleHistory.Rules) != 1 || rep.RuleHistory.Rules[0].Rule != "*.qq.com" {
		t.Errorf("按规则的累计没跟着走:%+v", rep.RuleHistory.Rules)
	}
	// **并列发布,绝不合并**:本次运行那份(Snapshot.Rules)与累计那份是两个数。
	if len(rep.Snapshot.Rules) != 0 {
		t.Errorf("累计历史污染了「本次运行」那份计数:%+v", rep.Snapshot.Rules)
	}
}

// **提供者返回 nil 时,Report.RuleHistory 必须是 nil,不是零值结构。**
//
// 零值会被消费方读成「累计 0 次、跑了 0 秒」—— 而那正好是判据用来说「门槛还没到」
// 的形状。于是「这一版 Core 没有这个概念 / 历史读不出来」与「机器刚装好」会给出
// 同一句话,而前者需要的是「没查」,后者需要的是「再等等」。
func TestStatusReporterKeepsRuleHistoryNilWhenThereIsNone(t *testing.T) {
	reporter := newStatusReporter(&stats.Counters{}, fakeReporterTunnel{}, "vless://host:443", "split", "proxy",
		nil, nil, &networkGuard{}, &stats.RateMeter{}, nil, nil,
		func() *stats.RuleHistorySnapshot { return nil })
	if rep := reporter(); rep.RuleHistory != nil {
		t.Fatalf("没有历史时发布了 %+v —— nil 与「累计为 0」必须分得开", rep.RuleHistory)
	}
}
