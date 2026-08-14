package cli

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/setup"
)

// **换服务器时那条被落下的 UDP 传输,必须当场说出来。**
//
// 上一版这里打的是「• UDP 传输保持不变:…」—— 听起来无害,而实际上它现在指着
// 一台不属于你的机器:UDP 健康检查失败 → fail-closed 全阻断 → 网页能开、
// 微信语音和 QUIC 全废,**而 bx status 显示 Protected**(主隧道确实健康)。
func TestTransportChangeWarnsAboutTheStrandedUDPTransport(t *testing.T) {
	before := setup.TransportsBefore{
		Server:       "vless://u@1.1.1.1:443?security=reality",
		UDPTransport: "hysteria2://p@1.1.1.1:443?insecure=1",
	}
	lines := strings.Join(transportChangeLines(before, []string{"vless://u@2.2.2.2:443?security=reality"}, ""), "\n")

	if strings.Contains(lines, "UDP 传输保持不变") {
		t.Errorf("把一条已经落在旧机器上的 UDP 传输报成了「保持不变」:\n%s", lines)
	}
	for _, want := range []string{"1.1.1.1", "fail-closed", "--udp"} {
		if !strings.Contains(lines, want) {
			t.Errorf("警告里缺 %q —— 说不清后果或下一步:\n%s", want, lines)
		}
	}
}

// 用户**故意**把 UDP 放在另一台时,一个字都不许多说。
func TestTransportChangeStaysQuietAboutADeliberateSeparateUDPServer(t *testing.T) {
	before := setup.TransportsBefore{
		Server:       "vless://u@1.1.1.1:443?security=reality",
		UDPTransport: "hysteria2://p@9.9.9.9:443?insecure=1",
	}
	lines := strings.Join(transportChangeLines(before, []string{"vless://u@2.2.2.2:443?security=reality"}, ""), "\n")
	if strings.Contains(lines, "⚠") {
		t.Errorf("对用户故意分开的 UDP 服务器发了警告:\n%s", lines)
	}
	if !strings.Contains(lines, "UDP 传输保持不变") {
		t.Errorf("该说的「保持不变」没说:\n%s", lines)
	}
}

// 主传输那一行照旧要报「从 A 到 B」。
func TestTransportChangeShowsTheServerMove(t *testing.T) {
	lines := strings.Join(transportChangeLines(
		setup.TransportsBefore{Server: "vless://u@1.1.1.1:443"},
		[]string{"vless://u@2.2.2.2:443"}, ""), "\n")
	if !strings.Contains(lines, "→") {
		t.Errorf("没报出主传输的变更:\n%s", lines)
	}
}
