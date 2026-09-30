package guardian

import (
	"os"
	"strings"
	"testing"
)

// 上报的三跳接线(构造 → 交给 Manager → 起发送循环)只在 RunDaemon 里,而 RunDaemon 要 root
// 与 launchd。少任何一跳,Manager 的每个 Record 都是零调用方的壳,或者报告落盘却永远不发,
// 三个包照样全绿 —— 这个仓库的事故都在组装根上。读源码是这里够得着的办法;锚点找不到就响亮失败。
func TestRunDaemonWiresTheReporterEndToEnd(t *testing.T) {
	src, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func RunDaemon(")
	if start < 0 {
		t.Fatal("RunDaemon not found in daemon.go")
	}
	fn := body[start:]
	for _, anchor := range []string{
		"reporterOptionsFor(",
		"loadOrCreateInstallID(reportsInstallIDPath)",
		"reporterOpts.Protected = ",
		"reporterOpts.Doctor = ",
		"reporterOpts.LogTail = ",
		"reporter := NewReporter(reporterOpts)",
		"Reporter:        reporter,",
		"go reporter.Run(runCtx)",
	} {
		if !strings.Contains(fn, anchor) {
			t.Fatalf("RunDaemon no longer contains %q — the reporter is not wired end to end", anchor)
		}
	}
	// 发送循环必须在 Manager 之后、且拿的是随 daemon 一起取消的 ctx:否则 Guardian 退出后循环还活着。
	if strings.Index(fn, "go reporter.Run(runCtx)") < strings.Index(fn, "mgr = manager") {
		t.Fatal("reporter.Run starts before the manager is assigned; Protected() would read a nil manager forever")
	}
}

// 规则同步的推送同样只在 RunDaemon 接线;少了它,Mac 从不推,手机永远只有默认规则,而推送器
// 自己的单测照样全绿。
func TestRunDaemonStartsThePolicyPusher(t *testing.T) {
	src, err := os.ReadFile("daemon.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	start := strings.Index(body, "func RunDaemon(")
	if start < 0 {
		t.Fatal("RunDaemon not found in daemon.go")
	}
	fn := body[start:]
	for _, anchor := range []string{
		"pusher := newPolicyPusher(options.ConfigPath,",
		"mgr.Status().Protection == ProtectionProtected",
		"go pusher.Run(runCtx, nil)",
	} {
		if !strings.Contains(fn, anchor) {
			t.Fatalf("RunDaemon no longer contains %q — the Mac never pushes its rules", anchor)
		}
	}
	if strings.Index(fn, "go pusher.Run(runCtx, nil)") < strings.Index(fn, "mgr = manager") {
		t.Fatal("the pusher starts before the manager is assigned; its protected() check would read a nil manager")
	}
}
