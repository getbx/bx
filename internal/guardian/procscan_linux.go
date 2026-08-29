//go:build linux

package guardian

import (
	"errors"
	"log"
	"os"
)

// scanRunningCores 的 Linux 实现:/proc 树扫描(纯 I/O 半在
// procscan_proctree.go,fixture 可测),判 Core 用与 darwin 共用的
// looksLikeCore,下限用共用的 decideCoreScan。
//
// **root 门槛在 Linux 上依然成立,理由换了一个**:darwin 是「非 root 读不出
// root 进程的 procargs2」;Linux 的 cmdline/status 通常人人可读,但 /proc
// 挂了 hidepid 时非 root 看不见别人的进程——而「看不见」与「没有」在返回值上
// 无法区分,正是这套扫描最忌讳的假「全清」。Guardian 本来就只以 root 跑,
// 收紧没有代价。
func scanRunningCores(reason string) ([]Process, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("scan for running Core processes requires root privileges (hidepid can blind a non-root scan, and blind reads as clean)")
	}
	enumerated, readable, cores := scanLinuxProcTree("/proc")
	if enumerated == 0 {
		return nil, errors.New("enumerate processes: /proc listed no processes")
	}
	// 与 darwin 同一行普查:放行一次 fork 的日志需要它判断「这个结论是查了
	// 多少个进程得出的」,reason 区分准入路径与调谐循环(频次差两个数量级)。
	log.Printf("guardian_core_scan reason=%s enumerated=%d readable=%d cores=%d", reason, enumerated, readable, len(cores))
	return decideCoreScan(enumerated, readable, cores)
}
