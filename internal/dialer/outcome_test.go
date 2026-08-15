package dialer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"

	"github.com/getbx/bx/internal/route"
)

// recordingCounter 记下数据面报上来的每一次决策与结果。
type recordingCounter struct {
	mu       sync.Mutex
	direct   int
	proxy    int
	dFail    int
	pFail    int
	attempts []string // "source|rule"
	failures []string
}

func (c *recordingCounter) Proxy()        { c.mu.Lock(); c.proxy++; c.mu.Unlock() }
func (c *recordingCounter) Direct()       { c.mu.Lock(); c.direct++; c.mu.Unlock() }
func (c *recordingCounter) Blocked()      {}
func (c *recordingCounter) UDPBlocked()   {}
func (c *recordingCounter) DirectFailed() { c.mu.Lock(); c.dFail++; c.mu.Unlock() }
func (c *recordingCounter) ProxyFailed()  { c.mu.Lock(); c.pFail++; c.mu.Unlock() }
func (c *recordingCounter) RuleAttempt(source, rule string) {
	c.mu.Lock()
	c.attempts = append(c.attempts, source+"|"+rule)
	c.mu.Unlock()
}

func (c *recordingCounter) RuleFailure(source, rule string) {
	c.mu.Lock()
	c.failures = append(c.failures, source+"|"+rule)
	c.mu.Unlock()
}

type failingDialer struct{ err error }

func (f failingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, f.err
}

type okDialer struct{}

func (okDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	c, _ := net.Pipe()
	return c, nil
}

type fixedResolver struct{}

func (fixedResolver) Resolve(context.Context, string) (netip.Addr, error) {
	return netip.MustParseAddr("203.0.113.9"), nil
}

// **这是整件事的落点:失败必须被记下来,并归因到逼出它的那条规则。**
//
// 真实场景:用户 config 里 `'*.steamstatic.com'` 强制直连,而那条直连路已经
// 完全不通 —— 每一次都在几毫秒内失败。bx 在数据面上**每一次都看见了**,
// 却只把它打进 debug 日志,于是 `bx status` 报 `direct 26186` 而对失败只字不提。
func TestDirectDialFailureIsCountedAndAttributedToTheRule(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{
		Resolver: fixedResolver{},
		Direct:   failingDialer{err: errors.New("connection reset")},
		Stats:    counter,
	}
	d.SetRouter(&route.Router{
		GlobalProxy: true,
		UserDirect:  route.NewDomainSet([]string{"*.steamstatic.com"}),
	})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "cdn.akamai.steamstatic.com", Port: 443}); err == nil {
		t.Fatal("这次拨号本该失败")
	}

	if counter.direct != 1 || counter.dFail != 1 {
		t.Errorf("direct=%d direct_failed=%d, want 1/1 —— 失败被扔掉了正是本次要修的",
			counter.direct, counter.dFail)
	}
	want := "user_direct|*.steamstatic.com"
	if len(counter.attempts) != 1 || counter.attempts[0] != want {
		t.Errorf("判定归因 = %v, want [%s]", counter.attempts, want)
	}
	if len(counter.failures) != 1 || counter.failures[0] != want {
		t.Errorf("失败归因 = %v, want [%s] —— 归因的全部价值就是点名该删哪一行",
			counter.failures, want)
	}
}

// 成功的拨号只记判定,不记失败。少了这一条,上面那条可以靠「什么都算失败」满足。
func TestSuccessfulDialIsNotCountedAsFailure(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter}
	d.SetRouter(&route.Router{
		GlobalProxy: true,
		UserDirect:  route.NewDomainSet([]string{"*.steamstatic.com"}),
	})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "a.steamstatic.com", Port: 443}); err != nil {
		t.Fatalf("这次拨号本该成功:%v", err)
	}
	if counter.dFail != 0 || len(counter.failures) != 0 {
		t.Errorf("成功的拨号被记成了失败:dFail=%d failures=%v", counter.dFail, counter.failures)
	}
}

// 走隧道那一侧同样要记 —— 隧道坏了的表现是**代理失败率飙升**,
// 而那是与「某条直连规则失效」完全不同的故障,处置也不同。
func TestProxyDialFailureIsCountedToo(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter}
	d.SetRouter(&route.Router{GlobalProxy: true})
	d.SetTransport(&Transport{
		Proxy:   failingDialer{err: errors.New("socks5 refused")},
		Healthy: func() bool { return true },
	})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "example.org", Port: 443}); err == nil {
		t.Fatal("这次拨号本该失败")
	}
	if counter.proxy != 1 || counter.pFail != 1 {
		t.Errorf("proxy=%d proxy_failed=%d, want 1/1", counter.proxy, counter.pFail)
	}
	if len(counter.failures) != 1 || counter.failures[0] != "default|" {
		t.Errorf("内建来源也要归因(只是没有规则原文可点名):%v", counter.failures)
	}
}

// **UDP 的失败此前一次都没被数过。**
//
// recordFailure 只在 TCP 那三条路上可达,而 UDP 分支在 DialContext 顶部就短路了。
// 于是一条 UDP 传输哪怕每次拨号都失败,`bx status` 也是一片安静:proxy 计数照涨
// (判定确实发生了),failed 恒为 0 —— 正是「结果计数」这件事要消灭的处境,
// 只不过上一轮把 UDP 这半边漏掉了。
func TestUDPProxyDialFailureIsCounted(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{GlobalProxy: true})
	d.SetTransport(&Transport{Proxy: failingDialer{err: errors.New("no route")}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("203.0.113.9"), Port: 443, UDP: true}); err == nil {
		t.Fatal("拨号应当失败")
	}
	if counter.proxy != 1 {
		t.Errorf("判定没记上:proxy=%d", counter.proxy)
	}
	if counter.pFail != 1 {
		t.Fatalf("UDP 失败没被数:proxy_failed=%d —— 一条每次都失败的 UDP 路径在 status 里隐形", counter.pFail)
	}
	if len(counter.failures) != 1 || counter.failures[0] != "udp_proxy_fallback|" {
		t.Errorf("失败没归因到来源:%v", counter.failures)
	}
}

// **归因到「模式」,因为 UDP 根本不问 router。**
//
// proxy 模式下所有 UDP 一律走隧道,没有「命中了哪条规则」可归。但它必须归到
// 某个东西 —— 否则 status 里那几百条 proxy 连接凭空出现、无从解释。
func TestUDPDecisionsAreAttributedToTheirMode(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{GlobalProxy: true})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})
	d.SetUDPTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("203.0.113.9"), Port: 443, UDP: true}); err != nil {
		t.Fatal(err)
	}
	// **查找而不是断言「恰好一行」**:同一条 UDP 还会额外记一行反事实
	// (udp_would_flip_* / udp_no_change),那是另一类行,不是判定。
	if !containsAttempt(counter.attempts, "udp_proxy|") {
		t.Fatalf("判定没归因:%v", counter.attempts)
	}
}

// **回落必须能被看见。**
//
// 专用 UDP 传输挂掉时 bx 静默退回主传输 —— 通路保住了(那是对的),而用户白白
// 失去了 QUIC 加速档,且此前没有任何地方说得出这件事正在发生。两种来源必须分开,
// 否则「一直在回落」与「一直好好的」在 status 里逐字节相同。
func TestUDPFallbackIsCountedSeparatelyFromTheDedicatedTransport(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{GlobalProxy: true})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})
	// 专用 UDP 传输不健康 → 回落主传输。
	d.SetUDPTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return false }})

	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("203.0.113.9"), Port: 443, UDP: true}); err != nil {
		t.Fatal(err)
	}
	if !containsAttempt(counter.attempts, "udp_proxy_fallback|") {
		t.Fatalf("回落没有单独记:%v —— 「一直在回落」与「一直好好的」会长得一模一样", counter.attempts)
	}
	if containsAttempt(counter.attempts, "udp_proxy|") {
		t.Fatalf("回落同时也记成了正常走加速档:%v", counter.attempts)
	}
}

// direct-realtime 那条路同样要数:它以**真实 IP** 直连,失败在那里最值得被看见。
// 解析失败也算 —— 此前它直接 return,一次都没被数过。
func TestUDPDirectRealtimeFailuresAreCounted(t *testing.T) {
	t.Run("拨号失败", func(t *testing.T) {
		counter := &recordingCounter{}
		d := &Dialer{
			Resolver: fixedResolver{}, Direct: failingDialer{err: errors.New("host unreachable")},
			Stats: counter, UDPMode: "direct-realtime",
		}
		d.SetRouter(&route.Router{GlobalProxy: true})
		d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

		if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("203.0.113.9"), Port: 443, UDP: true}); err == nil {
			t.Fatal("拨号应当失败")
		}
		if counter.dFail != 1 {
			t.Fatalf("direct_failed = %d, want 1", counter.dFail)
		}
		if len(counter.failures) != 1 || counter.failures[0] != "udp_direct_realtime|" {
			t.Errorf("失败没归因:%v", counter.failures)
		}
	})

	t.Run("解析失败", func(t *testing.T) {
		counter := &recordingCounter{}
		d := &Dialer{
			Resolver: failingResolver{}, Direct: okDialer{},
			Stats: counter, UDPMode: "direct-realtime",
		}
		d.SetRouter(&route.Router{GlobalProxy: true})
		d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

		if _, err := d.Dial(context.Background(), route.Meta{Domain: "example.com", Port: 443, UDP: true}); err == nil {
			t.Fatal("解析应当失败")
		}
		if counter.dFail != 1 {
			t.Fatalf("解析失败没被数:direct_failed = %d", counter.dFail)
		}
	})
}

type failingResolver struct{}

func (failingResolver) Resolve(context.Context, string) (netip.Addr, error) {
	return netip.Addr{}, errors.New("no such host")
}

// **加一条直连规则,是一句「这些流量不要经过隧道」的指令。**
//
// bx 此前只对 TCP 照办,对 UDP 当没听见,而且不说 —— 且 `udp.mode` 默认就是
// proxy,所以这影响每一个用默认配置的人;Apple/iCloud/Steam/微信这些最常见的
// 直连目标恰恰重度用 QUIC。
func TestUserDirectRuleAppliesToUDP(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{
		GlobalProxy: true,
		UserDirect:  route.NewDomainSet([]string{"*.steamcontent.com"}),
	})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "cdn.steamcontent.com", Port: 443, UDP: true}); err != nil {
		t.Fatal(err)
	}
	if counter.proxy != 0 {
		t.Fatalf("强制直连的域名,UDP 仍然走了隧道(proxy=%d)", counter.proxy)
	}
	if counter.direct != 1 {
		t.Fatalf("没走直连:direct=%d", counter.direct)
	}
	// **归因用真实的规则来源**,这样用户那条 config 里的规则连 UDP 的成败一起
	// 认领 —— 点名到的是他改得了的那一行。
	if len(counter.attempts) != 1 || counter.attempts[0] != "user_direct|*.steamcontent.com" {
		t.Fatalf("没归因到那条规则:%v", counter.attempts)
	}
}

// **私网恒直连,UDP 也不例外。** 这条不是取舍,是本项目明写的不变量;
// 违反它的后果是局域网 UDP(mDNS、AirPlay、打印机、SMB 发现)被送进隧道。
func TestPrivateAddressesStayDirectForUDP(t *testing.T) {
	private, err := route.NewCIDRSet(route.DefaultPrivateCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{GlobalProxy: true, PrivateDirect: private})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	for _, addr := range []string{"192.168.1.20", "10.0.0.5", "172.17.0.2"} {
		counter.proxy, counter.direct = 0, 0
		if _, err := d.Dial(context.Background(), route.Meta{
			IP: netip.MustParseAddr(addr), Port: 5353, UDP: true,
		}); err != nil {
			t.Fatal(err)
		}
		if counter.proxy != 0 {
			t.Errorf("%s 的 UDP 被送进了隧道 —— 违反「私网恒直连」", addr)
		}
	}
}

// **用户点名走隧道的域名,在 direct-realtime 模式下也必须走隧道。**
//
// 那个模式让所有 UDP 以真实 IP 出去 —— 包括用户明确标了强制走隧道的域名,
// 而那是直接违背一条显式指令的泄漏。这个方向的修复是**减少**泄漏。
func TestUserProxyRuleSurvivesDirectRealtimeMode(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "direct-realtime"}
	d.SetRouter(&route.Router{UserProxy: route.NewDomainSet([]string{"*.private.example"})})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "a.private.example", Port: 443, UDP: true}); err != nil {
		t.Fatal(err)
	}
	if counter.direct != 0 {
		t.Fatalf("强制走隧道的域名以真实 IP 直连了(direct=%d)", counter.direct)
	}
	if counter.proxy != 1 {
		t.Fatalf("没走隧道:proxy=%d", counter.proxy)
	}
}

// **用户点名走隧道 + 隧道挂了 = 阻断,绝不回落直连。** 与 TCP 同一条 kill-switch。
func TestUserProxyUDPFailsClosedWhenTunnelIsDown(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{
		Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter,
		UDPMode: "direct-realtime", Killswitch: true,
	}
	d.SetRouter(&route.Router{UserProxy: route.NewDomainSet([]string{"*.private.example"})})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return false }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "a.private.example", Port: 443, UDP: true}); !errors.Is(err, ErrBlocked) {
		t.Fatalf("隧道挂了却没有阻断:err=%v direct=%d", err, counter.direct)
	}
	if counter.direct != 0 {
		t.Fatal("回落成了直连 —— 真实 IP 泄漏")
	}
}

// **用户点名直连的域名不受 kill-switch 约束。** kill-switch 防的是隧道挂掉时
// *回落*直连,而这里是用户点名要直连 —— 与 TCP 的直连同理。
func TestUserDirectUDPIsNotBlockedByKillswitch(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{
		Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter,
		UDPMode: "proxy", Killswitch: true,
	}
	d.SetRouter(&route.Router{
		GlobalProxy: true,
		UserDirect:  route.NewDomainSet([]string{"*.steamcontent.com"}),
	})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return false }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "cdn.steamcontent.com", Port: 443, UDP: true}); err != nil {
		t.Fatalf("用户点名直连却被阻断:%v", err)
	}
	if counter.direct != 1 {
		t.Fatalf("没走直连:direct=%d", counter.direct)
	}
}

// **china 列表刻意不在此列。** 它是 bx 塞进去的 12165 条默认,用户没有逐条看过;
// 把它的作用域从 TCP 悄悄扩到 UDP,等于一次性把一大批流量推到真实 IP 上出去。
func TestChinaListStillDoesNotApplyToUDP(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{ChinaDomain: route.NewDomainSet([]string{"*.qq.com"})})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "im.qq.com", Port: 443, UDP: true}); err != nil {
		t.Fatal(err)
	}
	if counter.direct != 0 {
		t.Fatalf("china 列表把 UDP 送去了直连(direct=%d)—— 那是一个单独的决定,不该搭这趟车", counter.direct)
	}
}

func containsAttempt(attempts []string, want string) bool {
	for _, a := range attempts {
		if a == want {
			return true
		}
	}
	return false
}

// **「判不出来」那一桶不许被折进「不受影响」。**
//
// 无域名时 Explain 走 ExplainIP,而 fake IP 既不在 china CIDR 也不在私网段,
// 会落进「默认」——看起来就像「这条不受影响」。折进去的后果是把影响面**系统性
// 低估**,而这份测量存在的全部意义就是不靠推测。
func TestUDPCounterfactualBuckets(t *testing.T) {
	fake := fakeRange{netip.MustParsePrefix("198.18.0.0/15")}
	for _, tc := range []struct {
		name string
		meta route.Meta
		why  route.Reason
		pool fakeIPRange
		want string
	}{
		{
			name: "有域名且命中 china 域名列表 → 会改走直连",
			meta: route.Meta{Domain: "im.qq.com"},
			why:  route.Reason{Source: route.SourceChinaDomain},
			pool: fake, want: udpWouldFlipDomain,
		},
		{
			name: "有域名但没命中 → 不受影响",
			meta: route.Meta{Domain: "example.org"},
			why:  route.Reason{Source: route.SourceDefault},
			pool: fake, want: udpNoChange,
		},
		{
			name: "无域名 + 真实公网 IP 命中 china CIDR → 会改走直连",
			meta: route.Meta{IP: netip.MustParseAddr("223.5.5.5")},
			why:  route.Reason{Source: route.SourceChinaCIDR},
			pool: fake, want: udpWouldFlipCIDR,
		},
		{
			name: "无域名 + 地址在 fake-IP 段里 → 判不出来",
			meta: route.Meta{IP: netip.MustParseAddr("198.18.0.7")},
			why:  route.Reason{Source: route.SourceDefault},
			pool: fake, want: udpUndecidable,
		},
		{
			name: "无域名 + 真实公网 IP 没命中 → 不受影响",
			meta: route.Meta{IP: netip.MustParseAddr("203.0.113.9")},
			why:  route.Reason{Source: route.SourceDefault},
			pool: fake, want: udpNoChange,
		},
		{
			name: "没有 fake-IP 池:地址就是真实地址,不存在判不出来",
			meta: route.Meta{IP: netip.MustParseAddr("223.5.5.5")},
			why:  route.Reason{Source: route.SourceChinaCIDR},
			pool: nil, want: udpWouldFlipCIDR,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := udpCounterfactualBucket(tc.meta, tc.why, tc.pool); got != tc.want {
				t.Fatalf("桶 = %q, want %q", got, tc.want)
			}
		})
	}
}

// **没有 fake-IP 的部署一发 UDP 就不许崩。**
//
// d.Fake 是 *fakeip.Pool;为 nil 时若直接装进接口,会得到「非 nil 的接口值包着
// 一个 nil 指针」,于是判定里那句 `pool != nil` 为真、随即解引用 panic。
// Go 里最经典的那个坑,而它在这里的后果是整条 UDP 路径当场崩溃 —— 本轮真的踩了。
func TestUDPCounterfactualSurvivesANilFakeIPPool(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{GlobalProxy: true})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	// d.Fake 保持 nil —— 不设 fake-IP 的部署。
	if _, err := d.Dial(context.Background(), route.Meta{
		IP: netip.MustParseAddr("203.0.113.9"), Port: 443, UDP: true,
	}); err != nil {
		t.Fatal(err)
	}
	if !containsAttempt(counter.attempts, udpNoChange+"|") {
		t.Fatalf("反事实没记上:%v", counter.attempts)
	}
}

// 反事实计数**必须每条 UDP 都记**,否则那几个数没有分母 ——
// 「会改走直连的有 300 条」在不知道总数时说明不了任何事。
func TestEveryUDPConnectionGetsACounterfactualBucket(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter, UDPMode: "proxy"}
	d.SetRouter(&route.Router{})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	for i := 0; i < 3; i++ {
		if _, err := d.Dial(context.Background(), route.Meta{
			IP: netip.MustParseAddr("203.0.113.9"), Port: 443, UDP: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var buckets int
	for _, a := range counter.attempts {
		if strings.HasPrefix(a, "udp_would_flip") || strings.HasPrefix(a, "udp_no_change") ||
			strings.HasPrefix(a, "udp_undecidable") {
			buckets++
		}
	}
	if buckets != 3 {
		t.Fatalf("3 条连接只记了 %d 个反事实桶 —— 那几个数会没有分母:%v", buckets, counter.attempts)
	}
}

type fakeRange struct{ prefix netip.Prefix }

func (f fakeRange) Contains(ip netip.Addr) bool { return f.prefix.Contains(ip) }
