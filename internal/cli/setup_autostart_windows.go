//go:build windows

package cli

import "github.com/getbx/bx/internal/install"

// postSetupAutostart 在 windows setup 装完服务后设默认开机自启 ON
// (保住"装好即开机自启"的好默认;up/down 已与自启解耦,故须在 setup 显式设)。
func postSetupAutostart() error { return install.SetAutostart(true) }
