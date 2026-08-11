package leakcheck

import (
	"strings"
	"testing"
)

func dnsFinding(t *testing.T, local LocalFacts) Finding {
	t.Helper()
	for _, f := range Judge(fixedTime(), BrowserReport{}, local).Findings {
		if f.ID == FindingDNS {
			return f
		}
	}
	t.Fatalf("报告里没有 %s", FindingDNS)
	return Finding{}
}

func TestDNSRule(t *testing.T) {
	tunnel := InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"}

	for _, tc := range []struct {
		name        string
		local       LocalFacts
		want        Verdict
		wantMention string
	}{
		{
			// 解析器的包从 en0 出去,而默认路由归 utun4:DNS 可能绕过隧道。
			name: "解析器走在默认路由之外",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"192.0.2.53"},
				DNSServerEgress: map[string]string{"192.0.2.53": "en0"},
			},
			want:        Bad,
			wantMention: "192.0.2.53",
		},
		{
			// **这条就是「什么真实输入能让它变 bad」的答案。** 一个只装路由、
			// 不改系统 DNS 的第三方 VPN(wg-quick 的 `DNS =` 是可选的;
			// Tailscale exit node 关掉 MagicDNS 也是这个形状):默认路由归 utun3,
			// 而解析器还是局域网路由器,查询明文交给它 —— 教科书式的 DNS 泄漏。
			name: "第三方 VPN 只装路由不改 DNS(局域网解析器)",
			local: LocalFacts{
				DefaultRouteV4:  InterfaceRef{Name: "utun3", Display: "Unidentified VPN (utun3)"},
				DNSServers:      []string{"192.168.1.1"},
				DNSServerEgress: map[string]string{"192.168.1.1": "en0"},
			},
			want:        Bad,
			wantMention: "192.168.1.1",
		},
		{
			name: "解析器与默认路由同一个接口",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"192.0.2.53"},
				DNSServerEgress: map[string]string{"192.0.2.53": "utun4"},
			},
			want: OK,
		},
		{
			// bx 自己接管 DNS 时解析器就是 127.0.0.1,它当然走 lo0。
			// 这不是绕过,是设计如此 —— 恒把 loopback 判成 bad 会让每台开着
			// bx 的机器常年红灯。(本机 /etc/resolv.conf 实测就是这个形状。)
			name: "解析器是本机 loopback",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"127.0.0.1"},
				DNSServerEgress: map[string]string{"127.0.0.1": "lo0"},
			},
			want:        OK,
			wantMention: "127.0.0.1",
		},
		{
			// macOS 把本机解析器列成 127.0.0.1 与 ::1 两条是常态。
			// v6 那条的 loopback 判定不能漏。
			name: "loopback 的 v6 写法",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"::1"},
				DNSServerEgress: map[string]string{"::1": "lo0"},
			},
			want: OK,
		},
		{
			// 多个解析器,只要有一个在外面就要报 —— 一条泄漏通路就够了。
			name: "多个解析器里有一个在外面",
			local: LocalFacts{
				DefaultRouteV4: tunnel,
				DNSServers:     []string{"127.0.0.1", "192.0.2.53"},
				DNSServerEgress: map[string]string{
					"127.0.0.1":  "lo0",
					"192.0.2.53": "en0",
				},
			},
			want:        Bad,
			wantMention: "192.0.2.53",
		},
		{
			// 解析器不是一个认得出的 IP(采集侧拿到主机名、带端口、或被截断)。
			// **看不懂的地址不能当成安全的那一边** —— 它照样在默认路由之外。
			name: "解析器地址认不出来但确实在外面",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"dns.corp.internal"},
				DNSServerEgress: map[string]string{"dns.corp.internal": "en0"},
			},
			want:        Bad,
			wantMention: "dns.corp.internal",
		},
		{
			name:  "DNS 采集失败",
			local: LocalFacts{DefaultRouteV4: tunnel, DNSErr: "networksetup failed"},
			want:  NotChecked, wantMention: "networksetup failed",
		},
		{
			// 有解析器,但它的出口接口没查出来:少一个键 = 没问出来。
			name: "解析器出口未知",
			local: LocalFacts{
				DefaultRouteV4:  tunnel,
				DNSServers:      []string{"192.0.2.53"},
				DNSServerEgress: nil,
			},
			want: NotChecked,
		},
		{
			name:  "默认路由未知",
			local: LocalFacts{DNSServers: []string{"192.0.2.53"}, DNSServerEgress: map[string]string{"192.0.2.53": "en0"}},
			want:  NotChecked,
		},
		{
			// 一个解析器都没有:系统没配。这是没问出来,不是没问题。
			name:  "没有解析器",
			local: LocalFacts{DefaultRouteV4: tunnel},
			want:  NotChecked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := dnsFinding(t, tc.local)
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" && !strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须出示依据 %q,summary=%q evidence=%v", tc.wantMention, f.Summary, f.Evidence)
			}
		})
	}
}

// 这条结论是「**可能**绕过」,不是确定的泄漏。措辞必须谨慎:一条把可能说成
// 确定的结论,会让用户去追一个不存在的问题,然后学会不信这个界面。
func TestDNSBadFindingIsWordedAsPossibility(t *testing.T) {
	f := dnsFinding(t, LocalFacts{
		DefaultRouteV4:  InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
		DNSServers:      []string{"192.0.2.53"},
		DNSServerEgress: map[string]string{"192.0.2.53": "en0"},
	})
	if !strings.Contains(strings.ToLower(f.Summary), "may bypass") {
		t.Fatalf("DNS 的 bad 结论必须写成「可能绕过」,得到 %q", f.Summary)
	}
}

// 判成 bad 时要出示这个结论是怎么来的:哪个解析器、它从哪个接口出去、
// 默认路由归谁。三样缺一样,用户就没法自己复核 bx 判得对不对。
func TestDNSBadFindingShowsEgressAndRoute(t *testing.T) {
	f := dnsFinding(t, LocalFacts{
		DefaultRouteV4:  InterfaceRef{Name: "utun4", Display: "Unidentified VPN (utun4)"},
		DNSServers:      []string{"192.0.2.53"},
		DNSServerEgress: map[string]string{"192.0.2.53": "en0"},
	})
	joined := strings.Join(f.Evidence, "\n")
	for _, want := range []string{"192.0.2.53", "en0", "Unidentified VPN (utun4)"} {
		if !strings.Contains(joined, want) {
			t.Errorf("bad 结论的 evidence 缺 %q,得到 %v", want, f.Evidence)
		}
	}
}
