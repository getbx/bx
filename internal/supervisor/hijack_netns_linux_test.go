//go:build integration && linux

package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/route"
	"golang.org/x/sys/unix"
)

// mustIP 在当前(已 unshare 的)netns 内执行 ip 命令,失败即 fatal。
func mustIP(t *testing.T, args ...string) {
	t.Helper()
	out, err := exec.Command("ip", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("ip %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// ipRuleList 返回当前 netns 的 `ip rule list` 文本(用于基线比对与断言)。
func ipRuleList(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("ip", "rule", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("ip rule list: %v\n%s", err, out)
	}
	return string(out)
}

// TestNetConfRoundTripInNetns 在一个临时 netns 内证明:bx 现有的 netConf.up() 装上策略路由、
// down() 干净还原到基线。这是 Task 9 真快照器的"验证方式可行性" PoC —— 不发任何真实外网流量,
// 只断言 ip rule/route 状态,故宿主是否挂 VPN 与结果无关。需 root,门控在 build tag。
func TestNetConfRoundTripInNetns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root(在 Colima VM 或 CI 里以 sudo 运行)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令(iproute2)")
	}

	// 把本 goroutine 钉在当前 OS 线程并 unshare 进全新空 netns。
	// 故意不 UnlockOSThread:goroutine 结束时 Go 运行时销毁该线程,临时 netns 随之消失,
	// 绝不污染宿主/runner 的真实 netns。
	runtime.LockOSThread()
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Skipf("unshare(CLONE_NEWNET) 失败(无 CAP_SYS_ADMIN?): %v", err)
	}

	// 新 netns 里只有一个 down 的 lo;补齐最小拓扑:lo up + 一个 dummy 当 tunName。
	mustIP(t, "link", "set", "lo", "up")
	mustIP(t, "link", "add", "bxtest0", "type", "dummy")

	base := ipRuleList(t) // 改动前基线

	nc := &netConf{
		tunName:    "bxtest0",
		tunAddr:    "198.51.100.1/30",
		mainLookup: route.DefaultPrivateCIDRs, // 触发 pref 150(+CGNAT pref 149)
		// bypass 留空 → 不需要可达 gw;blockV6 false → 只验 v4(聚焦)。
	}

	if err := nc.up(); err != nil {
		t.Fatalf("netConf.up(): %v", err)
	}

	// 断言策略路由就位。
	rules := ipRuleList(t)
	for _, want := range []string{"100:", "150:", "200:"} {
		if !strings.Contains(rules, want) {
			t.Fatalf("up() 后缺策略规则 %q;ip rule list=\n%s", want, rules)
		}
	}
	if strings.Contains(strings.Join(route.DefaultPrivateCIDRs, ","), "100.64.0.0/10") &&
		!strings.Contains(rules, "149:") {
		t.Fatalf("up() 后缺 CGNAT pref 149 规则;ip rule list=\n%s", rules)
	}
	rt := func() string {
		out, _ := exec.Command("ip", "route", "show", "table", "100").CombinedOutput()
		return string(out)
	}()
	if !strings.Contains(rt, "default") || !strings.Contains(rt, "bxtest0") {
		t.Fatalf("table 100 缺 default dev bxtest0;得到:\n%s", rt)
	}

	// 还原并断言回到基线。
	nc.down()
	if after := ipRuleList(t); after != base {
		t.Fatalf("down() 未干净还原:\n--- base ---\n%s\n--- after ---\n%s", base, after)
	}
}
