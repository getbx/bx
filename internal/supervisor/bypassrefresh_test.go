package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
)

// 这些测试补的是复审指出的空洞:refreshServerBypass 整个闭包此前零覆盖,
// 复审在它内部做了四处变异(changed 恒 false、删掉写回、最后一跳传 nil、
// WithTimeout 换 WithCancel),每一处都制造出本任务存在的理由——成环——
// 而 `go test ./...` 四次全绿。

func writeTestConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// twoServerConfig:hk 当前、tokyo 已知。目标 new.example 刻意**不在**配置里
// —— spec 是「先热切、成功后才落盘 current」,切换那一刻正是这个样子。
func twoServerConfig(t *testing.T) string {
	t.Helper()
	return writeTestConfig(t, `
servers:
  - name: hk
    link: vless://u@hk.example:443
  - name: tokyo
    link: vless://u@tokyo.example:443
current: hk
`)
}

func staticResolver(m map[string][]string) func(context.Context, string) ([]netip.Addr, error) {
	return func(_ context.Context, host string) ([]netip.Addr, error) {
		raw, ok := m[host]
		if !ok {
			return nil, fmt.Errorf("no such host %q", host)
		}
		var out []netip.Addr
		for _, s := range raw {
			out = append(out, netip.MustParseAddr(s))
		}
		return out, nil
	}
}

func testRefresherDeps(t *testing.T, path string, resolve func(context.Context, string) ([]netip.Addr, error)) bypassRefreshDeps {
	t.Helper()
	return bypassRefreshDeps{
		configPath: path,
		resolve:    resolve,
		store:      newBypassStore(nil, nil, nil),
		timeout:    2 * time.Second,
	}
}

// 变异「changed 恒返回 false」与「删掉写回 store」各打死这里的一半:
// 前者让端点跳过 rehijack(路由不装 = 成环),后者让 Rehijack 拿到旧集合
// (同样是不装 = 成环)。两条断言必须都在。
func TestBypassRefreshReportsChangedAndPublishesNewSet(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example":    {"1.1.1.1"},
		"tokyo.example": {"2.2.2.2"},
		"new.example":   {"9.9.9.9"},
	}))
	refresh := newBypassRefresher(deps)

	changed, err := refresh(context.Background(), []string{"vless://u@new.example:443"})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("集合里多了一台服务器却报 changed=false —— 端点会跳过 rehijack,新服务器的路由永远不装 = 成环")
	}
	got := deps.store.cidrs()
	if !containsString(got, "9.9.9.9/32") {
		t.Fatalf("新服务器的 IP 必须写进 store(Rehijack 从这里读), got %v", got)
	}
}

// 变异「最后一跳传 nil 而不是 requiredLinks」正好落在端点侧测试看不见的地方:
// 端点确实把链接交出去了,是闭包自己把它丢了。
func TestBypassRefreshTreatsRequiredLinksAsRequired(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example":    {"1.1.1.1"},
		"tokyo.example": {"2.2.2.2"},
		// new.example 解析不了
	}))
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), []string{"vless://u@new.example:443"}); err == nil {
		t.Fatal("目标解析不了必须报错 —— 被当成「没在用的服务器」跳过就会不装 bypass 直接切过去 = 成环")
	}
}

// 变异「WithTimeout 换 WithCancel」:解析器挂住时刷新永不返回,而它跑在
// cs.mu 里,会把 /v0/commit、/v0/transport、/v0/rehijack 一起连坐。
func TestBypassRefreshBoundsHowLongItCanHoldTheControlLock(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), func(ctx context.Context, _ string) ([]netip.Addr, error) {
		<-ctx.Done() // 解析器挂住,只有 deadline 能救
		return nil, ctx.Err()
	})
	deps.timeout = 50 * time.Millisecond
	refresh := newBypassRefresher(deps)

	done := make(chan error, 1)
	go func() { _, err := refresh(context.Background(), []string{"vless://u@new.example:443"}); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("解析挂住必须报错(= 拒绝切换),不能当成功")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("刷新没有封顶 —— 控制面锁会被无限期持有(「71 分钟关不掉保护」就是这个形状)")
	}
}

// 刷新发生在 DNS 已经交给 bx 之后。若走系统解析器,新服务器(没有静态 A 记录)
// 会被 bx 自己回一个 198.18/15 的 fake IP,然后我们给一个**假 IP** 装 bypass 路由,
// 而真实服务器 IP 一次都没进过 bypass。
func TestBypassRefreshRejectsFakeIPAnswers(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example":    {"1.1.1.1"},
		"tokyo.example": {"2.2.2.2"},
		"new.example":   {"198.18.0.7"}, // bx 自己的 fake-IP 应答
	}))
	deps.fakeipCIDR = "198.18.0.0/15"
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), []string{"vless://u@new.example:443"}); err == nil {
		t.Fatal("fake IP 绝不能进 bypass —— 给假 IP 装路由 = 真 IP 永远不在 bypass 里 + 那个域名被黑洞")
	}
	if containsString(deps.store.cidrs(), "198.18.0.7/32") {
		t.Fatalf("fake IP 竟被写进了 store: %v", deps.store.cidrs())
	}
}

// 刷新是**替换**不是并集:一台已知服务器这轮 DNS 抖一下就会掉出集合,
// 而 runFailover 随时可能切到它 —— 那时它没有 bypass = 成环。
// 但只保留**配置仍然写着**的那些:用户删掉的服务器必须真的消失,
// 否则它的流量会绕开隧道。
func TestBypassRefreshRetainsKnownServerThatFailedThisRound(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example": {"1.1.1.1"},
		// tokyo.example 这轮解析不了
	}))
	deps.store = newBypassStore(
		[]string{"1.1.1.1/32", "2.2.2.2/32", "8.8.8.8/32"},
		map[string][]netip.Addr{
			"tokyo.example":   {netip.MustParseAddr("2.2.2.2")},
			"deleted.example": {netip.MustParseAddr("8.8.8.8")}, // 已从配置里删掉
		},
		map[string][]netip.Addr{
			"tokyo.example":   {netip.MustParseAddr("2.2.2.2")},
			"deleted.example": {netip.MustParseAddr("8.8.8.8")},
		},
	)
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	got := deps.store.cidrs()
	if !containsString(got, "2.2.2.2/32") {
		t.Fatalf("配置里仍写着的 tokyo 只是这轮解析失败,不该掉出 bypass, got %v", got)
	}
	if containsString(got, "8.8.8.8/32") {
		t.Fatalf("用户已删掉的服务器必须真的消失,否则它的流量绕开隧道, got %v", got)
	}
}

// tailscale 那组 CIDR 与「切到哪台服务器」无关。把它卷进每轮刷新,
// 一次 DERP map 拉取失败就会在下次 rehijack 时缩小 tailscale 共存旁路。
func TestBypassRefreshKeepsExtraCIDRs(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example":    {"1.1.1.1"},
		"tokyo.example": {"2.2.2.2"},
	}))
	deps.extraCIDRs = func() []string { return []string{"100.64.0.1/32"} }
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !containsString(deps.store.cidrs(), "100.64.0.1/32") {
		t.Fatalf("与切换无关的旁路(tailscale)不该被刷新抹掉, got %v", deps.store.cidrs())
	}
}

// bypass 是两半:路由 + 静态 DNS。只装路由不更新静态 DNS,隧道子进程
// 解析自己的服务器域名时照样落进 fake-IP —— 而静态 A 存在的唯一理由就是不让这一步发生。
func TestBypassRefreshPublishesStaticDNSToo(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example":    {"1.1.1.1"},
		"tokyo.example": {"2.2.2.2"},
		"new.example":   {"9.9.9.9"},
	}))
	var published map[string][]netip.Addr
	deps.setStaticA = func(m map[string][]netip.Addr) { published = m }
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), []string{"vless://u@new.example:443"}); err != nil {
		t.Fatal(err)
	}
	if len(published["new.example"]) != 1 || published["new.example"][0] != netip.MustParseAddr("9.9.9.9") {
		t.Fatalf("静态 DNS 必须与路由一起更新, got %v", published)
	}
	if len(deps.store.staticEntries()["new.example"]) == 0 {
		t.Fatal("store 里也要留一份,否则下轮刷新无从保留")
	}
}

// 用户在 hosts 里配的覆盖不能被刷新抹掉(启动时是合并进静态表的)。
func TestBypassRefreshKeepsUserHostOverrides(t *testing.T) {
	path := writeTestConfig(t, `
servers:
  - name: hk
    link: vless://u@hk.example:443
current: hk
hosts:
  intranet.corp: 10.1.2.3
`)
	deps := testRefresherDeps(t, path, staticResolver(map[string][]string{"hk.example": {"1.1.1.1"}}))
	var published map[string][]netip.Addr
	deps.setStaticA = func(m map[string][]netip.Addr) { published = m }
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if len(published["intranet.corp"]) == 0 {
		t.Fatalf("刷新不该抹掉用户 hosts 覆盖, got %v", published)
	}
}

// 用户 hosts 覆盖的 IP **永远**不能变成「传输服务器地址」。
//
// 那份地址会经 store.serverAddrs() → RuntimeState.ServerBypass → cli/guardian.go →
// guardian.BarrierContext.ServerBypass,变成 fail-closed `/2` 屏障上一个字面的
// permit 洞。而 `hosts:` 接受任何 IPv4 字面量、不限内网 —— 一条用户配置就能在
// 断网窗口里给任意公网 IP 打洞。
//
// 触发条件很窄但完全合法:某个域名先在 `hosts:` 里(于是进了 store 的静态表),
// 之后被用户改成传输服务器,而**这一轮**它解析失败 —— 上一轮那个用户 IP 就会被
// 「保留」逻辑捞回来,同时进 staticA 与屏障开口。讽刺的是 mergeHostOverrides
// 在**同一次刷新**里明确拒绝了这条覆盖(hosts_override_ignored),理由正是
// 「隧道会连到错误的地方 —— 而且是静默的」。
func TestBypassRefreshNeverPromotesHostOverrideIntoServerAddrs(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example": {"1.1.1.1"},
		// tokyo.example 这轮解析不了
	}))
	// 上一轮的 store:tokyo.example 那条来自用户 `hosts:`,不是解析出来的服务器地址。
	deps.store = newBypassStore(
		[]string{"1.1.1.1/32", "203.0.113.77/32"},
		map[string][]netip.Addr{
			"hk.example":    {netip.MustParseAddr("1.1.1.1")},
			"tokyo.example": {netip.MustParseAddr("203.0.113.77")}, // 用户 hosts 覆盖
		},
		map[string][]netip.Addr{
			"hk.example": {netip.MustParseAddr("1.1.1.1")}, // 真正的传输服务器只有 hk
		},
	)
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	carve := runtimeIPv4Bypass(deps.store.serverAddrs())
	if containsString(carve, "203.0.113.77/32") {
		t.Fatalf("用户 hosts 里的 IP 进了 Guardian 屏障开口 —— 这是 fail-closed 屏障上的一个洞, got %v", carve)
	}
	if got := deps.store.staticEntries()["tokyo.example"]; len(got) > 0 {
		t.Fatalf("被 mergeHostOverrides 当场拒绝的覆盖不该从「保留」那条路走回静态表, got %v", got)
	}
}

// 调用方点名的目标解析不了,刷新必须**失败**。
//
// `/v0/server` 用它作为「落实不了新服务器的 bypass 就绝不切过去」的判据。
// 沿用上一轮的地址回报成功,等于让一次「我不知道这台服务器今天在哪」的切换
// 照常进行 —— 而那正是本任务全部存在理由的那个成环。
// (非点名的已知服务器仍然保留,见 …RetainsKnownServerThatFailedThisRound:
// 那条是「别让一台正在用的服务器因为 DNS 抖一下掉出 bypass」,方向相反。)
func TestBypassRefreshRefusesWhenNamedTargetOnlyHasRetainedAddress(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example": {"1.1.1.1"},
		// tokyo.example 这轮解析不了
	}))
	deps.store = newBypassStore(
		[]string{"1.1.1.1/32", "2.2.2.2/32"},
		map[string][]netip.Addr{
			"hk.example":    {netip.MustParseAddr("1.1.1.1")},
			"tokyo.example": {netip.MustParseAddr("2.2.2.2")},
		},
		map[string][]netip.Addr{
			"hk.example":    {netip.MustParseAddr("1.1.1.1")},
			"tokyo.example": {netip.MustParseAddr("2.2.2.2")},
		},
	)
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), []string{"vless://u@tokyo.example:443"}); err == nil {
		t.Fatal("被点名的切换目标这轮解析不了,必须报错拒绝切换,而不是沿用上一轮地址报成功")
	}
}

// 同一个 host 既在配置清单里(非必需)又被调用方点名时,先被遍历到的那条
// 不能替点名的那条把「保留」用掉 —— 去重会让点名的那条根本走不到解析。
func TestResolveServerBypassNamedHostNeverRetainsEvenViaDuplicateLink(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@hk.example:443"},
			{Name: "tokyo", Link: "vless://u@tokyo.example:443", UDP: "hysteria2://u@tokyo.example:8443"},
		},
		Current: "hk",
	}
	retain := map[string][]netip.Addr{"tokyo.example": {netip.MustParseAddr("2.2.2.2")}}
	resolve := func(host string) []netip.Addr {
		if host == "hk.example" {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
		}
		return nil
	}
	_, _, err := resolveServerBypassRetaining(cfg, []string{"vless://u@tokyo.example:443"}, resolve, retain)
	if err == nil {
		t.Fatal("点名 host 的保留必须按 host 禁掉:清单里同 host 的另一条链接先跑,会替它把保留用掉再被去重跳过")
	}
}

func TestBypassRefreshFailsWithoutConfigPath(t *testing.T) {
	deps := testRefresherDeps(t, "", staticResolver(nil))
	deps.configPath = ""
	if _, err := newBypassRefresher(deps)(context.Background(), nil); err == nil {
		t.Fatal("没有配置路径就无从刷新,必须报错而不是默默说没变")
	}
}

func TestBypassRefreshFailsOnUnreadableConfig(t *testing.T) {
	deps := testRefresherDeps(t, filepath.Join(t.TempDir(), "missing.yaml"), staticResolver(nil))
	if _, err := newBypassRefresher(deps)(context.Background(), nil); err == nil {
		t.Fatal("读不到配置必须报错")
	}
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

var _ = errors.New

type allResolver struct{ addrs []netip.Addr }

func (r allResolver) Resolve(context.Context, string) (netip.Addr, error) { return r.addrs[0], nil }
func (r allResolver) ResolveAll(context.Context, string) ([]netip.Addr, error) {
	return r.addrs, nil
}

type oneResolver struct{ addr netip.Addr }

func (r oneResolver) Resolve(context.Context, string) (netip.Addr, error) { return r.addr, nil }

// 服务器域名有多条 A 记录时,bypass 必须覆盖**全部** —— 只留一条而子进程连了
// 另一条就是成环。故解析器能给全部时要用全部,给不了才退回单条。
func TestResolveAllPrefersEveryAddressWhenAvailable(t *testing.T) {
	want := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("2.2.2.2")}
	got, err := resolveAll(context.Background(), allResolver{addrs: want}, "h.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("多条 A 记录必须全进 bypass, got %v", got)
	}
}

func TestResolveAllFallsBackToSingleAddress(t *testing.T) {
	got, err := resolveAll(context.Background(), oneResolver{addr: netip.MustParseAddr("3.3.3.3")}, "h.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != netip.MustParseAddr("3.3.3.3") {
		t.Fatalf("给不了全部时退回单条, got %v", got)
	}
}

// IP 字面量的 link 根本不需要解析器 —— 解析器坏了也不该连累它们。
func TestResolveAllShortCircuitsIPLiterals(t *testing.T) {
	got, err := resolveAll(context.Background(), failingResolver{}, "4.4.4.4")
	if err != nil || len(got) != 1 || got[0] != netip.MustParseAddr("4.4.4.4") {
		t.Fatalf("IP 字面量不该经过解析器, got %v err=%v", got, err)
	}
}

type failingResolver struct{}

func (failingResolver) Resolve(context.Context, string) (netip.Addr, error) {
	return netip.Addr{}, errors.New("resolver down")
}

// RuntimeState.ServerBypass 不是显示用的:它经 cli/guardian.go 变成 Guardian
// 屏障的 BarrierContext.ServerBypass —— 屏障据此给服务器 IP 开口子。
// 用启动时那份冻结拷贝,切过服务器之后屏障放行的是**旧**服务器,新的那台被堵死。
func TestBypassRefreshUpdatesServerAddrsForRuntimeState(t *testing.T) {
	deps := testRefresherDeps(t, twoServerConfig(t), staticResolver(map[string][]string{
		"hk.example":    {"1.1.1.1"},
		"tokyo.example": {"2.2.2.2"},
		"new.example":   {"9.9.9.9"},
	}))
	deps.extraCIDRs = func() []string { return []string{"100.64.0.1/32"} }
	refresh := newBypassRefresher(deps)

	if _, err := refresh(context.Background(), []string{"vless://u@new.example:443"}); err != nil {
		t.Fatal(err)
	}
	got := runtimeIPv4Bypass(deps.store.serverAddrs())
	if !containsString(got, "9.9.9.9/32") {
		t.Fatalf("屏障要放行的是当前这台服务器, got %v", got)
	}
	// tailscale 与用户 hosts 覆盖不属于「传输服务器」,不该混进屏障开口。
	if containsString(got, "100.64.0.1/32") {
		t.Fatalf("与传输服务器无关的旁路不该进 RuntimeState.ServerBypass, got %v", got)
	}
}

// —— 接线(wireBypass)——
// 这一层存在的理由:本轮两个 Critical **都**是接线错误,而每个被抽出来的单元
// 各自都有测试。把组合本身抽出来,组合才有人盯着。

func testWiringParams(t *testing.T, configPath string) bypassWiringParams {
	t.Helper()
	return bypassWiringParams{
		configPath:    configPath,
		serverStatics: map[string][]netip.Addr{"hk.example": {netip.MustParseAddr("1.1.1.1")}},
		staticA:       map[string][]netip.Addr{"hk.example": {netip.MustParseAddr("1.1.1.1")}},
		resolver:      allResolver{addrs: []netip.Addr{netip.MustParseAddr("1.1.1.1")}},
	}
}

// 屏障开口(RuntimeState.ServerBypass → BarrierContext.ServerBypass)是在
// `/2` reject 屏障上**打洞**:每条都变成一条 `route add <ip>/32 <gateway>`。
// 用户 hosts 覆盖的 IP 混进去 = 在断网窗口里给一个任意 IPv4 开了口子
// (hosts: 接受任何 IPv4 字面量,不限内网)。
func TestWireBypassKeepsHostOverridesOutOfBarrierCarveOut(t *testing.T) {
	p := testWiringParams(t, twoServerConfig(t))
	// 与 run.go 一样:发布给 DNS 的静态表**已经**合并过用户 hosts 覆盖。
	p.staticA = map[string][]netip.Addr{
		"hk.example":    {netip.MustParseAddr("1.1.1.1")},
		"intranet.corp": {netip.MustParseAddr("10.1.2.3")},
		"nas.lan":       {netip.MustParseAddr("192.168.9.9")},
	}
	w := wireBypass(p)

	got := w.runtimeServerBypass()
	if len(got) != 1 || got[0] != "1.1.1.1/32" {
		t.Fatalf("屏障开口只能是传输服务器,不能带上用户 hosts 覆盖, got %v", got)
	}
}

// 刷新必须走注入的防环解析器。若接线接回系统解析器(hostToAddrs),这个域名
// 在真实世界解析不出来 —— 而生产里更糟:系统解析器此刻就是 bx,会回一个 fake IP。
func TestWireBypassRefreshUsesInjectedResolverNotSystemResolver(t *testing.T) {
	path := writeTestConfig(t, `
servers:
  - name: hk
    link: vless://u@only-resolvable-via-injected-resolver.invalid:443
current: hk
`)
	p := testWiringParams(t, path)
	p.resolver = allResolver{addrs: []netip.Addr{netip.MustParseAddr("9.9.9.9")}}
	w := wireBypass(p)

	if _, err := w.refresh(context.Background(), nil); err != nil {
		t.Fatalf("刷新应经注入的解析器解析成功(接回系统解析器就会失败): %v", err)
	}
	if !containsString(w.store.cidrs(), "9.9.9.9/32") {
		t.Fatalf("解析结果必须来自注入的解析器, got %v", w.store.cidrs())
	}
}

// runtimeServerBypass 必须现算。冻在启动值上 = 切过服务器之后屏障放行的还是旧那台。
func TestWireBypassRuntimeStateFollowsRefresh(t *testing.T) {
	p := testWiringParams(t, twoServerConfig(t))
	p.resolver = allResolver{addrs: []netip.Addr{netip.MustParseAddr("7.7.7.7")}}
	w := wireBypass(p)
	if got := w.runtimeServerBypass(); len(got) != 1 || got[0] != "1.1.1.1/32" {
		t.Fatalf("启动值, got %v", got)
	}

	if _, err := w.refresh(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if got := w.runtimeServerBypass(); !containsString(got, "7.7.7.7/32") {
		t.Fatalf("刷新之后屏障开口必须跟着走, got %v", got)
	}
}

// runBodyBetween 取 run.go 里一段源码,供接线守卫用。
func runSource(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("run.go")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 这条守的是**赋值**,不是逻辑 —— 它抬不出 Run():livePathRecoverer 的构造
// 捕获了 underlay/transports/tun/previous/verify 一串 Run 局部量,把它整个提出去
// 比这里守着的风险更大。
//
// **它同批的另外三条已经退场**(TestRunTakesRuntimeBypassFromWiringNotAFrozenSlice /
// TestRunWiresLiveMutatorToLiveBypassStore / TestNoPackageWritesToLiveMutatorStore):
// 集成台(`-tags integration`,真 Run + 真 netns + 真路由表)对它们守的属性各做过一次
// 变异,台子当场转红,守卫因此成了更弱的重复品。
//
// **这一条留下,因为台子照不到它 ——** 台子里没有任何一条测试驱动 `/v0/path-recovery`:
// livePathRecoverer 只在「underlay 观察到网络路径变了」时才跑,而那要求平台实现
// `Underlay()`(今天只有 darwin 有)并真的发生一次 Wi-Fi/网关切换。netns 里的
// linux 平台压根构造不出这个 recoverer,于是它读的是共享 store 还是一份冻结拷贝,
// 在台子里**没有任何可观测的差别**。
//
// 实测坐实(2026-08-09,不是推断):把这里的 `bypass: bypassState` 变异成
// `bypass: newBypassStore(bypassState.cidrs(), …)`,容器里跑完整 `-tags integration`
// 全套 —— **全绿,唯一转红的就是本测试**。哪天有人给台子补上一条驱动路径恢复的
// 断言(需要一个可注入的 underlayManager),再回来删这条。
//
// 文本守卫天生是漏的(CLAUDE.md 已经记着这一点),但「漏」好过「零」。
func TestRunWiresPathRecovererToLiveBypassStore(t *testing.T) {
	src := runSource(t)
	start := strings.Index(src, "recoverer = &livePathRecoverer{")
	if start < 0 {
		t.Fatal("找不到 livePathRecoverer 构造点(接线守卫失效,请更新本测试)")
	}
	body := src[start : start+strings.Index(src[start:], "\n\t\t\t}")]
	if !strings.Contains(body, "bypass:") || !strings.Contains(body, "bypassState") {
		t.Fatal("路径恢复必须接到共享 store(bypass: bypassState):拿启动时那份冻结拷贝,\n" +
			"切过服务器后一次 Wi-Fi 切换就会用旧集合重装旁路,把刚切过去的那台漏在外面 = 成环")
	}

	// 上面那两个子串挡不住这个(复审实测,能编译、`go test ./...` 全绿):
	//   bypass: newBypassStore(bypassState.cidrs(), bypassState.staticEntries(), …)
	// 它含 "bypass:" 也含 "bypassState",而 store 是**新的一份**,刷新写不进去
	// —— 正是本守卫名字里那个 bug。改判身份:那个位置必须是 bypassState 本身。
	why := "路径恢复读的必须是那份共享 store,不是它的任何拷贝/快照:\n" +
		"切过服务器后一次 Wi-Fi 切换就会用旧集合重装旁路,把刚切过去的那台漏在外面 = 静默成环"
	fn := runFuncDecl(t)
	requireSelector(t, soleAssignment(t, fn, "bypassState", why), "bypassWire", "store", why)
	requireNoFieldWrites(t, fn, "bypassState", why)
	requireNoWritesToFieldNamed(t, fn, "bypass", why)
	requireBareIdent(t, compositeField(t, fn, "livePathRecoverer", "bypass"), "bypassState", why)
}

// 这里曾有 TestRunWiresLiveMutatorToLiveBypassStore(liveMutator.store 必须是共享
// store 本身)与 TestRunTakesRuntimeBypassFromWiringNotAFrozenSlice(RuntimeState
// .ServerBypass 必须现算)。两条都被集成台接手,已删 —— 判据是变异实测,不是读代码:
//
//	① store: newBypassStore(bypassState.cidrs(), …)  /  store: nil
//	  → TestHarnessSwitchToNewServerInstallsBypassBeforeSwappingTransport 转红
//	    (换传输到清单外那台的**那一刻**,内核路由表里没有它的 /32)
//	② runtimeBypass = func() []string { return cachedBypass }(Run() 内事后冻结)
//	  以及在 Run() 外 freezeWiringForNow(&bypassWire)(包级冻结)
//	  → TestHarnessBarrierCarveOutFollowsCurrentServerOnly 转红
//	    (切换后屏障开口里没有新那台的 198.51.100.20/21)
//
// 台子问的是内核与控制面发布的事实,AST 守卫问的是 run.go 长什么样;后者本轮被
// 绕过 8 次。两道并列会让人误以为有两道防线,实际只有一道。

func TestRunFeedsBypassWiringTheLoopSafeResolver(t *testing.T) {
	src := runSource(t)
	start := strings.Index(src, "bypassWire := wireBypass(bypassWiringParams{")
	if start < 0 {
		t.Fatal("找不到 wireBypass 接线点(接线守卫失效,请更新本测试)")
	}
	body := src[start : start+strings.Index(src[start:], "\n\t})")]
	if !strings.Contains(body, "resolver:   newResolver(cfg.DNS.China, direct)") {
		t.Fatal("刷新必须走防环解析器(newResolver + DirectDialer):换成系统解析器就是\n" +
			"经 bx 自己解析 —— 新服务器会拿到 198.18/15 的 fake IP,然后给假 IP 装 bypass 路由")
	}
	if strings.Contains(body, "hostToAddrs") {
		t.Fatal("接线里出现了 hostToAddrs(系统解析器)—— 那正是 Critical 1")
	}
	// serverStatics 与 staticA 必须分开传:从 staticA 推 servers 会把用户 hosts
	// 覆盖的任意 IPv4 打进 Guardian 屏障开口。
	if !strings.Contains(body, "serverStatics: serverStatic") || !strings.Contains(body, "staticA:       staticA") {
		t.Fatal("serverStatics(屏障开口 + 保留基准)与 staticA(DNS 静态表)必须各传各的")
	}
}

// 屏障开口那条守卫此前只钉了**启动**那一次转置(wireBypass 的入参),没钉**刷新**
// 那一次发布。而 `d.store.set(next, staticA, serverStatic)` 里后两个实参同型
// (都是 map[string][]netip.Addr),写反了照样编译 —— 这正是 6c28339 把 servers
// 视图从 []netip.Addr 改成 map 时丢掉的那道编译期保护。
//
// 复审实测:把它改成 `set(next, staticA, staticA)`,既有守卫全绿,而用户 hosts:
// 里写的任意 IPv4 就此进了 fail-closed 屏障的放行口。既有守卫抓不到是因为它用的
// twoServerConfig 根本没有 hosts: 段,staticA == serverStatic,那个变异是空操作。
func TestBypassRefreshPublishKeepsHostOverridesOutOfBarrierCarveOut(t *testing.T) {
	path := writeTestConfig(t, `
servers:
  - name: hk
    link: vless://u@hk.example:443
current: hk
hosts:
    public-override.example: 203.0.113.77
`)
	p := testWiringParams(t, path)
	p.resolver = allResolver{addrs: []netip.Addr{netip.MustParseAddr("1.1.1.1")}}
	w := wireBypass(p)

	if _, err := w.refresh(context.Background(), nil); err != nil {
		t.Fatalf("刷新应成功: %v", err)
	}
	got := w.runtimeServerBypass()
	if len(got) != 1 || got[0] != "1.1.1.1/32" {
		t.Fatalf("刷新后的屏障开口只能是传输服务器地址,不得含用户 hosts 覆盖(203.0.113.77), got %v", got)
	}
}
