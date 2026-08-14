package install

import (
	"strings"
	"testing"
	"time"
)

// **`bx logs` 读的必须是 Core 此刻真正在写的那个文件。**
//
// 真机(2026-08-14):`bx logs` 读 `/var/log/bx.log` —— 那是 legacy launchd 时代
// 的路径,内容停在 8 月 3 日;而 Guardian 架构下 Core 是 Guardian 的子进程,
// 写的是 `/var/log/bx-guard.log`。**排查因此被带进沟里**(我自己就栽了一次:
// 拿一份三周前的日志推出了一整套关于「路径恢复没跑」的错误结论)。
//
// 一个看起来正常、其实是上个月的日志,比没有日志更糟。
func TestClientLogSourcesPutTheLiveFileFirst(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	sources := clientLogSources(func(path string) (time.Time, bool) {
		switch path {
		case GuardianStdoutLogPath:
			return now.Add(-2 * time.Minute), true
		case "/var/log/bx.log":
			return now.AddDate(0, 0, -11), true // 三周前的遗留
		}
		return time.Time{}, false
	}, now)

	if len(sources) == 0 {
		t.Fatal("一个日志源都没找到")
	}
	if sources[0].Path != GuardianStdoutLogPath {
		t.Fatalf("排在第一的是 %q —— 活着的那个必须在最前", sources[0].Path)
	}
	// 陈旧的仍然列出来(它可能有历史线索),但**必须被标出来**。
	var legacy *logSource
	for i := range sources {
		if sources[i].Path == "/var/log/bx.log" {
			legacy = &sources[i]
		}
	}
	if legacy == nil {
		t.Fatal("遗留日志被整个藏掉了 —— 它可能还有历史线索")
	}
	if !legacy.Stale {
		t.Fatal("三周没写过的文件没有被标成陈旧 —— 用户会把它当成现在的日志")
	}
}

// 不存在的文件不该出现在清单里。
func TestClientLogSourcesSkipMissingFiles(t *testing.T) {
	now := time.Now()
	sources := clientLogSources(func(string) (time.Time, bool) { return time.Time{}, false }, now)
	if len(sources) != 0 {
		t.Fatalf("文件都不存在却列出了 %v", sources)
	}
}

// **每个文件都要带上路径与「多久之前写的」。**
//
// 上一版单文件时 `tail` 连文件名都不打,用户完全不知道自己在看什么、多旧。
func TestLogSourceHeaderSaysWhichFileAndHowOld(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	fresh := logSourceHeader(logSource{Path: "/var/log/bx-guard.log", ModTime: now.Add(-90 * time.Second)}, now)
	if !strings.Contains(fresh, "/var/log/bx-guard.log") {
		t.Errorf("表头里没有路径:%q", fresh)
	}
	if !strings.Contains(fresh, "1m") && !strings.Contains(fresh, "90") {
		t.Errorf("表头里没说多久之前写的:%q", fresh)
	}
	stale := logSourceHeader(logSource{Path: "/var/log/bx.log", ModTime: now.AddDate(0, 0, -11), Stale: true}, now)
	if !strings.Contains(stale, "陈旧") {
		t.Errorf("陈旧文件的表头没有警告:%q", stale)
	}
}

// 判定「陈旧」的门槛:超过一天没被写过。
//
// 门槛不必精确 —— 它只是为了让「上个月的文件」看起来不像「刚才的文件」。
func TestStaleThreshold(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		age   time.Duration
		stale bool
	}{
		{time.Minute, false},
		{2 * time.Hour, false},
		{23 * time.Hour, false},
		{25 * time.Hour, true},
		{30 * 24 * time.Hour, true},
	} {
		if got := logSourceIsStale(now.Add(-tc.age), now); got != tc.stale {
			t.Errorf("%v 前写的,stale=%v want %v", tc.age, got, tc.stale)
		}
	}
}

// **`bx logs` 报出去的路径清单也必须以活着的那份为主。**
//
// 这条是行为守卫,不是文本匹配:`--json` 那条路把 ClientLogPaths() 直接发布给
// 调用方(诊断包也用它),上一版它只报 legacy 的两条 —— 于是拿着诊断包排查的人
// 连「还有另外两个文件」都不知道。
func TestClientLogPathsIncludeTheGuardianLogs(t *testing.T) {
	paths := ClientLogPaths()
	if len(paths) == 0 {
		t.Skip("非 darwin")
	}
	var sawGuardian, sawLegacy bool
	for _, p := range paths {
		if p == GuardianStdoutLogPath || p == GuardianStderrLogPath {
			sawGuardian = true
		}
		if p == "/var/log/bx.log" {
			sawLegacy = true
		}
	}
	if !sawGuardian {
		t.Fatalf("路径清单里没有 Guardian 的日志 —— 那才是当前 Core 在写的:%v", paths)
	}
	// legacy 仍然列出:老安装上它可能才是活的。
	if !sawLegacy {
		t.Errorf("legacy 路径被整个删掉了 —— 老安装上它可能才是活的:%v", paths)
	}
}
