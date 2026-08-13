package leakcheck

import (
	"strings"
	"testing"
)

func carrierFacts(owner, bxTun string, routes ...RouteEntry) LocalFacts {
	return LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: owner},
		BXTunInterface: bxTun,
		InterfaceKinds: map[string]InterfaceKind{
			"utun11": InterfaceTunnel, "utun4": InterfaceTunnel,
			"bx0": InterfaceTunnel, "en0": InterfacePhysical,
		},
		Routes: routes,
	}
}

// **今天没有任何一条结论回答「谁在拿你的流量」,而这是纯本机就答得了的。**
//
// 最要紧的那个情形也没人报:**bx 在跑,但流量被别的隧道拿走了** —— 那正是
// 「status 显绿而流量走别处」的形状,而这个仓库的观测层当初就是为它建的。
func TestCarrierRule(t *testing.T) {
	for _, tc := range []struct {
		name        string
		local       LocalFacts
		want        Verdict
		wantMention string
	}{
		{
			name:  "bx 在管,没有第二个claimant",
			local: carrierFacts("utun11", "utun11", RouteEntry{Destination: "0/1", Interface: "utun11"}),
			want:  OK,
		},
		{
			// **这一条是本次要补的洞。** bx 有 TUN(它在跑),而默认路由归别人 ——
			// 用户以为自己受保护,流量却在走另一条隧道。
			name:        "bx 在跑,却不是它在管",
			local:       carrierFacts("utun4", "utun11", RouteEntry{Destination: "0/1", Interface: "utun4"}),
			want:        Bad,
			wantMention: "utun4",
		},
		{
			// bx 没在跑、别人的 VPN 在管 —— **那不是问题**,那正是「拿 bx 检测
			// 别的 VPN」这个用法的常态。报成 bad 会把整个用法变成一片红。
			name:  "bx 没在跑,别人的 VPN 在管",
			local: carrierFacts("utun4", "", RouteEntry{Destination: "0/1", Interface: "utun4"}),
			want:  Info,
		},
		{
			// bx 有 TUN,而默认路由在物理网卡上 —— 劫持没生效。
			name:        "bx 在跑,而流量根本没进任何隧道",
			local:       carrierFacts("en0", "utun11"),
			want:        Bad,
			wantMention: "en0",
		},
		{
			name:  "什么隧道都没有,bx 也没跑",
			local: carrierFacts("en0", ""),
			want:  Info,
		},
		{
			name:  "归谁管没问出来",
			local: LocalFacts{DefaultRouteV4: InterfaceRef{Err: "route lookup failed"}},
			want:  NotChecked,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := identityFinding(t, FindingCarrier, BrowserReport{}, tc.local)
			if f.Verdict != tc.want {
				t.Fatalf("判定应为 %s,得到 %s(summary=%q)", tc.want, f.Verdict, f.Summary)
			}
			if tc.wantMention != "" &&
				!strings.Contains(f.Summary+strings.Join(f.Evidence, " "), tc.wantMention) {
				t.Errorf("结论必须点名,summary=%q evidence=%v", f.Summary, f.Evidence)
			}
		})
	}
}

// **第二个 claimant 要说出来,但不改判定。**
//
// bx 正在管这条路,而另一条隧道也 claim 了公网空间:此刻流量确实走 bx,所以不是
// 泄漏;但内核是按 metric 挑的,对方重连或改 metric 就可能悄悄接管。这件事值得
// 说,不值得报红 —— 报红会和另外几条「流量确实从 VPS 出去」的结论当面打架。
func TestASecondClaimantIsReportedWithoutChangingTheVerdict(t *testing.T) {
	local := carrierFacts(
		"utun11", "utun11",
		RouteEntry{Destination: "0/1", Interface: "utun11"},
		RouteEntry{Destination: "0/1", Interface: "utun4"},
		RouteEntry{Destination: "128.0/1", Interface: "utun4"},
	)
	f := identityFinding(t, FindingCarrier, BrowserReport{}, local)
	if f.Verdict != OK {
		t.Fatalf("此刻流量确实走 bx,不该报红:%v %q", f.Verdict, f.Summary)
	}
	joined := strings.Join(f.Evidence, " ")
	if !strings.Contains(joined, "utun4") {
		t.Errorf("第二个 claimant 没说出来:%v", f.Evidence)
	}
}

// 这一项**不吃浏览器** —— 纯本机路由观测。
func TestCarrierNeedsNothingFromTheBrowser(t *testing.T) {
	f := identityFinding(t, FindingCarrier, BrowserReport{},
		carrierFacts("utun4", "utun11", RouteEntry{Destination: "0/1", Interface: "utun4"}))
	if f.Verdict != Bad {
		t.Fatalf("判定 = %v,want Bad —— 这一项不需要浏览器", f.Verdict)
	}
}

// **「Guardian 没答上话」不是「bx 没在跑」——而这条结论此前正是这么推的。**
//
// `guardianTunAndProtection` 在「core is running but its TUN name could not be read」
// 那一支返回错误,于是 BXTunInterface 与 BXProtection 都是空的;而 input.go 明写着
// 空的 BXTunInterface「不表示 bx 没在跑」。此前 judge_carrier 用
// `BXTunInterface != ""` 当作「bx 在跑」,于是在那一支上说出
// 「bx is not running, so this is expected」—— 而 bx 明明在跑。
//
// 保护状态答上来了就用它;两个都问不出来才说不知道 —— **而那时不许断言 bx 没在跑**。
func TestCarrierDoesNotClaimBXIsStoppedFromAnEmptyTunName(t *testing.T) {
	// Guardian 说保护开着,但 TUN 名字没读出来。
	local := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun4"},
		BXProtection:   "protected",
		InterfaceKinds: map[string]InterfaceKind{"utun4": InterfaceTunnel},
	}
	f := identityFinding(t, FindingCarrier, BrowserReport{}, local)
	joined := strings.ToLower(f.Summary + " " + strings.Join(f.Evidence, " "))
	if strings.Contains(joined, "not running") {
		t.Errorf("保护状态说开着,却断言 bx 没在跑:%q", f.Summary)
	}
	if f.Verdict == Info {
		t.Errorf("保护开着而流量归别人管,这不是「预期之内」:%v %q", f.Verdict, f.Summary)
	}

	// 两个都问不出来:不许朝任何一边断言。
	silent := LocalFacts{
		DefaultRouteV4: InterfaceRef{Name: "utun4"},
		InterfaceKinds: map[string]InterfaceKind{"utun4": InterfaceTunnel},
	}
	g := identityFinding(t, FindingCarrier, BrowserReport{}, silent)
	if strings.Contains(strings.ToLower(g.Summary), "not running") {
		t.Errorf("什么都没问出来却断言 bx 没在跑:%q", g.Summary)
	}
}

// **「bx 在不在跑」的判据方向,以及它的第三态。**
//
// 上一版是**黑名单**:保护状态不是 ""/off/unknown 就算在跑。三处错:
//
//	① 方向错。真正会走到这个分支的情形只有一种 —— 采集器只在
//	   `Core == nil || !Core.Reachable` 时返回「有保护状态、无 TUN 名」
//	   (Core 可达时读得出 TUN 名;读不出时返回的是 error,两个字段都空)。
//	   也就是说这个分支恰好在 **Core 不可达**时说「bx 在跑」。
//	② 过渡态被当成稳态。starting / recovering 只持续几秒,而这条结论
//	   (「bx 在跑,但流量没走它」)在那几秒里字面为真却毫无意义 —— 它正在起来。
//	③ 未来新增的状态值一律落进「在跑」。黑名单对没见过的值总是选一边,
//	   而这里两边都可能错。
//
// 改成白名单 + 第三态:认得出且意味着「此刻应当在承载流量」才算在跑;
// 过渡态与认不出的值都判 not checked,不硬选一边。
func TestBXRunningVerdictIsAWhitelistWithAThirdState(t *testing.T) {
	for _, tc := range []struct {
		protection string
		want       runningState
	}{
		{"protected", isRunning},
		{"blocked", isRunning}, // kill-switch 生效:此刻更不该有公网流量漏出去
		{"needs_attention", isRunning},
		{"off", notRunning},
		{"", notRunning},
		{"starting", runningUnknown}, // 过渡态:几秒后自己会变
		{"recovering", runningUnknown},
		{"hibernating", runningUnknown}, // 将来新增的值 —— 不许替它选一边
	} {
		t.Run(tc.protection, func(t *testing.T) {
			got := bxRunningVerdict(LocalFacts{BXProtection: tc.protection})
			if got != tc.want {
				t.Errorf("bxRunningVerdict(%q) = %v, want %v", tc.protection, got, tc.want)
			}
		})
	}
	// TUN 名读得出来,那是 Core 可达的直接证据,压过一切状态字符串。
	if got := bxRunningVerdict(LocalFacts{BXTunInterface: "utun11", BXProtection: "starting"}); got != isRunning {
		t.Errorf("读得出 TUN 名却判 %v —— 那是 Core 可达的直接证据", got)
	}
}

// **结论里不许出现空括号。** 走到「有保护状态、无 TUN 名」那条路时,
// 上一版拼出来的是字面的 `bx is running (), but ...` —— 而那正是这个分支
// 唯一会发生的情形,不是边角。
func TestCarrierSummaryNeverShowsEmptyParentheses(t *testing.T) {
	for _, local := range []LocalFacts{
		{BXProtection: "protected", DefaultRouteV4: InterfaceRef{Name: "en0"}},
		{BXProtection: "needs_attention", DefaultRouteV4: InterfaceRef{Name: "utun4"}},
		{BXTunInterface: "utun11", DefaultRouteV4: InterfaceRef{Name: "en0"}},
	} {
		f := identityFinding(t, FindingCarrier, BrowserReport{ExitV4: "203.0.113.9"}, local)
		if strings.Contains(f.Summary, "()") {
			t.Errorf("结论里出现空括号:%q", f.Summary)
		}
	}
}
