package leakserve

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/tristate"
)

func fixedNow() time.Time { return time.Date(2026, 8, 11, 10, 30, 0, 0, time.UTC) }

// 全部依赖缺席(非 darwin 上就是这个形状)时,采到的必须是「什么都不知道」,
// 而不是一堆零值冒充的事实。IPv6DefaultPresent 必须是 Unknown,不是 False ——
// 后者会让 Judge 说出一句「这台机器没有 IPv6,不可能漏」的假话。
func TestCollectFactsWithNoDepsIsBlind(t *testing.T) {
	facts := CollectFacts(context.Background(), FactDeps{})
	if facts.DefaultRouteV4.Known() || facts.DefaultRouteV6.Known() {
		t.Fatalf("依赖缺席时不该有任何已知接口:%+v", facts)
	}
	// 「没有这个能力」必须留下原因,不能压成「查了,没有接口」。
	if facts.DefaultRouteV4.Err == "" || facts.DefaultRouteV6.Err == "" {
		t.Fatalf("依赖缺席时两条路由都要留下「没问过」的原因:%+v", facts)
	}
	if facts.IPv6DefaultPresent != tristate.Unknown {
		t.Fatalf("依赖缺席时 IPv6DefaultPresent 必须是 Unknown,得到 %v", facts.IPv6DefaultPresent)
	}
	if len(facts.DNSServers) != 0 {
		t.Fatalf("依赖缺席时不该有解析器:%v", facts.DNSServers)
	}
	// 而且送进 Judge 之后不许有任何 ok。
	for _, f := range leakcheck.Judge(fixedNow(), leakcheck.BrowserReport{}, facts).Findings {
		if f.Verdict == leakcheck.OK {
			t.Errorf("全盲的事实不许判出 ok:%s = %q", f.ID, f.Summary)
		}
	}
}

func TestCollectFactsHappyPath(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(_ context.Context, dest string, ipv6 bool) (string, error) {
			switch {
			case ipv6:
				return "utun4", nil
			case dest == "192.0.2.53":
				return "en0", nil
			default:
				return "utun4", nil
			}
		},
		InspectDNS: func(context.Context) ([]string, error) {
			return []string{"192.0.2.53"}, nil
		},
		GuardianStatus: func(context.Context) (string, string, error) {
			return "utun11", "off", nil
		},
		ListVPNServices: func(context.Context) ([]leakcheck.VPNService, error) {
			return []leakcheck.VPNService{{Name: "Work VPN", Connected: true}}, nil
		},
	}
	facts := CollectFacts(context.Background(), deps)

	if facts.DefaultRouteV4.Name != "utun4" {
		t.Errorf("v4 默认路由应是 utun4,得到 %+v", facts.DefaultRouteV4)
	}
	// **翻成人话必须在采集时就做好**:Judge 是纯函数,它拿不到 scutil。
	if facts.DefaultRouteV4.Display != "Work VPN (utun4)" {
		t.Errorf("v4 接口应翻成 %q,得到 %q", "Work VPN (utun4)", facts.DefaultRouteV4.Display)
	}
	if facts.IPv6DefaultPresent != tristate.True {
		t.Errorf("查到了 v6 默认路由,应为 True,得到 %v", facts.IPv6DefaultPresent)
	}
	if facts.DNSServerEgress["192.0.2.53"] != "en0" {
		t.Errorf("解析器出口应是 en0,得到 %v", facts.DNSServerEgress)
	}
	if facts.BXTunInterface != "utun11" || facts.BXProtection != "off" {
		t.Errorf("Guardian 那一半没接上:%+v", facts)
	}
}

// **单项失败不许连累别人,也不许伪装成事实**(与 internal/observe 同一条纪律)。
func TestCollectFactsPartialFailuresStayIsolated(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(_ context.Context, _ string, ipv6 bool) (string, error) {
			if ipv6 {
				return "", errors.New("no route to host")
			}
			return "utun4", nil
		},
		InspectDNS: func(context.Context) ([]string, error) {
			return nil, errors.New("networksetup failed")
		},
		GuardianStatus: func(context.Context) (string, string, error) {
			return "", "", errors.New("dial guardian: no such file")
		},
		ListVPNServices: func(context.Context) ([]leakcheck.VPNService, error) {
			return nil, errors.New("scutil failed")
		},
	}
	facts := CollectFacts(context.Background(), deps)

	if !facts.DefaultRouteV4.Known() {
		t.Error("v4 那条明明查到了,不该被别人的失败连累")
	}
	// v6 查失败 ⇒ 不是「没有 v6 默认路由」,是「没问出来」。
	if facts.IPv6DefaultPresent != tristate.Unknown {
		t.Errorf("v6 查询失败时必须是 Unknown,得到 %v —— False 会让 Judge 说出"+
			"「这台机器没有 IPv6」这句假话", facts.IPv6DefaultPresent)
	}
	if facts.DefaultRouteV6.Err == "" {
		t.Error("v6 那条要留下失败原因")
	}
	if facts.DNSErr == "" {
		t.Error("DNS 失败要留下原因,否则用户不知道是没配还是没问出来")
	}
	// scutil 失败只是翻不成人话,接口名照样在。
	if facts.DefaultRouteV4.Name != "utun4" {
		t.Error("scutil 失败不该抹掉接口名")
	}
	if facts.DefaultRouteV4.Display != "Unidentified VPN (utun4)" {
		t.Errorf("翻不出来时必须说翻不出,得到 %q", facts.DefaultRouteV4.Display)
	}
}

// **「路由查不到」与「查询本身失败」必须分开。** 前者(比如根本没有 v6 默认
// 路由)是一个确定的观测,能支撑一句诚实的 ok;后者不能。
func TestNoV6RouteIsObservedAbsenceNotFailure(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(_ context.Context, _ string, ipv6 bool) (string, error) {
			if ipv6 {
				return "", ErrNoRoute
			}
			return "utun4", nil
		},
	}
	facts := CollectFacts(context.Background(), deps)
	if facts.IPv6DefaultPresent != tristate.False {
		t.Fatalf("确知没有 v6 默认路由时应为 False(它支撑一句诚实的 ok),得到 %v",
			facts.IPv6DefaultPresent)
	}
}

// **解析器的出口必须逐个真查,查不出来的那个键必须缺席。**
//
// 这条是整条 DNS 规则的承重墙:judgeDNS 用「这个解析器的出口 ≠ 默认路由」判 bad,
// 而它读的就是这张表。如果采集这一层遇到查不出来就填个空串(或者干脆填默认路由
// 的接口名冒充),那条规则就从「检测」退化成「装饰」——它会对一台真在漏 DNS 的
// 机器说没问题。
func TestDNSEgressIsResolvedPerResolverAndOmittedWhenUnknown(t *testing.T) {
	var asked []string
	deps := FactDeps{
		LookupRoute: func(_ context.Context, dest string, ipv6 bool) (string, error) {
			switch dest {
			case probeV4:
				return "utun4", nil
			case "192.0.2.53":
				return "en0", nil
			case "192.0.2.54":
				return "", errors.New("route lookup failed")
			case "2001:db8::53":
				if !ipv6 {
					t.Errorf("v6 解析器 %q 必须按 v6 去查,而不是按 v4", dest)
				}
				return "utun9", nil
			}
			asked = append(asked, dest)
			return "", ErrNoRoute
		},
		InspectDNS: func(context.Context) ([]string, error) {
			return []string{"192.0.2.53", "192.0.2.54", "2001:db8::53"}, nil
		},
	}
	// 记录每个解析器都被单独问过(判据是「每个解析器的出口」,不是「随便问一次」)。
	deps.LookupRoute = recordingLookup(deps.LookupRoute, &asked)

	facts := CollectFacts(context.Background(), deps)

	if got := facts.DNSServerEgress["192.0.2.53"]; got != "en0" {
		t.Errorf("192.0.2.53 的出口应是 en0,得到 %q", got)
	}
	if got := facts.DNSServerEgress["2001:db8::53"]; got != "utun9" {
		t.Errorf("2001:db8::53 的出口应是 utun9,得到 %q", got)
	}
	if _, present := facts.DNSServerEgress["192.0.2.54"]; present {
		t.Errorf("查不出来的解析器必须**缺席**这张表,而不是带一个空串:%v —— "+
			"空串会被 judgeDNS 读成「查到了,出口是空」", facts.DNSServerEgress)
	}
	for _, server := range []string{"192.0.2.53", "192.0.2.54", "2001:db8::53"} {
		if !contains(asked, server) {
			t.Errorf("解析器 %q 从没被单独查过出口:整条 DNS 规则读的就是这张表", server)
		}
	}

	// 端到端:这份事实送进 Judge 之后必须判成 bad(默认路由在 utun4,而 en0 上
	// 那个解析器在它之外)。采集层敷衍一下,这里就会变成一句好听的假话。
	finding := findingByID(t, leakcheck.Judge(fixedNow(), leakcheck.BrowserReport{}, facts), leakcheck.FindingDNS)
	if finding.Verdict != leakcheck.Bad {
		t.Errorf("解析器在默认路由之外时应判 bad,得到 %v:%s", finding.Verdict, finding.Summary)
	}
}

func recordingLookup(next func(context.Context, string, bool) (string, error), seen *[]string) func(context.Context, string, bool) (string, error) {
	return func(ctx context.Context, dest string, ipv6 bool) (string, error) {
		*seen = append(*seen, dest)
		return next(ctx, dest, ipv6)
	}
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func findingByID(t *testing.T, rep leakcheck.Report, id string) leakcheck.Finding {
	t.Helper()
	for _, f := range rep.Findings {
		if f.ID == id {
			return f
		}
	}
	t.Fatalf("报告里没有 %q", id)
	return leakcheck.Finding{}
}

// **列不出 VPN 时不许编一个名字出来。**
//
// 这条是变异验证逼出来的:计划把这件事交给 TestCollectFactsPartialFailuresStayIsolated,
// 而那条测试里 GuardianStatus **也**失败了 —— 于是 DescribeInterface 走的是
// 「问不出 bx 占着哪个 utun,一律 Unidentified」那一支,服务清单**压根没被读到**。
// 实测:在 ListVPNServices 失败时塞一条 `{Name: "VPN", Connected: true}` 的假数据,
// 整套测试全绿。要让这件事可测,必须让归因真的走到服务清单那一步(Guardian 答得上话)。
//
// 编造出来的 VPN 名字比 "Unidentified" 有害得多:用户会照着它去关一个根本没在占
// 路由的 VPN。
func TestFailedVPNListingNeverFabricatesAName(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(context.Context, string, bool) (string, error) { return "utun4", nil },
		// Guardian 答得上话且说 bx 没有 TUN ⇒ 归因会去读服务清单(这正是
		// 「保护关着,认出别人的 VPN」那条主用途)。
		GuardianStatus: func(context.Context) (string, string, error) { return "", "off", nil },
		ListVPNServices: func(context.Context) ([]leakcheck.VPNService, error) {
			return nil, errors.New("scutil failed")
		},
	}
	facts := CollectFacts(context.Background(), deps)
	if facts.DefaultRouteV4.Display != "Unidentified VPN (utun4)" {
		t.Fatalf("列不出 VPN 时必须说翻不出,得到 %q —— 一个编造的名字会让用户去关一个"+
			"根本没在占路由的 VPN", facts.DefaultRouteV4.Display)
	}
}

// bx 自己那条 TUN「问出来了」与「问不出来」必须分开传给 DescribeInterface。
//
// 压成一个空串会朝最糟的方向出错:Guardian 不应答时,这台机器上恰好只有一个 VPN
// 连着,bx **自己的** utun 就会被贴上别人的名字 —— 一个猜测被当成事实报出去。
func TestUnreachableGuardianDoesNotLetAnotherVPNClaimTheInterface(t *testing.T) {
	deps := FactDeps{
		LookupRoute: func(context.Context, string, bool) (string, error) { return "utun4", nil },
		GuardianStatus: func(context.Context) (string, string, error) {
			return "", "", errors.New("dial guardian: no such file")
		},
		ListVPNServices: func(context.Context) ([]leakcheck.VPNService, error) {
			return []leakcheck.VPNService{{Name: "Work VPN", Connected: true}}, nil
		},
	}
	facts := CollectFacts(context.Background(), deps)
	if facts.DefaultRouteV4.Display != "Unidentified VPN (utun4)" {
		t.Errorf("问不出 bx 占着哪个 utun 时不许把它认成别人,得到 %q", facts.DefaultRouteV4.Display)
	}

	// 反过来:Guardian 答上了话、并且说 bx 没有 TUN(保护关着),那条 utun 就
	// **确实**是别人的 —— 这正是这个功能的主用途,不许一并保守掉。
	deps.GuardianStatus = func(context.Context) (string, string, error) { return "", "off", nil }
	facts = CollectFacts(context.Background(), deps)
	if facts.DefaultRouteV4.Display != "Work VPN (utun4)" {
		t.Errorf("Guardian 答了话、bx 没有 TUN 时应认出 Work VPN,得到 %q", facts.DefaultRouteV4.Display)
	}
}
