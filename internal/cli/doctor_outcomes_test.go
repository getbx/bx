package cli

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/stats"
)

// **「问不出来」不是「一切正常」。**
//
// 少了这一行,一台 Core 没在跑的机器与一台完全健康的机器,在 doctor 的这一段里
// 逐字节相同 —— 与本仓库 Tristate 同一条纪律。
func TestDoctorSaysWhenItCouldNotAsk(t *testing.T) {
	checks := doctorOutcomeChecks(trafficFacts{Err: errors.New("dial unix: connection refused")})
	if len(checks) != 1 {
		t.Fatalf("问不出来时给了 %d 行:%+v", len(checks), checks)
	}
	if checks[0].Status == "ok" {
		t.Fatal("没问到却报 ok")
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
	checks := doctorOutcomeChecks(trafficFacts{Report: stats.Report{Snapshot: stats.Snapshot{
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
	checks := doctorOutcomeChecks(trafficFacts{Report: stats.Report{
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

// **总状态的门槛必须与点名的门槛一致。**
//
// 看失败总数会让正常机器长期显示 warn:任何一台机器上都有连不上的目标(对方
// 关机、域名过期、网络抖动),而那既不是 bx 的问题也不是用户能处置的。更糟的是
// 会出现「总状态 warn 而下面一条都没有」—— 用户看着一个警告,找不到它在说什么。
func TestDoctorDoesNotWarnOnBackgroundFailures(t *testing.T) {
	checks := doctorOutcomeChecks(trafficFacts{Report: stats.Report{Snapshot: stats.Snapshot{
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
	checks := doctorOutcomeChecks(trafficFacts{Report: stats.Report{Snapshot: stats.Snapshot{
		Proxy: 100,
		Rules: []stats.RuleOutcome{
			{Source: "udp_proxy", Attempts: 10},
			{Source: "udp_proxy_fallback", Attempts: 90},
		},
	}}})
	var found bool
	for _, c := range checks {
		if c.Name == "udp" && strings.Contains(c.Detail, "加速档") {
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
	source := mustReadRepoFile(t, "doctor_outcomes.go")
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

// **接线必须是那一个表达式,不许拆开。**
//
// `doctorOutcomeChecks(supervisor.FetchStatusReport(path))` 用 Go 的多返回值
// 直接当实参列表,于是「问不出来」在结构上传不丢。拆成两句之后就能写
// `doctorOutcomeChecks(rep, nil)` —— 编译得过、纯函数的测试全绿,而一台 Core
// 没在跑的机器会被报成一切正常。这条守卫钉的就是那个形式。
func TestDoctorTrafficWiringCannotDropTheError(t *testing.T) {
	source := mustReadRepoFile(t, "cli.go")
	const wired = "doctorOutcomeChecks(doctorTrafficFacts(c.Context))"
	if !strings.Contains(source, wired) {
		t.Fatalf("doctor 的流量接线不再是那个单表达式 —— 「问不出来」会被压成一个具体答案")
	}
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
	facts := trafficFacts{
		Report: stats.Report{
			ConfigPath: "/etc/bx/config.yaml",
			Snapshot: stats.Snapshot{
				Direct: 64, DirectFailed: 51,
				Rules: []stats.RuleOutcome{{Source: "user_direct", Rule: "*.qq.com", Attempts: 64, Failures: 51}},
			},
		},
		DirectEgress: observe.False,
	}
	var named, egress *checkReport
	for i, c := range doctorOutcomeChecks(facts) {
		switch c.Name {
		case "failing rule":
			named = &doctorOutcomeChecks(facts)[i]
		case "direct egress":
			egress = &doctorOutcomeChecks(facts)[i]
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
	for _, egress := range []observe.Tristate{observe.Unknown, observe.True} {
		facts := trafficFacts{
			Report: stats.Report{
				ConfigPath: "/etc/bx/config.yaml",
				Snapshot: stats.Snapshot{
					Rules: []stats.RuleOutcome{{Source: "user_direct", Rule: "*.bad.example", Attempts: 20, Failures: 20}},
				},
			},
			DirectEgress: egress,
		}
		for _, c := range doctorOutcomeChecks(facts) {
			if c.Name == "failing rule" && !strings.Contains(c.Hint, "/etc/bx/config.yaml") {
				t.Errorf("egress=%v 时不该改口:%q", egress, c.Hint)
			}
			if c.Name == "direct egress" {
				t.Errorf("egress=%v 却报了直连出不去", egress)
			}
		}
	}
}
