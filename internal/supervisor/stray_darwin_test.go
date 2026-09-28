//go:build darwin

package supervisor

import (
	"net/netip"
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

// 真机 2026-09-28:`ssh -W … vps`(Codex 的 ProxyJump 跳板)连的是 bx 自己的 VPS 的
// 22 端口,菜单红字「Outside bx: ssh — quit and reopen」,而重开之后路由照样把它送去
// en0。这里守的是从「取到的 socket 表 + bx 绕开的网段」到告警这一跳:旁路里的那条
// 不点名,对照组(远端不在旁路里的 Chrome)照样点名。
func TestStrayWarningSparesConnectionsToBxOwnServerButStillNamesRealLeaks(t *testing.T) {
	en0 := netip.MustParseAddr("172.20.10.2")
	server := netip.MustParseAddr("203.0.113.92")
	ssh := appattr.PCB{LocalAddr: en0, RemoteAddr: server, RemotePort: 22, LastPID: 84208}
	chrome := appattr.PCB{LocalAddr: en0, RemoteAddr: netip.MustParseAddr("203.0.113.11"), RemotePort: 443, LastPID: 77335}
	names := map[int32]string{84208: "ssh", 77335: "Google Chrome"}
	name := func(pid int32) string { return names[pid] }
	routedAround := func() []netip.Prefix { return []netip.Prefix{netip.PrefixFrom(server, 32)} }

	w := strayWarningFrom("en0", []appattr.PCB{ssh, chrome}, []netip.Addr{en0}, nil, routedAround, name)
	if w.Name == "" || strings.Contains(w.Detail, "ssh") || !strings.Contains(w.Detail, "1 connection(s) from Google Chrome") {
		t.Fatalf("ssh to bx's own server must not be named while the real leak still is, got %+v", w)
	}
	if w := strayWarningFrom("en0", []appattr.PCB{ssh}, []netip.Addr{en0}, nil, routedAround, name); w.Name != "" {
		t.Fatalf("only a connection to bx's own server: no warning at all, got %+v", w)
	}
	// 不知道旁路(nil)时那条 ssh 仍然算 —— 排除只来自明说的网段,不是对 ssh 网开一面。
	if w := strayWarningFrom("en0", []appattr.PCB{ssh}, []netip.Addr{en0}, nil, nil, name); !strings.Contains(w.Detail, "ssh") {
		t.Fatalf("without a routed-around list ssh is stray like anything else, got %+v", w)
	}
}
