# bx 开箱即用引导 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `sudo bx up` / `sudo bx install` 配一行 config、零其它 flag 即可跑起来——brook 与 china 列表内嵌进二进制、首次运行落盘拉起,起来后经隧道定期刷新列表热重载,并由 CI 跟随上游 brook release 自动重新内嵌。

**Architecture:** 新增 `internal/embedded`(go:embed brook + 列表快照)与 `internal/provision`(解压到 `/var/lib/bx`,版本变更自动重解压);supervisor 改读 config(不再依赖 flag),并起后台 goroutine 经 brook socks5 拉最新列表、原子落盘、`atomic.Pointer` 热重载路由;cli/install 收敛为 `bx up -c <path>`;GitHub Actions 监听 `txthinking/brook` release 自动刷新内嵌资产。

**Tech Stack:** Go 1.26、`embed`、`sync/atomic`、`net/http`(经 socks5)、systemd、GitHub Actions。

参考 spec:`docs/superpowers/specs/2026-06-07-bx-zero-config-bootstrap-design.md`

---

## 文件结构

| 文件 | 职责 |
|---|---|
| `internal/embedded/assets/{brook_linux_amd64,china_domain.txt,china_cidr4.txt,BROOK_VERSION}` | 内嵌资产(CI 维护) |
| `internal/embedded/embedded.go` | 暴露内嵌字节 + 版本 |
| `internal/provision/provision.go` | `EnsureBrook` / `EnsureLists` 落盘 |
| `internal/config/config.go` | 新增 `Brook`/`DataDir`/`Lists.AutoUpdate`/`Lists.Interval` + 默认 |
| `internal/dialer/dialer.go` | `Router` 改 `atomic.Pointer` + `SetRouter`(热重载) |
| `internal/supervisor/refresh.go` | 列表刷新器(portable):拉取/原子写/重建/循环 |
| `internal/supervisor/run_linux.go` | 接 provision + 读 cfg + 起刷新 goroutine |
| `internal/cli/cli.go` | flag 转可选覆盖、config 路径回退、`buildExecStart` 收敛 |
| `.github/workflows/embed-brook.yml` | 跟上游 brook 自动重新内嵌 |
| `README.md` | 快速开始改零 flag |

---

## Task 1: `internal/embedded` 内嵌资产

**Files:**
- Create: `internal/embedded/assets/brook_linux_amd64`(从 14.37 取)
- Create: `internal/embedded/assets/china_domain.txt`、`china_cidr4.txt`、`BROOK_VERSION`
- Create: `internal/embedded/embedded.go`
- Test: `internal/embedded/embedded_test.go`

- [ ] **Step 1: 取真实资产入库**

```bash
cd /home/nategu/Documents/bx
mkdir -p internal/embedded/assets
scp 10.0.14.37:'~/.nami/bin/brook' internal/embedded/assets/brook_linux_amd64
scp 10.0.14.37:'~/.brook/china_domain.txt' internal/embedded/assets/china_domain.txt
scp 10.0.14.37:'~/.brook/china_cidr4.txt' internal/embedded/assets/china_cidr4.txt
chmod +x internal/embedded/assets/brook_linux_amd64
# BROOK_VERSION 存「上游 release tag」形式,与 CI 比对口径一致(14.37 brook 自报 20260101)
printf 'v20260101' > internal/embedded/assets/BROOK_VERSION
```

- [ ] **Step 2: 写失败测试**

`internal/embedded/embedded_test.go`:
```go
package embedded

import "testing"

func TestAssetsPresent(t *testing.T) {
	if len(Brook()) == 0 {
		t.Error("brook 资产为空")
	}
	if len(ChinaDomain()) == 0 {
		t.Error("china_domain 资产为空")
	}
	if len(ChinaCIDR()) == 0 {
		t.Error("china_cidr 资产为空")
	}
	if BrookVersion() == "" {
		t.Error("BROOK_VERSION 为空")
	}
}
```

- [ ] **Step 3: 运行测试确认失败**

Run: `go test ./internal/embedded/`
Expected: FAIL(build error:`Brook`/`ChinaDomain`/... 未定义)

- [ ] **Step 4: 写实现**

`internal/embedded/embedded.go`:
```go
// Package embedded 内嵌 brook 二进制与 china 分流列表快照,使 bx 成为零外部依赖的单文件。
// assets/ 由 .github/workflows/embed-brook.yml 跟随上游 txthinking/brook release 自动刷新。
package embedded

import (
	_ "embed"
	"strings"
)

//go:embed assets/brook_linux_amd64
var brook []byte

//go:embed assets/china_domain.txt
var chinaDomain []byte

//go:embed assets/china_cidr4.txt
var chinaCIDR []byte

//go:embed assets/BROOK_VERSION
var brookVersion string

// Brook 返回内嵌的 brook linux/amd64 二进制字节。
func Brook() []byte { return brook }

// ChinaDomain 返回内嵌的 china 域名列表快照。
func ChinaDomain() []byte { return chinaDomain }

// ChinaCIDR 返回内嵌的 china IP 段快照。
func ChinaCIDR() []byte { return chinaCIDR }

// BrookVersion 返回内嵌 brook 的版本(上游 release tag)。
func BrookVersion() string { return strings.TrimSpace(brookVersion) }
```

- [ ] **Step 5: 运行测试确认通过**

Run: `go test ./internal/embedded/`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/embedded
git commit -m "feat(embedded): 内嵌 brook 二进制与 china 列表快照"
```

---

## Task 2: config 扩字段 + 默认值

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `internal/config/config_test.go`:
```go
func TestParseDefaultsForBootstrap(t *testing.T) {
	c, err := Parse([]byte("server: \"brook://x\"\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/var/lib/bx" {
		t.Errorf("DataDir 默认应为 /var/lib/bx, got %q", c.DataDir)
	}
	if !c.Lists.AutoUpdateEnabled() {
		t.Error("AutoUpdate 默认应为 true")
	}
	if c.Lists.RefreshInterval() != 24*time.Hour {
		t.Errorf("Interval 默认应为 24h, got %v", c.Lists.RefreshInterval())
	}
}

func TestParseListsOverrides(t *testing.T) {
	c, err := Parse([]byte("server: \"brook://x\"\nlists:\n  auto_update: false\n  interval: 1h\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Lists.AutoUpdateEnabled() {
		t.Error("auto_update:false 应禁用")
	}
	if c.Lists.RefreshInterval() != time.Hour {
		t.Errorf("interval:1h 应解析为 1h, got %v", c.Lists.RefreshInterval())
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/config/`
Expected: FAIL(`AutoUpdateEnabled`/`RefreshInterval`/`DataDir` 未定义;缺 `time` import)

- [ ] **Step 3: 写实现**

`internal/config/config.go` —— 加 `time` import;扩 `Lists` 与 `Config`;加默认与方法:
```go
type Lists struct {
	ChinaDomain string `yaml:"china_domain"`
	ChinaCIDR   string `yaml:"china_cidr"`
	AutoUpdate  *bool  `yaml:"auto_update"` // nil=默认 true
	Interval    string `yaml:"interval"`    // 如 "24h";空=默认 24h
}

// AutoUpdateEnabled 报告是否启用列表自动刷新(默认 true)。
func (l Lists) AutoUpdateEnabled() bool {
	if l.AutoUpdate == nil {
		return true
	}
	return *l.AutoUpdate
}

// RefreshInterval 返回刷新间隔(非法/空时回退 24h)。
func (l Lists) RefreshInterval() time.Duration {
	if d, err := time.ParseDuration(l.Interval); err == nil && d > 0 {
		return d
	}
	return 24 * time.Hour
}
```
`Config` 结构体加两个字段(放在 `Bypass` 上方即可):
```go
	Brook   string `yaml:"brook"`    // 可选;空=用内嵌 brook
	DataDir string `yaml:"data_dir"` // 运行期数据目录;空=默认 /var/lib/bx
```
`Parse` 末尾 return 前加默认:
```go
	if c.DataDir == "" {
		c.DataDir = "/var/lib/bx"
	}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/config/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): 加 brook/data_dir/lists.auto_update/interval 及默认"
```

---

## Task 3: `internal/provision` 落盘 brook 与列表

**Files:**
- Create: `internal/provision/provision.go`
- Test: `internal/provision/provision_test.go`

- [ ] **Step 1: 写失败测试**

`internal/provision/provision_test.go`:
```go
package provision

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureBrookExtractsAndPins(t *testing.T) {
	dir := t.TempDir()
	p, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "brook") {
		t.Fatalf("路径不对: %q", p)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "BROOKv1" {
		t.Fatalf("内容不对: %q", b)
	}
}

func TestEnsureBrookSkipsWhenVersionMatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1"); err != nil {
		t.Fatal(err)
	}
	// 篡改落盘文件,版本不变时应不重写(保留篡改内容)
	_ = os.WriteFile(filepath.Join(dir, "brook"), []byte("SENTINEL"), 0o755)
	if _, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "brook"))
	if string(b) != "SENTINEL" {
		t.Fatalf("版本一致不应重写, got %q", b)
	}
}

func TestEnsureBrookReExtractsOnVersionChange(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureBrook(dir, "", []byte("BROOKv1"), "v1"); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureBrook(dir, "", []byte("BROOKv2"), "v2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "brook"))
	if string(b) != "BROOKv2" {
		t.Fatalf("版本变更应重写, got %q", b)
	}
}

func TestEnsureBrookOverride(t *testing.T) {
	dir := t.TempDir()
	ov := filepath.Join(dir, "mybrook")
	_ = os.WriteFile(ov, []byte("x"), 0o755)
	p, err := EnsureBrook(dir, ov, []byte("EMBED"), "v1")
	if err != nil {
		t.Fatal(err)
	}
	if p != ov {
		t.Fatalf("应返回 override 路径, got %q", p)
	}
	if _, err := EnsureBrook(dir, filepath.Join(dir, "nope"), nil, "v1"); err == nil {
		t.Fatal("override 不存在应报错")
	}
}

func TestEnsureListsCreatesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	dp, cp, err := EnsureLists(dir, []byte("D"), []byte("C"))
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dp); string(b) != "D" {
		t.Fatalf("domain 内容不对: %q", b)
	}
	if b, _ := os.ReadFile(cp); string(b) != "C" {
		t.Fatalf("cidr 内容不对: %q", b)
	}
	// 已存在(刷新过的新版)不应被内嵌快照覆盖
	_ = os.WriteFile(dp, []byte("FRESH"), 0o644)
	if _, _, err := EnsureLists(dir, []byte("D"), []byte("C")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dp); string(b) != "FRESH" {
		t.Fatalf("已存在列表不应被覆盖, got %q", b)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/provision/`
Expected: FAIL(包不存在)

- [ ] **Step 3: 写实现**

`internal/provision/provision.go`:
```go
// Package provision 把内嵌的 brook 与列表快照落盘到运行期数据目录(默认 /var/lib/bx)。
package provision

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureBrook 确保 brook 可执行存在并返回其路径。
// override 非空时直接用该路径(用户显式指定,需存在);否则把 brookBytes 解压到
// dataDir/brook,当 dataDir/.brook-version 与 version 不一致(或目标缺失)时重新解压。
func EnsureBrook(dataDir, override string, brookBytes []byte, version string) (string, error) {
	if override != "" {
		if _, err := os.Stat(override); err != nil {
			return "", fmt.Errorf("指定的 brook 路径不可用 %q: %w", override, err)
		}
		return override, nil
	}
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(dataDir, "brook")
	verFile := filepath.Join(dataDir, ".brook-version")
	if cur, err := os.ReadFile(verFile); err == nil && string(cur) == version {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}
	if err := atomicWrite(target, brookBytes, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(verFile, []byte(version), 0o644); err != nil {
		return "", err
	}
	return target, nil
}

// EnsureLists 确保 china 列表存在(缺失才从内嵌快照解压;已存在的可能是刷新过的新版,不覆盖)。
func EnsureLists(dataDir string, domainBytes, cidrBytes []byte) (domainPath, cidrPath string, err error) {
	if err = os.MkdirAll(dataDir, 0o755); err != nil {
		return "", "", err
	}
	domainPath = filepath.Join(dataDir, "china_domain.txt")
	cidrPath = filepath.Join(dataDir, "china_cidr4.txt")
	if _, e := os.Stat(domainPath); os.IsNotExist(e) {
		if err = atomicWrite(domainPath, domainBytes, 0o644); err != nil {
			return "", "", err
		}
	}
	if _, e := os.Stat(cidrPath); os.IsNotExist(e) {
		if err = atomicWrite(cidrPath, cidrBytes, 0o644); err != nil {
			return "", "", err
		}
	}
	return domainPath, cidrPath, nil
}

// atomicWrite 写临时文件后 rename,避免覆盖正在执行的文件触发 ETXTBSY/读到半截。
func atomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/provision/`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/provision
git commit -m "feat(provision): EnsureBrook/EnsureLists 内嵌资产原子落盘"
```

---

## Task 4: dialer 路由 `atomic.Pointer` 化(支持热重载)

**Files:**
- Modify: `internal/dialer/dialer.go`
- Test: `internal/dialer/dialer_test.go:29-41`(改 helper)+ 新增热切测试

- [ ] **Step 1: 写失败测试**

追加到 `internal/dialer/dialer_test.go`:
```go
func TestDialerHotSwapRouter(t *testing.T) {
	cn, _ := route.NewCIDRSet([]string{"1.2.0.0/16"})
	px, dr := &recordDialer{}, &recordDialer{}
	d := &Dialer{Proxy: px, Direct: dr, Healthy: func() bool { return true }}

	// 路由 A:8.8.8.8 非中国 → 代理
	d.SetRouter(&route.Router{ChinaCIDR: cn})
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}); err != nil {
		t.Fatal(err)
	}
	if px.lastAddr != "8.8.8.8:443" {
		t.Fatalf("路由A应代理, got proxy=%q", px.lastAddr)
	}

	// 热切到路由 B:8.8.8.8 用户直连 → 直连
	udi, _ := route.NewCIDRSet([]string{"8.8.8.8/32"})
	d.SetRouter(&route.Router{UserDirectIP: udi})
	if _, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("8.8.8.8"), Port: 443}); err != nil {
		t.Fatal(err)
	}
	if dr.lastAddr != "8.8.8.8:443" {
		t.Fatalf("热切后应直连, got direct=%q", dr.lastAddr)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/dialer/ -run TestDialerHotSwapRouter`
Expected: FAIL(`SetRouter` 未定义)

- [ ] **Step 3: 写实现**

`internal/dialer/dialer.go`:加 `"sync/atomic"` import;把结构体里 `Router *route.Router` 改为非导出 atomic 指针,并加 `SetRouter`;`Dial` 里改用 `Load()`:
```go
type Dialer struct {
	router     atomic.Pointer[route.Router]
	Fake       *fakeip.Pool
	Resolver   Resolver
	Proxy      ContextDialer
	Direct     ContextDialer
	Healthy    func() bool
	Killswitch bool
	Stats      DecisionCounter
}

// SetRouter 原子替换当前分流脑(用于列表刷新后的热重载)。
func (d *Dialer) SetRouter(r *route.Router) { d.router.Store(r) }
```
`Dial` 开头取一次快照,后续 `d.Router.Decide`/`d.Router.DecideIP` 全改成局部 `rt`:
```go
func (d *Dialer) Dial(ctx context.Context, m route.Meta) (net.Conn, error) {
	rt := d.router.Load()
	// 1) fake IP 反查域名
	if m.Domain == "" && d.Fake != nil {
		if dom, ok := d.Fake.Domain(m.IP); ok {
			m.Domain = dom
		}
	}
	dec := rt.Decide(m)
	...
		dec = rt.DecideIP(ip)
	...
}
```

- [ ] **Step 4: 改 helper 让旧测试编译**

`internal/dialer/dialer_test.go` 的 `newTestDialer`:把 `Router: r,` 从字面量移除,改成构造后 `SetRouter`:
```go
func newTestDialer(fake *fakeip.Pool, res Resolver, healthy bool, ks bool) (*Dialer, *recordDialer, *recordDialer) {
	cn, _ := route.NewCIDRSet([]string{"1.2.0.0/16"})
	r := &route.Router{
		UserProxy:   route.NewDomainSet([]string{"*.openai.com"}),
		ChinaDomain: route.NewDomainSet([]string{"baidu.com"}),
		ChinaCIDR:   cn,
	}
	px, dr := &recordDialer{}, &recordDialer{}
	d := &Dialer{
		Fake: fake, Resolver: res, Proxy: px, Direct: dr,
		Healthy: func() bool { return healthy }, Killswitch: ks,
	}
	d.SetRouter(r)
	return d, px, dr
}
```

- [ ] **Step 5: 运行确认全过(含 -race)**

Run: `go test -race ./internal/dialer/`
Expected: PASS(热切测试 + 原有 8 个测试)

- [ ] **Step 6: 提交**

```bash
git add internal/dialer/dialer.go internal/dialer/dialer_test.go
git commit -m "feat(dialer): Router 改 atomic.Pointer + SetRouter 支持热重载"
```

---

## Task 5: `internal/supervisor/refresh.go` 列表刷新器(portable)

**Files:**
- Create: `internal/supervisor/refresh.go`(无 build tag)
- Test: `internal/supervisor/refresh_test.go`

- [ ] **Step 1: 写失败测试**

`internal/supervisor/refresh_test.go`:
```go
package supervisor

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPGetOKAndError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bad" {
			w.WriteHeader(500)
			return
		}
		_, _ = w.Write([]byte("LIST"))
	}))
	defer srv.Close()
	b, err := httpGet(context.Background(), srv.Client(), srv.URL+"/ok")
	if err != nil || string(b) != "LIST" {
		t.Fatalf("200 应返回 body, got %q err=%v", b, err)
	}
	if _, err := httpGet(context.Background(), srv.Client(), srv.URL+"/bad"); err == nil {
		t.Fatal("非 200 应报错")
	}
}

func TestAtomicWriteFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f")
	if err := atomicWriteFile(p, []byte("A")); err != nil {
		t.Fatal(err)
	}
	if err := atomicWriteFile(p, []byte("B")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "B" {
		t.Fatalf("应覆盖为 B, got %q", b)
	}
}

func TestRefreshLoopRunsWhenHealthy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n int32
	refreshLoop(ctx, time.Millisecond, func() bool { return true }, func() error {
		if atomic.AddInt32(&n, 1) >= 3 {
			cancel()
		}
		return nil
	})
	if atomic.LoadInt32(&n) < 3 {
		t.Fatalf("健康时应反复刷新, got %d", n)
	}
}

func TestRefreshLoopSkipsWhenUnhealthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var n int32
	refreshLoop(ctx, time.Millisecond, func() bool { return false }, func() error {
		atomic.AddInt32(&n, 1)
		return nil
	})
	if atomic.LoadInt32(&n) != 0 {
		t.Fatalf("不健康不应刷新, got %d", n)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/supervisor/ -run 'HTTPGet|AtomicWriteFile|RefreshLoop'`
Expected: FAIL(`httpGet`/`atomicWriteFile`/`refreshLoop` 未定义)

- [ ] **Step 3: 写实现**

`internal/supervisor/refresh.go`:
```go
package supervisor

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/dialer"
	"github.com/getbx/bx/internal/route"
)

const (
	listDomainURL = "https://txthinking.github.io/bypass/china_domain.txt"
	listCIDRURL   = "https://txthinking.github.io/bypass/china_cidr4.txt"
)

// proxyHTTPClient 构造经 socks5 代理拨号的 http.Client(绕过 github 直连封锁)。
func proxyHTTPClient(pd dialer.ContextDialer) *http.Client {
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{DialContext: pd.DialContext},
	}
}

func httpGet(ctx context.Context, client *http.Client, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func atomicWriteFile(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fetchLists 经 client 拉两份列表并原子写入 dataDir。
func fetchLists(ctx context.Context, client *http.Client, dataDir string) error {
	for _, j := range []struct{ url, name string }{
		{listDomainURL, "china_domain.txt"},
		{listCIDRURL, "china_cidr4.txt"},
	} {
		body, err := httpGet(ctx, client, j.url)
		if err != nil {
			return fmt.Errorf("拉 %s: %w", j.name, err)
		}
		if err := atomicWriteFile(filepath.Join(dataDir, j.name), body); err != nil {
			return err
		}
	}
	return nil
}

func readListFile(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// rebuildRouterFromFiles 从落盘列表重建 Router(沿用 BuildRouter 优先级与内建私网直连)。
func rebuildRouterFromFiles(cfg *config.Config, domainPath, cidrPath string, global bool) (*route.Router, error) {
	r, err := BuildRouter(cfg, readListFile(domainPath), readListFile(cidrPath))
	if err != nil {
		return nil, err
	}
	r.GlobalProxy = global
	return r, nil
}

// refreshLoop 周期刷新:仅在 healthy() 为真时执行 doRefresh;失败非致命。ctx 取消即退出。
func refreshLoop(ctx context.Context, interval time.Duration, healthy func() bool, doRefresh func() error) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !healthy() {
				continue
			}
			if err := doRefresh(); err != nil {
				log.Printf("列表刷新失败(保留旧列表): %v", err)
			} else {
				log.Printf("china 列表已刷新并热重载")
			}
		}
	}
}
```

- [ ] **Step 4: 运行确认通过**

Run: `go test ./internal/supervisor/ -run 'HTTPGet|AtomicWriteFile|RefreshLoop'`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/refresh.go internal/supervisor/refresh_test.go
git commit -m "feat(supervisor): china 列表刷新器(socks5 拉取/原子写/热重载/周期循环)"
```

---

## Task 6: supervisor 接 provision + 读 cfg + 起刷新 goroutine

**Files:**
- Modify: `internal/supervisor/run_linux.go`
- Test: `internal/supervisor/helpers_test.go`(新增,测 `firstNonEmpty`)

- [ ] **Step 1: 写失败测试**

`internal/supervisor/helpers_test.go`:
```go
package supervisor

import "testing"

func TestFirstNonEmpty(t *testing.T) {
	if firstNonEmpty("a", "b") != "a" {
		t.Error("应取第一个非空")
	}
	if firstNonEmpty("", "b") != "b" {
		t.Error("第一个空应取第二个")
	}
	if firstNonEmpty("", "") != "" {
		t.Error("都空应为空")
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/supervisor/ -run TestFirstNonEmpty`
Expected: FAIL(`firstNonEmpty` 未定义)

- [ ] **Step 3: 写实现 —— 加 helper**

在 `internal/supervisor/refresh.go` 末尾加(portable):
```go
func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
```

- [ ] **Step 4: 运行确认 helper 通过**

Run: `go test ./internal/supervisor/ -run TestFirstNonEmpty`
Expected: PASS

- [ ] **Step 5: 接线 run_linux.go**

`internal/supervisor/run_linux.go` import 加 `"github.com/getbx/bx/internal/embedded"` 与 `"github.com/getbx/bx/internal/provision"`。

把 `Run` 里「1) 分流脑」段替换为先 provision、再按模式备列表、再 BuildRouter:
```go
	// 0) 物料:内嵌 brook/列表落盘(零外部依赖)
	brookPath, err := provision.EnsureBrook(cfg.DataDir, firstNonEmpty(opts.BrookBin, cfg.Brook), embedded.Brook(), embedded.BrookVersion())
	if err != nil {
		return fmt.Errorf("准备 brook: %w", err)
	}
	global := cfg.Global || opts.Global

	// 1) 分流脑(global 模式不需要 china 列表)
	var chinaDomain, chinaCIDR []string
	var domainPath, cidrPath string
	if !global {
		domainPath, cidrPath, err = provision.EnsureLists(cfg.DataDir, embedded.ChinaDomain(), embedded.ChinaCIDR())
		if err != nil {
			log.Printf("准备 china 列表失败(降级空列表,等刷新补): %v", err)
		}
		if opts.ChinaDomainPath != "" {
			domainPath = opts.ChinaDomainPath
		}
		if opts.ChinaCIDRPath != "" {
			cidrPath = opts.ChinaCIDRPath
		}
		chinaDomain = readLines(domainPath)
		chinaCIDR = readLines(cidrPath)
	}
	router, err := BuildRouter(cfg, chinaDomain, chinaCIDR)
	if err != nil {
		return fmt.Errorf("构建分流脑: %w", err)
	}
	router.GlobalProxy = global
```
把后面 `router.GlobalProxy = cfg.Global || opts.Global` 那一行**删除**(已用 `global` 设过)。

把 `tunnel.NewBrook(opts.BrookBin, ...)` 改为:
```go
	tun0, err := tunnel.NewBrook(brookPath, cfg.Server, opts.Probe)
```

dialer 构造改为先建后 `SetRouter`(因 `Router` 已非导出):
```go
	d := &dialer.Dialer{
		Fake:       pool,
		Resolver:   newResolver(cfg.DNS.China, direct),
		Proxy:      proxyDialer,
		Direct:     direct,
		Healthy:    tun0.Healthy,
		Killswitch: cfg.Killswitch,
		Stats:      counters,
	}
	d.SetRouter(router)
```

在 `nc.up()` 成功、两条 log 之后、`// 6) 阻塞` 之前,起刷新 goroutine:
```go
	// 列表自动刷新(仅分流模式):隧道健康后周期经 socks5 拉最新列表热重载
	if !global && cfg.Lists.AutoUpdateEnabled() {
		go refreshLoop(ctx, cfg.Lists.RefreshInterval(), tun0.Healthy, func() error {
			client := proxyHTTPClient(proxyDialer)
			if err := fetchLists(ctx, client, cfg.DataDir); err != nil {
				return err
			}
			nr, err := rebuildRouterFromFiles(cfg, domainPath, cidrPath, global)
			if err != nil {
				return err
			}
			d.SetRouter(nr)
			return nil
		})
		log.Printf("china 列表自动刷新已启用: 间隔=%s", cfg.Lists.RefreshInterval())
	}
```

- [ ] **Step 6: 编译 + 全测**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: BUILD OK;全 PASS(含既有 supervisor/route 测试)

- [ ] **Step 7: 提交**

```bash
git add internal/supervisor/run_linux.go internal/supervisor/refresh.go internal/supervisor/helpers_test.go
git commit -m "feat(supervisor): 接 provision 读 cfg 起 brook、起列表自动刷新热重载"
```

---

## Task 7: cli/install 瘦身(零 flag + config 回退 + ExecStart 收敛)

**Files:**
- Modify: `internal/cli/cli.go`
- Test: `internal/cli/cli_test.go`(新增)

- [ ] **Step 1: 写失败测试**

`internal/cli/cli_test.go`:
```go
package cli

import "testing"

func TestBuildExecStart(t *testing.T) {
	got := buildExecStart("/usr/local/bin/bx", "/etc/bx/config.yaml")
	want := "/usr/local/bin/bx up -c /etc/bx/config.yaml"
	if got != want {
		t.Fatalf("ExecStart 应收敛为仅 -c, got %q", got)
	}
}
```

- [ ] **Step 2: 运行确认失败**

Run: `go test ./internal/cli/`
Expected: FAIL(`buildExecStart` 未定义)

- [ ] **Step 3: 写实现**

`internal/cli/cli.go`:

(a) `upFlags()` 里:`config` 默认改 `/etc/bx/config.yaml`;`brook`/`china-domain`/`china-cidr` 默认改 `""`:
```go
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: "/etc/bx/config.yaml", Usage: "配置文件路径(默认 /etc/bx/config.yaml,非 root 回退 ~/.config/bx/config.yaml)"},
		...
		&cli.StringFlag{Name: "brook", Value: "", Usage: "brook 二进制路径(留空=用内嵌 brook)"},
		&cli.StringFlag{Name: "china-domain", Value: "", Usage: "china 域名列表(留空=用内嵌/自动刷新快照)"},
		&cli.StringFlag{Name: "china-cidr", Value: "", Usage: "china IP 段(留空=用内嵌/自动刷新快照)"},
```
(注:删掉这三个 flag 原本 `filepath.Join(home, ...)` 的默认值。)

(b) `loadConfig` 加路径回退;新增 `resolveConfigPath`:
```go
func loadConfig(path string) (*config.Config, error) {
	path = resolveConfigPath(path)
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读配置 %s: %w", path, err)
	}
	return config.Parse(b)
}

// resolveConfigPath:默认路径不存在时回退到家目录配置(便于非 root 只读命令)。
func resolveConfigPath(path string) string {
	if _, err := os.Stat(path); err == nil {
		return path
	}
	home, _ := os.UserHomeDir()
	alt := filepath.Join(home, ".config/bx/config.yaml")
	if _, err := os.Stat(alt); err == nil {
		return alt
	}
	return path
}
```

(c) `installAction` 用 `buildExecStart` + 绝对 config 路径,丢掉 brook/china/tun/probe(已在二进制内有默认):
```go
func installAction(c *cli.Context) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	cfgPath, err := filepath.Abs(resolveConfigPath(c.String("config")))
	if err != nil {
		return err
	}
	if err := install.Install(buildExecStart(bin, cfgPath)); err != nil {
		return err
	}
	fmt.Println("✅ bx 已安装为 systemd 服务并启动(开机自启)。`systemctl status bx` 查看,`bx status` 看面板。")
	return nil
}

// buildExecStart 构造自洽的 systemd ExecStart:只需绝对 bx 与绝对 config,其余走二进制内默认。
func buildExecStart(bin, configPath string) string {
	return fmt.Sprintf("%s up -c %s", bin, configPath)
}
```

- [ ] **Step 4: 运行确认通过 + 全测**

Run: `go test ./internal/cli/ && go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go
git commit -m "feat(cli): flag 转可选覆盖、config 路径回退、ExecStart 收敛为 -c"
```

---

## Task 8: CI 自动重新内嵌 + README 快速开始

**Files:**
- Create: `.github/workflows/embed-brook.yml`
- Modify: `README.md`

- [ ] **Step 1: 写 CI workflow**

`.github/workflows/embed-brook.yml`:
```yaml
name: embed-brook
on:
  schedule:
    - cron: '0 6 * * *'   # 每日 UTC 06:00
  workflow_dispatch:
jobs:
  refresh:
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
      - name: 取上游最新 brook tag 与当前内嵌版本
        id: v
        run: |
          tag=$(curl -fsSL https://api.github.com/repos/txthinking/brook/releases/latest | jq -r .tag_name)
          cur=$(cat internal/embedded/assets/BROOK_VERSION 2>/dev/null || echo none)
          echo "tag=$tag" >>"$GITHUB_OUTPUT"
          echo "cur=$cur" >>"$GITHUB_OUTPUT"
          echo "changed=$([ "$tag" != "$cur" ] && echo yes || echo no)" >>"$GITHUB_OUTPUT"
      - name: 下载并重新内嵌 brook + 列表
        if: steps.v.outputs.changed == 'yes'
        run: |
          curl -fsSL "https://github.com/txthinking/brook/releases/download/${{ steps.v.outputs.tag }}/brook_linux_amd64" \
               -o internal/embedded/assets/brook_linux_amd64
          chmod +x internal/embedded/assets/brook_linux_amd64
          curl -fsSL https://txthinking.github.io/bypass/china_domain.txt -o internal/embedded/assets/china_domain.txt
          curl -fsSL https://txthinking.github.io/bypass/china_cidr4.txt  -o internal/embedded/assets/china_cidr4.txt
          printf '%s' "${{ steps.v.outputs.tag }}" > internal/embedded/assets/BROOK_VERSION
      - uses: actions/setup-go@v5
        if: steps.v.outputs.changed == 'yes'
        with:
          go-version: '1.26'
      - name: 验证可编译可过测
        if: steps.v.outputs.changed == 'yes'
        run: go build ./... && go test ./...
      - name: 提交
        if: steps.v.outputs.changed == 'yes'
        run: |
          git config user.name github-actions
          git config user.email actions@github.com
          git add internal/embedded/assets
          git commit -m "chore(embed): brook 升级到 ${{ steps.v.outputs.tag }}(自动)"
          git push
```

- [ ] **Step 2: 重写 README 快速开始**

把 `README.md` 的「## 配置」之前插入(并删除 14.37 上那段未提交的旧《快速开始》草稿,以本节为准):
````markdown
## 快速开始(开箱即用)

bx 是**单一静态二进制**,brook 与 china 列表已内嵌——无需手动装 brook、无需下列表。

```bash
# ① 构建并安装(需 Go ≥ 1.26)
CGO_ENABLED=0 go build -trimpath -o bx . && sudo install -m755 bx /usr/local/bin/bx

# ② 写一行配置
sudo mkdir -p /etc/bx
sudo tee /etc/bx/config.yaml >/dev/null <<'YAML'
server: "brook://server?server=1.2.3.4%3A9999&password=你的密码"
global: true                 # 全局;按 china 列表分流则改 false
YAML

# ③ 跑 —— 二选一
sudo bx install              # systemd 自启,开机即跑(推荐)
sudo bx up                   # 或前台跑
```

就这些。`bx install` 的 `ExecStart` 收敛为 `bx up -c /etc/bx/config.yaml`,不再需要 `--brook`/`--china-*`。
docker/私网段内建绕过 tun(见下文「配置」注),分流模式下 china 列表起来后经隧道自动刷新。
RFC1918 内网已自动绕过,SSH 不会被锁死;仅当管理地址是公网 IP 才需在 `bypass:` 里写它。
````

- [ ] **Step 3: 编译 + 全测最终确认**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: BUILD OK;全 PASS

- [ ] **Step 4: 提交**

```bash
git add .github/workflows/embed-brook.yml README.md
git commit -m "ci(embed-brook): 跟上游 brook release 自动重新内嵌;docs: 零 flag 快速开始"
```

---

## 收尾:部署到 14.37

- [ ] 交叉编译并推送(含内嵌 brook,bx 体积约 +31MB):
```bash
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /tmp/bx-new .
scp /tmp/bx-new 10.0.14.37:~/bx-new
```
- [ ] 在 14.37 上(需 sudo,前台/tmux)用最小 config 验证零 flag:
```bash
sudo install -m755 ~/bx-new /usr/local/bin/bx
sudo bx down || true
sudo bx up --test-timeout 90s        # config 走 /etc/bx/config.yaml;无需任何路径 flag
```
- [ ] 确认 brook 落盘 `/var/lib/bx/brook`、`bx status` 正常、docker/compass 通。

---

## Self-Review 记录

- **Spec 覆盖**:embedded(T1)、provision(T3)、config 单一源(T2/T6/T7)、热重载(T4)+ 刷新器(T5)+ 接线(T6)、CI(T8)、README(T8)。bypass 自动保护是已交付的内核分流,无需新任务(README 已说明)。✓
- **占位符**:无 TBD/TODO;每个改码步骤含完整代码。✓
- **类型一致**:`EnsureBrook(dataDir, override string, brookBytes []byte, version string)`、`EnsureLists(dataDir string, domainBytes, cidrBytes []byte)`、`SetRouter(*route.Router)`、`firstNonEmpty`、`buildExecStart`、`refreshLoop`/`fetchLists`/`rebuildRouterFromFiles`/`proxyHTTPClient` 在定义与调用处签名一致。✓
