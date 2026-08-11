package leakcheck

import (
	"strings"
	"testing"
)

func TestDescribeInterface(t *testing.T) {
	for _, tc := range []struct {
		name     string
		iface    string
		services []VPNService
		bxTun    string
		// bxTunUnknown 置真 = 「问不出 bx 占着哪个 utun」。默认(零值 false)
		// 表示问出来了 —— 于是既有用例一个字都不用改,而新增的那条不确定用例
		// 必须显式表态。
		bxTunUnknown bool
		want         string
	}{
		{
			// **问不出 bx 占着哪个 utun 时,一个都不许归属。**
			//
			// Guardian 不应答时 bxTun 是空的,而这台机器上恰好只有一个 VPN 连着 ——
			// 若按「空就当 bx 没有 TUN」处理,bx **自己的** utun 会被贴上别人的名字。
			// 一个猜测被当成事实报出去,正是「『无法识别』是合法答案,猜不是」禁止的。
			name:  "问不出 bx 的 TUN 时不许把它认成别人",
			iface: "utun11", bxTunUnknown: true,
			services: []VPNService{{Name: "Work VPN", Connected: true}},
			want:     "Unidentified VPN (utun11)",
		},
		{
			// 反过来:**确知** bx 没有 TUN(保护关着)时,归属别的 VPN 正是主用途。
			name:  "确知 bx 没有 TUN 时,照常认出别的 VPN",
			iface: "utun4", bxTun: "",
			services: []VPNService{{Name: "Work VPN", Connected: true}},
			want:     "Work VPN (utun4)",
		},
		{
			name:  "bx 自己的 TUN 认得出来",
			iface: "utun11", bxTun: "utun11",
			want: "bx (utun11)",
		},
		{
			// **唯一一个**已连接的系统集成 VPN,而接口是 utun:可以归因。
			name:  "唯一一个已连接的系统 VPN",
			iface: "utun4",
			services: []VPNService{
				{Name: "Work VPN", Connected: true},
				{Name: "Old VPN", Connected: false},
			},
			want: "Work VPN (utun4)",
		},
		{
			// 两个都连着 —— 哪个占着 utun4 无从判断。**猜不是答案。**
			name:  "两个已连接就不许猜",
			iface: "utun4",
			services: []VPNService{
				{Name: "Work VPN", Connected: true},
				{Name: "Home VPN", Connected: true},
			},
			want: "Unidentified VPN (utun4)",
		},
		{
			name:  "utun 但一个系统 VPN 都没连",
			iface: "utun4",
			want:  "Unidentified VPN (utun4)",
		},
		{
			// 物理网卡不是 VPN。把 en0 说成「无法识别的 VPN」是另一种撒谎。
			name:     "物理网卡",
			iface:    "en0",
			services: []VPNService{{Name: "Work VPN", Connected: true}},
			want:     "en0",
		},
		{
			name:  "接口名为空",
			iface: "",
			want:  "not observed",
		},
		{
			// bxTun 匹配优先于系统 VPN 归因:bx 自己那条是确知的。
			name:  "bx TUN 优先",
			iface: "utun11", bxTun: "utun11",
			services: []VPNService{{Name: "Work VPN", Connected: true}},
			want:     "bx (utun11)",
		},
		{
			// scutil 给回来一条名字是空白的服务时,它不能算「一条已连接的 VPN」——
			// 否则归因会输出 " (utun4)",一个看起来像名字的空洞。
			name:  "已连接的服务名字是空白",
			iface: "utun4",
			services: []VPNService{
				{Name: "   ", Connected: true},
				{Name: "Work VPN", Connected: true},
			},
			want: "Work VPN (utun4)",
		},
		{
			// 只有空白名字的服务连着 = 一条能归因的都没有。
			name:     "唯一已连接的服务名字是空白",
			iface:    "utun4",
			services: []VPNService{{Name: "\t", Connected: true}},
			want:     "Unidentified VPN (utun4)",
		},
		{
			// 采集侧把接口名连空白一起送回来时,归因不能因此错过 bx 自己那条。
			name:  "接口名带空白",
			iface: "  utun11  ", bxTun: "utun11",
			want: "bx (utun11)",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeInterface(tc.iface, tc.services, tc.bxTun, !tc.bxTunUnknown); got != tc.want {
				t.Fatalf("DescribeInterface(%q, %+v, %q) = %q,want %q",
					tc.iface, tc.services, tc.bxTun, got, tc.want)
			}
		})
	}
}

// 证据清单必须把两半的原始事实都列出来 —— 包括那些**不冒充结论**的项
// (出口地址、默认路由归属、User-Agent)。它们是「小白看结论、老鸟展开」的落点。
func TestEvidenceListsBothHalves(t *testing.T) {
	rep := Judge(fixedTime(),
		BrowserReport{
			UserAgent: "TestBrowser/1.0",
			ExitV4:    "5.6.7.8",
			ExitV6:    "2001:db8:1::1",
			SRFLX:     []string{"5.6.7.8"},
		},
		LocalFacts{
			DefaultRouteV4: InterfaceRef{Name: "utun4", Display: "Work VPN (utun4)"},
			DefaultRouteV6: InterfaceRef{Name: "utun4", Display: "Work VPN (utun4)"},
			DNSServers:     []string{"127.0.0.1"},
			BXProtection:   "off",
		})
	joined := strings.Join(rep.Evidence, "\n")
	for _, want := range []string{
		"5.6.7.8",          // 浏览器那半:v4 出口
		"2001:db8:1::1",    // 浏览器那半:v6 出口
		"Work VPN (utun4)", // 本机那半:默认路由归谁,翻成人话
		"127.0.0.1",        // 本机那半:解析器
		"TestBrowser/1.0",  // 谁在报告
		"off",              // bx 自己怎么看自己
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("证据清单缺 %q,得到:\n%s", want, joined)
		}
	}
}

// 什么都没观测到时,证据清单不能编:每一项都要如实说「没观测到」。
func TestEvidenceSaysNotObservedWhenBlind(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{}, LocalFacts{})
	joined := strings.Join(rep.Evidence, "\n")
	if joined == "" {
		t.Fatal("即使什么都没观测到,证据清单也要在场并如实说明,不能是空的")
	}
	if strings.Contains(joined, "5.6.7.8") {
		t.Fatal("证据清单里出现了不存在的观测值")
	}
	if !strings.Contains(joined, "not observed") {
		t.Fatalf("全盲时证据清单必须逐项说 not observed,得到:\n%s", joined)
	}
	// **逐项**,不是「至少有一项」。上面那句实测挡不住 `value` 把没观测到的值
	// 渲染成空串:两条默认路由走的是 describeRef,它照样输出 "not observed",
	// 于是整个断言被一条**别的**代码路径满足 —— 变异 `value` 的 default 分支时
	// 一条测试都不转红(实测)。空白会被读成「没这回事」,而不是「没问过」。
	for _, line := range rep.Evidence {
		if !strings.Contains(line, "not observed") {
			t.Errorf("全盲时这一行没有说 not observed:%q", line)
		}
	}
}

// **两条默认路由必须各占一行。** 上面那条测试的 fixture 里 v4 与 v6 恰好是同一个
// 接口,于是删掉 v6 那一行它照样绿(实测如此)—— 一项证据可以静默消失而没有任何
// 断言拦得住。这条用两个不同的接口把「两条都在」钉死。
func TestEvidenceListsBothDefaultRoutesSeparately(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{},
		LocalFacts{
			DefaultRouteV4: InterfaceRef{Name: "utun4", Display: "Work VPN (utun4)"},
			DefaultRouteV6: InterfaceRef{Name: "en0", Display: "en0"},
		})
	var v4Line, v6Line bool
	for _, line := range rep.Evidence {
		if strings.HasPrefix(line, "default route (v4): ") && strings.Contains(line, "Work VPN (utun4)") {
			v4Line = true
		}
		if strings.HasPrefix(line, "default route (v6): ") && strings.Contains(line, "en0") {
			v6Line = true
		}
	}
	if !v4Line || !v6Line {
		t.Fatalf("两条默认路由必须各占一行(v4=%v v6=%v),得到:\n%s",
			v4Line, v6Line, strings.Join(rep.Evidence, "\n"))
	}
}

// bx 自己的 TUN 名字必须在证据里。它是**归因的依据本身**:用户看到
// "bx (utun11)" 时,得能在证据里核对 bx 报的接口确实是 utun11 ——
// 否则那条归因就成了一句无从复核的断言。
//
// (它也是 Task 3 报告点名的四个「定义了没人读」字段里的最后一个。)
func TestEvidenceCarriesBXTunInterface(t *testing.T) {
	rep := Judge(fixedTime(), BrowserReport{},
		LocalFacts{BXTunInterface: "utun11", BXProtection: "on"})
	joined := strings.Join(rep.Evidence, "\n")
	if !strings.Contains(joined, "utun11") {
		t.Fatalf("证据清单必须带上 bx 自己的 TUN 接口名,得到:\n%s", joined)
	}
}

// **「有没有隧道」是 IPv6 那条指控的前半句。**
//
// 判据故意偏向「认成隧道」:漏认一条真隧道的代价是把一条真泄漏说成「查不了」,
// 而多认一个的代价只是在一台两个地址族走不同接口的机器上少说一句指控。
// 物理网卡与 loopback 必须**认不出来** —— 认出来这条前半句就又白设了。
func TestIsTunnelInterface(t *testing.T) {
	for _, name := range []string{
		"utun0", "utun11", // bx / Tailscale / WireGuard / 系统 IKEv2
		"ipsec0",       // 系统集成 IPSec
		"ppp0",         // L2TP / PPTP
		"tun0", "tap0", // tuntaposx 那一系
		"  utun4  ", // 前后空白不该改变判定
	} {
		if !IsTunnelInterface(name) {
			t.Errorf("%q 是一条隧道接口 —— 认不出来,一条真 v6 泄漏就会被说成「查不了」", name)
		}
	}
	for _, name := range []string{
		"en0", "en1", "bridge0", "lo0", "awdl0", "llw0", "anpi0", "",
	} {
		if IsTunnelInterface(name) {
			t.Errorf("%q 不是隧道 —— 认成隧道,一台没开 VPN 的双宿机器就会被指控"+
				"「IPv6 绕过了隧道」,而它根本没有隧道", name)
		}
	}
}
