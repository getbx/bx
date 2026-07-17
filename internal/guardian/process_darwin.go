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

func darwinProcessGeneration(start unix.Timeval) (string, error) {
	if start.Sec == 0 && start.Usec == 0 {
		return "", errors.New("process start time unavailable")
	}
	return fmt.Sprintf("darwin:%d:%d", start.Sec, start.Usec), nil
}
