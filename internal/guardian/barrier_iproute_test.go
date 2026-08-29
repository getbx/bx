package guardian

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/barriercidr"
	"github.com/getbx/bx/internal/route"
)

// linux 屏障计划器的测试无 build tag:计划是纯函数,三条 CI 腿都该证明它。
// 语义背景(spec 2026-08-29-guardian-linux-adapter-design.md):darwin 屏障赢
// 靠最长前缀,linux 靠 rule pref;私网恒直连在 darwin 是主表连接路由白捡的,
// 在 linux 必须用 throw 亲手移植 —— 这些测试钉的就是那几条不许走样的语义。

func linuxBarrierCtx() BarrierContext {
	return BarrierContext{
		Gateway:      "192.168.1.1",
		ServerBypass: []string{"203.0.113.20/32"},
		BlockIPv6:    true,
	}
}

func commandLines(commands []Command) []string {
	lines := make([]string, 0, len(commands))
	for _, c := range commands {
		lines = append(lines, c.String())
	}
	return lines
}

func containsLine(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

// rule 必须最后武装、最先解除。表没填好就武装,私网 throw 还没进表的那一瞬,
// 私网流量会命中 /2 unreachable —— 一个瞬态的整机私网黑洞,拆除方向同理。
func TestPlanBarrierLinuxArmsRuleLastAndDisarmsFirst(t *testing.T) {
	apply, _, cleanup, err := PlanBarrierLinux(linuxBarrierCtx())
	if err != nil {
		t.Fatal(err)
	}
	lines := commandLines(apply)
	if len(lines) < 2 {
		t.Fatalf("apply 太短: %v", lines)
	}
	if lines[len(lines)-2] != "ip rule add pref 120 table 90" ||
		lines[len(lines)-1] != "ip -6 rule add pref 120 table 90" {
		t.Fatalf("rule 不在 apply 末尾: %v", lines[len(lines)-2:])
	}
	for _, l := range lines[:len(lines)-2] {
		if strings.Contains(l, "rule add") {
			t.Fatalf("rule 在表填好之前就武装了: %s", l)
		}
	}
	cleanupLines := commandLines(cleanup)
	if cleanupLines[0] != "ip -6 rule del pref 120 table 90" ||
		cleanupLines[1] != "ip rule del pref 120 table 90" {
		t.Fatalf("cleanup 没有先解除武装: %v", cleanupLines[:2])
	}
}

// 私网 carve-out:/2 覆盖整个 v4 空间,没有 throw 的表会把私网一起打死
// (darwin 靠主表最长前缀白捡这条,linux 必须亲手移植)。清单必须是数据面
// 那份 route.DefaultPrivateCIDRs,不许手抄第二份。
func TestPlanBarrierLinuxCarvesPrivateOutWithThrowRoutes(t *testing.T) {
	apply, _, _, err := PlanBarrierLinux(linuxBarrierCtx())
	if err != nil {
		t.Fatal(err)
	}
	lines := commandLines(apply)
	for _, cidr := range route.DefaultPrivateCIDRs {
		want := "ip route add throw " + cidr + " table 90"
		if !containsLine(lines, want) {
			t.Fatalf("私网段 %s 没有 throw carve-out", cidr)
		}
	}
	for _, cidr := range route.DefaultPrivateV6CIDRs {
		want := "ip -6 route add throw " + cidr + " table 90"
		if !containsLine(lines, want) {
			t.Fatalf("v6 私网段 %s 没有 throw carve-out", cidr)
		}
	}
}

// 阻断块与 server bypass 必须与 darwin 计划器读同一份语义源:这里逐条对
// barriercidr 与 ctx 断言,另有一条跨计划器对齐测试兜「两边悄悄漂开」。
func TestPlanBarrierLinuxBlocksEveryDeclaredBlockAndBypassesServer(t *testing.T) {
	apply, reassert, _, err := PlanBarrierLinux(linuxBarrierCtx())
	if err != nil {
		t.Fatal(err)
	}
	lines := commandLines(apply)
	v4, v6 := barriercidr.Blocking()
	for _, block := range v4 {
		if !containsLine(lines, "ip route add unreachable "+block+" table 90") {
			t.Fatalf("v4 阻断块 %s 缺席", block)
		}
	}
	for _, block := range v6 {
		if !containsLine(lines, "ip -6 route add unreachable "+block+" table 90") {
			t.Fatalf("v6 阻断块 %s 缺席", block)
		}
	}
	if !containsLine(lines, "ip route add 203.0.113.20/32 via 192.168.1.1 table 90") {
		t.Fatalf("server bypass 缺席: %v", lines)
	}
	reassertLines := commandLines(reassert)
	if len(reassertLines) != 1 ||
		reassertLines[0] != "ip route add 203.0.113.20/32 via 192.168.1.1 table 90" {
		t.Fatalf("reassert 应只重装 bypass: %v", reassertLines)
	}
}

// BlockIPv6=false 时一条 -6 命令都不许有(与 darwin 计划器同语义:v6 步骤
// 整个消失,不连累 v4)。
func TestPlanBarrierLinuxWithoutIPv6HasNoV6Commands(t *testing.T) {
	ctx := linuxBarrierCtx()
	ctx.BlockIPv6 = false
	apply, _, cleanup, err := PlanBarrierLinux(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range append(commandLines(apply), commandLines(cleanup)...) {
		if strings.Contains(l, "-6") {
			t.Fatalf("BlockIPv6=false 仍有 v6 命令: %s", l)
		}
	}
}

// 跨计划器语义对齐:linux 与 darwin 计划器必须从同一份语义源产出同一组
// bypass 与阻断网段 —— 这条是「语义只有一份」的防漂移守卫,两边任何一侧
// 多块/少块/换清单都会红。
func TestPlanBarrierLinuxMatchesDarwinSemantics(t *testing.T) {
	ctx := linuxBarrierCtx()
	darwinApply, _, _, err := PlanBarrier(ctx)
	if err != nil {
		t.Fatal(err)
	}
	linuxApply, _, _, err := PlanBarrierLinux(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// darwin: route -n add -net <cidr> <gw> / route -n add -net <cidr> 127.0.0.1 -reject
	darwinCidrs := map[string]bool{}
	for _, c := range darwinApply {
		if len(c.Args) >= 4 && c.Args[1] == "add" {
			for i, a := range c.Args {
				if a == "-net" && i+1 < len(c.Args) {
					darwinCidrs[c.Args[i+1]] = true
				}
			}
		}
	}
	// linux: bypass/unreachable 进对比;throw 是 linux 独有的移植物,不参与。
	linuxCidrs := map[string]bool{}
	for _, c := range linuxApply {
		args := c.Args
		if len(args) >= 2 && args[0] == "-6" {
			args = args[1:]
		}
		if len(args) < 3 || args[0] != "route" || args[1] != "add" {
			continue
		}
		switch args[2] {
		case "unreachable":
			linuxCidrs[args[3]] = true
		case "throw":
		default:
			linuxCidrs[args[2]] = true
		}
	}
	for cidr := range darwinCidrs {
		if !linuxCidrs[cidr] {
			t.Fatalf("darwin 有 %s 而 linux 没有 —— 语义漂开了", cidr)
		}
	}
	for cidr := range linuxCidrs {
		if !darwinCidrs[cidr] {
			t.Fatalf("linux 有 %s 而 darwin 没有 —— 语义漂开了", cidr)
		}
	}
}

// Release:解除武装 + 清掉自己表里的全部内容。linux 不需要 darwin 的
// 「转让」语义(专用表没有与 Core 共享的路由行),但 transferred 的形状
// 校验保留 —— 坏输入要响亮,不许静默吞。
func TestPlanBarrierReleaseLinuxDisarmsAndClearsOwnTable(t *testing.T) {
	release, err := PlanBarrierReleaseLinux(linuxBarrierCtx(), []string{"203.0.113.20/32"})
	if err != nil {
		t.Fatal(err)
	}
	lines := commandLines(release)
	if lines[0] != "ip -6 rule del pref 120 table 90" || lines[1] != "ip rule del pref 120 table 90" {
		t.Fatalf("release 没有先解除武装: %v", lines[:2])
	}
	if !containsLine(lines, "ip route del 203.0.113.20/32 table 90") {
		t.Fatalf("release 没清自己表里的 bypass: %v", lines)
	}
	if _, err := PlanBarrierReleaseLinux(linuxBarrierCtx(), []string{"not-a-cidr"}); err == nil {
		t.Fatal("坏 transferred 输入必须响亮报错")
	}
}

// 逃生口清理:无 ctx、可无条件跑 —— 解除两条 rule + flush 自己的专用表。
// 孤儿 pref-120 rule 与 darwin 的孤儿 /2 一样能打死连通,清理原语必须与
// 安装原语同批存在。
func TestPlanLinuxBarrierCleanupIsContextFree(t *testing.T) {
	lines := commandLines(PlanLinuxBarrierCleanup())
	for _, want := range []string{
		"ip -6 rule del pref 120 table 90",
		"ip rule del pref 120 table 90",
		"ip route flush table 90",
		"ip -6 route flush table 90",
	} {
		if !containsLine(lines, want) {
			t.Fatalf("逃生口清理缺 %q: %v", want, lines)
		}
	}
	if lines[0] != "ip -6 rule del pref 120 table 90" && lines[0] != "ip rule del pref 120 table 90" {
		t.Fatalf("清理必须先解除武装: %v", lines)
	}
}
