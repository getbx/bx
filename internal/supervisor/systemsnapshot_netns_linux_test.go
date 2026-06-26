//go:build integration && linux

package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/getbx/bx/internal/route"
	"golang.org/x/sys/unix"
)

// TestSnapshotterRoundTripInNetns:netns 内 Capture 基线 → netConf.up() 制造改动
// → Restore(基线) → 断言 ip rule/route table 100 回到基线。需 root,门控 build tag。
// v6 分支在 netns 内有 lo(::1)时自动启用。
func TestSnapshotterRoundTripInNetns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root(Colima VM 或 CI 里 sudo)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令")
	}
	runtime.LockOSThread() // 不 Unlock:goroutine 结束销毁线程,临时 netns 随之消失
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Skipf("unshare(CLONE_NEWNET) 失败: %v", err)
	}
	mustIP2 := func(args ...string) {
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
	ruleList6 := func() string {
		out, err := exec.Command("ip", "-6", "rule", "list").CombinedOutput()
		if err != nil {
			t.Fatalf("ip -6 rule list: %v\n%s", err, out)
		}
		return string(out)
	}
	mustIP2("link", "set", "lo", "up")
	mustIP2("link", "add", "bx0", "type", "dummy")

	// 探测 netns 内是否有 v6(lo up 后应有 ::1)。
	v6 := ipv6Enabled()

	snapper := NewSystemSnapshotter()
	base, err := snapper.Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	baseRules := ruleList()
	var baseRules6 string
	if v6 {
		baseRules6 = ruleList6()
	}

	// 制造改动:跑 bx 现有 netConf.up() 装一堆策略路由。
	nc := &netConf{tunName: "bx0", tunAddr: "198.51.100.1/30", mainLookup: route.DefaultPrivateCIDRs}
	if v6 {
		nc.blockV6 = true
		nc.mainLookupV6 = route.DefaultPrivateV6CIDRs
	}
	if err := nc.up(); err != nil {
		t.Fatalf("netConf.up(): %v", err)
	}
	if ruleList() == baseRules {
		t.Fatal("up() 后规则应已变化(测试前提不成立)")
	}

	// 还原并断言回到基线。
	if err := snapper.Restore(base); err != nil {
		t.Fatalf("Restore 报错: %v", err)
	}
	if got := ruleList(); got != baseRules {
		t.Fatalf("Restore 未回到基线:\n--- base ---\n%s\n--- got ---\n%s", baseRules, got)
	}
	// table 100 应被清空(基线时为空)。
	if out, _ := exec.Command("ip", "route", "show", "table", "100").CombinedOutput(); len(out) != 0 {
		t.Fatalf("Restore 后 table 100 应空,得到:\n%s", out)
	}

	// v6 断言:仅在 netns 内 v6 可用时执行。
	if v6 {
		if got := ruleList6(); got != baseRules6 {
			t.Fatalf("Restore 后 v6 规则未回到基线:\n--- base ---\n%s\n--- got ---\n%s", baseRules6, got)
		}
		if out, _ := exec.Command("ip", "-6", "route", "show", "table", "100").CombinedOutput(); len(out) != 0 {
			t.Fatalf("Restore 后 v6 table 100 应空,得到:\n%s", out)
		}
	}
}
