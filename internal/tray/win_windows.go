//go:build windows

package tray

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// MessageBox 相关常量。x/sys/windows 不导出这些 flag,自定义。
const (
	MB_OK              = 0x00000000
	MB_OKCANCEL        = 0x00000001
	MB_ICONQUESTION    = 0x00000020
	MB_ICONINFORMATION = 0x00000040
	IDOK               = 1
)

const cfUnicodeText = 13 // CF_UNICODETEXT

var (
	modUser32   = windows.NewLazySystemDLL("user32.dll")
	modKernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procOpenClipboard              = modUser32.NewProc("OpenClipboard")
	procCloseClipboard             = modUser32.NewProc("CloseClipboard")
	procGetClipboardData           = modUser32.NewProc("GetClipboardData")
	procIsClipboardFormatAvailable = modUser32.NewProc("IsClipboardFormatAvailable")

	procGlobalLock   = modKernel32.NewProc("GlobalLock")
	procGlobalUnlock = modKernel32.NewProc("GlobalUnlock")
	procFreeConsole  = modKernel32.NewProc("FreeConsole")
)

// elevateRun 用 ShellExecute verb "runas" 拉起一个新的、提权的自身副本跑 `bx <subcmd>`——
// 这一步会弹 UAC 提示。subcmd 可能已经带参数(如 `setup "bx://..."`),原样透传给 ShellExecute 的
// args 参数。用户拒绝 UAC 时返回 ERROR_CANCELLED(1223)包装的错误,交给上层提示。
func elevateRun(subcmd string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return fmt.Errorf("verb: %w", err)
	}
	file, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return fmt.Errorf("file: %w", err)
	}
	args, err := windows.UTF16PtrFromString(subcmd)
	if err != nil {
		return fmt.Errorf("args: %w", err)
	}
	if err := windows.ShellExecute(0, verb, file, args, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("ShellExecute runas: %w", err)
	}
	return nil
}

// readClipboardText 读剪贴板里的 CF_UNICODETEXT。剪贴板没有文本(格式不可用/句柄拿不到)时
// 返回空串、nil error——这不是异常情况,由调用方决定要不要提示。
func readClipboardText() (string, error) {
	avail, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText)
	if avail == 0 {
		return "", nil
	}

	r, _, err := procOpenClipboard.Call(0)
	if r == 0 {
		return "", fmt.Errorf("OpenClipboard: %w", err)
	}
	defer procCloseClipboard.Call()

	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", nil
	}

	ptr, _, _ := procGlobalLock.Call(h)
	if ptr == 0 {
		return "", nil
	}
	defer procGlobalUnlock.Call(h)

	return windows.UTF16PtrToString((*uint16)(unsafe.Pointer(ptr))), nil
}

// messageBox 是 windows.MessageBox 的薄封装,hwnd 恒为 0(无主窗口)。
func messageBox(title, text string, flags uint32) int32 {
	textPtr, err := windows.UTF16PtrFromString(text)
	if err != nil {
		return 0
	}
	titlePtr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return 0
	}
	ret, _ := windows.MessageBox(0, textPtr, titlePtr, flags)
	return ret
}

// confirm 弹 OK/Cancel + 问号图标的确认框,OK 才算确认。
func confirm(title, text string) bool {
	return messageBox(title, text, MB_OKCANCEL|MB_ICONQUESTION) == IDOK
}

// setAutostart 在 HKCU Run 注册开机自启,值名固定 "bx"、命令固定带 tray 子命令。幂等——
// 重复调用直接覆盖同名值。
func setAutostart(exePath string) error {
	k, _, err := registry.CreateKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Run`, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("registry.CreateKey: %w", err)
	}
	defer k.Close()

	if err := k.SetStringValue("bx", `"`+exePath+`" tray`); err != nil {
		return fmt.Errorf("SetStringValue: %w", err)
	}
	return nil
}

// freeConsole 释放当前进程挂着的控制台(隐藏从资源管理器双击启动时冒出的黑框)。
// best-effort——没有控制台可释放时静默失败,不影响托盘运行。
func freeConsole() {
	procFreeConsole.Call()
}
