//go:build !windows

package guardian

import (
	"errors"
	"os"
	"syscall"
)

func statExecutableIdentity(path string) (executableIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return executableIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return executableIdentity{}, errors.New("executable inode identity unavailable")
	}
	return executableIdentity{device: uint64(stat.Dev), inode: stat.Ino}, nil
}
