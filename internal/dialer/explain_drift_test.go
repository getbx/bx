package dialer

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/getbx/bx/internal/route"
)

// **这是这个功能唯一承重的测试。**
//
// `(*Dialer).Explain` 与 `dialInner` 是同一个对象上的两个方法,读同一批字段、
// 调同一个 killswitchBlocks —— 但分支结构是**分别写的两份**,而这个仓库反复
// 栽的正是「两份判据,一个改了另一个没改,两边的测试都绿」。
//
// 手法照抄本仓库既有先例(route.Decide 是 Explain 的薄壳,另有一条测试逐输入
// 比对以防有人拆开):给一张覆盖每条分支的输入表,断言 Explain **预言的**结局
// 与 dialInner **真的做出来的**结局一致。
//
// 一条预言错的 explain 比没有 explain 糟得多:它会让人和 agent 带着一个自信
// 的错答案去改别的东西。

// actualEffective 跑一次真的 dialInner,把结局归约成 Effective。
func actualEffective(t *testing.T, d *Dialer, m route.Meta, proxy, direct *recordDialer) Effective {
	t.Helper()
	proxy.lastAddr, direct.lastAddr = "", ""
	conn, err := d.Dial(context.Background(), m)
	if conn != nil {
		conn.Close()
	}
	switch {
	case errors.Is(err, ErrBlocked):
		return EffectiveBlocked
	case proxy.lastAddr != "":
		return EffectiveTunnel
	case direct.lastAddr != "":
		return EffectiveDirect
	case err != nil:
		// 拨号器没被碰过又报错 = 解析失败一类,不是路由结局。
		t.Fatalf("dialInner 既没拨号也没 Block:%v", err)
	}
	t.Fatal("dialInner 什么都没做")
	return EffectiveUnknown
}

func TestExplainPredictsWhatTheDialerActuallyDoes(t *testing.T) {
	targets := []route.Meta{
		{Domain: "api.openai.com", Port: 443},               // 用户 proxy 规则
		{Domain: "x.baidu.com", Port: 80},                   // china 域名 → 直连
		{Domain: "unlisted.example", Port: 443},             // 没命中 → 默认走代理
		{IP: netip.MustParseAddr("1.2.3.4"), Port: 443},     // china CIDR → 直连
		{IP: netip.MustParseAddr("192.168.1.9"), Port: 445}, // 私网恒直连
		{IP: netip.MustParseAddr("9.9.9.9"), Port: 443},     // 裸 IP 默认走代理
		{Domain: "api.openai.com", Port: 443, UDP: true},    // 用户规则对 UDP 同样生效
		{Domain: "unlisted.example", Port: 443, UDP: true},  // 落到 udp.mode
		{IP: netip.MustParseAddr("192.168.1.9"), Port: 5353, UDP: true},
	}

	for _, healthy := range []bool{true, false} {
		for _, ks := range []bool{true, false} {
			for _, udpMode := range []string{"proxy", "direct-realtime", "block"} {
				for _, m := range targets {
					res := fakeResolver{ip: netip.MustParseAddr("1.2.3.4")}
					d, px, dr := newTestDialer(nil, res, healthy, ks)
					d.UDPMode = udpMode

					predicted := d.Explain(m)
					actual := actualEffective(t, d, m, px, dr)

					if predicted.Effective != actual {
						t.Errorf("healthy=%v ks=%v udp_mode=%s target=%+v:\n  Explain 预言 %s,dialInner 实际 %s",
							healthy, ks, udpMode, m, predicted.Effective, actual)
					}
				}
			}
		}
	}
}

// Explain 绝不许有副作用 —— 尤其不许碰 UDP 的反事实计数。
//
// udpRuleOverride 在不命中用户规则时会调 countUDPCounterfactual;explain 走那条路
// 就等于拿一条**根本没发生**的连接去污染那份计数,而它正是用来决定「让 china
// 列表也对 UDP 生效」那个搁置选项的依据。一份被查询行为污染的计数比没有更糟。
func TestExplainRecordsNothing(t *testing.T) {
	counter := &countingStats{}
	d, _, _ := newTestDialer(nil, fakeResolver{ip: netip.MustParseAddr("1.2.3.4")}, true, true)
	d.Stats = counter
	d.UDPMode = "proxy"

	for i := 0; i < 20; i++ {
		d.Explain(route.Meta{Domain: "unlisted.example", Port: 443, UDP: true})
		d.Explain(route.Meta{Domain: "api.openai.com", Port: 443})
		d.Explain(route.Meta{IP: netip.MustParseAddr("1.2.3.4"), Port: 443})
	}
	if counter.calls != 0 {
		t.Errorf("Explain 记了 %d 笔账 —— 它必须是纯读的", counter.calls)
	}
}

// 没有路由表时必须**说自己不知道**,而不是让零值读起来像一个判定。
func TestExplainSaysSoWhenThereIsNoRouter(t *testing.T) {
	d := &Dialer{}
	out := d.Explain(route.Meta{Domain: "example.com", Port: 443})
	if !out.RouterMissing {
		t.Error("没有 Router 却没报 RouterMissing")
	}
	if out.Effective != EffectiveUnknown {
		t.Errorf("没有 Router 时结局是 %s,应当是 unknown", out.Effective)
	}
}

// 「没有健康探针」既不是健康也不是不健康 —— 但 kill-switch 按不健康处理。
// 压成 unhealthy 会把「传输还没装好」说成「隧道断了」;压成 healthy 是把
// fail-closed 说成 fail-open。
func TestExplainKeepsTunnelHealthUnknownWithoutAProbe(t *testing.T) {
	d, _, _ := newTestDialer(nil, fakeResolver{}, true, true)
	d.SetTransport(&Transport{Proxy: &recordDialer{}}) // 无 Healthy
	out := d.Explain(route.Meta{Domain: "api.openai.com", Port: 443})

	if out.TunnelHealth != TunnelHealthUnknown {
		t.Errorf("没有探针时健康是 %s,应当是 unknown", out.TunnelHealth)
	}
	if out.Effective != EffectiveBlocked || out.BlockedBy != blockedByKillswitch {
		t.Errorf("kill-switch 对没有探针的传输必须 fail-closed,得到 %s/%s", out.Effective, out.BlockedBy)
	}
}

// countingStats 只数「有没有人记账」。
type countingStats struct{ calls int }

func (c *countingStats) Direct()                    { c.calls++ }
func (c *countingStats) Proxy()                     { c.calls++ }
func (c *countingStats) Blocked()                   { c.calls++ }
func (c *countingStats) DirectFailed()              { c.calls++ }
func (c *countingStats) ProxyFailed()               { c.calls++ }
func (c *countingStats) UDPBlocked()                { c.calls++ }
func (c *countingStats) RuleAttempt(_, _ string)    { c.calls++ }
func (c *countingStats) RuleFailure(_, _, _ string) { c.calls++ }
