package macnetprobe

import "testing"

// 真机原文(2026-09-18,项目所有者的 Mac)。合成输入让缺陷完全不可见:它没有
// UUID、没有括号里的 bundle id、没有引号里的显示名,也没有 scutil 用来对齐的
// 那一长串空格。
const realScutilNCList = "Available network connection services in the current set (*=enabled):\n" +
	"* (Connected)     8B24B74E-7780-43B3-A524-4B313E712AA6 VPN (io.tailscale.ipn.macsys) \"Tailscale\"                      [VPN:io.tailscale.ipn.macsys]\n" +
	"* (Disconnected)  12A4FF57-1C97-47F9-A655-047495083D7F VPN (com.txthinking.brook) \"TxThinking Brook macOS\"         [VPN:com.txthinking.brook]\n"

func TestConnectedNetworkServiceNamesTheServiceNotTheWholeLine(t *testing.T) {
	if got := ConnectedNetworkService(realScutilNCList); got != "Tailscale" {
		t.Fatalf("connected service = %q, want %q", got, "Tailscale")
	}
}

// 认不出显示名时**绝不返回空串** —— 那会让「另一个 VPN 正开着」这条告警整个消失。
func TestConnectedNetworkServiceFallsBackInsteadOfGoingSilent(t *testing.T) {
	if got := ConnectedNetworkService("* (Connected)     some-service   with   padding\n"); got != "some-service with padding" {
		t.Fatalf("connected service = %q", got)
	}
	if got := ConnectedNetworkService("nothing here\n"); got != "" {
		t.Fatalf("没有连接中的服务时应答空串,got %q", got)
	}
}

func TestSystemProxyEnabled(t *testing.T) {
	if !SystemProxyEnabled("  HTTPSEnable : 1\n") {
		t.Fatal("HTTPSEnable : 1 应判为开着")
	}
	if SystemProxyEnabled("  HTTPSEnable : 0\n") {
		t.Fatal("HTTPSEnable : 0 不该判为开着")
	}
}

func TestHasTailscaleOverlayRoute(t *testing.T) {
	if !HasTailscaleOverlayRoute("100.64.0.0/10      utun4   USc\n") {
		t.Fatal("overlay 路由在却没认出来")
	}
	if HasTailscaleOverlayRoute("default   192.168.1.1   UGScg   en0\n") {
		t.Fatal("没有 overlay 路由却认成有")
	}
}
