//go:build darwin

package cli

import "golang.org/x/sys/unix"

// darwin/BSD 取 termios 的 ioctl 请求号。
const ioctlReadTermios = unix.TIOCGETA
