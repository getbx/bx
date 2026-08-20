package appattr

import (
	"path"
	"strings"
)

// ChooseOwner 决定一条 socket 记录该算在哪个进程头上。
//
//	so_e_pid 有值且**进程还活着** → 用它(代劳者归回真正的发起方)
//	否则                          → so_last_pid(也要活着)
//	都不行                        → unknown
//
// **活性检查不是可选的。** spike 真机撞到过 trustd 的 so_e_pid 指向一个早已退出
// 的 7961:委托方死了之后那个字段就是个陈旧值,照用会把流量记在不存在的应用上,
// 而这种错误在界面上完全看不出来。
func ChooseOwner(pcb PCB, alive func(int32) bool) (int32, bool) {
	if alive == nil {
		return 0, false
	}
	for _, pid := range [...]int32{pcb.EPID, pcb.LastPID} {
		if pid > 0 && alive(pid) {
			return pid, true
		}
	}
	return 0, false
}

// DisplayName 把可执行路径变成用户认得的名字。
//
// 取**最外层**的 .app bundle 名:Chrome / Claude 这类多进程应用的 helper 住在
// 内层 bundle 里(…/Claude.app/Contents/Frameworks/Claude Helper.app/…),
// 取内层会让一个应用散成一堆看不懂的行。没有 .app 就取 basename。
func DisplayName(execPath string) string {
	execPath = strings.TrimSpace(execPath)
	if execPath == "" {
		return ""
	}
	if i := strings.Index(execPath, ".app/"); i >= 0 {
		return path.Base(execPath[:i])
	}
	if strings.HasSuffix(execPath, ".app") {
		return path.Base(strings.TrimSuffix(execPath, ".app"))
	}
	return path.Base(execPath)
}
