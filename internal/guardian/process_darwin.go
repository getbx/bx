//go:build darwin

package guardian

import (
	"bytes"
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func inspectProcess(pid int) (Process, error) {
	if pid <= 0 {
		return Process{}, errors.New("process PID must be positive")
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) || errors.Is(err, unix.ENOENT) {
			return Process{}, fmt.Errorf("%w: PID %d", ErrProcessNotRunning, pid)
		}
		// macOS 的「进程不存在」不走 ESRCH:内核 sysctl 调用成功但写回 0 字节,
		// x/sys 因 n != SizeofKinfoProc 转成 EIO(syscall_darwin.go:513)。只认
		// ESRCH 会让 ErrProcessNotRunning 在本平台永不可达,Existing()/Start()
		// 的「已死即自愈」全成死代码,陈旧记录永久卡死 bx up(真机事故 2026-08-06)。
		//
		// 但 EIO 同样可能来自真实的 sysctl I/O 失败,这一层分辨不了。故不凭 EIO
		// 断言,改向内核求证:只有 kill(pid,0) 明确回 ESRCH 才判定进程不存在。
		// 其余情况(活着、EPERM、求证本身失败)一律保持不透明错误——误判「不存在」
		// 会放行第二个 Core,那正是本项目 fail-closed 要挡住的。
		if errors.Is(err, unix.EIO) && processConfirmedGone(pid) {
			return Process{}, fmt.Errorf("%w: PID %d", ErrProcessNotRunning, pid)
		}
		return Process{}, fmt.Errorf("inspect process PID %d: %w", pid, err)
	}
	if int(info.Proc.P_pid) != pid {
		return Process{}, fmt.Errorf("%w: PID %d", ErrProcessNotRunning, pid)
	}
	raw, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return Process{}, fmt.Errorf("read process executable: %w", err)
	}
	if len(raw) <= 4 {
		return Process{}, errors.New("process executable path missing")
	}
	pathBytes := raw[4:]
	if end := bytes.IndexByte(pathBytes, 0); end >= 0 {
		pathBytes = pathBytes[:end]
	}
	if len(pathBytes) == 0 {
		return Process{}, errors.New("process executable path missing")
	}
	generation, err := darwinProcessGeneration(info.Proc.P_starttime)
	if err != nil {
		return Process{}, err
	}
	return Process{
		PID:        pid,
		Executable: string(pathBytes),
		UID:        int(info.Eproc.Ucred.Uid),
		Generation: generation,
	}, nil
}

// processConfirmedGone 只在内核**明确**回答「没有这个进程」时返回 true。
//
// kill(pid, 0) 不投递信号,只做存在性与权限检查:ESRCH = 确无此进程;
// nil = 存在;EPERM = 存在但无权限。除 ESRCH 外一律当作「没问出来」,
// 保持 fail-closed。
func processConfirmedGone(pid int) bool {
	return errors.Is(unix.Kill(pid, 0), unix.ESRCH)
}

func darwinProcessGeneration(start unix.Timeval) (string, error) {
	if start.Sec == 0 && start.Usec == 0 {
		return "", errors.New("process start time unavailable")
	}
	return fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec), nil
}
