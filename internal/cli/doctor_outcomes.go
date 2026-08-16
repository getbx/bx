package cli

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/observe"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
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
// trafficFacts 是这一段要的全部事实。
//
// **打包成一个结构体,是为了让接线保持「一个表达式」那个性质。**
// 上一版是 doctorOutcomeChecks(supervisor.FetchStatusReport(path)) —— Go 的
// 多返回值直接当实参列表,于是「问不出来」在结构上传不丢。加第三个参数会把它
// 拆成两句,而拆开之后就能写 `…, nil, …` 把错误悄悄丢掉(守卫当场抓到了这次
// 改动,守卫是对的)。改成 doctorOutcomeChecks(doctorTrafficFacts(ctx)),
// 那个性质就回来了。
type trafficFacts struct {
	Report stats.Report
	Err    error
	// DirectEgress 是「bx 自己的直连出不出得去」。三态,零值 Unknown ——
	// 问不出来时不许倒向任何一边(见 failingRuleHint 为什么这很要紧)。
	DirectEgress observe.Tristate
}

func doctorOutcomeChecks(f trafficFacts) []checkReport {
	report, err, directEgress := f.Report, f.Err, f.DirectEgress
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
			Hint:   failingRuleHint(report.ConfigPath, directEgress),
		})
	}
	// **直连出不去时,单独说一句。** 它是上面那些失败的**共同原因**,
	// 而不是又一条并列的坏消息。
	if directEgress == observe.False {
		checks = append(checks, checkReport{
			Name: "direct egress", Status: "fail",
			Detail: "bx 自己的直连出不去(scoped 默认路由不见了)",
			Hint:   directEgressHint,
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

// failingRuleHint 给出下一步 —— **而下一步取决于坏的是谁**。
//
// 直连出不去时,失败的**不是这条规则**:bx 自己的直连器到不了公网(macOS 上
// 那条 scoped 默认路由不见了),于是每一条 direct 规则都在失败。此时若照旧建议
// 「改 rules」,用户会去删掉一条**完全正确**的规则,而问题原样留着 ——
// 一个在最需要它的时候把人指向错误方向的诊断,比不给建议更糟。
//
// **只有明确观测到 False 才改口。** Unknown(没观测到 / 非 darwin)时维持原样:
// 把「没问出来」当成「就是它」,是反过来的同一个错。
func failingRuleHint(configPath string, directEgress observe.Tristate) string {
	if directEgress == observe.False {
		return directEgressHint
	}
	where := configPath
	if where == "" {
		where = "配置文件"
	}
	return fmt.Sprintf("这条路已经不通;改 %s 的 rules 后 sudo bx down && sudo bx up", where)
}

// directEgressHint 是那条真正的下一步。**不提改规则** —— 规则是好的。
const directEgressHint = "不是你的规则:bx 自己的直连出不去,每一条 direct rule 都会失败。" +
	"重装路由:sudo bx down && sudo bx up"

// doctorTrafficFacts 一次问齐:流量成败,以及直连出不出得去。
func doctorTrafficFacts(ctx context.Context) trafficFacts {
	report, err := supervisor.FetchStatusReport(statusSocketPath())
	return trafficFacts{Report: report, Err: err, DirectEgress: doctorDirectEgress(ctx)}
}

// doctorDirectEgress 问一次「bx 自己的直连出不出得去」。
//
// **观测失败一律 Unknown**,绝不倒向任何一边:判成 False 会让一台正常机器上
// 正确的「改规则」建议被换掉;判成 True 会让这次诊断继续把系统故障说成用户
// 的配置问题。
func doctorDirectEgress(ctx context.Context) observe.Tristate {
	if observerForDoctor == nil {
		return observe.Unknown
	}
	ctx, cancel := context.WithTimeout(ctx, doctorObserveTimeout)
	defer cancel()
	return observerForDoctor(ctx).DirectEgressOK
}

// doctorObserveTimeout 与 bx status 那边同值同理由:宁可少答一项,
// 也不能让一次观测把 doctor 挂住 —— 它是出问题时最先敲的命令之一。
const doctorObserveTimeout = 5 * time.Second

// observerForDoctor 可为 nil = 这个平台没有观测原语(见 observerForPlatform)。
var observerForDoctor = observerForPlatform(runtime.GOOS)
