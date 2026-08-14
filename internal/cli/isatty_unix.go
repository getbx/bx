//go:build darwin || linux

package cli

import (
	"os"

	"golang.org/x/sys/unix"
)

// fdIsTerminal 判断这个 fd 是不是**真的终端**。
//
// **不能用 `Stat().Mode()&os.ModeCharDevice`** —— 那判的是「字符设备」,而
// `/dev/null` 与 `/dev/zero` 都是字符设备(实测 mode `Dcrw-rw-rw-`)。
// 后果不是理论问题:`bx server deploy` 因此在无终端环境里给 ssh 加了 `-t`,
// 每条远端命令都吐一句 "Pseudo-terminal will not be allocated…";而
// `confirmPrompt` 那一侧更糟 —— 它会把 `/dev/null` 当成可交互的终端去问用户。
//
// 唯一可靠的判据是问内核要 termios:只有终端答得上来。
func fdIsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	_, err := unix.IoctlGetTermios(int(f.Fd()), ioctlReadTermios)
	return err == nil
}
