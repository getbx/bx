//go:build !darwin && !windows

package guardian

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func inspectProcess(pid int) (Process, error) {
	procPath := filepath.Join("/proc", fmt.Sprint(pid))
	executable, err := os.Readlink(filepath.Join(procPath, "exe"))
	if err != nil {
		return Process{}, err
	}
	info, err := os.Stat(procPath)
	if err != nil {
		return Process{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Process{}, fmt.Errorf("process credentials unavailable")
	}
	return Process{PID: pid, Executable: executable, UID: int(stat.Uid)}, nil
}
