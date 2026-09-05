package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/pathview"
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
	if strings.Contains(got, "0 次判定") {
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
	if !strings.Contains(got, "本次    0 次判定") {
		t.Errorf("具名规则本次 0 次记录却没有明说:\n%s", got)
	}
	if !strings.Contains(got, "没见过") {
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
	if !strings.Contains(got, "还没有路由表") {
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
	if strings.Contains(got, "隧道      不健康") {
		t.Errorf("把「没有探针」说成了「不健康」:\n%s", got)
	}
	if !strings.Contains(got, "未知") {
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
	if !strings.Contains(got, "不是这个目标的") {
		t.Errorf("桶计数没有被归位,读起来像是这个目标的:\n%s", got)
	}
}

// 反过来:命中具体规则时那个数**就是**这条规则的,不许加那句限定 ——
// 多余的免责声明会让一个准确的数字显得可疑,而 `*.qq.com 410 次失败` 恰恰
// 是这个命令最有价值的输出。
func TestExplainDoesNotQualifyARealRulesCounts(t *testing.T) {
	got := renderExplain(explainFixture()) // fixture 命中 *.steamstatic.com
	if strings.Contains(got, "不是这个目标的") {
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
	if !strings.Contains(got, "对端不应答") || !strings.Contains(got, "路由不可达") {
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
	if !strings.Contains(got, "1.1 天") {
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
	if !strings.Contains(renderExplain(rep), "跨 2 个版本") {
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
	out, err := explainOutput(view, explainFixture(), nil, false)
	if err != nil {
		t.Fatal(err)
	}
	machine := strings.Index(out, "结论")
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
	out, err = explainOutput(view, supervisor.ExplainResponse{}, down, false)
	if err != nil {
		t.Fatalf("Core 连不上不该让 explain 失败,本机视角照样有用: %v", err)
	}
	if !strings.Contains(out, "结论") || !strings.Contains(out, "bx 没在跑") {
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
	out, err := explainOutput(view, explainFixture(), nil, true)
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
	out, err = explainOutput(view, supervisor.ExplainResponse{}, errors.New("down"), true)
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
		if w+len(strings.TrimPrefix(line, label))-len(strings.TrimLeft(strings.TrimPrefix(line, label), " ")) != 10 {
			t.Fatalf("标签没对齐到 10 列: %q", line)
		}
	}
}
