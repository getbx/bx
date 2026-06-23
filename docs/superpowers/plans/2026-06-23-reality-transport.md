# REALITY transport (pluggable, user-selectable) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a user-selectable VLESS-REALITY transport to bx (alongside the existing brook), driven by the `server:` link scheme, using sing-box downloaded on-demand as the transport subprocess — without bloating the bx binary or changing the data plane.

**Architecture:** bx's data plane is transport-agnostic: `internal/tunnel` consumes a `RunnerFactory` (start a subprocess that exposes a local socks5) + a `HealthCheck`. We add a second factory (`realityFactory`) that generates a minimal sing-box client config from a `vless://` link and runs `sing-box run -c <config>`. A scheme dispatch in `Run` picks brook vs reality. sing-box is downloaded to `/usrdata` (never embedded). TUN/DNS/router/fail-closed/health are untouched.

**Tech Stack:** Go (stdlib only: `net/url`, `encoding/json`, `crypto/sha256`, `net/http`, `os/exec`); sing-box (external engine, downloaded); existing `internal/tunnel`, `internal/provision`, `internal/config`, `internal/supervisor`.

## Global Constraints

- Engine = **sing-box**, **downloaded on-demand** to `cfg.DataDir` (default `/usrdata/proxy/data` on the Mudi) — NEVER embedded into the bx binary.
- Transport selected by the **`server:` link scheme**: `vless://` → reality, anything else (`brook://`) → brook. No new transport-selector field.
- The existing **brook path must keep working unchanged** (it is the fallback).
- **Stdlib only** — no new third-party Go dependencies.
- Downloaded engine is **SHA-256 verified** when a hash is configured; mismatch is a hard error.
- `fail-closed` routing, router-mode, fw4 rules, and the health probe are **transport-agnostic and unchanged**.
- New code matches existing style: Chinese doc-comments consistent with the file, `gofmt`, table-driven tests.

---

### Task 1: Parse `vless://` REALITY links

**Files:**
- Create: `internal/tunnel/vlesslink.go`
- Test: `internal/tunnel/vlesslink_test.go`

**Interfaces:**
- Produces: `type vlessLink struct { UUID, Host string; Port int; PublicKey, ShortID, SNI, Flow, Fingerprint string }` and `func parseVlessLink(s string) (vlessLink, error)`

- [ ] **Step 1: Write the failing test**

```go
package tunnel

import "testing"

func TestParseVlessLink(t *testing.T) {
	s := "vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443" +
		"?security=reality&pbk=PUBKEYxyz&sid=abcd1234&sni=www.microsoft.com" +
		"&flow=xtls-rprx-vision&fp=chrome&type=tcp#mudi"
	v, err := parseVlessLink(s)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v.UUID != "11111111-2222-3333-4444-555555555555" {
		t.Errorf("uuid=%q", v.UUID)
	}
	if v.Host != "203.0.113.10" || v.Port != 443 {
		t.Errorf("host:port=%s:%d", v.Host, v.Port)
	}
	if v.PublicKey != "PUBKEYxyz" || v.ShortID != "abcd1234" {
		t.Errorf("pbk=%q sid=%q", v.PublicKey, v.ShortID)
	}
	if v.SNI != "www.microsoft.com" || v.Flow != "xtls-rprx-vision" || v.Fingerprint != "chrome" {
		t.Errorf("sni=%q flow=%q fp=%q", v.SNI, v.Flow, v.Fingerprint)
	}
}

func TestParseVlessLinkErrors(t *testing.T) {
	cases := map[string]string{
		"not vless":     "brook://server?server=1.2.3.4%3A9999",
		"no uuid":       "vless://@1.2.3.4:443?security=reality&pbk=x&sid=y&sni=z",
		"not reality":   "vless://uid@1.2.3.4:443?security=tls&sni=z",
		"missing pbk":   "vless://uid@1.2.3.4:443?security=reality&sid=y&sni=z",
		"missing sni":   "vless://uid@1.2.3.4:443?security=reality&pbk=x&sid=y",
		"bad port":      "vless://uid@1.2.3.4:notaport?security=reality&pbk=x&sid=y&sni=z",
	}
	for name, s := range cases {
		if _, err := parseVlessLink(s); err == nil {
			t.Errorf("%s: expected error, got nil", name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tunnel/ -run TestParseVlessLink -v`
Expected: FAIL — `undefined: parseVlessLink`

- [ ] **Step 3: Write minimal implementation**

```go
package tunnel

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// vlessLink 是从 vless:// 分享链接里解出的 REALITY 参数(用于生成 sing-box 客户端配置)。
type vlessLink struct {
	UUID        string
	Host        string
	Port        int
	PublicKey   string // reality public key (pbk)
	ShortID     string // reality short id (sid)
	SNI         string // 借用的真实站点域名 (sni)
	Flow        string // 一般为 xtls-rprx-vision
	Fingerprint string // uTLS 指纹 (fp);空时默认 chrome
}

// parseVlessLink 解析 vless://uuid@host:port?security=reality&pbk=&sid=&sni=&flow=&fp= 形式的链接。
// 只接受 security=reality;缺 uuid/host/pbk/sid/sni 视为非法。
func parseVlessLink(s string) (vlessLink, error) {
	var v vlessLink
	if !strings.HasPrefix(s, "vless://") {
		return v, fmt.Errorf("不是 vless:// 链接")
	}
	u, err := url.Parse(s)
	if err != nil {
		return v, fmt.Errorf("解析 vless 链接: %w", err)
	}
	v.UUID = u.User.Username()
	if v.UUID == "" {
		return v, fmt.Errorf("vless 链接缺 uuid")
	}
	v.Host = u.Hostname()
	if v.Host == "" {
		return v, fmt.Errorf("vless 链接缺 host")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port <= 0 || port > 65535 {
		return v, fmt.Errorf("vless 链接端口非法: %q", u.Port())
	}
	v.Port = port
	q := u.Query()
	if q.Get("security") != "reality" {
		return v, fmt.Errorf("仅支持 security=reality, got %q", q.Get("security"))
	}
	v.PublicKey = q.Get("pbk")
	v.ShortID = q.Get("sid")
	v.SNI = q.Get("sni")
	v.Flow = q.Get("flow")
	v.Fingerprint = q.Get("fp")
	if v.PublicKey == "" || v.ShortID == "" || v.SNI == "" {
		return v, fmt.Errorf("reality 链接缺 pbk/sid/sni 之一")
	}
	if v.Flow == "" {
		v.Flow = "xtls-rprx-vision"
	}
	if v.Fingerprint == "" {
		v.Fingerprint = "chrome"
	}
	return v, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tunnel/ -run TestParseVlessLink -v`
Expected: PASS (both tests)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tunnel/vlesslink.go internal/tunnel/vlesslink_test.go
git add internal/tunnel/vlesslink.go internal/tunnel/vlesslink_test.go
git commit -m "feat(tunnel): parse vless:// reality links"
```

---

### Task 2: Generate sing-box client config from a vless link

**Files:**
- Modify: `internal/tunnel/vlesslink.go` (add method)
- Test: `internal/tunnel/vlesslink_test.go` (add test)

**Interfaces:**
- Consumes: `vlessLink` (Task 1)
- Produces: `func (v vlessLink) singboxConfig(socksAddr string) ([]byte, error)` — returns JSON for a sing-box client (one socks inbound on `socksAddr`, one vless-reality outbound)

- [ ] **Step 1: Write the failing test**

```go
func TestSingboxConfig(t *testing.T) {
	v := vlessLink{
		UUID: "uid", Host: "203.0.113.10", Port: 443,
		PublicKey: "PBK", ShortID: "SID", SNI: "www.microsoft.com",
		Flow: "xtls-rprx-vision", Fingerprint: "chrome",
	}
	b, err := v.singboxConfig("127.0.0.1:10800")
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatalf("not valid json: %v", err)
	}
	in := cfg["inbounds"].([]any)[0].(map[string]any)
	if in["type"] != "socks" || in["listen"] != "127.0.0.1" || in["listen_port"].(float64) != 10800 {
		t.Errorf("inbound wrong: %v", in)
	}
	out := cfg["outbounds"].([]any)[0].(map[string]any)
	if out["type"] != "vless" || out["server"] != "203.0.113.10" || out["server_port"].(float64) != 443 {
		t.Errorf("outbound wrong: %v", out)
	}
	tls := out["tls"].(map[string]any)
	reality := tls["reality"].(map[string]any)
	if tls["server_name"] != "www.microsoft.com" || reality["public_key"] != "PBK" || reality["short_id"] != "SID" {
		t.Errorf("tls/reality wrong: %v", tls)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tunnel/ -run TestSingboxConfig -v`
Expected: FAIL — `v.singboxConfig undefined`

- [ ] **Step 3: Write minimal implementation** (append to `vlesslink.go`; add `encoding/json`, `net` imports)

```go
// singboxConfig 生成最小 sing-box 客户端配置:本地 socks 入站 + vless-reality 出站。
// socksAddr 形如 "127.0.0.1:10800"。bx 数据面只连这个 socks,不关心引擎内部。
func (v vlessLink) singboxConfig(socksAddr string) ([]byte, error) {
	host, portStr, err := net.SplitHostPort(socksAddr)
	if err != nil {
		return nil, fmt.Errorf("拆分 socks 地址 %q: %w", socksAddr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("socks 端口 %q: %w", portStr, err)
	}
	cfg := map[string]any{
		"log": map[string]any{"level": "warn", "timestamp": false},
		"inbounds": []any{map[string]any{
			"type":        "socks",
			"tag":         "socks-in",
			"listen":      host,
			"listen_port": port,
		}},
		"outbounds": []any{map[string]any{
			"type":        "vless",
			"tag":         "reality-out",
			"server":      v.Host,
			"server_port": v.Port,
			"uuid":        v.UUID,
			"flow":        v.Flow,
			"tls": map[string]any{
				"enabled":     true,
				"server_name": v.SNI,
				"utls":        map[string]any{"enabled": true, "fingerprint": v.Fingerprint},
				"reality":     map[string]any{"enabled": true, "public_key": v.PublicKey, "short_id": v.ShortID},
			},
		}},
	}
	return json.MarshalIndent(cfg, "", "  ")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tunnel/ -run TestSingboxConfig -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tunnel/vlesslink.go internal/tunnel/vlesslink_test.go
git add internal/tunnel/vlesslink.go internal/tunnel/vlesslink_test.go
git commit -m "feat(tunnel): generate sing-box client config from vless link"
```

---

### Task 3: REALITY runner factory + NewReality tunnel constructor

**Files:**
- Create: `internal/tunnel/reality.go`
- Test: `internal/tunnel/reality_test.go`

**Interfaces:**
- Consumes: `parseVlessLink`, `singboxConfig` (Tasks 1-2); existing `execRunner`, `New`, `socks5Health`, `pickFreePort` (same package).
- Produces: `func NewReality(singboxBin, link, probe, confPath string) (*Tunnel, error)` and `func realityFactory(singboxBin, link, confPath string) RunnerFactory`

- [ ] **Step 1: Write the failing test**

```go
package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The factory must (a) write a valid sing-box config to confPath derived from the
// link, and (b) build a command pointing sing-box at that config. We use a fake
// binary path and assert the config file is produced + references the socks addr.
func TestRealityFactoryWritesConfig(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "sing-box.json")
	link := "vless://uid@1.2.3.4:443?security=reality&pbk=P&sid=S&sni=www.microsoft.com&flow=xtls-rprx-vision&fp=chrome"
	f := realityFactory("/bin/true", link, conf) // /bin/true exits 0 immediately
	r, err := f("127.0.0.1:10811")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer r.Kill()
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "10811") || !strings.Contains(string(data), "www.microsoft.com") {
		t.Errorf("config missing socks port or sni: %s", data)
	}
}

func TestRealityFactoryRejectsBadLink(t *testing.T) {
	f := realityFactory("/bin/true", "brook://x", filepath.Join(t.TempDir(), "c.json"))
	if _, err := f("127.0.0.1:1"); err == nil {
		t.Fatal("expected error for non-vless link")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/tunnel/ -run TestRealityFactory -v`
Expected: FAIL — `undefined: realityFactory`

- [ ] **Step 3: Write minimal implementation**

```go
package tunnel

import (
	"fmt"
	"os"
	"os/exec"
)

// realityFactory 返回一个 RunnerFactory:从 vless 链接生成 sing-box 配置写到 confPath,
// 再启动 `sing-box run -c confPath`(socks 入站监听传入的 socksAddr)。
// 与 brookFactory 同构:bx 数据面只连本地 socks,不感知引擎。
func realityFactory(singboxBin, link, confPath string) RunnerFactory {
	return func(socksAddr string) (Runner, error) {
		v, err := parseVlessLink(link)
		if err != nil {
			return nil, err
		}
		conf, err := v.singboxConfig(socksAddr)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(confPath, conf, 0o600); err != nil {
			return nil, fmt.Errorf("写 sing-box 配置: %w", err)
		}
		cmd := exec.Command(singboxBin, "run", "-c", confPath)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("启动 sing-box: %w", err)
		}
		return &execRunner{cmd: cmd}, nil
	}
}

// NewReality 用 sing-box 二进制构造 REALITY 隧道,socks5 端口自动选取。
// probe 同 brook(如 "1.1.1.1:443");confPath 是生成的 sing-box 配置落盘路径。
func NewReality(singboxBin, link, probe, confPath string) (*Tunnel, error) {
	if _, err := parseVlessLink(link); err != nil {
		return nil, err // 早失败:链接非法不必等子进程
	}
	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return New(addr, realityFactory(singboxBin, link, confPath), socks5Health(probe)), nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/tunnel/ -run TestRealityFactory -v && go test ./internal/tunnel/ -v`
Expected: PASS (new tests + existing tunnel tests still green)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/tunnel/reality.go internal/tunnel/reality_test.go
git add internal/tunnel/reality.go internal/tunnel/reality_test.go
git commit -m "feat(tunnel): REALITY runner via sing-box subprocess (NewReality)"
```

---

### Task 4: Teach `serverHostFromLink` about `vless://` (anti-loop bypass)

**Files:**
- Modify: `internal/supervisor/brooklink.go:46-63` (the `serverHostFromLink` func)
- Test: `internal/supervisor/brooklink_test.go` (create if absent, else append)

**Interfaces:**
- Consumes/Produces: existing `func serverHostFromLink(server string) (string, error)` — now also handles `vless://`.

- [ ] **Step 1: Write the failing test**

```go
package supervisor

import "testing"

func TestServerHostFromLinkVless(t *testing.T) {
	h, err := serverHostFromLink("vless://uid@203.0.113.10:443?security=reality&pbk=p&sid=s&sni=www.microsoft.com")
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if h != "203.0.113.10" {
		t.Fatalf("host=%q want 203.0.113.10", h)
	}
}

func TestServerHostFromLinkBrookStillWorks(t *testing.T) {
	h, err := serverHostFromLink("brook://server?server=203.0.113.10%3A9999&password=x")
	if err != nil || h != "203.0.113.10" {
		t.Fatalf("brook host=%q err=%v", h, err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/supervisor/ -run TestServerHostFromLinkVless -v`
Expected: FAIL — vless link returns wrong host or error (current code only handles `brook://` and bare host:port)

- [ ] **Step 3: Write minimal implementation** (add a branch at the top of `serverHostFromLink`, before the `brook://` branch)

```go
	if strings.HasPrefix(server, "vless://") {
		u, err := url.Parse(server)
		if err != nil {
			return "", fmt.Errorf("解析 vless link: %w", err)
		}
		host := u.Hostname()
		if host == "" {
			return "", fmt.Errorf("vless link 缺 host")
		}
		return host, nil
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/supervisor/ -run TestServerHostFromLink -v`
Expected: PASS (vless + brook both)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/supervisor/brooklink.go internal/supervisor/brooklink_test.go
git add internal/supervisor/brooklink.go internal/supervisor/brooklink_test.go
git commit -m "feat(supervisor): serverHostFromLink handles vless:// (anti-loop bypass)"
```

---

### Task 5: On-demand sing-box download (`EnsureSingbox`)

**Files:**
- Create: `internal/provision/singbox.go`
- Test: `internal/provision/singbox_test.go`

**Interfaces:**
- Consumes: existing `atomicWrite` (same package, `provision.go`).
- Produces: `func EnsureSingbox(dataDir, override, url, sha256hex string) (string, error)` — returns the sing-box path; downloads to `dataDir/sing-box` and SHA-256-verifies when `sha256hex` is set; caches by hash.

- [ ] **Step 1: Write the failing test**

```go
package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestEnsureSingboxDownloadsAndVerifies(t *testing.T) {
	payload := []byte("#!/bin/sh\necho fake-singbox\n")
	sum := sha256.Sum256(payload)
	hexsum := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	p, err := EnsureSingbox(dir, "", srv.URL, hexsum)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(payload) {
		t.Fatalf("content mismatch")
	}
	// second call uses cache (server can be down): close server, call again
	srv.Close()
	if _, err := EnsureSingbox(dir, "", srv.URL, hexsum); err != nil {
		t.Fatalf("cached ensure failed: %v", err)
	}
}

func TestEnsureSingboxRejectsBadHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	if _, err := EnsureSingbox(t.TempDir(), "", srv.URL, "deadbeef"); err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
}

func TestEnsureSingboxOverride(t *testing.T) {
	f := t.TempDir() + "/mybin"
	os.WriteFile(f, []byte("x"), 0o755)
	p, err := EnsureSingbox(t.TempDir(), f, "", "")
	if err != nil || p != f {
		t.Fatalf("override p=%q err=%v", p, err)
	}
}

func TestEnsureSingboxNoSource(t *testing.T) {
	if _, err := EnsureSingbox(t.TempDir(), "", "", ""); err == nil {
		t.Fatal("expected error when neither override nor url given")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/provision/ -run TestEnsureSingbox -v`
Expected: FAIL — `undefined: EnsureSingbox`

- [ ] **Step 3: Write minimal implementation**

```go
package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// EnsureSingbox 确保 sing-box 可执行存在并返回路径(REALITY 传输按需用)。
// override 非空:直接用该路径(需存在)。否则从 url 下载到 dataDir/sing-box,
// 当 dataDir/.singbox-sha 记录与 sha256hex 一致且文件在时复用(免重复下载,过固件靠 /usrdata)。
// sha256hex 非空时强校验,不匹配硬失败(供应链防护)。
func EnsureSingbox(dataDir, override, url, sha256hex string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("指定的 sing-box 路径不可用 %q: %w", override, err)
		}
		return override, nil
	}
	if url == "" {
		return "", fmt.Errorf("reality 传输需要 singbox_url 或 singbox_bin")
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dataDir, "sing-box")
	verFile := filepath.Join(dataDir, ".singbox-sha")
	if sha256hex != "" {
		if cur, err := os.ReadFile(verFile); err == nil && string(cur) == sha256hex {
			if _, err := os.Stat(target); err == nil {
				return target, nil
			}
		}
	}
	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", fmt.Errorf("下载 sing-box: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 sing-box: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("读 sing-box 响应: %w", err)
	}
	if sha256hex != "" {
		sum := sha256.Sum256(data)
		if got := hex.EncodeToString(sum[:]); got != sha256hex {
			return "", fmt.Errorf("sing-box SHA256 不匹配: 期望 %s 实得 %s", sha256hex, got)
		}
	}
	if err := atomicWrite(target, data, 0o755); err != nil {
		return "", err
	}
	if sha256hex != "" {
		_ = os.WriteFile(verFile, []byte(sha256hex), 0o644)
	}
	return target, nil
}
```

> Note: the downloaded artifact is expected to be a raw executable. If the VPS hosts a `.gz`, the build step that publishes it should host the decompressed binary (keep `EnsureSingbox` simple — no archive handling). Document this in the deploy task.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/provision/ -run TestEnsureSingbox -v`
Expected: PASS (all four)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/provision/singbox.go internal/provision/singbox_test.go
git add internal/provision/singbox.go internal/provision/singbox_test.go
git commit -m "feat(provision): on-demand sing-box download with SHA-256 verify"
```

---

### Task 6: Config fields for the sing-box engine source

**Files:**
- Modify: `internal/config/config.go` (the `Config` struct, ~line 59-73)
- Test: `internal/config/config_test.go` (append)

**Interfaces:**
- Produces: `Config.SingboxURL string` (`yaml:"singbox_url"`), `Config.SingboxSHA256 string` (`yaml:"singbox_sha256"`), `Config.SingboxBin string` (`yaml:"singbox_bin"`).

- [ ] **Step 1: Write the failing test**

```go
func TestParseSingboxFields(t *testing.T) {
	y := []byte("server: vless://uid@1.2.3.4:443?security=reality&pbk=p&sid=s&sni=www.microsoft.com\n" +
		"singbox_url: https://vps.example.com/dl/sing-box-arm64\n" +
		"singbox_sha256: abcdef\n")
	c, err := Parse(y)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.SingboxURL != "https://vps.example.com/dl/sing-box-arm64" || c.SingboxSHA256 != "abcdef" {
		t.Fatalf("singbox fields: url=%q sha=%q", c.SingboxURL, c.SingboxSHA256)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/config/ -run TestParseSingboxFields -v`
Expected: FAIL — `KnownFields(true)` rejects unknown `singbox_url` (`yaml: unknown field`)

- [ ] **Step 3: Write minimal implementation** (add three fields to the `Config` struct)

```go
	SingboxURL    string `yaml:"singbox_url"`    // reality 传输:按需下载 sing-box 的地址(托管在自己 VPS)
	SingboxSHA256 string `yaml:"singbox_sha256"` // 下载校验(强烈建议设置)
	SingboxBin    string `yaml:"singbox_bin"`    // 可选:直接指定本地 sing-box 路径(免下载)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/config/ -run TestParseSingboxFields -v && go test ./internal/config/ -v`
Expected: PASS (new + existing config tests)

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): singbox_url/singbox_sha256/singbox_bin for reality transport"
```

---

### Task 7: Dispatch transport by scheme in `Run`

**Files:**
- Modify: `internal/supervisor/run.go:124-128` (replace the unconditional `tunnel.NewBrook` with a scheme dispatch)

**Interfaces:**
- Consumes: `tunnel.NewBrook` (existing), `tunnel.NewReality` (Task 3), `provision.EnsureSingbox` (Task 5), `cfg.SingboxURL/SingboxSHA256/SingboxBin` (Task 6).

- [ ] **Step 1: Write the failing test** — this is integration glue; assert it builds + the brook path is unchanged via the existing supervisor tests. Add a focused dispatch unit by extracting the choice into a tiny pure helper.

Create the helper test in `internal/supervisor/run_transport_test.go`:

```go
package supervisor

import "testing"

func TestTransportKind(t *testing.T) {
	if transportKind("vless://uid@1.2.3.4:443?security=reality") != "reality" {
		t.Error("vless should be reality")
	}
	if transportKind("brook://server?server=1.2.3.4%3A9999") != "brook" {
		t.Error("brook should be brook")
	}
	if transportKind("anything-else") != "brook" {
		t.Error("default should be brook")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/supervisor/ -run TestTransportKind -v`
Expected: FAIL — `undefined: transportKind`

- [ ] **Step 3: Write minimal implementation**

Add the helper near the top of `run.go` (after imports):

```go
// transportKind 由 server link 的 scheme 选传输:vless://→reality,其余→brook。
func transportKind(server string) string {
	if strings.HasPrefix(server, "vless://") {
		return "reality"
	}
	return "brook"
}
```

Replace `run.go` lines 124-128 (the `tun0, err := tunnel.NewBrook(...)` block) with:

```go
	// 2) 隧道:按 server link 的 scheme 选传输(brook | reality),数据面不变。
	var tun0 *tunnel.Tunnel
	switch transportKind(cfg.Server) {
	case "reality":
		singboxPath, err := provision.EnsureSingbox(cfg.DataDir, cfg.SingboxBin, cfg.SingboxURL, cfg.SingboxSHA256)
		if err != nil {
			return fmt.Errorf("准备 sing-box: %w", err)
		}
		confPath := filepath.Join(cfg.DataDir, "sing-box.json")
		tun0, err = tunnel.NewReality(singboxPath, cfg.Server, opts.Probe, confPath)
		if err != nil {
			return fmt.Errorf("构建 reality 隧道: %w", err)
		}
	default:
		tun0, err = tunnel.NewBrook(brookPath, cfg.Server, opts.Probe, cfg.HTTPProxy)
		if err != nil {
			return fmt.Errorf("构建隧道: %w", err)
		}
	}
```

(`filepath` and `strings` are already imported in run.go; verify with `goimports`/build.)

- [ ] **Step 4: Run test + build to verify**

Run: `go test ./internal/supervisor/ -run TestTransportKind -v && go build ./... && go vet ./...`
Expected: test PASS, build OK, vet clean

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/supervisor/run.go internal/supervisor/run_transport_test.go
git add internal/supervisor/run.go internal/supervisor/run_transport_test.go
git commit -m "feat(supervisor): dispatch transport by server link scheme (brook|reality)"
```

---

### Task 8: Full build, suite, arm64 cross-compile, dry-run

**Files:** none (verification task)

- [ ] **Step 1: Run the full test suite**

Run: `go test ./... -count=1`
Expected: all packages PASS (no regressions in tunnel/supervisor/config/provision/dns/gateway).

- [ ] **Step 2: vet + gofmt check**

Run: `go vet ./... && gofmt -l internal/`
Expected: vet clean; `gofmt -l` prints nothing.

- [ ] **Step 3: arm64 cross-compile (the Mudi target)**

Run: `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -o /tmp/bx_arm64 .`
Expected: builds; `ls -la /tmp/bx_arm64` ~unchanged size (sing-box NOT embedded — confirms no binary bloat).

- [ ] **Step 4: Commit (if any gofmt fixes)**

```bash
git add -A && git commit -m "chore: gofmt + vet for reality transport" || echo "nothing to commit"
```

---

## Deployment & verification (post-merge, on the Mudi — manual, references existing flow)

These steps are operational, not bx code; do them after Tasks 1-8 land and are merged.

- [ ] **Publish a sing-box arm64 binary on the VPS** at `https://vps.example.com/dl/sing-box-arm64` (decompressed, raw executable). Record its `sha256sum`. (Trim via sing-box build tags if easy; else the official linux-arm64 binary is fine — it's on /usrdata, not in bx.)
- [ ] **Get the vless:// link** for the VPS REALITY server (uuid + public-key + short-id + sni=www.microsoft.com + flow=xtls-rprx-vision). The public key is the one from the old VLESS setup (`VLESS_PUBKEY`).
- [ ] **On the Mudi**, set `/usrdata/proxy/etc/bx.yaml`: `server: <vless://…>`, `singbox_url: https://vps.example.com/dl/sing-box-arm64`, `singbox_sha256: <sum>`. Keep the brook-wss link commented as the fallback.
- [ ] Push the new `bx` arm64 binary to `/usrdata/proxy/bin/bx`; `/etc/init.d/bx restart`; confirm sing-box downloaded to `/usrdata/proxy/data/sing-box`, tunnel `● 健康`, `curl https://github.com` = 200, exit IP = VPS, Tailscale online.
- [ ] **Re-run the adversarial leak audit** (browserleaks from a real LAN client: IP=VPS, no DNS leak, no WebRTC IP, no IPv6; kill-switch test) on the REALITY transport.
- [ ] **Update `provision-mudi.sh` + `recover.sh`** (glinet repo) to carry `singbox_url`/`singbox_sha256` in `mudi.env` so REALITY survives a firmware wipe (sing-box binary already persists on /usrdata). *(Small follow-up; separate from the bx code plan.)*

## Separate tracks (not this plan)
- **VPS decoy site** (nginx serves a believable site for non-`/ws` on `vps.example.com`) — hardens the brook-wss fallback. VPS-only.
- **Adversarial leak audit** — verification (also referenced above for the REALITY path).
