package cli

import (
	"fmt"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/guardian"
)

// downReportLines produces the lines `bx down` shows the user, split into
// stdout and stderr exactly as macOSDownAction prints them. It is pure —
// no printing — so the wording that is the user's only guidance at the
// worst possible moment (Guardian unreachable or permanently refusing, the
// forced teardown having run instead of the clean shutdown) can be pinned
// by tests. The wording itself must not be edited here without also
// updating internal/cli/downreport_test.go: it deliberately never claims
// the network was restored on the forced path, because six best-effort
// steps cannot promise that.
func downReportLines(result macOSDownResult) (stdout []string, stderr []string) {
	// 「两条意图都没写成」必须自己占一行,而且在最前面。
	//
	// 它与下面每一条都正交:保护可能干净地停了、Guardian 可能一切正常,而盘上
	// 仍然既没有维护挂起也没有 desired=off。macOSDownLifecycleFor 同时返回一个
	// error,但**这一行是给忽略 error 的调用方留的** —— 这个文件顶上那条既有的
	// 教训(零值 result 会平静地渲染成「✅ bx 已停止」)说的正是这种调用方。
	if result.IntentUnrecorded != nil {
		stderr = append(stderr, fmt.Sprintf(
			"⚠️  Could not record the intent to stop (neither the maintenance hold nor desired=off was written): %v; protection may still start itself on the next boot.",
			result.IntentUnrecorded,
		))
	}
	// **HoldFallback 不在这里渲染,这是有意的。**
	//
	// 退回只发生在升级那条来由上(recordStopIntent 对 downPurposeUser 直接返回),
	// 而这个函数唯一的生产调用方是 macOSDownAction —— 也就是 `bx down`,
	// downPurposeUser。在这里写一支分支,就是写一段生产永远走不到的代码,再配一条
	// 「自己把两头接起来」的测试;这一期已经抓到过同样的形状。
	// 升级那条路的渲染住在 runUpgrade(holdFallbackWarning),那里才有真的 producer。
	if result.Forced {
		stderr = append(stderr, "⚠️  "+forcedTeardownReason(result)+".")
		// 如实描述做过的动作,不断言"网络已还原"——是否真的恢复要用户自己确认。
		stdout = append(
			stdout,
			"Done: recorded the intent to stop (no more start-at-boot), asked Core to exit (so it restores the routes it installed), stopped the Guardian service, removed the barrier blocking routes, restored system DNS.",
			"Please check whether your network is back (bx status, or just open any web page). If it is still broken, run "+elevate.Prefix+"bx uninstall (it keeps /etc/bx).",
		)
		return stdout, stderr
	}
	// 干净路径**也不能无条件说「已停止」**。Guardian 现在会在报告 off 之前向系统
	// 求证还有没有 Core 在跑(它自己的记账看不见 legacy Core、看不见 sudo bx run
	// 起的 Core、更看不见手删过 core-process.json 的机器),求证不过就发
	// needs_attention。那份判断此前只有菜单栏读得到 —— 菜单读 protection_state,
	// 而这里从不看 result.Status,于是 bx down 照样打印 ✅。
	// 机制建好而最后一寸没接,等于没建。
	if !downConfirmedStopped(result) {
		stderr = append(stderr, "⚠️  Guardian could not confirm that protection is off ("+downUnconfirmedReason(result)+").")
		stdout = append(
			stdout,
			"The teardown steps all ran, but a bx Core process may still be running on this system (from another terminal's "+elevate.Prefix+"bx run, say, or left over from an older version).",
			// **不要让用户去跑一条看不到答案的命令。** 这里曾写「请执行 bx status 查看原因」,
			// 而 bx status 根本不显示 last_error —— 那是把人支进死胡同。具体 PID 只在
			// Guardian 日志里(刻意不进 Status:扫到的进程是「疑似」,把第三方 PID 放进
			// Status 是 7778b53 专门修掉的错)。
			"To see which process: sudo tail -50 /var/log/bx-guard.err.log. Once you are satisfied, "+elevate.Prefix+"bx uninstall clears everything out.",
		)
		return stdout, stderr
	}
	stdout = append(stdout, "✅ bx stopped, and start-at-boot is off.")
	return stdout, stderr
}

// holdFallbackWarning 是「没能武装维护挂起,已退回写 desired=off」那一行的措辞。
//
// 它住在这个文件(而不是 upgraderun.go)只为一件事:与它旁边那几行 `bx down`
// 的文案受同一份约束 —— 它们是用户在最糟糕的时刻唯一的指引,改字要连着测试一起改。
//
// **它不是失败**:保护干净地停了、升级会照常走完。它说的是盘上留下的那句
// desired=off 会撒谎,以及那句谎在升级结束时会被纠正。
func holdFallbackWarning(cause error) string {
	return fmt.Sprintf(
		"Could not arm the maintenance hold (%v), so desired=off was recorded instead: during the upgrade bx status will say \"off\" rather than \"on hold\"; "+
			"protection is turned back on when the upgrade finishes.",
		cause,
	)
}

// downConfirmedStopped 报告 Guardian 是否**确认**保护已经关闭。
//
// 空 Protection 视为已确认,只是为了让零值 result 仍渲染成干净成功(既有测试钉着
// 那个形状)。真实的 200 应答一定带 protection_state,所以这条在生产里不可达 ——
// 但它是「把未知塌缩成好消息」的同一形状,别在别处照抄。
func downConfirmedStopped(result macOSDownResult) bool {
	p := result.Status.Protection
	return p == "" || p == guardian.ProtectionOff
}

// downUnconfirmedReason 给用户一个能查的理由:优先用 Guardian 报的失败码
// (core_still_running / core_scan_failed),它比 protection_state 具体得多。
func downUnconfirmedReason(result macOSDownResult) string {
	if code := result.Status.LastError; code != "" {
		return code
	}
	return result.Status.Protection
}

// forcedTeardownReason 说明**为什么**走了强制路径,不带句尾标点 —— 调用方各自
// 接自己的下文(bx down 直接句号;升级路径接「;请自行确认网络是否恢复」)。
//
// 它是纯函数且**必须是唯一的一份**:升级路径(appinstall_darwin.go)此前内联着
// 自己的副本,于是 legacy 那条分支一加,升级就开始打印「Guardian 未响应」——
// 而 Guardian 明明应答了,是我们主动选的重路径。同一句话散成两份,修一份就是
// 修一半;这个函数存在的意义就是让那种半修不可能发生。
func forcedTeardownReason(result macOSDownResult) string {
	switch {
	case result.LegacyCore:
		// 措辞用「可能」是如实的:探查失败时我们同样走这条路,那时确实只是
		// 不能排除,而不是确知有。
		return "An older Core outside Guardian's control may be running, so the forced teardown was used instead (it is the only path that can stop it)"
	case result.Cause != nil:
		return fmt.Sprintf("Guardian's clean shutdown failed (%v), so the forced teardown was used instead", result.Cause)
	default:
		return "Guardian did not respond, so the forced teardown was used instead"
	}
}
