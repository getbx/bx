//go:build !darwin && !linux

package cli

import "os"

// fdIsTerminal 在没有 termios 的平台上退回旧判据(字符设备)。
//
// **它会把 /dev/null 判成终端** —— 这是已知的不精确,保留是因为改对它需要
// 各平台的控制台 API,而今天唯一的调用方(bx server deploy / confirmPrompt)
// 都只在 unix 上真正走到。谁把它们移植到别处,要先把这里做对。
func fdIsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
