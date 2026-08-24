package dialer

import (
	"context"
	"net/netip"
	"sync"
	"testing"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/route"
)

// appCall 是 AppRecorder 收到的一次记录。**udp 必须一起记下来** ——
// 把它写死成 false 不会有编译错误,而后果是所有 UDP 流量被归到 TCP 键上,
// 界面上只看得到一个应用名、看不到冲突。
type appCall struct {
	srcPort uint16
	udp     bool
	path    appattr.Path
	source  string
	rule    string
	dest    string
}

type fakeAppRecorder struct {
	mu     sync.Mutex
	calls  []appCall
	closed []releaseCall
	nextID uint64
	issued []uint64
}

// ConnClosed 记下一次释放。与 Record 成对 —— 两者住在同一个接口里,是为了让
// 「记了账没人释放」在编译期就不成立。
func (f *fakeAppRecorder) ConnClosed(flowID uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = append(f.closed, releaseCall{flowID})
}

func (f *fakeAppRecorder) Record(srcPort uint16, udp bool, path appattr.Path, source, rule, dest string) uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, appCall{srcPort, udp, path, source, rule, dest})
	// 从 1 起发号:0 是「没记账」,与生产侧 flowSeq 同一条约定。
	f.nextID++
	f.issued = append(f.issued, f.nextID)
	return f.nextID
}

// onlyIssued 是「这次拨号发出的那一个流 ID」。与 only(t) 同一条纪律:恰好一个,
// 否则响亮失败 —— 一次拨号记两笔账而只释放一笔,正是配平不成立的那种形状。
func (f *fakeAppRecorder) onlyIssued(t *testing.T) uint64 {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.issued) != 1 {
		t.Fatalf("发了 %d 个流 ID, want 1: %v", len(f.issued), f.issued)
	}
	return f.issued[0]
}

func (f *fakeAppRecorder) only(t *testing.T) appCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) != 1 {
		t.Fatalf("记了 %d 次, want 1: %+v", len(f.calls), f.calls)
	}
	return f.calls[0]
}

// newAppDialer 造一个带用户规则的 Dialer:
// *.qq.com 强制直连、*.openai.com 强制走隧道,与 newTestDialer 同构但带归因器。
func newAppDialer(t *testing.T, healthy bool) (*Dialer, *fakeAppRecorder) {
	t.Helper()
	rec := &fakeAppRecorder{}
	priv, err := route.NewCIDRSet(route.DefaultPrivateCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	// **Stats 必须挂上,而且这一条是承重的。** countUDPCounterfactual 第一句是
	// `if d.Stats == nil { return }` —— 不挂 Stats,那整段反事实代码在测试里根本
	// 不执行,于是「反事实不许再记第二条」这条断言(only(t) 要求恰好一条)就
	// 什么也守不住:把 recordApp 加回反事实那一行,全套测试照样绿。
	d := &Dialer{
		Resolver:    fixedResolver{},
		Direct:      okDialer{},
		Killswitch:  true,
		AppRecorder: rec,
		Stats:       noopCounter{},
		UDPMode:     "proxy",
	}
	d.SetRouter(&route.Router{
		UserProxy:     route.NewDomainSet([]string{"*.openai.com"}),
		UserDirect:    route.NewDomainSet([]string{"*.qq.com"}),
		PrivateDirect: priv,
	})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return healthy }})
	return d, rec
}

// 应用归因是**旁观者**:它拿到判定结果,但绝不影响判定。这条同时钉住
// 「记了」和「记的是判定实际走的那条路」。
func TestDialerRecordsPathAndRuleForAppAttribution(t *testing.T) {
	d, rec := newAppDialer(t, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "a.qq.com", Port: 443, SrcPort: 51234}); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{51234, false, appattr.PathDirect, "user_direct", "*.qq.com", "a.qq.com"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsTunnelPathForProxiedTCP(t *testing.T) {
	d, rec := newAppDialer(t, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "api.openai.com", Port: 443, SrcPort: 40001}); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{40001, false, appattr.PathTunnel, "user_proxy", "*.openai.com", "api.openai.com"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

// kill-switch 挡掉的连接必须进 blocked 组 —— 它直接回答「为什么这个 App
// 一开 bx 就废了」,而这个问题今天完全没有答案。
func TestDialerRecordsBlockedConnections(t *testing.T) {
	d, rec := newAppDialer(t, false) // 隧道不健康
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "api.openai.com", Port: 443, SrcPort: 40002}); err != ErrBlocked {
		t.Fatalf("应被 kill-switch 阻断, got %v", err)
	}
	got := rec.only(t)
	want := appCall{40002, false, appattr.PathBlocked, "user_proxy", "*.openai.com", "api.openai.com"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

// —— 下面全部是 UDP。漏掉 UDP 分支正好会漏掉腾讯会议的媒体流,
// 也就是这个功能最初的用例。

func TestDialerRecordsUDPDirectByUserRule(t *testing.T) {
	d, rec := newAppDialer(t, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "meeting.qq.com", Port: 8000, UDP: true, SrcPort: 51001}); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{51001, true, appattr.PathDirect, "user_direct", "*.qq.com", "meeting.qq.com"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsUDPProxyByUserRule(t *testing.T) {
	d, rec := newAppDialer(t, true)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "x.openai.com", Port: 443, UDP: true, SrcPort: 51002}); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{51002, true, appattr.PathTunnel, "user_proxy", "*.openai.com", "x.openai.com"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsUDPBlockedByUserRuleWhenTunnelDown(t *testing.T) {
	d, rec := newAppDialer(t, false)
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "x.openai.com", Port: 443, UDP: true, SrcPort: 51003}); err != ErrBlocked {
		t.Fatalf("应 fail-closed, got %v", err)
	}
	got := rec.only(t)
	want := appCall{51003, true, appattr.PathBlocked, "user_proxy", "*.openai.com", "x.openai.com"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsUDPPrivateDirect(t *testing.T) {
	d, rec := newAppDialer(t, true)
	m := route.Meta{IP: netip.MustParseAddr("192.168.1.7"), Port: 5353, UDP: true, SrcPort: 51004}
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{51004, true, appattr.PathDirect, "private", "", "192.168.1.7"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

// udp.mode=proxy 这条**没有规则可归因**的路也必须记 —— 否则 bx status 里
// 那几百条 UDP 连接凭空出现。同时钉住:反事实计数(countUDPCounterfactual)
// **不许**再记第二条,它不是判定。
func TestDialerRecordsUDPProxyModeOnceOnly(t *testing.T) {
	d, rec := newAppDialer(t, true)
	m := route.Meta{IP: netip.MustParseAddr("198.18.0.9"), Port: 443, UDP: true, SrcPort: 51005}
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	// 没配 UDP 专用传输 → 回落主传输,来源是 fallback 那一档(生产行为,不是笔误)。
	want := appCall{51005, true, appattr.PathTunnel, udpSourceProxyFallback, "", "198.18.0.9"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsUDPProxyModeBlockedWhenTunnelDown(t *testing.T) {
	d, rec := newAppDialer(t, false)
	m := route.Meta{IP: netip.MustParseAddr("198.18.0.9"), Port: 443, UDP: true, SrcPort: 51006}
	if _, err := d.Dial(context.Background(), m); err != ErrBlocked {
		t.Fatalf("应 fail-closed, got %v", err)
	}
	got := rec.only(t)
	want := appCall{51006, true, appattr.PathBlocked, udpSourceProxyFallback, "", "198.18.0.9"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsUDPDirectRealtime(t *testing.T) {
	d, rec := newAppDialer(t, true)
	d.UDPMode = "direct-realtime"
	m := route.Meta{IP: netip.MustParseAddr("198.18.0.9"), Port: 3478, UDP: true, SrcPort: 51007}
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{51007, true, appattr.PathDirect, udpSourceDirectRealtime, "", "198.18.0.9"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsUDPDirectRealtimeBlockedWhenTunnelDown(t *testing.T) {
	d, rec := newAppDialer(t, false)
	d.UDPMode = "direct-realtime"
	m := route.Meta{IP: netip.MustParseAddr("198.18.0.9"), Port: 3478, UDP: true, SrcPort: 51008}
	if _, err := d.Dial(context.Background(), m); err != ErrBlocked {
		t.Fatalf("应 fail-closed, got %v", err)
	}
	got := rec.only(t)
	want := appCall{51008, true, appattr.PathBlocked, udpSourceDirectRealtime, "", "198.18.0.9"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsUDPModeBlock(t *testing.T) {
	d, rec := newAppDialer(t, true)
	d.UDPMode = "block"
	m := route.Meta{IP: netip.MustParseAddr("198.18.0.9"), Port: 443, UDP: true, SrcPort: 51009}
	if _, err := d.Dial(context.Background(), m); err != ErrBlocked {
		t.Fatalf("应阻断, got %v", err)
	}
	got := rec.only(t)
	want := appCall{51009, true, appattr.PathBlocked, udpSourceModeBlock, "", "198.18.0.9"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

// **udp_proxy(非 fallback)那一档在真机数据里是主力**(udp_proxy 72 vs
// udp_proxy_fallback 3),而它与 fallback 只差一个健康的 UDP 专用传输。
// 没有这条用例时,把两条 source 记反不会有任何测试转红。
func TestDialerRecordsUDPProxyModeWithDedicatedTransport(t *testing.T) {
	d, rec := newAppDialer(t, true)
	d.SetUDPTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})
	m := route.Meta{IP: netip.MustParseAddr("198.18.0.9"), Port: 443, UDP: true, SrcPort: 51012}
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{51012, true, appattr.PathTunnel, udpSourceProxy, "", "198.18.0.9"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

// 具名出口:走的是另一条隧道,但仍然不是直连 —— 归到 tunnel 组。
func TestDialerRecordsViaEgress(t *testing.T) {
	d, rec := newAppDialer(t, true)
	eg, err := route.NewEgressSet([][2]string{{"office", "10.84.3.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	d.SetRouter(&route.Router{UserEgress: eg})
	d.SetEgresses(map[string]ContextDialer{"office": okDialer{}})
	m := route.Meta{IP: netip.MustParseAddr("10.84.3.239"), Port: 22, SrcPort: 51010}
	if _, err := d.Dial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := rec.only(t)
	want := appCall{51010, false, appattr.PathTunnel, "user_egress", "office", "10.84.3.239"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

func TestDialerRecordsViaEgressBlockedWhenUnwired(t *testing.T) {
	d, rec := newAppDialer(t, true)
	eg, err := route.NewEgressSet([][2]string{{"office", "10.84.3.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	d.SetRouter(&route.Router{UserEgress: eg})
	m := route.Meta{IP: netip.MustParseAddr("10.84.3.239"), Port: 22, SrcPort: 51011}
	if _, err := d.Dial(context.Background(), m); err != ErrBlocked {
		t.Fatalf("出口没接线应阻断, got %v", err)
	}
	got := rec.only(t)
	want := appCall{51011, false, appattr.PathBlocked, "user_egress", "office", "10.84.3.239"}
	if got != want {
		t.Fatalf("记的内容不对: got %+v want %+v", got, want)
	}
}

// 没接归因器时不许 panic —— AppRecorder 可空是这个功能的隐私前提
// (没人订阅时数据面一个字都不记)。
func TestDialerWithoutAppRecorderDoesNotPanic(t *testing.T) {
	d, _ := newAppDialer(t, true)
	d.AppRecorder = nil
	if _, err := d.Dial(context.Background(), route.Meta{Domain: "a.qq.com", Port: 443, SrcPort: 1}); err != nil {
		t.Fatal(err)
	}
}

// —— 下面三条直接测 recordApp 的目的地选值,不经过 Dial —— 域名优先、
// 没有域名回落裸 IP、两者都没有报空串。

func TestRecordAppPrefersDomainOverIP(t *testing.T) {
	rec := &fakeAppRecorder{}
	d := &Dialer{AppRecorder: rec}
	m := route.Meta{Domain: "api.openai.com", IP: netip.MustParseAddr("198.18.0.9"), SrcPort: 1}
	d.recordApp(&flowSlot{}, m, appattr.PathTunnel, "user_proxy", "*.openai.com")
	got := rec.only(t)
	if got.dest != "api.openai.com" {
		t.Fatalf("dest = %q, want 域名优先于 IP", got.dest)
	}
}

func TestRecordAppFallsBackToTheLiteralIP(t *testing.T) {
	rec := &fakeAppRecorder{}
	d := &Dialer{AppRecorder: rec}
	m := route.Meta{IP: netip.MustParseAddr("198.18.0.9"), SrcPort: 1}
	d.recordApp(&flowSlot{}, m, appattr.PathDirect, "private", "")
	got := rec.only(t)
	if got.dest != "198.18.0.9" {
		t.Fatalf("dest = %q, want 回落到裸 IP 字面量", got.dest)
	}
}

func TestRecordAppReportsNoDestinationWhenItHasNeither(t *testing.T) {
	rec := &fakeAppRecorder{}
	d := &Dialer{AppRecorder: rec}
	m := route.Meta{SrcPort: 1}
	d.recordApp(&flowSlot{}, m, appattr.PathBlocked, "private", "")
	got := rec.only(t)
	if got.dest != "" {
		t.Fatalf("dest = %q, want 空串(既无域名也无有效 IP)", got.dest)
	}
}

// noopCounter 是 DecisionCounter 的空实现:这一批测试只关心归因,不关心计数,
// 但**必须让计数那条路真的执行**(见 newAppDialer 里的注释)。
type noopCounter struct{}

func (noopCounter) Proxy()                  {}
func (noopCounter) Direct()                 {}
func (noopCounter) Blocked()                {}
func (noopCounter) UDPBlocked()             {}
func (noopCounter) DirectFailed()           {}
func (noopCounter) ProxyFailed()            {}
func (noopCounter) RuleAttempt(_, _ string) {}
func (noopCounter) RuleFailure(_, _ string) {}
