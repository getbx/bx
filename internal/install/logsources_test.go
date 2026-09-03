package install

import (
	"errors"
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

// —— 「一份当前日志都没读到」要有一句收尾话(2026-09-02)——
//
// 真机排障:非 root 跑 `bx logs`,四段里前三段是 Permission denied,最后一段是
// 一个月前的旧实例内容。**逐段的提示都在**(读失败会打、陈旧会标),但读的人
// 拿走的是最后一屏文字 —— 而那一屏恰恰是唯一读得到的、也是最旧的那一段,
// 于是把旧实例的状态当成了现在的状态。
//
// **这不是「没提示」,是提示被排在了它要否定的那段内容前面。**

func src(path string, stale bool) logSource { return logSource{Path: path, Stale: stale} }

// 当前日志读不到、只读到陈旧的 ⇒ 必须收尾说清楚,并给出路。
func TestStaleOnlyNoticeFiresWhenNoCurrentLogWasReadable(t *testing.T) {
	sources := []logSource{src("/var/log/bx.log", false), src("/var/log/bx.err.log", true)}
	got := staleOnlyNotice(sources, map[string]bool{"/var/log/bx.err.log": true})
	if got == "" {
		t.Fatal("只读到旧实例的日志却一个字没说")
	}
	if !strings.Contains(got, "不代表现在的状态") {
		t.Errorf("没说清那不是当前状态:%s", got)
	}
	if !strings.Contains(got, "sudo") {
		t.Errorf("没给出路:%s", got)
	}
}

// **读到了当前日志就一个字都不说。**
// 一句恒真的提示会被训练成噪声,而这条要在真出事时被看见。
func TestStaleOnlyNoticeStaysQuietWhenTheCurrentLogWasRead(t *testing.T) {
	sources := []logSource{src("/var/log/bx.log", false), src("/var/log/bx.err.log", true)}
	got := staleOnlyNotice(sources, map[string]bool{"/var/log/bx.log": true, "/var/log/bx.err.log": true})
	if got != "" {
		t.Errorf("当前日志读到了还在提示:%s", got)
	}
}

// **压根没有当前日志是另一回事,别在这里下结论。**
// 那可能是「服务从没启动过」,而这条提示说的是「读不到」—— 说错方向会把人
// 送去 sudo 一遍,然后发现还是什么都没有。
func TestStaleOnlyNoticeSaysNothingWhenThereIsNoCurrentLogAtAll(t *testing.T) {
	sources := []logSource{src("/var/log/bx.err.log", true)}
	if got := staleOnlyNotice(sources, map[string]bool{"/var/log/bx.err.log": true}); got != "" {
		t.Errorf("没有当前日志时下了「读不到」的结论:%s", got)
	}
}

// 一个字节都没读到时**仍然要说**。
// 那是权限问题最纯粹的形状,而静默会让人以为「没有日志」。
func TestStaleOnlyNoticeFiresEvenWhenNothingWasReadable(t *testing.T) {
	sources := []logSource{src("/var/log/bx.log", false), src("/var/log/bx.err.log", true)}
	if got := staleOnlyNotice(sources, nil); got == "" {
		t.Error("一个字节都没读到却什么也没说")
	}
}

// **接线守卫**:收尾那句真的被拼进了给人看的那一屏。
//
// 变异实测:把 renderLogSources 里那一行删掉,判据层的四条测试**照样全绿** ——
// 又一次「守卫钉住的是缺陷旁边的东西」。
func TestRenderedLogsCarryTheStaleOnlyNotice(t *testing.T) {
	moment := time.Now()
	sources := []logSource{
		{Path: "/var/log/bx.log", ModTime: moment, Stale: false},
		{Path: "/var/log/bx.err.log", ModTime: moment.Add(-30 * 24 * time.Hour), Stale: true},
	}
	out := renderLogSources(sources, moment, func(s logSource) ([]byte, error) {
		if s.Stale {
			return []byte("一个月前的旧内容\n"), nil
		}
		return []byte("tail: Permission denied\n"), errors.New("exit status 1")
	})
	if !strings.Contains(out, "不代表现在的状态") {
		t.Fatalf("收尾话没被拼进输出:\n%s", out)
	}
	// **它必须在最后**:整个修法的理由就是「读的人拿走的是最后一屏文字」。
	if !strings.Contains(out[len(out)/2:], "不代表现在的状态") {
		t.Errorf("收尾话没排在后半段,起不到收尾作用:\n%s", out)
	}
}

// 读到了当前日志时那一屏里不该有那句话。
func TestRenderedLogsStayQuietWhenTheCurrentLogWasRead(t *testing.T) {
	moment := time.Now()
	sources := []logSource{{Path: "/var/log/bx.log", ModTime: moment, Stale: false}}
	out := renderLogSources(sources, moment, func(logSource) ([]byte, error) {
		return []byte("当前内容\n"), nil
	})
	if strings.Contains(out, "不代表现在的状态") {
		t.Errorf("当前日志读到了还在提示:\n%s", out)
	}
}
