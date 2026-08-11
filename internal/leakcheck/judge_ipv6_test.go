package leakcheck

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/tristate"
)

func ipv6Finding(t *testing.T, browser BrowserReport, local LocalFacts) Finding {
	t.Helper()
	for _, f := range Judge(fixedTime(), browser, local).Findings {
		if f.ID == FindingIPv6 {
			return f
		}
	}
	t.Fatalf("报告里没有 %s", FindingIPv6)
	return Finding{}
}

func TestIPv6Rule(t *testing.T) {
	tunnelV4 := InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"}
	physV6 := InterfaceRef{Name: "en0", Display: "en0"}

	for _, tc := range []struct {
		name        string
		browser     BrowserReport
		local       LocalFacts
		want        Verdict
		wantMention string
	}{
		{
			// 教科书那条:v4 交给某个隧道接口,而 v6 出口是一个公网 v6 地址、
			// 且 v6 默认路由归的是另一个(物理)接口。**两半都用上了。**
			name:    "v4 走隧道而 v6 从物理口漏出去",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: tristate.True,
			},
			want:        Bad,
			wantMention: "2001:db8:1::1",
		},
		{
			// v6 也交给同一个隧道接口:没漏。
			name:    "v6 与 v4 同归一个隧道",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     tunnelV4,
				IPv6DefaultPresent: tristate.True,
			},
			want: OK,
		},
		{
			// 这台机器压根没有 v6:既没有 v6 默认路由,回声也失败。
			// 这是一句**诚实的 ok** —— 两半都观测到了,结论是没有 v6 通路。
			name:    "根本没有 IPv6",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6Err: "load failed"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				IPv6DefaultPresent: tristate.False,
			},
			want:        OK,
			wantMention: "no IPv6",
		},
		{
			// 回声失败,但本机确实**有** v6 默认路由 —— 那就是没问出来,不是没漏。
			name:    "有 v6 默认路由但回声失败",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6Err: "load failed"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: tristate.True,
			},
			want:        NotChecked,
			wantMention: "load failed",
		},
		{
			// **fake-IP 的那个坑**:v6-only 主机名被答了 A 记录,回声返回的是
			// 一个 IPv4 字面量 —— 浏览器根本没走 v6,判不了。
			//
			// 这个 fixture 是本机实测出来的形状:开着 bx 时
			// `dig ipv6.icanhazip.com` 返回 `198.18.1.7`(一个 bx fake-IP),
			// 请求于是经隧道走 v4 出去,回声看到的源地址就是 v4 出口本身。
			name:    "v6 回声返回了 IPv4 字面量",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "5.6.7.8"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: tristate.True,
			},
			want:        NotChecked,
			wantMention: "not an IPv6 address",
		},
		{
			// 同一个坑的另一种写法:双栈套接字把 v4 源地址报成 v4-mapped v6。
			// `::ffff:5.6.7.8` 在字面上是个 v6 地址,语义上不是 —— 浏览器仍然
			// 没走 v6,判不了。
			name:    "v6 回声返回 v4-mapped v6",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "::ffff:5.6.7.8"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: tristate.True,
			},
			want:        NotChecked,
			wantMention: "not an IPv6 address",
		},
		{
			// 回声端返回的根本不是地址(改版、错误页、被中间设备替换)。
			// 看不懂的答案不是答案。
			name:    "v6 回声返回看不懂的东西",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "<html>error</html>"},
			local: LocalFacts{
				DefaultRouteV4:     tunnelV4,
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: tristate.True,
			},
			want:        NotChecked,
			wantMention: "not an IPv6 address",
		},
		{
			// 本机 v6 观测不出来(tristate.Unknown 是零值)——不许拿它凑结论。
			name:    "本机 v6 事实观测不到",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6Err: "load failed"},
			local:   LocalFacts{DefaultRouteV4: tunnelV4, IPv6DefaultPresent: tristate.Unknown},
			want:    NotChecked,
		},
		{
			// v4 那半不知道归谁:「v4 走 VPN 而 v6 是 ISP」的前半句立不住。
			name:    "v4 默认路由未知",
			browser: BrowserReport{ExitV6: "2001:db8:1::1"},
			local:   LocalFacts{DefaultRouteV6: physV6, IPv6DefaultPresent: tristate.True},
			want:    NotChecked,
		},
		{
			// **同一性必须按接口名判,不能按展示名。** 两个不同接口的 Display
			// 都翻不出来(空串)时,按 Display 比较会把它们判成同一个接口 →
			// 一条真泄漏被说成 ok。这条子用例就是为了钉住那个写法。
			name:    "两个接口都翻不出名字时仍按接口名判",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     InterfaceRef{Name: "utun4"},
				DefaultRouteV6:     InterfaceRef{Name: "en0"},
				IPv6DefaultPresent: tristate.True,
			},
			want:        Bad,
			wantMention: "2001:db8:1::1",
		},
		{
			// **设计的前提是「v4 走 VPN 而 v6 出口是 ISP」,而实现丢了前半句。**
			//
			// 一台**一个 VPN 都没开**的双宿 Mac —— 插着坞站、v4 默认路由在有线
			// 网口(公司网络只有 v4)、v6 默认路由在 Wi-Fi —— 两个接口名不同,
			// 于是旧实现直接喊「IPv6 is bypassing the tunnel」。没有隧道可绕。
			name:    "没有任何隧道的双宿机器不许被指控",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     InterfaceRef{Name: "en0", Display: "en0"},
				DefaultRouteV6:     InterfaceRef{Name: "en1", Display: "en1"},
				IPv6DefaultPresent: tristate.True,
			},
			want:        NotChecked,
			wantMention: "no tunnel",
		},
		{
			// bx 自己占着 v4 默认路由、v6 却从物理口出去:这是真泄漏,
			// 而且是 bx 最该抓到的那一种。上面那条不许把它一并保守掉。
			name:    "bx 占着 v4 而 v6 从物理口漏出去",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     InterfaceRef{Name: "utun11", Display: "bx (utun11)"},
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: tristate.True,
				BXTunInterface:     "utun11",
			},
			want:        Bad,
			wantMention: "bypassing",
		},
		{
			// 系统集成 VPN 走的是 ppp/ipsec 而不是 utun(L2TP、IKEv2)。
			// 判据只认 utun 会漏掉它们 —— 漏认的代价是一条真泄漏被说成「查不了」。
			name:    "ppp 上的 VPN 同样算隧道",
			browser: BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
			local: LocalFacts{
				DefaultRouteV4:     InterfaceRef{Name: "ppp0", Display: "Work VPN (ppp0)"},
				DefaultRouteV6:     physV6,
				IPv6DefaultPresent: tristate.True,
			},
			want:        Bad,
			wantMention: "bypassing",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ipv6Finding(t, tc.browser, tc.local)
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" && !strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// 判成 bad 时,两半的依据都要在:v6 出口地址(浏览器那半)与两条默认路由的
// 归属(本机那半)。少任何一半,用户就看不出这个结论是怎么来的。
func TestIPv6BadFindingCarriesBothHalves(t *testing.T) {
	f := ipv6Finding(t,
		BrowserReport{ExitV4: "5.6.7.8", ExitV6: "2001:db8:1::1"},
		LocalFacts{
			DefaultRouteV4:     InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
			DefaultRouteV6:     InterfaceRef{Name: "en0", Display: "en0"},
			IPv6DefaultPresent: tristate.True,
		})
	joined := strings.Join(f.Evidence, "\n")
	for _, want := range []string{"2001:db8:1::1", "utun4", "en0"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bad 结论的 evidence 缺 %q,得到 %v", want, f.Evidence)
		}
	}
}

// describeRef 的四个分支各有一个可达输入,而且「没观测到」必须说出来 ——
// 它是三条规则共用的渲染器,一旦它把「没观测到」渲染成一个好听的词,
// 三条规则的证据行会同时开始撒谎。
func TestDescribeRef(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  InterfaceRef
		want string
	}{
		{"翻得出人话", InterfaceRef{Name: "utun4", Display: "Work VPN (utun4)"}, "Work VPN (utun4)"},
		{"翻不出就用接口名", InterfaceRef{Name: "en0"}, "en0"},
		{"采集失败要说原因", InterfaceRef{Err: "route: no route to host"}, "not observed (route: no route to host)"},
		{"什么都没有", InterfaceRef{}, "not observed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := describeRef(tc.ref); got != tc.want {
				t.Fatalf("describeRef(%+v) = %q,want %q", tc.ref, got, tc.want)
			}
		})
	}
}
