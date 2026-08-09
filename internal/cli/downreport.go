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
		if result.Cause != nil {
			stderr = append(stderr, fmt.Sprintf("⚠️  Guardian 正常关闭事务失败(%v),已改走强制停止。", result.Cause))
		} else {
			stderr = append(stderr, "⚠️  Guardian 未响应,已改走强制停止。")
		}
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
