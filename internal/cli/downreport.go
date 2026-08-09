package cli

import "fmt"

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
	stdout = append(stdout, "✅ bx 已停止并取消开机自启。")
	return stdout, stderr
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
