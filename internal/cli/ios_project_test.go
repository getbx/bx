package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// iOS 构建里有三个标识符散在四个文件里:扩展的 bundle id(project.yml 与 App 端
// providerBundleIdentifier)、App Group(两份 entitlements、App 与扩展各自的常量)。任何一处
// 漂了,构建照样成功、签名照样通过,而隧道在真机上起不来或读不到配置 —— 两边都不报错。
func TestIOSIdentifiersAgreeAcrossTheProject(t *testing.T) {
	root := repoRootForMenuGuard(t)
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(b)
	}
	const tunnelID, appID, group = "com.getbx.bx.ios.tunnel", "com.getbx.bx.ios", "group.com.getbx.bx"
	checks := []struct{ file, want string }{
		{"apps/ios/project.yml", "PRODUCT_BUNDLE_IDENTIFIER: " + appID + "\n"},
		{"apps/ios/project.yml", "PRODUCT_BUNDLE_IDENTIFIER: " + tunnelID + "\n"},
		{"apps/ios/App/Driver.swift", `tunnelBundleID = "` + tunnelID + `"`},
		{"apps/ios/App/Driver.swift", `appGroup = "` + group + `"`},
		{"apps/ios/Tunnel/PacketTunnelProvider.swift", `appGroup = "` + group + `"`},
		{"apps/ios/App/App.entitlements", "<string>" + group + "</string>"},
		{"apps/ios/Tunnel/Tunnel.entitlements", "<string>" + group + "</string>"},
		{"apps/ios/App/App.entitlements", "<string>packet-tunnel-provider</string>"},
		{"apps/ios/Tunnel/Tunnel.entitlements", "<string>packet-tunnel-provider</string>"},
		{"scripts/ios-dev.sh", "com.getbx.bx.ios --scenario"},
	}
	for _, c := range checks {
		if !strings.Contains(read(c.file), c.want) {
			t.Errorf("%s lacks %q", c.file, c.want)
		}
	}
}

// includeAllNetworks 必须报真值:sing-tun 按它挑 TCP 栈,报成常量 false 时 kill-switch 构建
// DNS 通、TCP 全死(2026-09-29 真机)。另钉 raw 探测只打自己的服务器 —— kill-switch 测试里
// 那一枪若漏出去,只许落在已经知道我们家 IP 的那台机器上。
func TestIOSKillSwitchWiringStaysHonest(t *testing.T) {
	root := repoRootForMenuGuard(t)
	pi, err := os.ReadFile(filepath.Join(root, "apps/ios/Tunnel/PlatformInterface.swift"))
	if err != nil {
		t.Fatal(err)
	}
	body, ok := swiftFunctionBody(string(pi), "func includeAllNetworks() -> Bool {")
	if !ok {
		t.Fatal("includeAllNetworks() not found (or became a one-liner constant) in PlatformInterface.swift")
	}
	if !strings.Contains(body, "protocolConfiguration") || strings.Contains(body, "return false") || strings.Contains(body, "return true") {
		t.Fatalf("includeAllNetworks() must read the tunnel's protocol configuration, got:\n%s", body)
	}
	drv, err := os.ReadFile(filepath.Join(root, "apps/ios/App/Driver.swift"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(drv), `if let host = exp["server_host"] as? String {`) || strings.Count(string(drv), "RawProbe.run(") != 1 {
		t.Fatal("the raw kill-switch probe must target only the configured server host, from exactly one call site")
	}
}
