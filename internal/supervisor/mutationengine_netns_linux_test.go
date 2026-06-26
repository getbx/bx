//go:build integration && linux

package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestMutationEngineNetnsAutoRevert:引擎接真 NewSystemSnapshotter(),在 netns 内
// Arm 一个合成路由 mutation(加一条 ip rule)→ 不 commit → 推进时钟 + tick →
// 断言 ip rule 真机回到 arm 前基线。证明死手在守护进程引擎里、用真快照器、能自动回滚。
func TestMutationEngineNetnsAutoRevert(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root(Colima VM 或 CI sudo)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令")
	}
	runtime.LockOSThread() // 不 Unlock:goroutine 结束销毁线程,临时 netns 随之消失
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Skipf("unshare(CLONE_NEWNET) 失败: %v", err)
	}
	must := func(args ...string) {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
	}
	ruleList := func() string {
		out, err := exec.Command("ip", "rule", "list").CombinedOutput()
		if err != nil {
			t.Fatalf("ip rule list: %v\n%s", err, out)
		}
		return string(out)
	}
	must("link", "set", "lo", "up")

	clk := &engClock{t: time.Unix(0, 0)}
	e := newMutationEngine(NewSystemSnapshotter(), 240*time.Second, clk.now, nil)

	base := ruleList()
	// 合成 mutation:加一条独特的 ip rule;undo 为 nil(靠快照网删掉)。
	apply := func() error {
		return exec.Command("ip", "rule", "add", "pref", "12345", "table", "main").Run()
	}
	if err := e.Arm(apply, nil); err != nil {
		t.Fatalf("Arm: %v", err)
	}
	if ruleList() == base {
		t.Fatal("Arm 后规则应已变(测试前提不成立)")
	}

	// 不 commit,推进时钟过窗口,tick → 自动 revert。
	clk.t = clk.t.Add(241 * time.Second)
	rev, err := e.tick()
	if err != nil {
		t.Fatalf("tick revert 报错: %v", err)
	}
	if !rev {
		t.Fatal("未 commit 到点应自动 revert")
	}
	if got := ruleList(); got != base {
		t.Fatalf("revert 未回到基线:\n--- base ---\n%s\n--- got ---\n%s", base, got)
	}
}
