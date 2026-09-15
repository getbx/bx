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
	enumerated, kernel, readable, cores := scanLinuxProcTree("/proc")
	if enumerated == 0 {
		return nil, errors.New("enumerate processes: /proc listed no processes")
	}
	// 与 darwin 同一行普查:放行一次 fork 的日志需要它判断「这个结论是查了
	// 多少个进程得出的」,reason 区分准入路径与调谐循环(频次差两个数量级)。
	log.Printf("guardian_core_scan reason=%s enumerated=%d kernel=%d readable=%d cores=%d",
		reason, enumerated, kernel, readable, len(cores))
	// **分母去掉内核线程。** 它们按构造不可能是 Core(没有用户态 argv),
	// 不是盲区;而 Linux 上它们常占进程表的四分之三 —— 算进分母会让
	// coreScanReadableFloor 那道门在一台完全正常的机器上恒假(那个 0.5 是按
	// darwin 的 891/892 定的,2026-09-15 CI 实测 ubuntu runner 是 43/169)。
	// 原始的 enumerated 仍然照发进普查日志:那一行的用处正是让人看得出
	// 「这个结论是查了多少个进程得出的」。
	return decideCoreScan(enumerated-kernel, readable, cores)
}
