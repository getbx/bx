package observe

import (
	"context"
	"errors"
	"testing"
	"time"
)

// **「bx 自己的直连出得去吗」是这一层该问、而此前没问的问题。**
//
// 同一个故障今天在真机上出现了两次,两次都是:保护状态 Protected、劫持生效、
// 屏障正常、隧道健康 —— 而 bx 自己的直连一条都出不去(`IP_BOUND_IF` 找不到
// scoped 路由),所有用户 direct 规则 100% 失败。**观测层四项全绿,它答不出这件事。**
//
// 判据是只读的:问内核「作用域到物理网卡时,一个公网地址有没有路由」。
// 不发包、不拨号 —— 与本包其余观测同一性质。
func TestDirectEgressIsObservedAndReported(t *testing.T) {
	base := func(egress func(context.Context) (Tristate, error)) Deps {
		return Deps{
			TunName:      func() (string, error) { return "utun0", nil },
			BarrierCIDRs: func() ([]string, []string) { return nil, nil },
			LookupRoute: func(context.Context, string, bool) (RouteResult, error) {
				return RouteResult{Interface: "utun0"}, nil
			},
			InspectDNS:   func(context.Context) (DNSResult, error) { return DNSResult{Enabled: true}, nil },
			FetchRuntime: func() (RuntimeResult, error) { return RuntimeResult{TunnelHealthy: true}, nil },
			Now:          func() time.Time { return time.Unix(0, 0) },
			DirectEgress: egress,
		}
	}
	for _, tc := range []struct {
		name string
		fn   func(context.Context) (Tristate, error)
		want Tristate
	}{
		{"出得去", func(context.Context) (Tristate, error) { return True, nil }, True},
		{"出不去", func(context.Context) (Tristate, error) { return False, nil }, False},
		{"问不出来", func(context.Context) (Tristate, error) { return Unknown, errors.New("route: not in table") }, Unknown},
		{"没接线", nil, Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Observe(context.Background(), base(tc.fn))
			if got.DirectEgressOK != tc.want {
				t.Fatalf("direct_egress = %v, want %v", got.DirectEgressOK, tc.want)
			}
		})
	}
}

// 问不出来时要进 UnobservableItems —— 「没问出来」与「答案是没有」必须分得开。
func TestDirectEgressUnknownIsReportedAsUnobservable(t *testing.T) {
	state := ObservedState{DirectEgressOK: Unknown}
	found := false
	for _, item := range state.UnobservableItems() {
		if item == "direct_egress" {
			found = true
		}
	}
	if !found {
		t.Fatal("问不出来却没进 UnobservableItems")
	}
}

// **保护开着而直连出不去,必须产出一条自解释的 divergence。**
//
// 这正是今天真机上那个「四项全绿而直连全死」的场景:少了这一条,观测层
// 对它一个字都说不出来。
func TestProtectedButDirectEgressBrokenDiverges(t *testing.T) {
	observed := ObservedState{
		CaptureOK: True, BarrierPresent: False, DNSManaged: True,
		CoreSocket: True, TunnelHealthy: True, DirectEgressOK: False,
	}
	diffs := Diverge(Intent{Desired: "on"}, observed, Believed{Protection: "protected"})
	var hit string
	for _, d := range diffs {
		if d.Field == "direct_egress" {
			hit = d.Note
		}
	}
	if hit == "" {
		t.Fatalf("直连出不去却没有 divergence:%+v", diffs)
	}
	// 结论必须说清**后果**,而不只是报一个字段名 —— 用户看到「direct_egress=false」
	// 不知道那意味着什么。
	for _, want := range []string{"direct rule", "隧道"} {
		if !containsFold(hit, want) {
			t.Errorf("divergence 说不清后果(缺 %q):%q", want, hit)
		}
	}
}

// 一切正常时不许说话 —— 与本包其余观测同一条纪律。
func TestHealthyDirectEgressIsSilent(t *testing.T) {
	observed := ObservedState{
		CaptureOK: True, BarrierPresent: False, DNSManaged: True,
		CoreSocket: True, TunnelHealthy: True, DirectEgressOK: True,
	}
	for _, d := range Diverge(Intent{Desired: "on"}, observed, Believed{Protection: "protected"}) {
		if d.Field == "direct_egress" {
			t.Fatalf("健康时也在说话:%+v", d)
		}
	}
}

// 非 darwin 上这一项**不适用**(那里没有 IP_BOUND_IF 这套 scoped 路由语义),
// 不该每次观测都吐一条「无法观测」。
func TestDirectEgressIsNotApplicableOffDarwin(t *testing.T) {
	for _, goos := range []string{"linux", "windows"} {
		found := false
		for _, item := range NotApplicableForPlatform(goos) {
			if item == "direct_egress" {
				found = true
			}
		}
		if !found {
			t.Errorf("%s 上 direct_egress 没被声明为不适用", goos)
		}
	}
	for _, item := range NotApplicableForPlatform("darwin") {
		if item == "direct_egress" {
			t.Error("darwin 上 direct_egress 被声明成不适用了 —— 那正是它要守的平台")
		}
	}
}

func containsFold(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (indexFold(haystack, needle) >= 0)
}

func indexFold(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := 0; j < len(sub); j++ {
			a, b := s[i+j], sub[j]
			if 'A' <= a && a <= 'Z' {
				a += 'a' - 'A'
			}
			if 'A' <= b && b <= 'Z' {
				b += 'a' - 'A'
			}
			if a != b {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}
