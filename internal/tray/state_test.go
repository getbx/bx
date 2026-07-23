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
		if got := trayStateFrom(c.svcRunning, c.configOK, c.healthy, false); got != c.want {
			t.Errorf("%s: trayStateFrom(%v,%v,%v)=%v want %v", c.name, c.svcRunning, c.configOK, c.healthy, got, c.want)
		}
	}
}

func TestTrayStateFromPriority(t *testing.T) {
	cases := []struct {
		name                      string
		svc, cfg, healthy, update bool
		want                      TrayState
	}{
		{"未配置", false, false, false, false, StateNotSetup},
		{"未配置即便有更新", false, false, false, true, StateNotSetup},
		{"有配置未运行", false, true, false, false, StateOff},
		{"未运行即便有更新", false, true, false, true, StateOff},
		{"运行但不健康", true, true, false, false, StateAttention},
		{"不健康优先于更新", true, true, false, true, StateAttention},
		{"健康且有更新", true, true, true, true, StateWarning},
		{"健康无更新", true, true, true, false, StateProtected},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := trayStateFrom(c.svc, c.cfg, c.healthy, c.update); got != c.want {
				t.Fatalf("trayStateFrom(%v,%v,%v,%v)=%v want %v", c.svc, c.cfg, c.healthy, c.update, got, c.want)
			}
		})
	}
}

func TestMenuItemsForWarningShowsUpdate(t *testing.T) {
	m := menuItemsFor(StateWarning)
	if !m.Update.Visible {
		t.Fatal("StateWarning 应显示 Update 项")
	}
	if !m.Disconnect.Visible {
		t.Fatal("StateWarning 应显示 Disconnect 项")
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

// 恶意剪贴板值:合法 scheme 前缀但内嵌引号/空白,可能注入额外参数到提权的 `bx setup "<link>"`。
// 真实 bx/vless/… 链接是 URL-safe base64,绝不含引号或空白,故一律拒绝(纵深防御)。
func TestParseSetupLinkRejectsInjection(t *testing.T) {
	for _, in := range []string{
		`bx://x" --evil-flag "y`, // 内嵌引号:拆 args
		"bx://x y",               // 内嵌空格:拆 args
		"bx://x\ty",              // 制表符
		"vless://a\"b",           // 引号(vless)
	} {
		if _, ok := parseSetupLink(in); ok {
			t.Errorf("应拒绝注入型输入 %q", in)
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
