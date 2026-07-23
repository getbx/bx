//go:build windows

package tray

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"fyne.io/systray"
)

const configPath = `C:\ProgramData\bx\config.yaml`

const logPath = `C:\ProgramData\bx\service.log`

// Run 启动托盘(阻塞到退出)。
func Run() error {
	freeConsole() // 隐藏 Explorer 双击时的控制台黑框
	systray.Run(onReady, func() {})
	return nil
}

// toggleItems 是随托盘态显隐的四个动作项——onReady 建好后交给 pollLoop 据态 Show/Hide。
type toggleItems struct {
	Connect    *systray.MenuItem
	Disconnect *systray.MenuItem
	Setup      *systray.MenuItem
	Restart    *systray.MenuItem
}

func onReady() {
	exe, _ := os.Executable()
	systray.SetTitle("bx")

	mConnect := systray.AddMenuItem("连接", "接管整机流量,经 bx 出站")
	mDisconnect := systray.AddMenuItem("断开", "流量回到直连")
	mSetup := systray.AddMenuItem("从剪贴板设置…", "用剪贴板里的 bx:// 链接配置")
	mRestart := systray.AddMenuItem("重启保护", "重启 bx 服务")
	mStatus := systray.AddMenuItem("打开状态", "查看 bx 当前状态")
	mLogs := systray.AddMenuItem("查看日志", "用记事本打开服务日志")
	mQuit := systray.AddMenuItem("退出", "关闭托盘(保护继续运行)")

	go func() {
		for range mConnect.ClickedCh {
			if confirm("连接 bx", "bx 将接管整机流量,继续?") {
				_ = elevateRun("up")
			}
		}
	}()
	go func() {
		for range mDisconnect.ClickedCh {
			if confirm("断开 bx", "断开后流量回到直连,继续?") {
				_ = elevateRun("down")
			}
		}
	}()
	go func() {
		for range mSetup.ClickedCh {
			txt, _ := readClipboardText()
			link, ok := parseSetupLink(txt)
			if !ok {
				messageBox("从剪贴板设置", "请先复制 bx:// 链接,再点此。", MB_ICONINFORMATION)
				continue
			}
			if confirm("从剪贴板设置", "用剪贴板里的链接配置 bx?") {
				_ = elevateRun(`setup "` + link + `"`)
			}
		}
	}()
	go func() {
		for range mRestart.ClickedCh {
			if confirm("重启保护", "重启 bx 保护,继续?") {
				_ = elevateRun("restart")
			}
		}
	}()
	go func() {
		for range mStatus.ClickedCh {
			out, _ := exec.Command(exe, "status").CombinedOutput()
			messageBox("bx 状态", string(out), MB_ICONINFORMATION)
		}
	}()
	go func() {
		for range mLogs.ClickedCh {
			_ = exec.Command("notepad", logPath).Start()
		}
	}()
	go func() {
		for range mQuit.ClickedCh {
			systray.Quit()
			return
		}
	}()

	go pollLoop(exe, toggleItems{
		Connect:    mConnect,
		Disconnect: mDisconnect,
		Setup:      mSetup,
		Restart:    mRestart,
	})
}

// pollLoop 定期刷新图标 + tooltip + 动作项显隐;首轮顺带注册开机自启(幂等,只需一次)。
func pollLoop(exe string, items toggleItems) {
	var autostartOnce sync.Once
	for {
		state, detail := detectState(exe, configPath, false)
		systray.SetIcon(iconFor(state))
		systray.SetTooltip(tooltipFor(state, detail))

		m := menuItemsFor(state)
		showOrHide(items.Connect, m.Connect.Visible)
		showOrHide(items.Disconnect, m.Disconnect.Visible)
		showOrHide(items.Setup, m.Setup.Visible)
		showOrHide(items.Restart, m.Restart.Visible)

		autostartOnce.Do(func() {
			_ = setAutostart(exe)
		})

		time.Sleep(3 * time.Second)
	}
}

// showOrHide 按 visible 切一个菜单项的显隐。
func showOrHide(item *systray.MenuItem, visible bool) {
	if visible {
		item.Show()
	} else {
		item.Hide()
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
