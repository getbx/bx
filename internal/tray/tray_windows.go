//go:build windows

package tray

import (
	"fmt"
	"os"
	"time"

	"fyne.io/systray"
)

const configPath = `C:\ProgramData\bx\config.yaml`

// Run 启动托盘(阻塞到退出)。
func Run() error {
	freeConsole() // 隐藏 Explorer 双击时的控制台黑框
	systray.Run(onReady, func() {})
	return nil
}

func onReady() {
	exe, _ := os.Executable()
	systray.SetTitle("bx")
	mQuit := systray.AddMenuItem("退出", "关闭托盘(保护继续运行)")
	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
			return
		}
	}()
	go pollLoop(exe)
}

// pollLoop 定期刷新图标 + tooltip。
func pollLoop(exe string) {
	for {
		state, detail := detectState(exe, configPath)
		systray.SetIcon(iconFor(state))
		systray.SetTooltip(tooltipFor(state, detail))
		time.Sleep(3 * time.Second)
	}
}

// tooltipFor 按态渲染 tooltip 文案。
func tooltipFor(s TrayState, d StatusDetail) string {
	switch s {
	case StateProtected:
		return fmt.Sprintf("bx 保护中 · 延迟 %dms · %s", d.LatencyMS, d.Server)
	case StateAttention:
		return "bx 需注意(隧道不健康)"
	case StateOff:
		return "bx 已关闭"
	default:
		return "bx 未配置——复制 bx:// 链接后从菜单设置"
	}
}
