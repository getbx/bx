package tray

import "strings"

type TrayState int

const (
	StateNotInstalled TrayState = iota // 服务未注册(理论态;托盘一般已随安装存在)
	StateNotSetup                      // 无 config
	StateOff                           // 有 config,服务未跑
	StateProtected                     // 服务跑且隧道健康
	StateAttention                     // 服务跑但隧道不健康
)

// trayStateFrom 由三个非提权可得的信号合成托盘态。
func trayStateFrom(svcRunning, configExists, tunnelHealthy bool) TrayState {
	if !configExists {
		return StateNotSetup
	}
	if !svcRunning {
		return StateOff
	}
	if tunnelHealthy {
		return StateProtected
	}
	return StateAttention
}

// setupLinkPrefixes 是 bx setup 认的链接前缀(对齐现有 setup/blink 支持)。
var setupLinkPrefixes = []string{"bx://", "blink://", "vless://", "hysteria2://", "hy2://", "trojan://", "ss://", "vmess://", "brook://"}

// parseSetupLink 从剪贴板文本取一条受支持的 setup 链接(trim;校验前缀)。ok=false 表示不是链接。
func parseSetupLink(clipboard string) (string, bool) {
	s := strings.TrimSpace(clipboard)
	for _, p := range setupLinkPrefixes {
		if strings.HasPrefix(strings.ToLower(s), p) {
			return s, true
		}
	}
	return "", false
}

// menuItem 描述一个菜单项的呈现(由态决定)。
type menuItem struct {
	Visible bool
	Label   string
}

// TrayMenu 是按态生成的菜单蓝图(纯数据;windows 侧据此显隐 systray 项)。
type TrayMenu struct {
	Connect    menuItem
	Disconnect menuItem
	Setup      menuItem
	Restart    menuItem
}

func menuItemsFor(s TrayState) TrayMenu {
	m := TrayMenu{
		Connect:    menuItem{Label: "连接"},
		Disconnect: menuItem{Label: "断开"},
		Setup:      menuItem{Label: "从剪贴板设置…"},
		Restart:    menuItem{Label: "重启保护"},
	}
	switch s {
	case StateNotSetup, StateNotInstalled:
		m.Setup.Visible = true
	case StateOff:
		m.Connect.Visible = true
		m.Setup.Visible = true // 允许换链接
	case StateProtected:
		m.Disconnect.Visible = true
	case StateAttention:
		m.Disconnect.Visible = true
		m.Restart.Visible = true
	}
	return m
}
