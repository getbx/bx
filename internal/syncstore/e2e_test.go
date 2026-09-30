package syncstore_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/policysync"
	"github.com/getbx/bx/internal/socks5"
	"github.com/getbx/bx/internal/srvgen"
	"github.com/getbx/bx/internal/syncstore"
)

const link = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"

// 服务端一半整条走一遍:真 sing-box(加固后的服务端路由)+ 回环上的 sync-store + 一台设备
// 用链接派生钥匙、封好规则、经隧道 PUT,另一台同链接的设备经隧道 GET 回来解开。
func TestADeviceStoresAndAnotherReadsItsRulesThroughTheTunnel(t *testing.T) {
	raw := embedded.Singbox()
	if len(raw) == 0 {
		t.Skip("this build embeds no sing-box")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0") // a free port: tests in other packages may hold the real one
	if err != nil {
		t.Fatal(err)
	}
	storeAddr := ln.Addr().String()
	_, sp, _ := net.SplitHostPort(storeAddr)
	storePort, _ := strconv.Atoi(sp)
	store := &http.Server{Handler: syncstore.Handler(t.TempDir())}
	go func() { _ = store.Serve(ln) }()
	t.Cleanup(func() { _ = store.Close() })

	socks := runHardenedServer(t, raw, storePort)
	client := viaSocks(t, socks)

	mac, _ := policysync.Derive(link)
	want := policysync.Policy{Version: 3, Direct: []string{"*.apple.com"}, Proxy: []string{"x.com"}}
	blob, _ := policysync.Seal(mac, want)
	req, _ := http.NewRequest(http.MethodPut, "http://"+storeAddr+"/v1/blob/"+mac.BlobID, bytes.NewReader(blob))
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusNoContent {
		t.Fatalf("PUT through the tunnel: %v %v", resp, err)
	}
	resp.Body.Close()

	phone, _ := policysync.Derive(link)
	resp, err = client.Get("http://" + storeAddr + "/v1/blob/" + phone.BlobID)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	p, err := policysync.Open(phone, got)
	if err != nil || p.Version != 3 || p.Direct[0] != "*.apple.com" {
		t.Fatalf("the other device read %+v, %v", p, err)
	}
}

func TestSyncStoreUnitIsLoopbackOnly(t *testing.T) {
	u := install.SyncStoreUnitText("/usr/local/bin/bx server sync-store")
	for _, want := range []string{"IPAddressDeny=any", "IPAddressAllow=localhost", "ReadWritePaths=/var/lib/bx", "NoNewPrivileges=true"} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %s", want)
		}
	}
}

func runHardenedServer(t *testing.T, bin []byte, storePort int) string {
	t.Helper()
	dir := t.TempDir()
	exe := filepath.Join(dir, "sing-box")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	if err := os.WriteFile(exe, bin, 0o755); err != nil {
		t.Fatal(err)
	}
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	_ = l.Close()
	_, port, _ := net.SplitHostPort(addr)
	p, _ := strconv.Atoi(port)
	cfg, _ := json.Marshal(map[string]any{
		"log":       map[string]any{"level": "error"},
		"inbounds":  []any{map[string]any{"type": "socks", "tag": "in", "listen": "127.0.0.1", "listen_port": p}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "direct"}},
		"route":     srvgen.HardenedRouteForTest(storePort),
	})
	_ = os.WriteFile(filepath.Join(dir, "config.json"), cfg, 0o600)
	cmd := exec.Command(exe, "run", "-c", "config.json")
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond); err == nil {
			_ = c.Close()
			return addr
		}
	}
	t.Fatal("sing-box did not start")
	return ""
}

func viaSocks(t *testing.T, socks string) *http.Client {
	t.Helper()
	d, err := socks5.NewDialer(socks, &net.Dialer{Timeout: 3 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{DialContext: d.DialContext}}
}
