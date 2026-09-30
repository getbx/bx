package srvgen_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/policysync"
	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/srvgen"
)

// 服务端此前只有一个 direct 出站、没有路由规则:任何持有链接的人(含 bx server share 分出去的)
// 都能经隧道连到 VPS 的回环、内网与云元数据地址。这里用**真 sing-box**验:把生成的服务端路由配上
// 一个本机 socks 入站(路由与 vless 入站同一套),本机回环上起一个 HTTP 服务当「VPS 上只对本机
// 开放的服务」,分别按地址与按名字(localhost —— 服务端解析之后才判)去连。
// **对照组是必需的**:没有这套路由时必须连得上,否则「连不上」什么也证明不了。
func TestHardenedServerRefusesLoopbackPrivateAndMetadataDestinations(t *testing.T) {
	bin := singbox(t)
	secret := startLoopbackService(t)

	open := runServer(t, bin, nil)
	if body, err := fetchVia(open, secret); err != nil || body != "vps-local-secret" {
		t.Fatalf("control: without the hardened route the loopback service should be reachable (that is the bug); got %q, %v", body, err)
	}

	storeAddr := startLoopbackService(t) // stands in for the sync store, on a free port
	_, storePortText, _ := net.SplitHostPort(storeAddr)
	hardened := runServer(t, bin, srvgen.HardenedRouteForTest(atoi(storePortText)))
	for _, target := range []string{secret, strings.Replace(secret, "127.0.0.1", "localhost", 1)} {
		if body, err := fetchVia(hardened, target); err == nil {
			t.Errorf("hardened server still reaches %s (got %q)", target, body)
		}
	}
	// 唯一的例外:规则同步存储的那个回环端口,经隧道必须够得着(别的回环端口上面已经验过是拒的)。
	if body, err := fetchVia(hardened, storeAddr); err != nil || body != "vps-local-secret" {
		t.Errorf("hardened server does not let the sync store through: %q, %v", body, err)
	}
	for _, target := range []string{"169.254.169.254:80", "10.0.0.1:80", "192.168.1.1:80"} {
		if _, err := fetchVia(hardened, target); err == nil {
			t.Errorf("hardened server connected to %s", target)
		}
	}
}

// 已装好的服务器由 bx server harden 补上:只加路由,入站(钥匙、用户)一个字节不动;再补一次不变。
func TestHardenPatchesAnExistingConfigWithoutTouchingInbounds(t *testing.T) {
	rp := srvgen.RealityParams{UUID: "11111111-2222-3333-4444-555555555555", Port: 443, SNI: "www.cloudflare.com", PrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ShortID: "abcd"}
	old := legacyServerConfig(t, rp)
	patched, changed, err := srvgen.Harden(old)
	if err != nil || !changed {
		t.Fatalf("Harden: changed=%v err=%v", changed, err)
	}
	var before, after map[string]any
	_ = json.Unmarshal(old, &before)
	_ = json.Unmarshal(patched, &after)
	b1, _ := json.Marshal(before["inbounds"])
	b2, _ := json.Marshal(after["inbounds"])
	if string(b1) != string(b2) {
		t.Fatal("Harden changed the inbounds (keys / users)")
	}
	if after["route"] == nil {
		t.Fatal("no route after Harden")
	}
	again, changed2, err := srvgen.Harden(patched)
	if err != nil || changed2 || string(again) != string(patched) {
		t.Fatalf("Harden is not idempotent: changed=%v err=%v", changed2, err)
	}
	// 新装的机器从一开始就带这套路由。
	fresh, _ := rp.ServerConfig()
	if _, changed3, _ := srvgen.Harden(fresh); changed3 {
		t.Fatal("a freshly generated server config is not hardened")
	}
}

func legacyServerConfig(t *testing.T, rp srvgen.RealityParams) []byte {
	t.Helper()
	fresh, err := rp.ServerConfig()
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	_ = json.Unmarshal(fresh, &cfg)
	delete(cfg, "route") // what every server installed before 2026-09-29 looks like
	out, _ := json.MarshalIndent(cfg, "", "  ")
	return out
}

func singbox(t *testing.T) string {
	t.Helper()
	raw := embedded.Singbox()
	if len(raw) == 0 {
		t.Skip("this build embeds no sing-box")
	}
	path := filepath.Join(t.TempDir(), "sing-box")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	if err := os.WriteFile(path, raw, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func startLoopbackService(t *testing.T) string {
	t.Helper()
	return startLoopbackServiceOn(t, "127.0.0.1:0")
}

func startLoopbackServiceOn(t *testing.T, addr string) string {
	t.Helper()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "vps-local-secret") })}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// runServer starts sing-box with a socks inbound on loopback and the given route (nil = none,
// i.e. what servers had before), returning the socks address.
func runServer(t *testing.T, bin string, route map[string]any) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	_, port, _ := net.SplitHostPort(addr)
	cfg := map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []any{map[string]any{"type": "socks", "tag": "probe-in", "listen": "127.0.0.1", "listen_port": atoi(port)}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
	}
	if route != nil {
		cfg["route"] = route
	}
	raw, _ := json.Marshal(cfg)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "run", "-c", "config.json")
	cmd.Dir = dir
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return addr
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("sing-box did not start: %s", stderr.String())
	return ""
}

func fetchVia(socksAddr, target string) (string, error) {
	d, err := socks5.NewDialer(socksAddr, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		return "", err
	}
	client := &http.Client{Timeout: 4 * time.Second, Transport: &http.Transport{DialContext: d.DialContext}}
	resp, err := client.Get("http://" + target + "/")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func atoi(s string) int {
	var n int
	_, _ = fmt.Sscanf(s, "%d", &n)
	return n
}

// The route servers actually get opens the one exception on the real store port, not another.
func TestTheRealRouteOpensTheRealStorePort(t *testing.T) {
	b, _ := json.Marshal(srvgen.HardenedRoute())
	if !strings.Contains(string(b), fmt.Sprintf(`"port":[%d]`, policysync.StorePort)) {
		t.Fatalf("HardenedRoute does not open policysync.StorePort:\n%s", b)
	}
}
