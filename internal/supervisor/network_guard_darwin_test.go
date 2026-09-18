//go:build darwin

package supervisor

import "testing"

func TestDarwinGuardHasTailscaleOverlayRoute(t *testing.T) {
	routes := `
Destination        Gateway            Flags        Netif Expire
100.64/10          link#24            UCS          utun3
`
	if !darwinHasTailscaleOverlayRoute(routes) {
		t.Fatal("expected Tailscale overlay route")
	}
}

func TestDarwinGuardSystemProxyEnabled(t *testing.T) {
	out := `
<dictionary> {
  HTTPEnable : 0
  HTTPSEnable : 1
}
`
	if !darwinSystemProxyEnabled(out) {
		t.Fatal("expected HTTPS system proxy to be detected")
	}
}

func TestDarwinGuardConnectedNetworkService(t *testing.T) {
	out := `
Available network connection services in the current set (*=enabled):
* (Disconnected) Personal VPN
* (Connected) Work VPN [VPN:com.example.vpn]
`
	if got := darwinConnectedNetworkService(out); got != "Work VPN [VPN:com.example.vpn]" {
		t.Fatalf("connected service = %q", got)
	}
}

// 真机原文(2026-09-17,项目所有者的 Mac)。上面那条合成输入让缺陷完全不可见:
// 它没有 UUID、没有括号里的 bundle id、没有引号里的显示名,也没有 scutil 用来
// 对齐的那一长串填充空格。而这句话是**常驻**的 —— 它同时出现在 `bx status`、
// `bx doctor` 与菜单上,长这样:
//
//	macOS VPN service active: 8B24B74E-7780-43B3-A524-4B313E712AA6 VPN
//	(io.tailscale.ipn.macsys) "Tailscale"                      [VPN:io.tailscale.ipn.macsys]
//
// 用户要的只有 "Tailscale" 四个字;其余是 scutil 的排版,连 bundle id 都印了两遍。
func TestDarwinGuardConnectedNetworkServiceNamesTheServiceNotScutilsWholeLine(t *testing.T) {
	out := "Available network connection services in the current set (*=enabled):\n" +
		"* (Connected)     8B24B74E-7780-43B3-A524-4B313E712AA6 VPN (io.tailscale.ipn.macsys) \"Tailscale\"                      [VPN:io.tailscale.ipn.macsys]\n" +
		"* (Disconnected)  12A4FF57-1C97-47F9-A655-047495083D7F VPN (com.txthinking.brook) \"TxThinking Brook macOS\"         [VPN:com.txthinking.brook]\n"
	if got := darwinConnectedNetworkService(out); got != "Tailscale" {
		t.Fatalf("connected service = %q, want %q", got, "Tailscale")
	}
}

// 认不出显示名时**绝不返回空串** —— 那会让「另一个 VPN 正开着」这条告警整个
// 消失,而它正是这个函数存在的理由。退路是原文压掉多余空白。
func TestDarwinGuardConnectedNetworkServiceFallsBackToTheLineInsteadOfGoingSilent(t *testing.T) {
	out := "* (Connected)     some-service   with   padding\n"
	if got := darwinConnectedNetworkService(out); got != "some-service with padding" {
		t.Fatalf("connected service = %q", got)
	}
}
