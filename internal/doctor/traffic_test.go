package doctor

import (
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tristate"
)

// **「问不出来」不是「一切正常」。**
//
// 少了这一行,一台 Core 没在跑的机器与一台完全健康的机器,在 doctor 的这一段里
// 逐字节相同 —— 与本仓库 Tristate 同一条纪律。
func TestDoctorSaysWhenItCouldNotAsk(t *testing.T) {
	checks := trafficChecks(&TrafficFact{Err: "dial unix: connection refused"})
	if len(checks) != 1 {
		t.Fatalf("问不出来时给了 %d 行:%+v", len(checks), checks)
	}
	if checks[0].Status != StatusNotChecked {
		t.Fatalf("没问到却报 %q —— 只有 not_checked 说得出「这一项没查」", checks[0].Status)
	}
	if !strings.Contains(checks[0].Detail, "没问到") {
		t.Errorf("没说清是问不出来:%q", checks[0].Detail)
	}
	if checks[0].Hint == "" {
		t.Error("没给下一步")
	}
}

// **一切正常时也要说一句。**
//
// 这与 status 那边「正常就闭嘴」刻意不同:status 是常驻面板,永远在的一行会变成
// 墙纸;doctor 是人主动问「哪里不对」时跑的,那时「直连 45 条、0 失败」是有价值的
// 答案 —— 排查最耗时的一步正是排除。
func TestDoctorReportsHealthyTrafficWithNumbers(t *testing.T) {
	checks := trafficChecks(&TrafficFact{Report: stats.Report{Snapshot: stats.Snapshot{
		Direct: 45, Proxy: 300,
	}}})
	if len(checks) != 1 || checks[0].Status != "ok" {
		t.Fatalf("健康机器上给了 %+v", checks)
	}
	for _, want := range []string{"45", "300"} {
		if !strings.Contains(checks[0].Detail, want) {
			t.Errorf("没给出数字 %s:%q", want, checks[0].Detail)
		}
	}
}

// **成片失败的用户规则要点名,并给出可执行的下一步。**
//
// 这就是 2026-08-13 那个 bug 被抓住的形状:`*.qq.com 1291 条,失败 1289`。
// 它当时只出现在 status 里,而人在出问题时敲的是 doctor。
func TestDoctorNamesFailingUserRules(t *testing.T) {
	checks := trafficChecks(&TrafficFact{Report: stats.Report{
		ConfigPath: "/etc/bx/config.yaml",
		Snapshot: stats.Snapshot{
			Direct: 1291, DirectFailed: 1289,
			Rules: []stats.RuleOutcome{
				{Source: "user_direct", Rule: "*.qq.com", Attempts: 1291, Failures: 1289},
			},
		},
	}})
	if len(checks) < 2 {
		t.Fatalf("没点名那条规则:%+v", checks)
	}
	if checks[0].Status != "fail" {
		t.Errorf("总状态 = %q,有规则在成片失败时应当是 fail", checks[0].Status)
	}
	named := checks[1]
	if !strings.Contains(named.Detail, "*.qq.com") {
		t.Errorf("没点名到 config 里那一行:%q", named.Detail)
	}
	// **指引要指向他改得了的那个文件**,而且要说清改完得重连。
	if !strings.Contains(named.Hint, "/etc/bx/config.yaml") {
		t.Errorf("指引没点名配置文件:%q", named.Hint)
	}
	if !strings.Contains(named.Hint, "bx up") {
		t.Errorf("没说改完要重连:%q", named.Hint)
	}
}

// **多条成片失败的规则合并成恰好一条 check,而且一条都不许少说。**
//
// 每条规则各产出一条同名 check 的话,--json 的消费方(agent/MCP,按名字取)
// 只会拿到其中一条 —— 55ef8ea 那次事故的形状,riskyRuleFinding 已经为同一个
// 理由合并过一次。而合并之后另一个陷阱是「只说第一条」:那会让用户改完一条
// 以为完事了。
func TestDoctorMergesFailingRulesIntoOneCheckThatNamesThemAll(t *testing.T) {
	checks := trafficChecks(&TrafficFact{Report: stats.Report{
		ConfigPath: "/etc/bx/config.yaml",
		Snapshot: stats.Snapshot{
			Rules: []stats.RuleOutcome{
				{Source: "user_direct", Rule: "*.qq.com", Attempts: 100, Failures: 99},
				{Source: "user_direct", Rule: "*.icloud.com", Attempts: 50, Failures: 40},
				{Source: "user_direct", Rule: "*.push.apple.com", Attempts: 30, Failures: 30},
			},
		},
	}})
	var named []Check
	for _, c := range checks {
		if c.Name == TrafficFailingRulesCheckName {
			named = append(named, c)
		}
	}
	if len(named) != 1 {
		t.Fatalf("失败规则给了 %d 条同名 check —— 按名字取的消费方只会拿到一条", len(named))
	}
	for _, rule := range []string{"*.qq.com", "*.icloud.com", "*.push.apple.com"} {
		if !strings.Contains(named[0].Detail, rule) {
			t.Errorf("少说了 %s:%q", rule, named[0].Detail)
		}
	}
}

// **总状态的门槛必须与点名的门槛一致。**
//
// 看失败总数会让正常机器长期显示 warn:任何一台机器上都有连不上的目标(对方
// 关机、域名过期、网络抖动),而那既不是 bx 的问题也不是用户能处置的。更糟的是
// 会出现「总状态 warn 而下面一条都没有」—— 用户看着一个警告,找不到它在说什么。
func TestDoctorDoesNotWarnOnBackgroundFailures(t *testing.T) {
	checks := trafficChecks(&TrafficFact{Report: stats.Report{Snapshot: stats.Snapshot{
		Direct: 5000, DirectFailed: 40, Proxy: 9000, ProxyFailed: 60,
		// 没有任何一条规则过点名门槛。
		Rules: []stats.RuleOutcome{{Source: "china_domain", Attempts: 5000, Failures: 40}},
	}}})
	if len(checks) != 1 {
		t.Fatalf("零星失败却多报了几行:%+v", checks)
	}
	if checks[0].Status != "ok" {
		t.Fatalf("零星失败被报成 %q —— 那会让每台机器长期挂着一个找不到出处的警告",
			checks[0].Status)
	}
	// 但数字仍然要给出来:用户自己判断 40/5000 要不要紧。
	if !strings.Contains(checks[0].Detail, "40") {
		t.Errorf("失败数没显示:%q", checks[0].Detail)
	}
}

// UDP 那句话要到得了 doctor —— 它是这条路上唯一带明确动作的信号
// (去看那台 UDP 服务器)。
func TestDoctorSurfacesTheUDPNotice(t *testing.T) {
	checks := trafficChecks(&TrafficFact{Report: stats.Report{Snapshot: stats.Snapshot{
		Proxy: 100,
		Rules: []stats.RuleOutcome{
			{Source: "udp_proxy", Attempts: 10},
			{Source: "udp_proxy_fallback", Attempts: 90},
		},
	}}})
	var found bool
	for _, c := range checks {
		if c.Name == udpTrafficCheckName && strings.Contains(c.Detail, "加速档") {
			found = true
		}
	}
	if !found {
		t.Fatalf("UDP 那句话没进 doctor:%+v", checks)
	}
}

// **判据与 status 那一段同源,不另写一份。** 两处各写一份判据是这个仓库反复栽的
// 形状:一个改了另一个没改,而两边测试都绿。这条钉住 doctor 用的就是 stats 那两个
// 导出判据 —— 换成手写阈值会让它悄悄漂开。
func TestDoctorUsesTheSameJudgementAsStatus(t *testing.T) {
	source := mustReadRepoFile(t, "traffic.go")
	for _, want := range []string{"report.FailingRules()", "report.UDPNotice()"} {
		if !strings.Contains(source, want) {
			t.Errorf("doctor 没有用 %s —— 判据一旦分叉,status 与 doctor 会对同一台机器说不同的话", want)
		}
	}
	for _, banned := range []string{">= 5", "0.5", "ruleReportMin"} {
		if strings.Contains(source, banned) {
			t.Errorf("doctor 里出现了手写阈值 %q —— 判据必须只有一份", banned)
		}
	}
}

func mustReadRepoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 守卫已经失效,先修守卫", name, err)
	}
	return string(data)
}

// **直连出不去时,不许把锅甩给用户的规则。**
//
// 这是 2026-08-15 在项目所有者机器上当场看到的:`*.qq.com 51/64 失败`、
// `*.icloud.com 31/244`、`*.push.apple.com 159/317` —— 而 `route -n get
// -ifscope en0 8.8.8.8` 是 `not in table`。规则完全正确,坏的是 bx 自己的
// 直连器(macOS 上那条 scoped 默认路由不见了)。
//
// 照旧建议「改 rules」的后果是:用户去删掉一条**正确**的规则,问题原样留着 ——
// 一个在最需要它的时候把人指向错误方向的诊断,比不给建议更糟。
func TestDoctorDoesNotBlameRulesWhenDirectEgressIsDown(t *testing.T) {
	facts := &TrafficFact{
		Report: stats.Report{
			ConfigPath: "/etc/bx/config.yaml",
			Snapshot: stats.Snapshot{
				Direct: 64, DirectFailed: 51,
				Rules: []stats.RuleOutcome{{Source: "user_direct", Rule: "*.qq.com", Attempts: 64, Failures: 51}},
			},
		},
		DirectEgress: tristate.False,
	}
	var named, egress *Check
	checks := trafficChecks(facts)
	for i, c := range checks {
		switch c.Name {
		case TrafficFailingRulesCheckName:
			named = &checks[i]
		case directEgressCheckName:
			egress = &checks[i]
		}
	}
	if named == nil {
		t.Fatal("没点名那条规则")
	}
	if strings.Contains(named.Hint, "改 /etc/bx/config.yaml") {
		t.Errorf("直连出不去却建议改规则 —— 用户会删掉一条正确的规则:%q", named.Hint)
	}
	if !strings.Contains(named.Hint, "不是你的规则") {
		t.Errorf("没说清不是规则的问题:%q", named.Hint)
	}
	if egress == nil {
		t.Fatal("没有单独说一句直连出不去 —— 它是上面那些失败的共同原因")
	}
}

// **只有明确观测到 False 才改口。** 把「没问出来」当成「就是它」,是反过来的
// 同一个错:一台规则真的写错了的机器上,用户会被告知「不是你的规则」。
func TestDoctorKeepsTheRuleHintWhenEgressIsUnknown(t *testing.T) {
	for _, egress := range []tristate.Tristate{tristate.Unknown, tristate.True} {
		facts := &TrafficFact{
			Report: stats.Report{
				ConfigPath: "/etc/bx/config.yaml",
				Snapshot: stats.Snapshot{
					Rules: []stats.RuleOutcome{{Source: "user_direct", Rule: "*.bad.example", Attempts: 20, Failures: 20}},
				},
			},
			DirectEgress: egress,
		}
		for _, c := range trafficChecks(facts) {
			if c.Name == TrafficFailingRulesCheckName && !strings.Contains(c.Hint, "/etc/bx/config.yaml") {
				t.Errorf("egress=%v 时不该改口:%q", egress, c.Hint)
			}
			if c.Name == directEgressCheckName {
				t.Errorf("egress=%v 却报了直连出不去", egress)
			}
		}
	}
}

// ——— 这一整块是这次修复真正的那一半:「没查」不许读成「查了、没问题」———

// **一份没采到流量事实的报告,在用户看得见的东西上必须与「查了、一切正常」
// 分得开。**
//
// 这就是这次要修的缺陷的一般形式:升级之后菜单的 Checks 页走 /v1/doctor,
// 而那条路上没有人采流量成败 —— 页面顶上一句加粗的 `0 failed · 0 warnings`
// 把「一整类结论从没检查过」说成了「你的机器很好」。
//
// 断言打在**渲染得出来的东西**上(每行的 name/status/detail,以及并排发布的
// NotChecked 计数),不是打在 Facts 上:后者在两种情形下当然不同,而那正是
// 「守卫钉住的是缺陷旁边的东西」。
func TestJudgeMakesUncheckedTrafficLookDifferentFromHealthyTraffic(t *testing.T) {
	base := Facts{Version: "test", ConfigPath: "/etc/bx/config.yaml", Config: FileFact{Mode0600: tristate.True}, Parsed: healthyConfig()}

	absent := Judge(base)
	healthy := base
	healthy.Traffic = &TrafficFact{Report: stats.Report{Snapshot: stats.Snapshot{Direct: 45, Proxy: 300}}}
	checked := Judge(healthy)

	if strings.Join(renderRows(absent), "\n") == strings.Join(renderRows(checked), "\n") {
		t.Fatal("没采到流量事实的报告与「查了、一切正常」的报告逐行相同 —— " +
			"用户没有任何办法分辨「查过了」和「压根没查」")
	}
	// 具体分辨在哪儿:一边有一条 not_checked,另一边一条都没有。
	if absent.NotChecked != 1 {
		t.Errorf("没采到流量事实时 not_checked = %d,want 1(%v)", absent.NotChecked, renderRows(absent))
	}
	if checked.NotChecked != 0 {
		t.Errorf("查过了却报 not_checked = %d(%v)", checked.NotChecked, renderRows(checked))
	}
	// 而且那一行必须**在**:一条缺席的行没有人会想起来去找它。
	if findCheck(absent, TrafficOutcomesCheckName) == nil {
		t.Error("没采到流量事实时连一行都不说 —— 沉默与健康在输出上完全一样")
	}
	if got := findCheck(checked, TrafficOutcomesCheckName); got == nil || got.Status != "ok" {
		t.Errorf("查过且健康时那一行 = %+v,want status=ok", got)
	}
}

// **not_checked 不许把 OK 打成 false,也不许被算成 ok。**
//
// 前半句是刻意的取舍(见 Report.OK 那段注释):Core 没在跑时流量必然查不到,
// 而那正是用户 `bx down` 之后该有的样子 —— 判成 false 就是把一台正常机器说成
// 坏的(2026-09-10 真机上 guardian_dns 栽的那个形状)。后半句是这次修复的全部
// 意义:代价由 NotChecked 那个计数抵掉,它必须真的数得出来。
func TestNotCheckedDoesNotFailTheReportButIsCountedSeparately(t *testing.T) {
	rep := Judge(Facts{Version: "test", ConfigPath: "/etc/bx/config.yaml", Config: FileFact{Mode0600: tristate.True}, Parsed: healthyConfig()})
	if !rep.OK {
		t.Error("只有「没查」时 OK 变成了 false —— 一台用户自己关掉保护的机器会被说成坏的")
	}
	if rep.NotChecked == 0 {
		t.Fatal("有一条 not_checked 却数出 0 —— OK=true 于是成了一句安全的假话")
	}
	if rep.CountNotChecked() != rep.NotChecked {
		t.Errorf("发布的 NotChecked=%d 与实际数出来的 %d 不同", rep.NotChecked, rep.CountNotChecked())
	}
}

// healthyConfig 是一份不会产出任何 fail 的配置 —— 这几条测的是「没查」这一项
// 自己的影响,别的行必须全绿,否则断言的是别人的 fail。
func healthyConfig() *config.Config {
	cfg := &config.Config{Server: "bx://example"}
	cfg.UDP.Mode = "proxy"
	return cfg
}

func renderRows(rep Report) []string {
	rows := make([]string, 0, len(rep.Checks))
	for _, c := range rep.Checks {
		rows = append(rows, c.Status+"|"+c.Name+"|"+c.Detail+"|"+c.Hint)
	}
	return rows
}

func findCheck(rep Report, name string) *Check {
	for i, c := range rep.Checks {
		if c.Name == name {
			return &rep.Checks[i]
		}
	}
	return nil
}
