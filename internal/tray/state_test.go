package tray

import "testing"

func TestTrayStateFrom(t *testing.T) {
	cases := []struct {
		name       string
		svcRunning bool
		configOK   bool
		healthy    bool
		want       TrayState
	}{
		{"未配置", false, false, false, StateNotSetup},
		{"已配置未跑", false, true, false, StateOff},
		{"跑且健康", true, true, true, StateProtected},
		{"跑但不健康", true, true, false, StateAttention},
	}
	for _, c := range cases {
		if got := trayStateFrom(c.svcRunning, c.configOK, c.healthy); got != c.want {
			t.Errorf("%s: trayStateFrom(%v,%v,%v)=%v want %v", c.name, c.svcRunning, c.configOK, c.healthy, got, c.want)
		}
	}
}

func TestParseSetupLinkAccepts(t *testing.T) {
	for _, in := range []string{
		"bx://abc", "  bx://abc  \n", "vless://x@h:443", "hysteria2://x@h", "brook://x", "blink://x",
	} {
		if link, ok := parseSetupLink(in); !ok || link == "" {
			t.Errorf("应接受 %q", in)
		}
	}
	// trim 生效
	if link, _ := parseSetupLink("  bx://abc  "); link != "bx://abc" {
		t.Errorf("应 trim, got %q", link)
	}
}

func TestParseSetupLinkRejects(t *testing.T) {
	for _, in := range []string{"", "   ", "hello world", "https://x", "not-a-link"} {
		if _, ok := parseSetupLink(in); ok {
			t.Errorf("应拒绝 %q", in)
		}
	}
}

func TestMenuItemsFor(t *testing.T) {
	// 保护中:显示"断开",不显示"连接";显示状态/日志/退出
	m := menuItemsFor(StateProtected)
	if !m.Disconnect.Visible || m.Connect.Visible {
		t.Error("保护中应显示断开、隐藏连接")
	}
	// 已关闭:显示"连接"
	if m := menuItemsFor(StateOff); !m.Connect.Visible || m.Disconnect.Visible {
		t.Error("已关闭应显示连接、隐藏断开")
	}
	// 未配置:显示"从剪贴板设置",连接/断开都隐藏
	if m := menuItemsFor(StateNotSetup); !m.Setup.Visible || m.Connect.Visible || m.Disconnect.Visible {
		t.Error("未配置应只显示设置")
	}
	// 需注意:显示"重启"
	if m := menuItemsFor(StateAttention); !m.Restart.Visible {
		t.Error("需注意应显示重启")
	}
}
