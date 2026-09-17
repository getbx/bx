package cli

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialfail"
	"github.com/getbx/bx/internal/pathview"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
)

// `bx explain` 存在的全部理由是回答**请求级的「为什么」**。三次真实排查
// (Steam 图片全裂 / 腾讯会议绕一圈 / *.qq.com 57% 失败)里,决定性的那句话
// 从来不是「判定是 DIRECT」,而是「**是这一行规则**决定的,而它 1291 次里
// 失败了 1289 次」。渲染层把哪一半丢了,这个命令就白做。

func explainFixture() supervisor.ExplainResponse {
	return supervisor.ExplainResponse{
		Target:       "steamstatic.com",
		Domain:       "steamstatic.com",
		TunnelHealth: "healthy",
		TCP: supervisor.ExplainPath{
			Effective: "direct", Decision: "direct",
			Source: "user_direct", Rule: "*.steamstatic.com",
			Run:     &stats.RuleOutcome{Source: "user_direct", Rule: "*.steamstatic.com", Attempts: 1291, Failures: 1289},
			History: &stats.RuleOutcome{Source: "user_direct", Rule: "*.steamstatic.com", Attempts: 8113, Failures: 8000},
		},
		UDP: supervisor.ExplainPath{
			Effective: "direct", Decision: "direct",
			Source: "user_direct", Rule: "*.steamstatic.com",
		},
		UDPTransportHealth: "unknown",
	}
}

// 规则**原文**必须出现 —— 内部把 `*.a.com` 存成 `a.com`,报归一化形式会让
// 用户去搜一个在自己配置里搜不到的串。
func TestExplainNamesTheRuleVerbatim(t *testing.T) {
	got := renderExplain(explainFixture())
	if !strings.Contains(got, "*.steamstatic.com") {
		t.Errorf("没有点名规则原文:\n%s", got)
	}
}

// 本次运行与跨重启累计**并列出现,绝不合并**。
// 合成一个数之后,「0 次」到底指哪一个再也表达不出来 —— 而一台刚重连的机器上
// 每条规则的本次运行计数都是 0,那正是死规则判据要靠累计值的原因。
func TestExplainShowsRunAndHistorySideBySide(t *testing.T) {
	got := renderExplain(explainFixture())
	for _, want := range []string{"1291", "1289", "8113"} {
		if !strings.Contains(got, want) {
			t.Errorf("少了 %s:\n%s", want, got)
		}
	}
}

// **没有规则可点名、又没有记录时,不许渲染成「0 次」。**
// 那一档(默认 / 内建列表)nil 是「这条路没被记过」,而 0 是「记了、一次没走过」
// —— 把前者说成后者正是死规则判据最忌讳的假阳性。
func TestExplainSaysNothingWhenThereIsNoCount(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Rule, rep.TCP.Source = "", "default"
	rep.TCP.Run, rep.TCP.History = nil, nil
	rep.UDP.Rule, rep.UDP.Source = "", "default"
	got := renderExplain(rep)
	if strings.Contains(got, "0 decisions") {
		t.Errorf("把「没有记录」渲染成了「0 次」:\n%s", got)
	}
}

// **命中了一条具名规则、而本次运行一次记录都没有,必须明说「0 次」。**
//
// 真机 2026-09-03:Tailscale 拨 brook.youdamaster.cc 的 fake-IP 超时,
// `bx explain` 判 DIRECT、依据那条 direct 规则,而「本次」那一行整个缺席。
// 缺席正是线索 —— bx 从没见过那条连接(tailscaled 的 socket 绑在 en0,压根
// 没进 TUN)—— 而缺席最容易被看漏,于是被记成「explain 说 DIRECT 而拨号不通、
// 诊断工具骗人」。具名规则的每一次判定都按名字计数,没有条目就是 0 次,
// 这里 nil 与 0 是同一件事,把它说出来才对得起「为什么」这个问题。
func TestExplainSaysZeroWhenANamedRuleWasNeverHitThisRun(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Run = nil
	got := renderExplain(rep)
	if !strings.Contains(got, "This run 0 decisions") {
		t.Errorf("具名规则本次 0 次记录却没有明说:\n%s", got)
	}
	if !strings.Contains(got, "has not seen a connection") {
		t.Errorf("没有把「bx 没见过走这条规则的连接」说出来:\n%s", got)
	}
	// 累计那一行不受影响:它仍按「有记录才占地方」渲染。
	if !strings.Contains(got, "8113") {
		t.Errorf("累计计数被连累丢了:\n%s", got)
	}
}

// **被 kill-switch 拦下时,那一跳必须说出来。**
// 少了它用户只看到一个 BLOCKED,不知道该去修隧道还是去改规则 ——
// 而「判定是走隧道、隧道不健康所以被拦」正是「我的请求为什么失败」的答案。
func TestExplainSpellsOutTheKillswitchHop(t *testing.T) {
	rep := explainFixture()
	rep.TunnelHealth = "unhealthy"
	rep.TCP = supervisor.ExplainPath{
		Effective: "blocked", Decision: "proxy",
		Source: "default", BlockedBy: "killswitch",
	}
	got := renderExplain(rep)
	if !strings.Contains(got, "kill-switch") {
		t.Errorf("没说是 kill-switch 拦的:\n%s", got)
	}
	if !strings.Contains(got, "proxy") {
		t.Errorf("没说路由判定本来是 proxy:\n%s", got)
	}
}

// 没有路由表时**说不知道**,不让零值读起来像一个判定。
func TestExplainSaysSoWithoutARouter(t *testing.T) {
	got := renderExplain(supervisor.ExplainResponse{Target: "x.com", RouterMissing: true})
	if !strings.Contains(got, "no routing table to ask") {
		t.Errorf("没有路由表却渲染出了一个判定:\n%s", got)
	}
	if strings.Contains(got, "TCP") {
		t.Errorf("没有判定可言时不该摆出 TCP/UDP 两行:\n%s", got)
	}
}

// 「没有健康探针」不许显示成「不健康」——
// 那会把「传输还没装好」说成「隧道断了」,两者该做的事完全不同。
func TestExplainDoesNotCallAMissingProbeUnhealthy(t *testing.T) {
	rep := explainFixture()
	rep.TunnelHealth = "unknown"
	got := renderExplain(rep)
	if strings.Contains(got, "Tunnel    unhealthy") {
		t.Errorf("把「没有探针」说成了「不健康」:\n%s", got)
	}
	if !strings.Contains(got, "unknown") {
		t.Errorf("没有如实说未知:\n%s", got)
	}
}

// TCP 与 UDP 必须各自成行。
// udp.transport / udp.mode 让同一个目的地的两个方向可能去往完全不同的地方
// (甚至一个走隧道一个被 Block),压成一行会把这个事实整个抹掉。
func TestExplainReportsBothProtocols(t *testing.T) {
	rep := explainFixture()
	rep.UDP = supervisor.ExplainPath{Effective: "blocked", Decision: "proxy", Source: "udp_block", BlockedBy: "udp_mode_block"}
	got := renderExplain(rep)
	if !strings.Contains(got, "TCP") || !strings.Contains(got, "UDP") {
		t.Errorf("两个协议方向没有各自成行:\n%s", got)
	}
	if !strings.Contains(got, "udp.mode=block") {
		t.Errorf("没说清 UDP 是被 udp.mode 丢掉的:\n%s", got)
	}
}

// **没有规则可点名时,计数是一个桶的合计,必须说清楚不是这个目标的。**
//
// 真机 2026-09-01 实测:`bx explain steamstatic.com` 与 `bx explain 1.1.1.1`
// 拿到**逐字相同**的 49/15228/73 —— 因为两者都落在 Source="default"、Rule=""
// 那一档,而计数按 (source, rule) 记。数字没错,错的是把它印在一个具体目标
// 下面:读起来就是那个目标的。**一个看起来在说 A、实际在说 B 的数**,正是这
// 整个命令要消灭的东西。
func TestExplainQualifiesBucketCountsWhenThereIsNoRule(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Rule = ""
	rep.TCP.Source = "default"
	got := renderExplain(rep)
	if !strings.Contains(got, "not this target") {
		t.Errorf("桶计数没有被归位,读起来像是这个目标的:\n%s", got)
	}
}

// 反过来:命中具体规则时那个数**就是**这条规则的,不许加那句限定 ——
// 多余的免责声明会让一个准确的数字显得可疑,而 `*.qq.com 410 次失败` 恰恰
// 是这个命令最有价值的输出。
func TestExplainDoesNotQualifyARealRulesCounts(t *testing.T) {
	got := renderExplain(explainFixture()) // fixture 命中 *.steamstatic.com
	if strings.Contains(got, "not this target") {
		t.Errorf("给一条真规则的计数加了不该有的限定:\n%s", got)
	}
}

// **一个百分比答不出该不该管。**
//
// 真机 2026-09-01:`*.qq.com` 本次运行 78 次判定 / 15 次失败(19.2%),而这个
// 数字**无法行动** —— 全是 unreachable 就要立刻去查路由(2026-08-13 那个
// DirectDialer 故障的签名),全是 timeout 就一个字都不用改。错误对象一直在
// 手边(`conn, err := d.Direct.DialContext(...)`),此前只进 debug 日志。
func TestExplainBreaksFailuresIntoActionableKinds(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Run.FailureKinds = map[string]int64{"timeout": 12, "unreachable": 3}
	got := renderExplain(rep)
	if !strings.Contains(got, "peer did not answer") || !strings.Contains(got, "route unreachable") {
		t.Errorf("没有把失败拆开:\n%s", got)
	}
	// **最大的一类排最前,而且每次都一样。**
	// map 迭代序是随机的:一个只跑一次的顺序断言会偶发通过,而
	// 「一个会偶发红的闸门比没有闸门更糟」—— 跑够多次把随机性挤掉。
	for i := 0; i < 50; i++ {
		out := renderExplain(rep)
		if strings.Index(out, "对端不应答") > strings.Index(out, "路由不可达") {
			t.Fatalf("第 %d 次渲染没有按次数倒序(输出不稳定就 diff 不了):\n%s", i, out)
		}
	}
}

// 没有分类时不许显示成「各类都是 0」——
// 「这一版没分类」与「归不了类」都不是「一次都没发生」。
func TestExplainSaysNothingAboutKindsWhenThereAreNone(t *testing.T) {
	got := renderExplain(explainFixture()) // fixture 无 FailureKinds
	if strings.Contains(got, "×0") || strings.Contains(got, "[]") {
		t.Errorf("没有分类却渲染了一张空表:\n%s", got)
	}
}

// **「累计」必须说清楚覆盖多长时间。**
// 一个跨了半年、几个版本的 15% 与一天之内的 15% 是完全不同的两件事,
// 而读的人会默认它是后者。
func TestExplainSaysHowLongTheCumulativeCountCovers(t *testing.T) {
	rep := explainFixture()
	rep.HistoryWindowSeconds = 95040 // 1.1 天
	got := renderExplain(rep)
	if !strings.Contains(got, "1.1 days") {
		t.Errorf("没说累计覆盖多长时间:\n%s", got)
	}
}

// 跨版本要说 —— 中间几版的计数行为可能并不一致,那份累计要打折看。
// 单一版本时不说:一句恒真的话会被训练成噪声。
func TestExplainFlagsAMultiVersionCumulativeCount(t *testing.T) {
	rep := explainFixture()
	rep.HistoryWindowSeconds = 95040
	rep.HistoryVersions = []string{"dev"}
	if strings.Contains(renderExplain(rep), "跨 ") {
		t.Error("单一版本却报了跨版本")
	}
	rep.HistoryVersions = []string{"v0.9", "dev"}
	if !strings.Contains(renderExplain(rep), "spanning 2 versions") {
		t.Errorf("跨版本没说:\n%s", renderExplain(rep))
	}
}

// 没有历史时一个字都不说 —— 「累计覆盖 0 天」比不说更容易被读错。
func TestExplainSaysNothingAboutAnAbsentHistory(t *testing.T) {
	if strings.Contains(renderExplain(explainFixture()), "累计口径") {
		t.Error("没有历史却报了累计口径")
	}
}

// 本机视角(internal/pathview)排在 Core 那一半**前面**,而且 Core 连不上时不再
// 是一个错误 —— 那正是这个视角存在的理由:bx 没在跑时,这个目标怎么走照样答得出。
func TestExplainPrintsMachineViewBeforeCoreAndSurvivesCoreBeingDown(t *testing.T) {
	view := pathview.View{
		Conclusion: "普通程序连它会进 bx,去向由 bx 判定(见下)。 绑了网卡的程序(如 Tailscale)会从 en0 直出,源 IP 是你的真实 IP。",
		Kind:       pathview.KindPublic,
		Lines: []pathview.Line{
			{Label: "解析", Text: "是 IP,不用解析"},
			{Label: "本机路由", Text: "utun0(bx 的 TUN)"},
			{Label: "绑网卡时", Text: "en0 via 192.168.50.2(物理网卡)"},
		},
	}
	out, err := explainOutput(view, explainFixture(), nil, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	machine := strings.Index(out, "Verdict")
	core := strings.Index(out, "TCP       DIRECT")
	if machine < 0 || core < 0 || machine > core {
		t.Fatalf("本机视角要排在 Core 判定之前:\n%s", out)
	}
	for _, want := range []string{"绑了网卡", "utun0(bx 的 TUN)", "en0 via 192.168.50.2"} {
		if !strings.Contains(out, want) {
			t.Fatalf("少了 %q:\n%s", want, out)
		}
	}

	down := errors.New("dial unix /var/run/bx/core.sock: connect: no such file or directory")
	out, err = explainOutput(view, supervisor.ExplainResponse{}, down, false, nil)
	if err != nil {
		t.Fatalf("Core 连不上不该让 explain 失败,本机视角照样有用: %v", err)
	}
	if !strings.Contains(out, "Verdict") || !strings.Contains(out, "bx is not running") {
		t.Fatalf("Core 连不上时要有本机视角 + 一句「bx 没在跑」:\n%s", out)
	}
	if strings.Contains(out, "TCP       ") {
		t.Fatalf("Core 连不上时不许渲染一份零值判定:\n%s", out)
	}
}

// --json 在 Core 的应答上**追加** machine 键,原有顶层字段一个不动(MCP 的
// bx_explain 直接转发这份 JSON,agent 已在读 tcp/udp)。
func TestExplainJSONAddsMachineWithoutMovingCoreFields(t *testing.T) {
	view := pathview.View{Conclusion: "x", Kind: pathview.KindPublic}
	out, err := explainOutput(view, explainFixture(), nil, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("不是合法 JSON: %v\n%s", err, out)
	}
	if _, ok := got["tcp"]; !ok {
		t.Fatalf("Core 的顶层字段被挪走了: %v", got)
	}
	if _, ok := got["machine"]; !ok {
		t.Fatalf("没有 machine 键: %v", got)
	}
	out, err = explainOutput(view, supervisor.ExplainResponse{}, errors.New("down"), true, nil)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["tcp"]; ok {
		t.Fatalf("Core 连不上时不许发布零值判定: %v", got)
	}
	if got["core_unavailable"] == nil || got["machine"] == nil {
		t.Fatalf("Core 连不上时要有 machine 与 core_unavailable: %v", got)
	}
}

// 标签按显示宽度对齐到 10 列(CJK 每字两列),否则「目标类型」比「解析」凸出去。
func TestMachineViewLabelsAlignByDisplayWidth(t *testing.T) {
	view := pathview.View{Conclusion: "c", Lines: []pathview.Line{{Label: "解析", Text: "x"}, {Label: "目标类型", Text: "y"}}}
	for _, line := range strings.Split(strings.TrimSpace(renderMachineView(view)), "\n") {
		_, text, ok := strings.Cut(line, "  ")
		if !ok {
			t.Fatalf("行里找不到分隔: %q", line)
		}
		label := strings.TrimSuffix(line, "  "+text)
		w := 0
		for _, r := range label {
			if r > 0x7f {
				w += 2
			} else {
				w++
			}
		}
		// **12 列,不是 10** —— 判据文案改英文之后,"Bound to NIC" 这类标签比
		// 原来的 CJK 标签更长,10 列会把它挤出去而让整栏错位。这个数字与
		// padLabel 里那个必须一致;它们不一致时错位只在**某些**标签上出现,
		// 看起来像随机的排版毛病而不是一个可查的常量。
		if w+len(strings.TrimPrefix(line, label))-len(strings.TrimLeft(strings.TrimPrefix(line, label), " ")) != 10 {
			t.Fatalf("标签没对齐到 10 列: %q", line)
		}
	}
}

// **假 IP 段是用户可配的(`dns.fakeip_cidr`),而 explain 曾经硬编码 198.18/15。**
//
// 后果不是显示错一行:`pathview` 认不出那是假 IP ⇒ 判成普通公网,于是那条
// 「绑了网卡的程序拿到的是假 IP,从物理网卡发出去石沉大海;域名进
// dns.fakeip_filter/hosts 或直接写 IP」的话整个不出现 —— 而那句话正是 DERP
// 域名那次真机排查的产物,也正是自定义了 fakeip_cidr 的人最需要的一句。
//
// 判据取 **Core 此刻在用的那个值**(RuntimeState.FakeipCIDR,与 DNSUpstream /
// ConfigPath 同一条纪律:发布运行中的值,不是盘上的值),Core 不在跑或那一版
// Core 不发这个字段时退回内建默认。
func TestExplainUsesTheFakeIPRangeCoreIsActuallyUsing(t *testing.T) {
	custom := netip.MustParsePrefix("100.100.0.0/16")
	for _, tc := range []struct {
		name    string
		runtime func() (supervisor.RuntimeState, error)
		probe   netip.Addr // 这个地址必须被判成假 IP
		want    netip.Prefix
	}{
		{
			name: "Core 报了自定义段 —— 用它,而不是内建默认",
			runtime: func() (supervisor.RuntimeState, error) {
				return supervisor.RuntimeState{TunName: "utun9", FakeipCIDR: custom.String()}, nil
			},
			probe: netip.MustParseAddr("100.100.0.7"),
			want:  custom,
		},
		{
			name: "Core 不在跑 —— 退回内建默认,别把假 IP 判成公网",
			runtime: func() (supervisor.RuntimeState, error) {
				return supervisor.RuntimeState{}, errors.New("dial: no such file")
			},
			probe: netip.MustParseAddr("198.18.0.7"),
			want:  netip.MustParsePrefix(config.DefaultFakeipCIDR),
		},
		{
			name: "旧版 Core 不发这个字段 —— 同样退回默认,不是退回一个无效前缀",
			runtime: func() (supervisor.RuntimeState, error) {
				return supervisor.RuntimeState{TunName: "utun9"}, nil
			},
			probe: netip.MustParseAddr("198.18.0.7"),
			want:  netip.MustParsePrefix(config.DefaultFakeipCIDR),
		},
		{
			name: "值坏了 —— 退回默认。留一个无效 Prefix 会让这一类判定**静默关掉**",
			runtime: func() (supervisor.RuntimeState, error) {
				return supervisor.RuntimeState{TunName: "utun9", FakeipCIDR: "不是一个网段"}, nil
			},
			probe: netip.MustParseAddr("198.18.0.7"),
			want:  netip.MustParsePrefix(config.DefaultFakeipCIDR),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := collectPathFactsWith(context.Background(), tc.probe.String(), tc.runtime)
			if f.FakeIP != tc.want {
				t.Fatalf("采集到的假 IP 段 = %v, want %v", f.FakeIP, tc.want)
			}
			// **断言打在用户看得见的结论上**,不只打在那个 Prefix 字段上:
			// 字段对了而 pathview 认不出来,与没修一样。
			if kind := pathview.Judge(f).Kind; kind != pathview.KindFakeIP {
				t.Fatalf("%v 没被判成假 IP,Kind = %q —— 那条「从物理网卡发出去石沉大海」"+
					"的话于是整个不会出现", tc.probe, kind)
			}
		})
	}
}

// —— 判决行:把 `internal/dialfail` 每个常量注释里已经写着的处置印出来 ——
//
// explain 此前答的是「会怎么走」加「数了多少次、分成哪几类」。而
// **分类到处置之间那一步一直留给读的人自己走** —— 判据从 2026-09-01 起就在
// dialfail 里,是 `LooksLikeOurFault`,零生产调用方。

// 路由不可达占多数 ⇒ 指向 bx 自己,而且必须点名去哪儿查。
// 这是 2026-08-13 与 08-16 两次真机故障的签名。
func TestExplainBlamesTheDirectDialerWhenTheRouteIsUnreachable(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Run.Attempts, rep.TCP.Run.Failures = 500, 420
	rep.TCP.Run.FailureKinds = map[string]int64{"unreachable": 410, "timeout": 10}
	got := renderExplain(rep)
	if !strings.Contains(got, "Blame ") {
		t.Fatalf("一份 98%% 都是路由不可达的失败没有判决行:\n%s", got)
	}
	if !strings.Contains(got, "direct_egress") {
		t.Errorf("判决没点名去哪儿查(direct_egress):\n%s", got)
	}
}

// **对端那一档的措辞绝不许断言对方的状态。** 本机自己没网时同样表现为
// 不应答,而一句「那台服务器挂了」会让人去重启一台好好的机器
// (与 core_tunnel_unreachable 同一条纪律)。
func TestExplainNeverAssertsWhatTheOtherEndIsDoing(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Run.Attempts, rep.TCP.Run.Failures = 100, 90
	rep.TCP.Run.FailureKinds = map[string]int64{"timeout": 90}
	got := renderExplain(rep)
	if !strings.Contains(got, "Blame ") {
		t.Fatalf("90%% 超时没有判决行:\n%s", got)
	}
	for _, forbidden := range []string{"挂了", "宕机", "服务器不可用", "对方已下线"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("判决断言了对端的状态(%q)—— bx 只观测到自己没收到回应:\n%s", forbidden, got)
		}
	}
}

// **两档的措辞必须相反。** 指向本机的那一档要说「去查」,指向对端的那一档
// 要说「改 bx 的规则没用」;两者渲染成同一句话,这个功能就整个没有意义,
// 而那种退化不会有任何测试因为别的理由转红。
func TestExplainLocalAndRemoteVerdictsReadDifferently(t *testing.T) {
	verdictFor := func(kinds map[string]int64) string {
		rep := explainFixture()
		rep.TCP.Run.Attempts, rep.TCP.Run.Failures = 100, 90
		rep.TCP.Run.FailureKinds = kinds
		for _, line := range strings.Split(renderExplain(rep), "\n") {
			if strings.Contains(line, "Blame ") {
				return line
			}
		}
		t.Fatalf("没有判决行,输入 %v", kinds)
		return ""
	}
	local := verdictFor(map[string]int64{"unreachable": 90})
	remote := verdictFor(map[string]int64{"timeout": 90})
	if local == remote {
		t.Errorf("「指向 bx 自己」与「指向对端」渲染成了同一句话:\n%s", local)
	}
}

// **域名不存在不是故障**,判决必须说「不用改」——
// 真机上 `*.qq.com` 966 次失败全是这一类,而那是微信在查一批不存在的主机名。
func TestExplainSaysNXDOMAINNeedsNoFix(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Run.Attempts, rep.TCP.Run.Failures = 1454, 963
	rep.TCP.Run.FailureKinds = map[string]int64{"dns_nxdomain": 963}
	got := renderExplain(rep)
	if !strings.Contains(got, "Blame ") {
		t.Fatalf("NXDOMAIN 占绝对多数却没有判决行:\n%s", got)
	}
	if strings.Contains(got, "direct_egress") {
		t.Errorf("把「应用在查不存在的名字」派去查 bx 的直连出口了:\n%s", got)
	}
}

// **没有单一主因时不许硬挑一个。** 4/3/3 里最多的那一类只占 40%,
// 说「主因是它」就是编答案;而那一行的 `[×N ×N ×N]` 拆分本身已经说明
// 「它不是一个原因造成的」,那才是可行动的信息。
func TestExplainRefusesToNameACauseWhenFailuresAreMixed(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Run.Attempts, rep.TCP.Run.Failures = 100, 100
	rep.TCP.Run.FailureKinds = map[string]int64{"unreachable": 40, "timeout": 30, "reset": 30}
	if got := renderExplain(rep); strings.Contains(got, "Blame ") {
		t.Errorf("一份 40/30/30 的失败被安上了单一主因:\n%s", got)
	}
}

// 没有失败就没有判决 —— 一台健康机器上这一行一个字都不占。
// **两种「没有」各测一遍**:真的一次没失败,以及这一版根本没做分类 ——
// 后者是 fixture 的原样,少了它这条测试会因为 fixture 恰好没分类而假绿。
func TestExplainPrintsNoVerdictWithoutFailures(t *testing.T) {
	healthy := explainFixture()
	healthy.TCP.Run.Failures = 0
	healthy.TCP.Run.FailureKinds = nil
	if got := renderExplain(healthy); strings.Contains(got, "Blame ") {
		t.Errorf("没有失败却出现了判决行:\n%s", got)
	}

	// 有失败、但这一版没有分类:分不出主因就没有判决可下。
	unclassified := explainFixture() // Failures=1289 而 FailureKinds 为 nil
	if unclassified.TCP.Run.Failures == 0 {
		t.Fatal("fixture 改了,这条测试的前提没了")
	}
	if got := renderExplain(unclassified); strings.Contains(got, "Blame ") {
		t.Errorf("没有分类却下了判决:\n%s", got)
	}
}

// **判决必须说清它读的是哪一份数。** 本次与累计可以给出相反的答案
// (刚修好的机器:累计全是 unreachable,本次一次都没有),而那两句话的
// 处置完全相反 —— 不说来源就没法知道该信哪一句。
func TestExplainVerdictNamesWhichSampleItRead(t *testing.T) {
	rep := explainFixture()
	rep.TCP.History = &stats.RuleOutcome{
		Source: rep.TCP.Source, Rule: rep.TCP.Rule,
		Attempts: 9000, Failures: 8000,
		FailureKinds: map[string]int64{"unreachable": 8000},
	}
	got := renderExplain(rep)
	var verdict string
	for _, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "Blame ") {
			verdict = line
		}
	}
	if verdict == "" {
		t.Fatalf("累计里 8000 次失败没有判决行:\n%s", got)
	}
	if !strings.Contains(verdict, "the totals") {
		t.Errorf("判决读的是累计却没说:\n%s", verdict)
	}
}

// **渲染出来的话里不许有 markdown。** 用户在终端里读到的是字面上的星号,
// 而这条判决唯一的目的就是「照着做」——
// 与 corestartadvice 那条同源(本仓库为它单独立过一条守卫)。
// 判据打在**每一类的产出**上,穷举,不靠某一次渲染碰巧覆盖到哪几类。
func TestExplainVerdictsCarryNoMarkdown(t *testing.T) {
	kinds := []string{
		dialfail.Unreachable, dialfail.Timeout, dialfail.Refused, dialfail.Reset,
		dialfail.DNS, dialfail.DNSNotFound, dialfail.Canceled,
		dialfail.EgressUnwired, dialfail.Other, "某个将来才有的类别",
	}
	for _, kind := range kinds {
		text := explainVerdictText(kind)
		if text == "" {
			t.Errorf("%q 没有判决措辞 —— 认不出的类别也该有一句如实的话", kind)
		}
		// 反引号在这里不只是难看:这个仓库真的被它坑过一次
		// (文档里反引号包着的命令被 zsh 当成命令替换执行了),
		// 而这一行的全部目的就是让人照着敲。命令一律裸写,与仓库其余提示一致。
		for _, sym := range []string{"**", "`", "__"} {
			if strings.Contains(text, sym) {
				t.Errorf("%q 的判决带着 markdown 符号 %q,用户读到的是字面符号:%s", kind, sym, text)
			}
		}
	}
}

// **累计有失败但没分类时,判决要落回本次那份。**
// 累计那份可能有 8000 次失败却一个分类都没有(旧版本记的、或这一轮还没落盘);
// 按「哪份有失败就读哪份」会把唯一答得出问题的样本整个扔掉,而输出与
// 「这一版不下判决」逐字节相同 —— 一次静默的功能缺失。
func TestExplainFallsBackToTheRunWhenHistoryCannotAnswer(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Run.Attempts, rep.TCP.Run.Failures = 100, 90
	rep.TCP.Run.FailureKinds = map[string]int64{"unreachable": 90}
	rep.TCP.History = &stats.RuleOutcome{
		Source: rep.TCP.Source, Rule: rep.TCP.Rule,
		Attempts: 9000, Failures: 8000, // 有失败,但这一版没记分类
	}
	got := renderExplain(rep)
	if !strings.Contains(got, "Blame ") {
		t.Fatalf("累计答不出问题时没有落回本次那份:\n%s", got)
	}
	if !strings.Contains(got, "by this run") {
		t.Errorf("判决没说它读的是本次那份:\n%s", got)
	}
}

// —— 体检行:`bx explain` 第一次答得出「这条规则该不该留」——
//
// 判据整份早就在(`internal/rulereview` 的四类 + 死规则),而 explain 从来不调它:
// 它答得出「这个目标会怎么走」,答不出「把它送上这条路的那一行本身有没有问题」。
// 用户的原话是「是否有的可以放行,有的必须关掉」—— 那正是这份体检的四类。

func explainFixtureWithFinding(class rulereview.Class, summary, coveredBy string) (supervisor.ExplainResponse, *rulereview.Report) {
	return explainFixture(), &rulereview.Report{Findings: []rulereview.Finding{{
		Kind: "direct", Rule: "*.steamstatic.com", Class: class,
		Summary: summary, CoveredBy: coveredBy,
	}}}
}

// 命中的那条规则有体检结论时,explain 要说出来。
func TestExplainSurfacesTheRuleReviewVerdict(t *testing.T) {
	rep, review := explainFixtureWithFinding(
		rulereview.ClassShadowedByBuiltinList, "already on bx's built-in china direct list; deleting it changes no traffic", "the built-in list",
	)
	got := renderExplainWithReview(rep, review)
	if !strings.Contains(got, "Review") {
		t.Fatalf("命中的规则有体检结论却一个字没说:\n%s", got)
	}
	if !strings.Contains(got, "changes no traffic") {
		t.Errorf("体检结论没有被渲染出来:\n%s", got)
	}
}

// **按 kind 分开查。** 同一条原文可以同时在 direct 与 proxy 里、语义相反 ——
// 只按原文比,会把 proxy 那条的结论安到 direct 这条头上
// (与死规则判据「按 Source 把 direct/proxy 分开查」同一条)。
func TestExplainDoesNotBorrowTheOppositeTablesVerdict(t *testing.T) {
	rep := explainFixture() // TCP.Source = user_direct
	review := &rulereview.Report{Findings: []rulereview.Finding{{
		Kind: "proxy", Rule: "*.steamstatic.com",
		Class: rulereview.ClassRisky, Summary: "this is the proxy verdict",
	}}}
	if got := renderExplainWithReview(rep, review); strings.Contains(got, "this is the proxy verdict") {
		t.Errorf("把对面那张表的结论安到了这条规则头上:\n%s", got)
	}
}

// 没有命中用户规则时(内建列表 / 默认)不许安一个结论上去 —— 那一档没有哪一行可点名。
func TestExplainSaysNothingAboutReviewWithoutAUserRule(t *testing.T) {
	rep := explainFixture()
	rep.TCP.Source, rep.TCP.Rule = "china_domain", ""
	rep.UDP.Source, rep.UDP.Rule = "china_domain", ""
	review := &rulereview.Report{Findings: []rulereview.Finding{{
		Kind: "direct", Rule: "*.steamstatic.com",
		Class: rulereview.ClassRisky, Summary: "去匿名化风险",
	}}}
	if got := renderExplainWithReview(rep, review); strings.Contains(got, "Review") {
		t.Errorf("没有用户规则可点名却渲染了体检行:\n%s", got)
	}
}

// **体检拿不到时一个字都不说,绝不说成「这条规则没问题」。**
// 「没查」与「查了没有」是两件事,而这个仓库为把前者渲染成后者栽过很多次。
func TestExplainNeverCallsAnUnreviewedRuleHealthy(t *testing.T) {
	got := renderExplainWithReview(explainFixture(), nil)
	if strings.Contains(got, "Review") {
		t.Errorf("体检缺席时仍渲染了体检行:\n%s", got)
	}
	// **禁词要卡在「关于规则的断言」上,不是卡在某个字。**
	// 第一版把「健康」整个禁了,而同一份输出里 `隧道      健康` 是另一件事的
	// 正确答案 —— 一条会误报的闸门比没有闸门更糟。
	for _, forbidden := range []string{"这条规则没有问题", "规则健康", "体检通过"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("体检缺席时冒出了 %q —— 那是把「没查」说成「查了没问题」:\n%s", forbidden, got)
		}
	}
}

// 一条规则可以同时踩中两类(既危险、又被更宽的一条盖住),两条都要说。
// **按名字取一条就静默丢掉其余安全结论** —— 本仓库为这个形状栽过。
func TestExplainShowsEveryVerdictForTheRule(t *testing.T) {
	rep := explainFixture()
	review := &rulereview.Report{Findings: []rulereview.Finding{
		{Kind: "direct", Rule: "*.steamstatic.com", Class: rulereview.ClassRisky, Summary: "去匿名化风险"},
		{Kind: "direct", Rule: "*.steamstatic.com", Class: rulereview.ClassShadowedByUserRule, Summary: "被同表更宽的一条盖住"},
	}}
	got := renderExplainWithReview(rep, review)
	for _, want := range []string{"去匿名化风险", "被同表更宽的一条盖住"} {
		if !strings.Contains(got, want) {
			t.Errorf("少说了一条结论(%q):\n%s", want, got)
		}
	}
}

// **体检结论也要进 --json。** 只长在文本路径上的诊断正是本仓库记着的那类缺陷:
// 菜单与 agent 走的是另一条路,于是同一台机器上一边说得出问题、一边一个字都不说
// (bx doctor 的「哪条规则在成片失败」就这么消失过一次)。
func TestExplainJSONCarriesTheRuleReviewVerdict(t *testing.T) {
	rep, review := explainFixtureWithFinding(rulereview.ClassRisky, "公有云直连,去匿名化风险", "")
	out, err := explainOutput(pathview.View{}, rep, nil, true, review)
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal([]byte(out), &top); err != nil {
		t.Fatalf("输出不是合法 JSON:%v\n%s", err, out)
	}
	if _, ok := top["tcp_rule_findings"]; !ok {
		t.Errorf("--json 里没有体检结论 —— agent 与文本路径看到的不是同一件事:\n%s", out)
	}
	// 顶层既有字段一个不许动 —— MCP 的 bx_explain 直接转发这份 JSON。
	for _, key := range []string{"target", "tcp", "udp", "tunnel_health"} {
		if _, ok := top[key]; !ok {
			t.Errorf("顶层字段 %q 被这次改动碰掉了", key)
		}
	}
}

// **那根线必须有人守。** 判据写对了、测试也绿,而 explainAction 递给它一个
// 写死的 nil —— 整个功能在真机上死透而没有任何东西转红。这是本仓库编号的
// 第七种失效写法,这一支上它已经出现过四次。
func TestExplainActionReallyFetchesTheRuleReview(t *testing.T) {
	src, err := os.ReadFile("explain.go")
	if err != nil {
		t.Fatalf("读不出 explain.go:%v", err)
	}
	body, ok := goFunctionBody(string(src), "func explainAction(c *cli.Context) error {")
	if !ok {
		t.Fatal("读不出 explainAction —— 这条守卫读不懂现在的代码了,先修它")
	}
	if !strings.Contains(body, "explainOutput(view, rep, err, c.Bool(\"json\"), explainRuleReview())") {
		t.Errorf("explainAction 没有把真的体检递给 explainOutput(写死 nil 也会让每条测试保持绿):\n%s", body)
	}
}
