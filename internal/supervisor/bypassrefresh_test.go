package supervisor

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"
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
		store:      newBypassStore(nil, nil),
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
		})
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
