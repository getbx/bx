package guardian

import (
	"errors"
	"reflect"
	"testing"

	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
)

// 菜单要看见「哪些应用在绕过 bx 以真实 IP 收发」(known-gaps A11)。那份名单只住在 Core 的
// 告警里,CoreRuntime 此前根本没有这一栏 —— bx status 看得见,菜单看不见,而菜单才是用户
// 天天看的地方。取的是告警里**结构化**的 Apps,不从 Detail 的文字里抠。
func TestCoreRuntimeCarriesTheAppsBypassingBX(t *testing.T) {
	report := stats.Report{Warnings: []stats.Warning{
		{Name: "tailscale", Severity: "warn", Detail: "unrelated"},
		{Name: stats.WarningConnectionsBypassingBX, Severity: "error", Detail: "…", Apps: []string{"Google Chrome", "steam_osx"}},
	}}
	got := coreRuntimeFrom(report, supervisor.RuntimeState{}, errors.New("no runtime state"))
	if !reflect.DeepEqual(got.BypassingApps, []string{"Google Chrome", "steam_osx"}) {
		t.Fatalf("BypassingApps = %v, want the apps named by the Core's warning", got.BypassingApps)
	}
	if clean := coreRuntimeFrom(stats.Report{}, supervisor.RuntimeState{}, nil); clean.BypassingApps != nil {
		t.Fatalf("no warning must mean no apps, got %v", clean.BypassingApps)
	}
}

// 两段式(2026-09-28):Core 把「刚开始退场」的连接只报数(stats.WarningConnectionsSettling
// 的 Count),菜单要显示那个数、而且不裂图标。取的是结构化的 Count,不从文字里抠。
func TestCoreRuntimeCarriesTheSettlingConnectionCount(t *testing.T) {
	report := stats.Report{Warnings: []stats.Warning{
		{Name: stats.WarningConnectionsSettling, Severity: "warn", Detail: "3 connection(s) …", Count: 3},
	}}
	got := coreRuntimeFrom(report, supervisor.RuntimeState{}, errors.New("no runtime state"))
	if got.SettlingConnections != 3 {
		t.Fatalf("SettlingConnections = %d, want the count from the Core's warning", got.SettlingConnections)
	}
	if got.BypassingApps != nil {
		t.Fatalf("settling connections are not named apps, got %v", got.BypassingApps)
	}
	if clean := coreRuntimeFrom(stats.Report{}, supervisor.RuntimeState{}, nil); clean.SettlingConnections != 0 {
		t.Fatalf("no warning must mean zero, got %d", clean.SettlingConnections)
	}
}
