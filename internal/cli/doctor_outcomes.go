package cli

import (
	"fmt"

	"github.com/getbx/bx/internal/stats"
)

// `bx doctor` 一直只答得出「装没装好」——**对「流量到底成不成」一个字都不说**。
//
// 这不是补一栏好看的数字。2026-08-13 那个 bug(macOS 的 DirectDialer 从来到不了
// 公网,十条用户规则全 ENETUNREACH)存在了很久,而它被发现的唯一原因是 bx 终于
// 开始**数**失败。数出来之后那个数字只出现在 `bx status` 里,还要过阈值才显示;
// 而人在出问题时敲的是 `doctor`。
//
// 判据与 `bx status` 那一段**同源**(stats.FailingRules / stats.UDPNotice),
// 不另写一份 —— 两处各写一份判据是这个仓库反复栽的形状。

// doctorOutcomeChecks 把数据面的成败翻成 doctor 的几行。
//
// **一切正常时也说一句。** 这与 status 那边「正常就闭嘴」的纪律不同,而且是
// 刻意的:status 是常驻面板,一行永远在的字会变成墙纸;doctor 是人**主动问**
// 「哪里不对」的时候跑的,那时「直连 45 条,0 失败」是一个有价值的答案 ——
// 它把「这条路是好的」从猜测变成观测,而排查最耗时的一步正是排除。
// **它收 (report, err) 而不是 (report, bool),是为了让接线在结构上不出错。**
//
// 收 bool 时,调用方可以写 `doctorOutcomeChecks(rep, true)` —— 编译得过、
// 测试全绿,而「问不出来」被悄悄压成了「一切正常」;这个仓库记录过同一形状
// (`core.answering ?? false` 把三态压回二值)。收 error 之后,生产调用点就是
// `doctorOutcomeChecks(supervisor.FetchStatusReport(path))` 那一个表达式 ——
// Go 的多返回值直接当实参列表,想撒谎得先把它拆成两句,而那是看得见的。
func doctorOutcomeChecks(report stats.Report, err error) []checkReport {
	if err != nil {
		// **「问不出来」不是「一切正常」。** 没有这一行的话,一台 Core 没在跑的
		// 机器与一台完全健康的机器,在 doctor 的这一段里逐字节相同。
		return []checkReport{{
			Name: "traffic outcomes", Status: "warn", Detail: "没问到(Core 没在跑?)",
			Hint: "sudo bx up",
		}}
	}
	checks := []checkReport{{
		Name:   "traffic outcomes",
		Status: outcomeStatus(report),
		Detail: fmt.Sprintf("直连 %d(失败 %d)· 代理 %d(失败 %d)",
			report.Direct, report.DirectFailed, report.Proxy, report.ProxyFailed),
	}}
	// 点名成片失败的用户规则:它们是用户**改得了**的那几行,而这正是 doctor
	// 最该给出的东西 —— 一个可执行的下一步,不是一个百分比。
	for _, rule := range report.FailingRules() {
		pct := float64(rule.Failures) / float64(rule.Attempts) * 100
		checks = append(checks, checkReport{
			Name:   "failing rule",
			Status: "fail",
			Detail: fmt.Sprintf("%s:%d 条里失败 %d(%.0f%%)", rule.Rule, rule.Attempts, rule.Failures, pct),
			Hint:   failingRuleHint(report.ConfigPath),
		})
	}
	if notice := report.UDPNotice(); notice != "" {
		checks = append(checks, checkReport{Name: "udp", Status: "warn", Detail: notice})
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

func failingRuleHint(configPath string) string {
	where := configPath
	if where == "" {
		where = "配置文件"
	}
	return fmt.Sprintf("这条路已经不通;改 %s 的 rules 后 sudo bx down && sudo bx up", where)
}
