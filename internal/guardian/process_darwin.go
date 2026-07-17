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
		return Process{}, err
	}
	if int(info.Proc.P_pid) != pid {
		return Process{}, errors.New("process is not alive")
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
	return Process{PID: pid, Executable: string(pathBytes), UID: int(info.Eproc.Ucred.Uid)}, nil
}
