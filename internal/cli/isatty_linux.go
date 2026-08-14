//go:build linux

package cli

import "golang.org/x/sys/unix"

// linux 取 termios 的 ioctl 请求号。
const ioctlReadTermios = unix.TCGETS
