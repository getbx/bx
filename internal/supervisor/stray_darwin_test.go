//go:build darwin

package supervisor

import (
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/stats"
)

// 没有绕过 bx 的连接就一个字都不说(常驻的告警会被训练成墙纸);有的时候点名应用、
// 说出从哪块网卡、给出能做的那件事,并且是 error —— 这是一次真实的泄漏,不是建议。
func TestStrayConnectionWarningNamesTheAppsOnlyWhenThereAreAny(t *testing.T) {
	if w := strayConnectionWarning("en0", nil, nil); w.Name != "" {
		t.Fatalf("no stray connections must mean no warning, got %+v", w)
	}
	names := map[int32]string{42: "Google Chrome", 43: "steam_osx"}
	w := strayConnectionWarning("en0", []appattr.PCB{{LastPID: 43}, {LastPID: 42}, {LastPID: 42}, {LastPID: 7}}, func(pid int32) string { return names[pid] })
	// warn 不是 error(所有者 2026-09-28):老连接不是要人动手的事故,不许拉总状态、不许裂图标。
	if w.Severity != "warn" {
		t.Fatalf("stubborn leftovers are a warning, not an error, got %q", w.Severity)
	}
	for _, want := range []string{"Google Chrome, PID 7, steam_osx still have an older connection outside bx", "4,", "en0", "real IP"} {
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

	// tracker 为 nil:全部当顽固(点名那条路),这里要验的是旁路那一跳。
	in := strayInputs{device: "en0", physical: []netip.Addr{en0}, routedAround: routedAround, name: name}
	w := strayWarningsFrom(in, []appattr.PCB{ssh, chrome}, time.Now())
	if len(w) != 1 || strings.Contains(w[0].Detail, "ssh") || !strings.Contains(w[0].Detail, "Google Chrome still has an older connection outside bx") {
		t.Fatalf("ssh to bx's own server must not be named while the real leak still is, got %+v", w)
	}
	if w := strayWarningsFrom(in, []appattr.PCB{ssh}, time.Now()); len(w) != 0 {
		t.Fatalf("only a connection to bx's own server: no warning at all, got %+v", w)
	}
	// 不知道旁路(nil)时那条 ssh 仍然算 —— 排除只来自明说的网段,不是对 ssh 网开一面。
	in.routedAround = nil
	if w := strayWarningsFrom(in, []appattr.PCB{ssh}, time.Now()); len(w) != 1 || !strings.Contains(w[0].Detail, "ssh") {
		t.Fatalf("without a routed-around list ssh is stray like anything else, got %+v", w)
	}
}

// 两段式(2026-09-28):刚开始退场的连接只报数、不点名、不升级(warn:CLI 不把总状态
// 降成 Needs Attention,菜单不裂图标);满了门槛还在的才是 error 并点名「退出重开」。
// 两组同时在时两条都发;哪组空哪条就不发。
func TestStrayWarningsSplitSettlingCountFromStubbornNames(t *testing.T) {
	en0 := netip.MustParseAddr("172.20.10.2")
	chrome := appattr.PCB{LocalPort: 49528, LocalAddr: en0, RemoteAddr: netip.MustParseAddr("203.0.113.11"), RemotePort: 443, LastPID: 77335}
	mail := appattr.PCB{LocalPort: 56589, LocalAddr: en0, RemoteAddr: netip.MustParseAddr("203.0.113.12"), RemotePort: 993, LastPID: 5156}
	names := map[int32]string{77335: "Google Chrome", 5156: "Mail"}
	name := func(pid int32) string { return names[pid] }
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	tr := &strayTracker{}
	in := strayInputs{device: "en0", physical: []netip.Addr{en0}, name: name, tracker: tr}

	first := strayWarningsFrom(in, []appattr.PCB{chrome, mail}, t0)
	if len(first) != 1 || first[0].Name != stats.WarningConnectionsSettling || first[0].Severity != "warn" || first[0].Count != 2 || len(first[0].Apps) != 0 {
		t.Fatalf("fresh leftovers must be one warn-level count-only warning, got %+v", first)
	}
	if strings.Contains(first[0].Detail, "Chrome") || strings.Contains(first[0].Hint, "reopen") {
		t.Fatalf("settling connections are neither named nor told to reopen, got %+v", first[0])
	}
	if !strings.Contains(first[0].Detail, "2 connection(s)") || !strings.Contains(first[0].Detail, "before protection") {
		t.Fatalf("the count and the reason must be in the text, got %q", first[0].Detail)
	}

	// 五分钟后 Chrome 还在、Mail 走了、WeChat 新来:一条 error 点名 Chrome,一条 warn 数 WeChat。
	wechat := appattr.PCB{LocalPort: 55390, LocalAddr: en0, RemoteAddr: netip.MustParseAddr("203.0.113.13"), RemotePort: 443, LastPID: 44301}
	names[44301] = "WeChat"
	later := strayWarningsFrom(in, []appattr.PCB{chrome, wechat}, t0.Add(strayStubbornAfter))
	if len(later) != 2 {
		t.Fatalf("stubborn + settling must be two warnings, got %+v", later)
	}
	var stubborn, settling *stats.Warning
	for i := range later {
		switch later[i].Name {
		case stats.WarningConnectionsBypassingBX:
			stubborn = &later[i]
		case stats.WarningConnectionsSettling:
			settling = &later[i]
		}
	}
	if stubborn == nil || stubborn.Severity != "warn" || len(stubborn.Apps) != 1 || stubborn.Apps[0] != "Google Chrome" || !strings.Contains(stubborn.Hint, "quit and reopen Google Chrome") {
		t.Fatalf("a connection that outlived the threshold must be named and told to reopen, got %+v", stubborn)
	}
	if settling == nil || settling.Count != 1 || strings.Contains(settling.Detail, "WeChat") {
		t.Fatalf("the newcomer is counted, not named, got %+v", settling)
	}
	if got := strayWarningsFrom(in, nil, t0.Add(time.Hour)); len(got) != 0 {
		t.Fatalf("nothing stray must mean no warning at all, got %+v", got)
	}
}
