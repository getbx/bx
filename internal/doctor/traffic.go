package doctor

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/tristate"
)

// `bx doctor` 一直只答得出「装没装好」——**对「流量到底成不成」一个字都不说**。
//
// 这不是补一栏好看的数字。2026-08-13 那个 bug(macOS 的 DirectDialer 从来到不了
// 公网,十条用户规则全 ENETUNREACH)存在了很久,而它被发现的唯一原因是 bx 终于
// 开始**数**失败。
//
// **这一段判据 2026-09-12 从 internal/cli 搬进本包。** 搬家的理由是一次静默的
// 退化:它此前只长在 `bx doctor` 的**文本**路径上(cli.go 那个 for 循环),而
// 菜单的「Check for Problems」自从 Guardian 声明 `doctor` 能力起就改走
// `/v1/doctor` → `doctor.Judge`。也就是说升级之后,那台报着
// `*.qq.com 1291 条失败 1289` 的机器在 Checks 页上看到的是一句加粗的
// `0 failed · 0 warnings` —— **不是少了一条结论,是那条结论被一句相反的话顶掉了。**
// 判据只有一份、三个消费方(文本 / --json / 菜单)共用它,这类退化才不可能再发生。
//
// 与 `bx status` 那一段仍然**同源**(stats.FailingRules / stats.UDPNotice),
// 不另写一份阈值 —— 两处各写一份判据是这个仓库反复栽的形状。

// StatusNotChecked 是 ok/warn/fail/info 之外的第四种状态:**这一项根本没查**。
//
// 它必须与 ok 分开,也必须与 warn 分开:
//   - 当成 ok(或者干脆不产出这一行),「没查」与「查了、没问题」在用户眼里逐字
//     相同 —— 这正是本仓库被罚得最狠的那种失效(leakcheck 的 NotChecked、
//     rulereview 的 BuiltinListChecked、observe 的 Tristate,全是同一条);
//   - 当成 warn,一台用户自己 `bx down` 的机器会挂着一个找不到出处的警告,而
//     那台机器完全正常(2026-09-10 真机上 guardian_dns 就栽过这个)。
//
// 它**不让 Report.OK 变 false**(见 Report.OK 头上那段),消费方靠
// Report.NotChecked 那个计数看见它。
const StatusNotChecked = "not_checked"

// 流量那几行的 check 名。**agent 与 MCP 按名字取,名字变了就是接口变了。**
const (
	// TrafficOutcomesCheckName 恒在:查了报数字,没查报 not_checked ——
	// 这一行**永远不缺席**是「没查」能被看见的前提(缺席的行没人会想起来找)。
	TrafficOutcomesCheckName = "traffic_outcomes"
	// TrafficFailingRulesCheckName 是**恰好一条**,不是每条规则一行 ——
	// 见 trafficChecks 里那段。
	TrafficFailingRulesCheckName = "traffic_failing_rules"
	directEgressCheckName        = "direct_egress"
	udpTrafficCheckName          = "udp_traffic"
)

// TrafficFact 是数据面成败这一格的事实。**判断一句都不在这里** —— 采集方
// (internal/cli 与 internal/guardian)各自填它,判断全在 trafficChecks。
//
// **Facts.Traffic 为 nil 与「Err 非空」是两件事**,故用指针:
//   - nil = 这条采集路径**根本没问**(某个采集方没填这份事实);
//   - Err 非空 = 问了,没问到(Core 没在跑 / 拨不通)。
//
// 两者都产出 not_checked,但措辞不同 —— 前者是 bx 自己漏了一步,后者是机器状态。
type TrafficFact struct {
	// Report 是 Core 的统计快照;Err 非空时它是零值,不许当真。
	Report stats.Report
	// Err 非空 = 没问到。**它是字符串不是 error**:Facts 是数据,不是接口。
	Err string
	// DirectEgress 是「bx 自己的直连出不出得去」。三态,零值 Unknown ——
	// 问不出来时不许倒向任何一边(见 failingRuleHint 为什么这很要紧)。
	DirectEgress tristate.Tristate
}

// trafficChecks 把数据面的成败翻成 doctor 的几行。
//
// **一切正常时也说一句。** 这与 status 那边「正常就闭嘴」的纪律不同,而且是
// 刻意的:status 是常驻面板,一行永远在的字会变成墙纸;doctor 是人**主动问**
// 「哪里不对」的时候跑的,那时「直连 45 条,0 失败」是一个有价值的答案 ——
// 它把「这条路是好的」从猜测变成观测,而排查最耗时的一步正是排除。
func trafficChecks(f *TrafficFact) []Check {
	if f == nil {
		// **这一支是 bx 自己漏了一步,不是机器的毛病**,所以不给 hint ——
		// 没有哪条命令是用户该敲的。措辞点名「这一版」,让读到的人知道该去
		// 看采集方而不是去修自己的网络。
		return []Check{{
			Name: TrafficOutcomesCheckName, Status: StatusNotChecked,
			Detail: "This path did not collect traffic outcomes, so nothing below says anything about rule failures",
		}}
	}
	if f.Err != "" {
		// **「问不出来」不是「一切正常」。** 没有这一行的话,一台 Core 没在跑的
		// 机器与一台完全健康的机器,在 doctor 的这一段里逐字节相同。
		return []Check{{
			Name: TrafficOutcomesCheckName, Status: StatusNotChecked,
			Detail: "Could not ask (is Core running?): " + f.Err,
			Hint:   "" + elevate.Prefix + "bx up",
		}}
	}
	report := f.Report
	checks := []Check{{
		Name:   TrafficOutcomesCheckName,
		Status: outcomeStatus(report),
		Detail: fmt.Sprintf("direct %d (%d failed) · proxy %d (%d failed)",
			report.Direct, report.DirectFailed, report.Proxy, report.ProxyFailed),
	}}
	// 点名成片失败的用户规则:它们是用户**改得了**的那几行,而这正是 doctor
	// 最该给出的东西 —— 一个可执行的下一步,不是一个百分比。
	//
	// **全部规则合并成恰好一条 check**,与 riskyRuleFinding 同一条理由:check 名
	// 的整个存在理由是「按名字取」,每条规则各产出一条同名 check 会让 --json 的
	// 消费方(agent/MCP)只拿到其中一条,静默丢掉其余 —— 55ef8ea 那次事故的形状,
	// 仓库级守卫 TestDoctorReportHasNoDuplicateCheckNames 正盯着它。
	// **详情不截断**:少报一条规则等于没报那一条,而这一类恰恰是要人去改配置的。
	if failing := report.FailingRules(); len(failing) > 0 {
		named := make([]string, 0, len(failing))
		for _, rule := range failing {
			pct := float64(rule.Failures) / float64(rule.Attempts) * 100
			named = append(named, fmt.Sprintf("%s: %d of %d failed (%.0f%%)", rule.Rule, rule.Failures, rule.Attempts, pct))
		}
		checks = append(checks, Check{
			Name:   TrafficFailingRulesCheckName,
			Status: "fail",
			Detail: fmt.Sprintf("%d rule(s) failing in bulk — %s", len(failing), strings.Join(named, "、")),
			Hint:   failingRuleHint(report.ConfigPath, f.DirectEgress),
		})
	}
	// **直连出不去时,单独说一句。** 它是上面那些失败的**共同原因**,
	// 而不是又一条并列的坏消息。
	if f.DirectEgress == tristate.False {
		checks = append(checks, Check{
			Name: directEgressCheckName, Status: "fail",
			Detail: "bx's own direct dialer cannot get out (the scoped default route is gone)",
			Hint:   directEgressHint,
		})
	}
	if notice := report.UDPNotice(); notice != "" {
		checks = append(checks, Check{Name: udpTrafficCheckName, Status: "warn", Detail: notice})
	}
	return checks
}

// outcomeStatus 只看**有没有值得报的失败规则**,不看失败总数。
//
// 看总数会让一台正常机器长期显示 warn:任何一台机器上都有连不上的目标(对方
// 关机、域名过期、网络抖动),而那不是 bx 的问题也不是用户能处置的。
// 报告的门槛必须与点名的门槛一致,否则会出现「总状态是 warn 而下面一条都没有」
// —— 用户看着一个警告却找不到它在说什么。
func outcomeStatus(report stats.Report) string {
	if len(report.FailingRules()) > 0 {
		return "fail"
	}
	if report.UDPNotice() != "" {
		return "warn"
	}
	return "ok"
}

// failingRuleHint 给出下一步 —— **而下一步取决于坏的是谁**。
//
// 直连出不去时,失败的**不是这条规则**:bx 自己的直连器到不了公网(macOS 上
// 那条 scoped 默认路由不见了),于是每一条 direct 规则都在失败。此时若照旧建议
// 「改 rules」,用户会去删掉一条**完全正确**的规则,而问题原样留着 ——
// 一个在最需要它的时候把人指向错误方向的诊断,比不给建议更糟。
//
// **只有明确观测到 False 才改口。** Unknown(没观测到 / 非 darwin / Guardian
// 那侧没有这个原语)时维持原样:把「没问出来」当成「就是它」,是反过来的同一个错。
func failingRuleHint(configPath string, directEgress tristate.Tristate) string {
	if directEgress == tristate.False {
		return directEgressHint
	}
	where := configPath
	if where == "" {
		where = "the config file"
	}
	return fmt.Sprintf("That path is dead; edit the rules in %s, then "+elevate.Prefix+"bx down && "+elevate.Prefix+"bx up", where)
}

// directEgressHint 是那条真正的下一步。**不提改规则** —— 规则是好的。
const directEgressHint = "Not your rules: bx's own direct dialer cannot get out, so every direct rule will fail. " +
	"Reinstall the routes: " + elevate.Prefix + "bx down && " + elevate.Prefix + "bx up"
