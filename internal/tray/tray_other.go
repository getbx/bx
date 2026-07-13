//go:build !windows

package tray

import "errors"

// Run 仅 Windows:非 windows 返回清晰错误。
func Run() error { return errors.New("bx tray 仅支持 Windows") }
