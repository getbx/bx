//go:build integration && linux

// guardian 侧集成台的第一块:把 linux 屏障装到**真内核**上,断言打在
// `ip route get` 的判决上而不是命令串上。
//
// 单元测试已经证明「生成了哪些命令」;只有内核能回答本设计真正押注的那件事:
// **pref 120 + table 90 + throw 私网,合起来到底产不产生「公网封死、私网直连、
// server bypass 经网关」**。那三条语义在 darwin 上是最长前缀白给的,在 linux
// 要靠 rule 优先级与 throw 亲手移植 —— 移植对不对,命令串看不出来。
//
// 设计与逐条语义对齐清单:
// docs/superpowers/specs/2026-08-29-guardian-linux-adapter-design.md
package guardian

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/netnsguard"
	"github.com/getbx/bx/internal/route"
)

const (
	// 假上行:与 supervisor 台子同一段 TEST-NET-3,理由相同(不与 TUN 的
	// TEST-NET-2、fake-IP 的 198.18/15 冲突)。
	harnessUplinkDev = "bxup0"
	harnessGateway   = "203.0.113.1"
	harnessUplinkIP  = "203.0.113.2/24"
	// 被 bypass 的「服务器」。/32 且落在上行同段,好让它经网关可达。
	harnessServerIP = "203.0.113.9"
)

func enterGuardianNetns(t *testing.T) {
	t.Helper()
	netnsguard.Enter(t, netnsguard.Options{
		MountPoint:  filepath.Dir(RuntimeDir),
		HiddenPaths: []string{SocketPath},
		Uplink: netnsguard.Uplink{
			Dev: harnessUplinkDev, Addr: harnessUplinkIP, Gateway: harnessGateway,
		},
	})
}

// routeVerdict 问内核「去这个地址会怎么走」,返回它的判决。
//
// **判据取内核的路由决策,不取我们装了什么。** 「装了一条 unreachable」与
// 「这个地址真的走不通」是两句话,后者才是屏障存在的意义 —— 而 linux 的 rule
// 优先级、throw、表内最长前缀三者合起来的结果,只有内核算得出来。
func routeVerdict(t *testing.T, dst string) string {
	t.Helper()
	out, err := netnsguard.IPQuiet("route", "get", dst)
	if err != nil {
		// `ip route get` 对 unreachable 目的地本身就以非零退出 —— 那正是
		// 我们要区分的一种判决,不是测试失败。原样返回让断言自己判。
		return "ERROR:" + out
	}
	return out
}

func harnessBarrierContext() BarrierContext {
	return BarrierContext{
		Gateway:      harnessGateway,
		ServerBypass: []string{harnessServerIP + "/32"},
		BlockIPv6:    true,
	}
}

// 屏障在位时,内核对三类目的地必须给出三种不同的判决。这是本适配器全部押注
// 的地方,也是 darwin 语义能不能算「忠实移植」的唯一真凭据。
func TestHarnessLinuxBarrierBlocksPublicButKeepsPrivateAndBypass(t *testing.T) {
	enterGuardianNetns(t)
	ctx := context.Background()

	// 装屏障之前先取基线:公网此刻应当是通的(经假上行的默认路由),
	// 否则后面那条「装上之后不通了」证明不了任何事 —— 它在一台本来就
	// 不通的机器上同样成立。
	if before := routeVerdict(t, "1.1.1.1"); strings.HasPrefix(before, "ERROR:") {
		t.Fatalf("基线不成立:装屏障之前公网就已经不可达\n%s", before)
	}

	barrier := NewBarrier(nil)
	if err := barrier.Install(ctx, harnessBarrierContext()); err != nil {
		t.Fatalf("装屏障: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Remove(context.Background(), harnessBarrierContext()) })

	// ① 公网:必须被判 unreachable。
	if got := routeVerdict(t, "1.1.1.1"); !strings.Contains(got, "unreachable") && !strings.HasPrefix(got, "ERROR:") {
		t.Fatalf("屏障在位而公网仍可达 —— fail-closed 没生效:\n%s", got)
	}
	// ② 私网:必须仍然直连。**这是 throw 那一半的真凭据** —— barriercidr 的四条
	// /2 覆盖整个 v4 空间,私网一定命中其中一条;没有 throw 把它送回后续 rule,
	// 一装屏障整机私网就断(而 darwin 上主表最长前缀是白给的)。
	for _, private := range []string{"10.1.2.3", "192.168.1.1", "172.16.0.1"} {
		got := routeVerdict(t, private)
		if strings.Contains(got, "unreachable") || strings.HasPrefix(got, "ERROR:") {
			t.Fatalf("私网 %s 被屏障连累了 —— throw carve-out 没生效:\n%s", private, got)
		}
	}
	// ③ server bypass:必须经物理网关可达,否则隧道永远重建不起来。
	got := routeVerdict(t, harnessServerIP)
	if strings.Contains(got, "unreachable") || strings.HasPrefix(got, "ERROR:") {
		t.Fatalf("server bypass 不可达 —— 隧道将永远无法重建:\n%s", got)
	}
	if !strings.Contains(got, harnessGateway) && !strings.Contains(got, harnessUplinkDev) {
		t.Fatalf("server bypass 没有走物理网关:\n%s", got)
	}
}

// 私网清单必须**逐条**都被 carve 出来:上面那条只抽查了三个地址,而清单是
// route.DefaultPrivateCIDRs(数据面那份唯一来源)。少 carve 一段,那一段的
// 用户流量会在屏障在位时静默断掉,而三个抽样地址照样通过。
func TestHarnessLinuxBarrierCarvesEveryDeclaredPrivateBlock(t *testing.T) {
	enterGuardianNetns(t)
	ctx := context.Background()
	barrier := NewBarrier(nil)
	if err := barrier.Install(ctx, harnessBarrierContext()); err != nil {
		t.Fatalf("装屏障: %v", err)
	}
	t.Cleanup(func() { _ = barrier.Remove(context.Background(), harnessBarrierContext()) })

	table := netnsguard.MustIP(t, "route", "show", "table", linuxBarrierTable)
	for _, cidr := range route.DefaultPrivateCIDRs {
		if !strings.Contains(table, "throw "+cidr) {
			t.Fatalf("私网段 %s 没有 throw carve-out,屏障在位时它会被打死\ntable 90:\n%s", cidr, table)
		}
	}
}

// 拆除必须把自己装的东西**全部**带走:rule 与表都不许留残渣。
// 孤儿 pref-120 rule 与 darwin 的孤儿 /2 一样能打死整机连通,而 Guardian 被
// bootout 之后内存里的所有权记账就没了 —— 内核里留下的才是真问题。
func TestHarnessLinuxBarrierRemoveLeavesNothingBehind(t *testing.T) {
	enterGuardianNetns(t)
	ctx := context.Background()
	barrierCtx := harnessBarrierContext()
	barrier := NewBarrier(nil)

	rulesBefore := netnsguard.MustIP(t, "rule", "show")
	if err := barrier.Install(ctx, barrierCtx); err != nil {
		t.Fatalf("装屏障: %v", err)
	}
	if rules := netnsguard.MustIP(t, "rule", "show"); !strings.Contains(rules, linuxBarrierRulePref+":") {
		t.Fatalf("屏障装上了却没有 pref %s 的 rule:\n%s", linuxBarrierRulePref, rules)
	}
	if err := barrier.Remove(ctx, barrierCtx); err != nil {
		t.Fatalf("拆屏障: %v", err)
	}

	if rules := netnsguard.MustIP(t, "rule", "show"); strings.Contains(rules, linuxBarrierRulePref+":") {
		t.Fatalf("拆除之后 pref %s 的 rule 还在 —— 孤儿 rule 会打死整机连通:\n%s", linuxBarrierRulePref, rules)
	}
	if rules := netnsguard.MustIP(t, "rule", "show"); rules != rulesBefore {
		t.Fatalf("拆除之后 rule 表与装之前不一致:\nbefore:\n%s\nafter:\n%s", rulesBefore, rules)
	}
	if table, _ := netnsguard.IPQuiet("route", "show", "table", linuxBarrierTable); strings.TrimSpace(table) != "" {
		t.Fatalf("拆除之后 table %s 仍有残留:\n%s", linuxBarrierTable, table)
	}
	// 公网恢复可达 —— 这条才证明「拆干净了」这句话对用户意味着什么。
	if got := routeVerdict(t, "1.1.1.1"); strings.Contains(got, "unreachable") || strings.HasPrefix(got, "ERROR:") {
		t.Fatalf("屏障拆了而公网仍不可达:\n%s", got)
	}
}

// 逃生口:不查任何所有权记账、可无条件跑,并且真的把孤儿屏障清掉。
// 它面对的正是「Guardian 已被 bootout、内存记账没了,而内核里的规则还在」
// 这个残局 —— 所以这里刻意**不经 barrier.Remove**,而是直接跑逃生口。
func TestHarnessLinuxEscapeHatchClearsOrphanBarrier(t *testing.T) {
	enterGuardianNetns(t)
	ctx := context.Background()
	if err := NewBarrier(nil).Install(ctx, harnessBarrierContext()); err != nil {
		t.Fatalf("装屏障: %v", err)
	}
	if got := routeVerdict(t, "1.1.1.1"); !strings.Contains(got, "unreachable") && !strings.HasPrefix(got, "ERROR:") {
		t.Fatalf("前置不成立:屏障没有真的挡住公网\n%s", got)
	}

	if err := RemoveBlockingBarrierRoutes(ctx, nil); err != nil {
		t.Fatalf("逃生口清理失败 —— 用户将无法脱离黑洞: %v", err)
	}
	if rules := netnsguard.MustIP(t, "rule", "show"); strings.Contains(rules, linuxBarrierRulePref+":") {
		t.Fatalf("逃生口跑完 pref %s 的 rule 还在:\n%s", linuxBarrierRulePref, rules)
	}
	if got := routeVerdict(t, "1.1.1.1"); strings.Contains(got, "unreachable") || strings.HasPrefix(got, "ERROR:") {
		t.Fatalf("逃生口跑完公网仍不可达:\n%s", got)
	}
}
