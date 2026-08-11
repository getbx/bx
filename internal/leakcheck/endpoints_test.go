package leakcheck

import (
	"net/url"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/route"
)

// 这三个地址是**用户可见契约**:页面在联网前照原样显示它们,换掉它们等于换掉
// 用户的暴露对象。所以改这三个串必须先改这条测试,不能顺手改。
func TestEndpointsArePinned(t *testing.T) {
	if EchoV4URL != "https://ipv4.icanhazip.com" {
		t.Errorf("v4 回声端变了:%q", EchoV4URL)
	}
	if EchoV6URL != "https://ipv6.icanhazip.com" {
		t.Errorf("v6 回声端变了:%q", EchoV6URL)
	}
	if STUNURL != "stun:stun.cloudflare.com:3478" {
		t.Errorf("STUN 变了:%q", STUNURL)
	}
	d := Endpoints()
	if d.EchoV4 != EchoV4URL || d.EchoV6 != EchoV6URL || d.STUN != STUNURL {
		t.Fatalf("Endpoints() 必须原样带出三个常量,得到 %+v", d)
	}
}

// v4 与 v6 必须是**两个不同的主机名**。用同一个主机名(比如某个双栈地址)就
// 问不出「v6 出口是谁」,而那正是别的 VPN 最常见的漏洞。
func TestEchoEndpointsAreTwoDistinctHosts(t *testing.T) {
	v4, err := url.Parse(EchoV4URL)
	if err != nil {
		t.Fatal(err)
	}
	v6, err := url.Parse(EchoV6URL)
	if err != nil {
		t.Fatal(err)
	}
	if v4.Hostname() == v6.Hostname() {
		t.Fatalf("v4 与 v6 回声端不能是同一个主机名(%q):双栈主机名问不出 v6 出口", v4.Hostname())
	}
	for _, u := range []*url.URL{v4, v6} {
		if u.Scheme != "https" {
			t.Errorf("%s 必须是 https:明文回声在路上可被改写,判据就整个失效", u)
		}
	}
}

// **这条测试是为一个真实发生过的错误写的。** 项目拿 ifconfig.me 当出口探测,
// 而它本就在直连列表里,于是「漏直连」是自摆乌龙。同一个坑今天还活在
// internal/cli 的 collectNetworkProbe 里(api.ipify.org,而 ipify.org 在列表里)。
//
// 判据用的就是生产环境那一份 DomainSet 与那一份内嵌列表 —— 不是抄一份规则。
func TestEchoEndpointsAreNotOnTheChinaDirectList(t *testing.T) {
	patterns := strings.Split(string(embedded.ChinaDomain()), "\n")
	if len(patterns) < 1000 {
		t.Fatalf("内嵌 china 列表只有 %d 行,本守卫读不懂现在的资产,请连同它一起重写", len(patterns))
	}
	set := route.NewDomainSet(patterns)
	// 自检:列表里确实有东西能命中,否则下面的「没命中」是假绿。
	if !set.Match("ipify.org") {
		t.Fatal("自检失败:ipify.org 本应在 china 直连列表里(第 6045 行)——" +
			"若上游列表变了,请更新这条自检,而不是删掉它")
	}
	for _, raw := range []string{EchoV4URL, EchoV6URL} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		if set.Match(u.Hostname()) {
			t.Errorf("%s 在 china 直连列表里:bx 开着时它会直连,回声报出真实 ISP 出口,"+
				"把一台完全正常的机器判成泄漏(ifconfig.me 那个坑)", u.Hostname())
		}
	}
	// STUN 的主机名同理:它是 UDP,不走 DomainSet,但域名维度的分流规则一样适用。
	host := strings.TrimPrefix(STUNURL, "stun:")
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if set.Match(host) {
		t.Errorf("STUN 主机 %s 在 china 直连列表里,srflx 会报出真实出口", host)
	}
}
