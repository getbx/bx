//go:build darwin

package guardian

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// parseProcArgs 解析 macOS 的 kern.procargs2 布局:
//
//	[4 字节 argc][可执行路径 NUL][若干填充 NUL][argv[0] NUL][argv[1] NUL]…
//
// 畸形输入一律报错,绝不返回「看起来合理的空结果」——那会让调用方以为这个进程
// 不像 Core,进而漏认一个真的 Core。
func parseProcArgs(raw []byte) (string, []string, error) {
	if len(raw) < 4 {
		return "", nil, errors.New("procargs2 too short")
	}
	argc := int(binary.NativeEndian.Uint32(raw[:4]))
	if argc == 0 {
		return "", nil, errors.New("procargs2 has argc=0")
	}
	rest := raw[4:]
	end := bytes.IndexByte(rest, 0)
	if end <= 0 {
		return "", nil, errors.New("procargs2 has no executable path")
	}
	executable := string(rest[:end])
	rest = rest[end:]
	for len(rest) > 0 && rest[0] == 0 { // 路径与 argv[0] 之间的填充
		rest = rest[1:]
	}
	var argv []string
	for i := 0; i < argc; i++ {
		if len(rest) == 0 {
			return "", nil, errors.New("procargs2 incomplete: buffer exhausted before reading all argv")
		}
		end := bytes.IndexByte(rest, 0)
		if end < 0 {
			// 没有找到 NUL 结尾——这是截断缓冲区的标志
			return "", nil, errors.New("procargs2 incomplete: final argv item missing NUL terminator")
		}
		argv = append(argv, string(rest[:end]))
		rest = rest[end+1:]
	}
	return executable, argv, nil
}

// looksLikeCore 判定一个进程是不是 bx 的 Core。
//
// **刻意不依赖具体可执行路径。** 更新之后旧版 Core 跑在 runtime/<旧版本>/bx 下,
// 用「路径 == 当前 Core 路径」做判据会漏认它,于是起第二个 Core——正是 af81632
// 被回退的那个双 Core 风险。
//
// 也刻意偏向过度匹配:多认一个的后果是拒绝启动(安全),漏认一个是灾难。用户手工
// `sudo bx run` 会被认出来,而那本来就是一个 Core,认出来是对的。
func looksLikeCore(executable string, argv []string, uid int) bool {
	if uid != 0 {
		return false
	}
	if len(argv) < 2 || argv[1] != "run" {
		return false
	}
	if filepath.Base(executable) == "bx" {
		return true
	}
	return filepath.Base(argv[0]) == "bx"
}

// scanRunningCores 枚举系统里所有在跑 bx Core 的进程。
//
// 单个进程读不出信息(权限、刚退出)就跳过它,不让整次扫描失败——但**整体
// sysctl 失败必须报错**,由调用方保持 fail-closed。
//
// 两条判据是「没查到 Core」与「查不出来」的分界线,都必须报错而不是安静地
// 返回空列表——空列表在调用方(resolveOrphanLaunchMarker)眼里就是「确认没有
// Core,可以自愈」,一旦被误当成真的查过,后果是把一个真实存在的孤儿 launch
// marker 判定为安全,而实际上系统里可能正跑着 Core(真机 2026-08-07 复审时
// 用非 root 身份测出:874 个进程里 305 个 procargs2 读不出、其余 uid 不含
// root,结果是「0 个 Core、error=nil」——与「真的没有 Core」在返回值上完全
// 无法区分):
//   - **非 root 调用方**:root 跑的 Core 的 procargs2/ucred 对非 root 不可读,
//     这个身份本身就没有资格得出「没有 Core」的结论,不管扫到什么都不可信。
//   - **一个都没读成功**(enumerated>0 但 readable==0):不管是不是 root,这
//     种系统状态本身就反常,不能把「读不出任何进程参数」悄悄折叠成「没有
//     bx 进程」。
//
// 这条下限的判定抽在 decideCoreScan(纯函数,procscan.go)里,syscall 这一半
// 单测造不出来,而那条下限必须被钉住。
//
// **僵尸被显式排除**:见 isZombieProcess —— 崩溃重启路径靠它才不会被刚死的
// 旧 Core 卡住。
func scanRunningCores() ([]Process, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("scan for running Core processes requires root privileges (non-root cannot see a root-owned Core)")
	}
	procs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("enumerate processes: %w", err)
	}
	var cores []Process
	readable := 0
	for i := range procs {
		pid := int(procs[i].Proc.P_pid)
		if pid <= 0 {
			continue
		}
		if isZombieProcess(procs[i].Proc.P_stat) {
			continue // 已经退出、只等回收:不持有 socket 也不持有路由
		}
		raw, err := unix.SysctlRaw("kern.procargs2", pid)
		if err != nil {
			continue // 权限不足或进程刚退出:跳过这一个
		}
		executable, argv, err := parseProcArgs(raw)
		if err != nil {
			continue
		}
		readable++
		uid := int(procs[i].Eproc.Ucred.Uid)
		if !looksLikeCore(executable, argv, uid) {
			continue
		}
		cores = append(cores, Process{PID: pid, Executable: executable, UID: uid})
	}
	// 普查数据只有这里拿得到,而放行一次 fork 的那条日志(guardian_orphan_launch_marker /
	// guardian_no_core_record)最需要它来判断「这个结论是查了多少个进程得出的」。
	log.Printf("guardian_core_scan enumerated=%d readable=%d cores=%d", len(procs), readable, len(cores))
	return decideCoreScan(len(procs), readable, cores)
}
