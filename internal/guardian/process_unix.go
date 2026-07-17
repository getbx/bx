//go:build !darwin && !windows

package guardian

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

func inspectProcess(pid int) (Process, error) {
	procPath := filepath.Join("/proc", fmt.Sprint(pid))
	executable, err := os.Readlink(filepath.Join(procPath, "exe"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Process{}, fmt.Errorf("%w: PID %d", ErrProcessNotRunning, pid)
		}
		return Process{}, err
	}
	info, err := os.Stat(procPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Process{}, fmt.Errorf("%w: PID %d", ErrProcessNotRunning, pid)
		}
		return Process{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return Process{}, fmt.Errorf("process credentials unavailable")
	}
	generation, err := linuxProcessGeneration(procPath)
	if err != nil {
		return Process{}, err
	}
	return Process{PID: pid, Executable: executable, UID: int(stat.Uid), Generation: generation}, nil
}

func linuxProcessGeneration(procPath string) (string, error) {
	b, err := os.ReadFile(filepath.Join(procPath, "stat"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", ErrProcessNotRunning, procPath)
		}
		return "", fmt.Errorf("read process generation: %w", err)
	}
	closing := strings.LastIndex(string(b), ") ")
	if closing < 0 {
		return "", errors.New("parse process generation: malformed stat")
	}
	fields := strings.Fields(string(b[closing+2:]))
	const startTimeIndex = 19 // field 22 after removing PID and parenthesized comm
	if len(fields) <= startTimeIndex || fields[startTimeIndex] == "" {
		return "", errors.New("parse process generation: start time missing")
	}
	return "linux:" + fields[startTimeIndex], nil
}
