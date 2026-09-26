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
