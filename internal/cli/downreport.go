package cli

import (
	"fmt"

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
			"⚠️  未能记录停机意图(维护挂起与 desired=off 都没写成): %v;下次开机可能仍会自动启动保护。",
			result.IntentUnrecorded,
		))
	}
	// 「退回写了 desired=off」同样要自己占一行,理由与上面那条一样,但**它不产生
	// error**:退回是成功路径的一种(保护干净地停了、升级会照常走完),所以
	// 没有任何调用方会因为它多说一个字。而它的后果实打实 —— 盘上留下的是
	// 「用户不想要保护」,而用户其实想要;那正是维护挂起这一期要消灭的谎。
	if result.HoldFallback != nil {
		stderr = append(stderr, fmt.Sprintf(
			"⚠️  未能武装维护挂起(%v),已退回记录 desired=off:升级期间 bx status 会显示「已关闭」而非「维护挂起」;"+
				"升级结束后保护会被重新打开。",
			result.HoldFallback,
		))
	}
	if result.Forced {
		stderr = append(stderr, "⚠️  "+forcedTeardownReason(result)+"。")
		// 如实描述做过的动作,不断言"网络已还原"——是否真的恢复要用户自己确认。
		stdout = append(
			stdout,
			"已执行:记录关闭意图(不再开机自启)、请求 Core 退出(由它自己还原它装的路由)、停止 Guardian 服务、删除屏障阻断路由、还原系统 DNS。",
			"请确认网络是否已恢复(例如 bx status 或打开任意网页);若仍不通,执行 sudo bx uninstall(会保留 /etc/bx 配置)。",
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
		stderr = append(stderr, "⚠️  Guardian 没能确认保护已经关闭("+downUnconfirmedReason(result)+")。")
		stdout = append(
			stdout,
			"拆除步骤已执行完,但系统里可能仍有 bx 的 Core 进程在跑(例如另一个终端里的 sudo bx run,或旧版本残留)。",
			// **不要让用户去跑一条看不到答案的命令。** 这里曾写「请执行 bx status 查看原因」,
			// 而 bx status 根本不显示 last_error —— 那是把人支进死胡同。具体 PID 只在
			// Guardian 日志里(刻意不进 Status:扫到的进程是「疑似」,把第三方 PID 放进
			// Status 是 7778b53 专门修掉的错)。
			"具体是哪个进程见:sudo tail -50 /var/log/bx-guard.err.log;确认无误后可用 sudo bx uninstall 彻底清理。",
		)
		return stdout, stderr
	}
	stdout = append(stdout, "✅ bx 已停止并取消开机自启。")
	return stdout, stderr
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
		return "可能有不受 Guardian 掌管的旧版 Core 在运行,已改走强制停止(只有这条路停得下它)"
	case result.Cause != nil:
		return fmt.Sprintf("Guardian 正常关闭事务失败(%v),已改走强制停止", result.Cause)
	default:
		return "Guardian 未响应,已改走强制停止"
	}
}
