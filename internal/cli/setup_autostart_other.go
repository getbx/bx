//go:build !windows

package cli

// postSetupAutostart 在非 windows 无操作(linux/mac 的开机自启由各自 up/enable 语义处理)。
func postSetupAutostart() error { return nil }
