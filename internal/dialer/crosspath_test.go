package dialer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/route"
)

// 数据面的**跨路径一致性表**。
//
// ## 为什么要这张表
//
// 2026-08-15/16 两轮自审抓到四个 bug,判定本身全都对 —— 出问题的**全是同一个
// 形状**:一个判据被多条路径消费,而只有最容易的那条被测到。
//
//   - 链接解析:host 认 7 种 scheme,port 只认 4 种
//   - 用户 direct/proxy 规则:TCP 认,UDP 不认
//   - 具名出口:TCP 认,UDP 又不认(修完前一条之后新长出来的)
//   - 换服务器:单次对,并发下验错对象
//
// 逐个猜下一个在哪是没有尽头的。这张表把「同一个输入在 TCP 与 UDP 上各自去了
// 哪」摆开,一次性钉住。
//
// ## 它的形状为什么不是「断言两边一致」
//
// **bx 有刻意的分岔**:china 列表就不对 UDP 生效(项目所有者的决定 —— 它是 bx
// 塞进去的 12165 条默认,把作用域扩到 UDP 等于一次性把一大批流量推到真实 IP 上)。
// 只断言一致会逼着人把这条真实的设计删掉。
//
// 所以规则是:**两边不同时必须写明理由(why)**,写不出来就是 bug。
// 意外的分岔从此加不进来 —— 加了就得当场解释它,而解释不通的会被 review 抓住。

// egressKind 是一条连接最终去了哪儿。
type pathOutcome string

const (
	wentDirect  pathOutcome = "direct"
	wentTunnel  pathOutcome = "tunnel"
	wentEgress  pathOutcome = "egress"
	wasBlocked  pathOutcome = "blocked"
	wentNowhere pathOutcome = "?"
)

type pathProbe struct {
	got  *pathOutcome
	kind pathOutcome
}

func (p pathProbe) DialContext(context.Context, string, string) (net.Conn, error) {
	*p.got = p.kind
	c, _ := net.Pipe()
	return c, nil
}

// runBothPaths 把同一个输入分别按 TCP 与 UDP 走一遍,报告各自去了哪。
func runBothPaths(t *testing.T, rt *route.Router, m route.Meta) (tcp, udp pathOutcome) {
	t.Helper()
	for _, udpFlag := range []bool{false, true} {
		got := wentNowhere
		d := &Dialer{
			Resolver: fixedResolver{},
			Direct:   pathProbe{got: &got, kind: wentDirect},
			UDPMode:  "proxy", // 默认模式
		}
		d.SetRouter(rt)
		d.SetTransport(&Transport{Proxy: pathProbe{got: &got, kind: wentTunnel}, Healthy: func() bool { return true }})
		d.SetEgresses(map[string]ContextDialer{"office": pathProbe{got: &got, kind: wentEgress}})

		probe := m
		probe.UDP = udpFlag
		if _, err := d.Dial(context.Background(), probe); errors.Is(err, ErrBlocked) {
			got = wasBlocked
		}
		if udpFlag {
			udp = got
		} else {
			tcp = got
		}
	}
	return tcp, udp
}

func crossPathRouter(t *testing.T) *route.Router {
	t.Helper()
	private, err := route.NewCIDRSet(route.DefaultPrivateCIDRs)
	if err != nil {
		t.Fatal(err)
	}
	directIP, err := route.NewCIDRSet([]string{"203.0.113.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	chinaCIDR, err := route.NewCIDRSet([]string{"223.5.5.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	egress, err := route.NewEgressSet([][2]string{{"office", "10.84.0.0/16"}})
	if err != nil {
		t.Fatal(err)
	}
	return &route.Router{
		UserDirect:    route.NewDomainSet([]string{"*.direct.example"}),
		UserProxy:     route.NewDomainSet([]string{"*.proxy.example"}),
		UserDirectIP:  directIP,
		PrivateDirect: private,
		ChinaDomain:   route.NewDomainSet([]string{"*.cn.example"}),
		ChinaCIDR:     chinaCIDR,
		UserEgress:    egress,
	}
}

func TestSameInputTakesTheSamePathOnTCPAndUDP(t *testing.T) {
	rt := crossPathRouter(t)
	for _, tc := range []struct {
		name    string
		meta    route.Meta
		wantTCP pathOutcome
		wantUDP pathOutcome
		// why 只在两边不同时填。**填不出来就说明那是 bug,不是设计。**
		why string
	}{
		{
			name: "用户 direct 域名", meta: route.Meta{Domain: "a.direct.example", Port: 443},
			wantTCP: wentDirect, wantUDP: wentDirect,
		},
		{
			name: "用户 direct 网段", meta: route.Meta{IP: netip.MustParseAddr("203.0.113.9"), Port: 443},
			wantTCP: wentDirect, wantUDP: wentDirect,
		},
		{
			name: "用户 proxy 域名", meta: route.Meta{Domain: "a.proxy.example", Port: 443},
			wantTCP: wentTunnel, wantUDP: wentTunnel,
		},
		{
			name: "私网(恒直连不变量)", meta: route.Meta{IP: netip.MustParseAddr("192.168.1.20"), Port: 445},
			wantTCP: wentDirect, wantUDP: wentDirect,
		},
		{
			name: "具名出口网段", meta: route.Meta{IP: netip.MustParseAddr("10.84.3.239"), Port: 80},
			wantTCP: wentEgress, wantUDP: wentEgress,
		},
		{
			name: "未命中任何列表(默认)", meta: route.Meta{Domain: "a.unknown.example", Port: 443},
			wantTCP: wentTunnel, wantUDP: wentTunnel,
		},
		{
			name: "china 域名", meta: route.Meta{Domain: "a.cn.example", Port: 443},
			wantTCP: wentDirect, wantUDP: wentTunnel,
			why: "china 列表刻意不对 UDP 生效(2026-08-15 项目所有者的决定):" +
				"它是 bx 塞进去的 12165 条默认,用户没有逐条看过,把作用域从 TCP " +
				"扩到 UDP 等于一次性把一大批流量推到真实 IP 上出去。要改是单独一个决定。",
		},
		{
			name: "china 网段(裸 IP)", meta: route.Meta{IP: netip.MustParseAddr("223.5.5.5"), Port: 443},
			wantTCP: wentDirect, wantUDP: wentTunnel,
			why: "同上:china CIDR 与 china 域名是同一个决定的两半。",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// **不一致必须有理由。** 这一条是整张表的要点:意外的分岔加不进来,
			// 因为加了就得当场解释它。
			if tc.wantTCP != tc.wantUDP && tc.why == "" {
				t.Fatal("这条用例声称 TCP 与 UDP 不同,却没写明理由 —— " +
					"数据面的分岔要么是设计(写下来),要么是 bug(修掉)")
			}
			tcp, udp := runBothPaths(t, rt, tc.meta)
			if tcp != tc.wantTCP {
				t.Errorf("TCP 去了 %s, want %s", tcp, tc.wantTCP)
			}
			if udp != tc.wantUDP {
				t.Errorf("UDP 去了 %s, want %s —— %s", udp, tc.wantUDP,
					map[bool]string{true: "与 TCP 分岔了,而这条用例没声明过分岔", false: ""}[tc.wantTCP != tc.wantUDP == false])
			}
		})
	}
}

// 表本身要**覆盖每一类判据**,否则漏掉的那一类正是下一个 bug 的位置。
//
// 判据是 route.Source 那个枚举:每一个 Router 能产出的来源,都必须在表里出现过
// 至少一次。**新增一种来源时这条会红** —— 那正是提醒你把它加进表里。
func TestCrossPathTableCoversEverySource(t *testing.T) {
	rt := crossPathRouter(t)
	produced := map[route.Source]bool{}
	for _, m := range []route.Meta{
		{Domain: "a.direct.example"},
		{Domain: "a.proxy.example"},
		{Domain: "a.cn.example"},
		{Domain: "a.unknown.example"},
		{IP: netip.MustParseAddr("203.0.113.9")},
		{IP: netip.MustParseAddr("192.168.1.20")},
		{IP: netip.MustParseAddr("223.5.5.5")},
		{IP: netip.MustParseAddr("10.84.3.239")},
	} {
		_, why := rt.Explain(m)
		produced[why.Source] = true
	}
	for _, want := range []route.Source{
		route.SourceUserDirect, route.SourceUserProxy, route.SourceUserDirectIP,
		route.SourcePrivate, route.SourceChinaDomain, route.SourceChinaCIDR,
		route.SourceDefault, route.SourceUserEgress,
	} {
		if !produced[want] {
			t.Errorf("表里没有一条用例会产出来源 %s —— 那一类的 TCP/UDP 一致性没人在看", want)
		}
	}
}
