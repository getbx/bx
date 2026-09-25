//go:build darwin

package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/appattr"
)

// 没有绕过 bx 的连接就一个字都不说(常驻的告警会被训练成墙纸);有的时候点名应用、
// 说出从哪块网卡、给出能做的那件事,并且是 error —— 这是一次真实的泄漏,不是建议。
func TestStrayConnectionWarningNamesTheAppsOnlyWhenThereAreAny(t *testing.T) {
	if w := strayConnectionWarning("en0", nil, nil); w.Name != "" {
		t.Fatalf("no stray connections must mean no warning, got %+v", w)
	}
	names := map[int32]string{42: "Google Chrome", 43: "steam_osx"}
	w := strayConnectionWarning("en0", []appattr.PCB{{LastPID: 43}, {LastPID: 42}, {LastPID: 42}, {LastPID: 7}}, func(pid int32) string { return names[pid] })
	if w.Severity != "error" {
		t.Fatalf("a live leak must be an error, got %q", w.Severity)
	}
	for _, want := range []string{"4 connection(s)", "Google Chrome, PID 7, steam_osx", "en0", "real IP"} {
		if !strings.Contains(w.Detail, want) {
			t.Fatalf("detail %q misses %q", w.Detail, want)
		}
	}
	if !strings.Contains(w.Hint, "quit and reopen Google Chrome, PID 7, steam_osx") {
		t.Fatalf("hint %q does not say what to do", w.Hint)
	}
}
