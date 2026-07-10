# Windows 内嵌二进制 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `bx.exe` 自包含——内嵌 wintun.dll + sing-box + brook(windows amd64/arm64),零下载、零手动随行文件,消掉企业 TLS MITM 挡 github 下载的坑。

**Architecture:** 对齐 linux/darwin 已有做法:`internal/embedded` 按 GOOS/GOARCH 条件 `//go:embed` 真二进制,`provision.Ensure*` 首次释放。特例:wintun.dll 受 wireguard-go 加载器约束(只搜 exe 目录 + System32),故释放到 **exe 同目录**(sing-box/brook 是子进程、释放到 DataDir)。

**Tech Stack:** Go 1.26、`//go:embed`、gofumpt、GitHub Actions。

## Global Constraints

- Go 1.26;仓库按 **gofumpt** 格式化(改完 `gofumpt -w`,勿 `gofmt`)。
- 条件 embed 按 GOOS/GOARCH,复刻现有 `embedded_*.go` 的 `//go:build` + `//go:embed` 模式。
- sing-box windows 资产 = **自建静态**:`CGO_ENABLED=0 GOOS=windows GOARCH=<arch> go build -tags with_utls,with_quic -trimpath`(REALITY 需 with_utls、hysteria2 需 with_quic),源用 tag `v1.13.14`(= 现有 `SINGBOX_VERSION`)。
- wintun = wintun.net **0.14.1** 官方签名 dll(amd64/arm64)。
- brook = 官方 `brook_windows_{amd64,arm64}.exe`,tag = 现有 `BROOK_VERSION`(`v20260101.0`)。
- 验证命令:`go build ./... && go vet ./... && go test ./...`;跨平台 `GOOS=windows GOARCH=amd64 go build -o /dev/null ./...`、`GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`。
- 提交信息:中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在默认分支直接提交。
- **资产二进制提交进仓库**(同现有 linux/darwin 真二进制)。

## File Structure

- `internal/provision/execname.go` (+ `_test.go`) — `execName(base)`:windows 加 `.exe` 后缀(纯逻辑)。
- `internal/provision/wintun.go` (+ `_test.go`) — `EnsureWintun(exeDir, bytes, version)`:释放 wintun.dll 到 exe 目录(平台无关文件 I/O,可 Linux 单测)。
- `internal/embedded/assets/` — 新增真二进制:`wintun_windows_{amd64,arm64}.dll`、`singbox_windows_{amd64,arm64}`、`brook_windows_{amd64,arm64}`、`WINTUN_VERSION`。
- `internal/embedded/embedded_wintun_windows_{amd64,arm64}.go` + `embedded_wintun_other.go` — `Wintun()`/`WintunVersion()`。
- `internal/embedded/embedded_singbox_windows_{amd64,arm64}.go` — `var singbox`。
- `internal/embedded/embedded_windows_{amd64,arm64}.go` — `var brook`。
- `internal/embedded/embedded_other.go`、`embedded_singbox_other.go` — 更新 `//go:build` 纳入 windows/amd64/arm64。
- `internal/embedded/embedded.go` — 加 `WINTUN_VERSION` embed 变量 + `Wintun()`/`WintunVersion()`(或放 wintun 专属文件)。
- `internal/supervisor/platform_windows.go` — `OpenTUN` 首行调 `EnsureWintun`;`EnsureSingbox`/`EnsureBrook` 目标名过 `execName`。
- `internal/install/sidefiles_windows.go`、`sidefiles_other.go` — 删;`install.go` 去掉 `installPlatformSideFiles` 调用。
- `.github/workflows/embed-singbox.yml`、`embed-brook.yml` — 加 windows。

---

### Task 1: `execName` — windows 可执行名加 `.exe`

**Files:**
- Create: `internal/provision/execname.go`
- Test: `internal/provision/execname_test.go`

**Interfaces:**
- Produces: `func execName(base string) string` — windows 返回 `base+".exe"`,其余原样。

- [ ] **Step 1: 写失败测试**

```go
// internal/provision/execname_test.go
package provision

import (
	"runtime"
	"testing"
)

func TestExecName(t *testing.T) {
	got := execName("sing-box")
	want := "sing-box"
	if runtime.GOOS == "windows" {
		want = "sing-box.exe"
	}
	if got != want {
		t.Fatalf("execName(sing-box) on %s = %q, want %q", runtime.GOOS, got, want)
	}
}

// 已带 .exe 不重复加(幂等)。
func TestExecNameIdempotent(t *testing.T) {
	if runtime.GOOS == "windows" && execName("sing-box.exe") != "sing-box.exe" {
		t.Fatalf("execName 应幂等,不重复加 .exe")
	}
}
```

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/provision/ -run TestExecName -v`
Expected: FAIL(`undefined: execName`)

- [ ] **Step 3: 实现**

```go
// internal/provision/execname.go
package provision

import (
	"runtime"
	"strings"
)

// execName 给可执行基名按平台补后缀:windows 加 .exe(已有则不重复),其余原样。
// 内嵌/下载的 sing-box、brook 释放到磁盘后要 exec,windows 需 .exe 才稳妥可执行。
func execName(base string) string {
	if runtime.GOOS == "windows" && !strings.HasSuffix(strings.ToLower(base), ".exe") {
		return base + ".exe"
	}
	return base
}
```

- [ ] **Step 4: 跑测试看绿**

Run: `go test ./internal/provision/ -run TestExecName -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
gofumpt -w internal/provision/execname.go internal/provision/execname_test.go
git add internal/provision/execname.go internal/provision/execname_test.go
git commit -m "feat(provision): execName——windows 可执行名加 .exe 后缀

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `EnsureBrook`/`EnsureSingbox` 释放目标名过 `execName`

**Files:**
- Modify: `internal/provision/provision.go`(`EnsureBrook` 里 `target := filepath.Join(dataDir, "brook")`)
- Modify: `internal/provision/singbox.go`(`EnsureSingbox` 里 `target := filepath.Join(dataDir, "sing-box")`)

**Interfaces:**
- Consumes: `execName` (Task 1)。

- [ ] **Step 1: 改 EnsureBrook 目标**

`internal/provision/provision.go` 里:
```go
	target := filepath.Join(dataDir, "brook")
```
改为:
```go
	target := filepath.Join(dataDir, execName("brook"))
```

- [ ] **Step 2: 改 EnsureSingbox 目标**

`internal/provision/singbox.go` 里:
```go
	target := filepath.Join(dataDir, "sing-box")
```
改为:
```go
	target := filepath.Join(dataDir, execName("sing-box"))
```

- [ ] **Step 3: 验证不回归(现有 provision 测试仍绿)**

Run: `go build ./... && go test ./internal/provision/`
Expected: ok(linux 下 execName 无副作用,目标名不变;现有 `TestEnsureBrook*`/singbox 测试通过)

- [ ] **Step 4: 提交**

```bash
git add internal/provision/provision.go internal/provision/singbox.go
git commit -m "fix(provision): brook/sing-box 释放目标在 windows 用 .exe(execName)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: `EnsureWintun` — 释放 wintun.dll 到 exe 目录(TDD)

**Files:**
- Create: `internal/provision/wintun.go`
- Test: `internal/provision/wintun_test.go`

**Interfaces:**
- Consumes: `embedCacheKey`、`atomicWrite`(provision.go 现有)。
- Produces: `func EnsureWintun(exeDir string, wintunBytes []byte, version string) (string, error)` — 确保 `exeDir/wintun.dll` 存在且与 `(version,bytes)` 匹配,返回 dll 路径。`wintunBytes` 为空(无内嵌 arch)→ 返回空路径 + nil(不报错,调用方靠系统已装的 dll)。

- [ ] **Step 1: 写失败测试**

```go
// internal/provision/wintun_test.go
package provision

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureWintunWritesAndPins(t *testing.T) {
	dir := t.TempDir()
	p, err := EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1")
	if err != nil {
		t.Fatal(err)
	}
	if p != filepath.Join(dir, "wintun.dll") {
		t.Fatalf("路径不对: %q", p)
	}
	b, _ := os.ReadFile(p)
	if string(b) != "WINTUNv1" {
		t.Fatalf("内容不对: %q", b)
	}
}

func TestEnsureWintunSkipsWhenVersionMatches(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1"); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "wintun.dll"), []byte("SENTINEL"), 0o644)
	if _, err := EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "wintun.dll"))
	if string(b) != "SENTINEL" {
		t.Fatalf("版本一致不应重写, got %q", b)
	}
}

func TestEnsureWintunReExtractsOnVersionChange(t *testing.T) {
	dir := t.TempDir()
	_, _ = EnsureWintun(dir, []byte("WINTUNv1"), "0.14.1")
	if _, err := EnsureWintun(dir, []byte("WINTUNv2"), "0.14.2"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "wintun.dll"))
	if string(b) != "WINTUNv2" {
		t.Fatalf("版本变更应重写, got %q", b)
	}
}

// 无内嵌(nil bytes):返回空路径 + nil,不写文件(靠系统已装 wintun.dll)。
func TestEnsureWintunNoEmbedNoop(t *testing.T) {
	dir := t.TempDir()
	p, err := EnsureWintun(dir, nil, "")
	if err != nil {
		t.Fatalf("nil bytes 不应报错: %v", err)
	}
	if p != "" {
		t.Fatalf("nil bytes 应返回空路径, got %q", p)
	}
	if _, err := os.Stat(filepath.Join(dir, "wintun.dll")); err == nil {
		t.Fatal("nil bytes 不应写出 wintun.dll")
	}
}
```

- [ ] **Step 2: 跑测试看红**

Run: `go test ./internal/provision/ -run TestEnsureWintun -v`
Expected: FAIL(`undefined: EnsureWintun`)

- [ ] **Step 3: 实现**

```go
// internal/provision/wintun.go
package provision

import (
	"os"
	"path/filepath"
)

// EnsureWintun 确保 exeDir/wintun.dll 与内嵌字节一致并返回其路径。wireguard-go 的 wintun 加载器
// 只搜 exe 目录 + System32(LOAD_LIBRARY_SEARCH_APPLICATION_DIR|SEARCH_SYSTEM32),故释放到 exe 目录。
// wintunBytes 为空(无内嵌 arch)→ 返回 ("", nil):调用方靠系统已安装的 wintun.dll。
// 版本键缓存(exeDir/.wintun-version = version+内容hash):一致且文件在则复用,否则原子写出。
func EnsureWintun(exeDir string, wintunBytes []byte, version string) (string, error) {
	if len(wintunBytes) == 0 {
		return "", nil
	}
	target := filepath.Join(exeDir, "wintun.dll")
	verFile := filepath.Join(exeDir, ".wintun-version")
	key := embedCacheKey(version, wintunBytes)
	if cur, err := os.ReadFile(verFile); err == nil && string(cur) == key {
		if _, err := os.Stat(target); err == nil {
			return target, nil
		}
	}
	if err := atomicWrite(target, wintunBytes, 0o644); err != nil {
		return "", err
	}
	_ = os.WriteFile(verFile, []byte(key), 0o644)
	return target, nil
}
```

- [ ] **Step 4: 跑测试看绿**

Run: `go test ./internal/provision/ -run TestEnsureWintun -v`
Expected: PASS(4 个测试全过)

- [ ] **Step 5: 提交**

```bash
gofumpt -w internal/provision/wintun.go internal/provision/wintun_test.go
git add internal/provision/wintun.go internal/provision/wintun_test.go
git commit -m "feat(provision): EnsureWintun——释放内嵌 wintun.dll 到 exe 目录(版本键缓存)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: wintun 内嵌访问器 + 资产 + 接进 OpenTUN

**Files:**
- Create: `internal/embedded/assets/WINTUN_VERSION`、`assets/wintun_windows_amd64.dll`、`assets/wintun_windows_arm64.dll`
- Create: `internal/embedded/embedded_wintun_windows_amd64.go`、`embedded_wintun_windows_arm64.go`、`embedded_wintun_other.go`
- Modify: `internal/embedded/embedded.go`(加 `WINTUN_VERSION` embed + 访问器)
- Modify: `internal/supervisor/platform_windows.go`(`OpenTUN` 首行调 `EnsureWintun`)

**Interfaces:**
- Consumes: `provision.EnsureWintun`(Task 3)。
- Produces: `embedded.Wintun() []byte`、`embedded.WintunVersion() string`。

- [ ] **Step 1: 取 wintun.dll(amd64/arm64)+ 版本文件**

```bash
cd internal/embedded/assets
curl -fsSL -o /tmp/wintun.zip https://www.wintun.net/builds/wintun-0.14.1.zip
unzip -oj /tmp/wintun.zip 'wintun/bin/amd64/wintun.dll' -d /tmp && cp /tmp/wintun.dll wintun_windows_amd64.dll
unzip -oj /tmp/wintun.zip 'wintun/bin/arm64/wintun.dll' -d /tmp && cp /tmp/wintun.dll wintun_windows_arm64.dll
printf '0.14.1' > WINTUN_VERSION
ls -la wintun_windows_*.dll WINTUN_VERSION
```
Expected: 两个 ~427KB 的 dll + WINTUN_VERSION。

- [ ] **Step 2: 条件 embed .go 文件**

```go
// internal/embedded/embedded_wintun_windows_amd64.go
//go:build windows && amd64

package embedded

import _ "embed"

//go:embed assets/wintun_windows_amd64.dll
var wintun []byte
```
```go
// internal/embedded/embedded_wintun_windows_arm64.go
//go:build windows && arm64

package embedded

import _ "embed"

//go:embed assets/wintun_windows_arm64.dll
var wintun []byte
```
```go
// internal/embedded/embedded_wintun_other.go
//go:build !(windows && amd64) && !(windows && arm64)

package embedded

// 非 windows/amd64|arm64:无内嵌 wintun(wintun 仅 windows 用),wintun 为 nil。
var wintun []byte
```

- [ ] **Step 3: `embedded.go` 加 WINTUN_VERSION + 访问器**

在 `embedded.go` 的 embed 变量区加:
```go
//go:embed assets/WINTUN_VERSION
var wintunVersion string
```
在访问器区加:
```go
// Wintun 返回内嵌的、与当前架构匹配的 wintun.dll 字节(仅 windows amd64/arm64 非空;
// 其他平台为 nil,调用方靠系统已安装的 wintun.dll)。只读,不得修改返回的切片。
func Wintun() []byte { return wintun }

// WintunVersion 返回内嵌 wintun 的版本。
func WintunVersion() string { return strings.TrimSpace(wintunVersion) }
```

- [ ] **Step 4: OpenTUN 首行接 EnsureWintun**

`internal/supervisor/platform_windows.go` 的 `OpenTUN` 开头(`if name == ""` 之前)加:
```go
	if exe, err := os.Executable(); err == nil {
		if _, werr := provision.EnsureWintun(filepath.Dir(exe), embedded.Wintun(), embedded.WintunVersion()); werr != nil {
			return nil, tunHandle{}, nil, fmt.Errorf("释放内嵌 wintun.dll 到 exe 目录: %w", werr)
		}
	}
```
并在 import 加 `"os"`、`"path/filepath"`、`"github.com/getbx/bx/internal/embedded"`、`"github.com/getbx/bx/internal/provision"`(若未导入)。

- [ ] **Step 5: 跨平台构建验证**

Run: `go build ./... && GOOS=windows GOARCH=amd64 go build -o /dev/null ./... && GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`
Expected: 全过(windows 构建 `embedded.Wintun()` 非空,`EnsureWintun` 接线编译通过)

- [ ] **Step 6: embedded_test.go 加 windows 断言**

`internal/embedded/embedded_test.go` 加(与现有 brook/singbox windows 断言同风格):
```go
func TestWintunEmbeddedOnWindows(t *testing.T) {
	if runtime.GOOS == "windows" && len(Wintun()) == 0 {
		t.Fatal("windows 构建应内嵌 wintun.dll")
	}
	if runtime.GOOS != "windows" && len(Wintun()) != 0 {
		t.Fatal("非 windows 不应有内嵌 wintun")
	}
}
```

- [ ] **Step 7: 提交**

```bash
gofumpt -w internal/embedded/*.go internal/supervisor/platform_windows.go
go build ./... && go test ./internal/embedded/ ./internal/supervisor/
git add internal/embedded/ internal/supervisor/platform_windows.go
git commit -m "feat(windows): 内嵌 wintun.dll(amd64/arm64)+ OpenTUN 首行自动释放到 exe 目录

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: sing-box windows 内嵌(自建静态资产 + 条件文件)

**Files:**
- Create: `internal/embedded/assets/singbox_windows_amd64`、`singbox_windows_arm64`
- Create: `internal/embedded/embedded_singbox_windows_amd64.go`、`embedded_singbox_windows_arm64.go`
- Modify: `internal/embedded/embedded_singbox_other.go`(build 约束纳入 windows/amd64/arm64)

- [ ] **Step 1: 自建静态 windows sing-box(amd64/arm64)**

```bash
SBVER=$(cat internal/embedded/assets/SINGBOX_VERSION)   # 1.13.14
git clone --depth 1 --branch "v$SBVER" https://github.com/SagerNet/sing-box /tmp/sing-box-src
cd /tmp/sing-box-src
for arch in amd64 arm64; do
  CGO_ENABLED=0 GOOS=windows GOARCH=$arch go build -tags with_utls,with_quic -trimpath \
    -ldflags "-s -w -X github.com/sagernet/sing-box/constant.Version=$SBVER" \
    -o "$OLDPWD/internal/embedded/assets/singbox_windows_$arch" ./cmd/sing-box
done
cd "$OLDPWD"
file internal/embedded/assets/singbox_windows_amd64 | grep -q 'PE32+' || { echo "非 windows PE,中止"; exit 1; }
ls -la internal/embedded/assets/singbox_windows_*
```
Expected: 两个 ~28MB 的 windows PE(`PE32+ ... x86-64` / `Aarch64`)。

- [ ] **Step 2: 条件 embed .go**

```go
// internal/embedded/embedded_singbox_windows_amd64.go
//go:build windows && amd64

package embedded

import _ "embed"

//go:embed assets/singbox_windows_amd64
var singbox []byte
```
```go
// internal/embedded/embedded_singbox_windows_arm64.go
//go:build windows && arm64

package embedded

import _ "embed"

//go:embed assets/singbox_windows_arm64
var singbox []byte
```

- [ ] **Step 3: 更新 `embedded_singbox_other.go` build 约束**

```go
//go:build !(linux && amd64) && !(linux && arm64) && !(darwin && amd64) && !(darwin && arm64) && !(windows && amd64) && !(windows && arm64)
```
(其余内容不变)

- [ ] **Step 4: 跨平台构建 + windows 内嵌断言**

Run: `GOOS=windows GOARCH=amd64 go build -o /dev/null ./... && GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`
Expected: 全过。若 `embedded_singbox_test.go` 有 windows 断言则加(同 brook 风格:windows 下 `Singbox()` 非空)。

- [ ] **Step 5: 提交**

```bash
git add internal/embedded/assets/singbox_windows_* internal/embedded/embedded_singbox_windows_*.go internal/embedded/embedded_singbox_other.go
git commit -m "feat(windows): 内嵌自建静态 sing-box(amd64/arm64,with_utls,with_quic)——reality/vless 免下载

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: brook windows 内嵌(官方 exe 资产 + 条件文件)

**Files:**
- Create: `internal/embedded/assets/brook_windows_amd64`、`brook_windows_arm64`
- Create: `internal/embedded/embedded_windows_amd64.go`、`embedded_windows_arm64.go`
- Modify: `internal/embedded/embedded_other.go`(build 约束纳入 windows/amd64/arm64)

- [ ] **Step 1: 取官方 brook windows(amd64/arm64)**

```bash
BKVER=$(cat internal/embedded/assets/BROOK_VERSION)   # v20260101.0
cd internal/embedded/assets
curl -fsSL -o brook_windows_amd64 "https://github.com/txthinking/brook/releases/download/$BKVER/brook_windows_amd64.exe"
curl -fsSL -o brook_windows_arm64 "https://github.com/txthinking/brook/releases/download/$BKVER/brook_windows_arm64.exe"
file brook_windows_amd64 | grep -q 'PE32+' || { echo "非 windows PE,中止"; exit 1; }
ls -la brook_windows_*
```
Expected: 两个 windows PE(若 arm64 资产 upstream 不存在,见 Task 8 风险——arm64 brook 走 nil 兜底、amd64 主线不阻塞)。

- [ ] **Step 2: 条件 embed .go**

```go
// internal/embedded/embedded_windows_amd64.go
//go:build windows && amd64

package embedded

import _ "embed"

//go:embed assets/brook_windows_amd64
var brook []byte
```
```go
// internal/embedded/embedded_windows_arm64.go
//go:build windows && arm64

package embedded

import _ "embed"

//go:embed assets/brook_windows_arm64
var brook []byte
```

- [ ] **Step 3: 更新 `embedded_other.go` build 约束**

```go
//go:build !(linux && amd64) && !(linux && arm64) && !(darwin && amd64) && !(darwin && arm64) && !(windows && amd64) && !(windows && arm64)
```

- [ ] **Step 4: 跨平台构建验证**

Run: `GOOS=windows GOARCH=amd64 go build -o /dev/null ./... && GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`
Expected: 全过。

- [ ] **Step 5: 提交**

```bash
git add internal/embedded/assets/brook_windows_* internal/embedded/embedded_windows_*.go internal/embedded/embedded_other.go
git commit -m "feat(windows): 内嵌 brook(amd64/arm64)——brook 传输免下载

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: 删除 wintun 随行逻辑(改为内嵌释放)

**Files:**
- Delete: `internal/install/sidefiles_windows.go`、`internal/install/sidefiles_other.go`
- Modify: `internal/install/install.go`(`SelfInstall` 去掉 `installPlatformSideFiles` 调用)
- Modify: `internal/supervisor/platform_windows.go`(`OpenTUN` 的 `CreateTUN` 错误信息更新)

**Interfaces:**
- 现在 wintun.dll 由 `EnsureWintun`(Task 4)在 OpenTUN 释放,`SelfInstall` 不再管随行 dll。

- [ ] **Step 1: 删 sidefiles**

```bash
git rm internal/install/sidefiles_windows.go internal/install/sidefiles_other.go
```

- [ ] **Step 2: `SelfInstall` 去掉 side-files 调用**

`internal/install/install.go` 的 `SelfInstall` 里删掉这段:
```go
	// 平台随行文件:Windows 需把 wintun.dll 一并装到 BinPath 同目录……
	if err := installPlatformSideFiles(self, BinPath); err != nil {
		return "", err
	}
```

- [ ] **Step 3: OpenTUN 错误信息更新**

`platform_windows.go` 的 `CreateTUN` 失败信息:
```go
		return nil, tunHandle{}, nil, fmt.Errorf("创建 wintun 适配器(需管理员 + wintun.dll 同目录): %w", err)
```
改为:
```go
		return nil, tunHandle{}, nil, fmt.Errorf("创建 wintun 适配器(需管理员;wintun.dll 由 bx 自动释放到 exe 目录,若失败见该目录是否可写): %w", err)
```

- [ ] **Step 4: 全平台构建/测试**

Run: `go build ./... && go vet ./... && go test ./... && GOOS=windows GOARCH=amd64 go build -o /dev/null ./... && GOOS=windows GOARCH=arm64 go build -o /dev/null ./...`
Expected: 全绿(sidefiles 删除后无残留引用)。

- [ ] **Step 5: 提交**

```bash
git add -A internal/install/ internal/supervisor/platform_windows.go
git commit -m "refactor(windows): 删 wintun 随行逻辑(SelfInstall 拷 dll)——改为内嵌自动释放

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: CI 跟上 windows 内嵌资产

**Files:**
- Modify: `.github/workflows/embed-singbox.yml`(构建 os 列表加 windows)
- Modify: `.github/workflows/embed-brook.yml`(资产抓取加 windows amd64/arm64)

**Interfaces:**
- CI 复刻本地资产获取(Task 4/5/6),保证换版本时资产自动重嵌。

- [ ] **Step 1: embed-singbox.yml 加 windows**

在自建 job 的 os 循环里,把 `linux darwin` 扩为 `linux darwin windows`(输出名 `singbox_${os}_${arch}` 已通用,windows PE 无需 .exe 后缀作为 embed 资产名)。校验行加 windows PE 检查:
```yaml
          file internal/embedded/assets/singbox_windows_amd64 | grep -q 'PE32+' || { echo "windows 非 PE,中止"; exit 1; }
```
跨平台构建校验加:
```yaml
          CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
          CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -o /dev/null ./...
```

- [ ] **Step 2: embed-brook.yml 加 windows**

在资产抓取步骤加 `brook_windows_amd64`、`brook_windows_arm64`(从 upstream release,同现有 linux/darwin 抓取逻辑)。

- [ ] **Step 3: (可选)wintun 版本刷新说明**

在 `embed-singbox.yml` 或新增注释:wintun.dll 版本由 `assets/WINTUN_VERSION` 固定,升级手动跑 Task 4 Step 1(wintun 无 CI 自动 release 跟随,升级频率低)。

- [ ] **Step 4: YAML 语法自检 + 提交**

```bash
python3 -c "import yaml,sys; [yaml.safe_load(open(f)) for f in ['.github/workflows/embed-singbox.yml','.github/workflows/embed-brook.yml']]; print('yaml ok')"
git add .github/workflows/embed-singbox.yml .github/workflows/embed-brook.yml
git commit -m "ci: embed-singbox/brook 跟随 windows amd64/arm64 内嵌资产

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9: 真机验收(030-SJWJ-GSR-B,安全梯度 + 死手)

**非 TDD——真机 e2e 验收,不改代码;失败则回到对应 Task 修。**

- [ ] **Step 1: 交叉编译单文件 bx.exe**

Run: `GOOS=windows GOARCH=amd64 go build -o bx.exe .`
Expected: ~80MB(含内嵌 wintun/sing-box/brook)。

- [ ] **Step 2: 空目录部署 + reality e2e(无任何随行文件)**

只拷**一个 bx.exe** 到空目录;config 只含 reality server(**不设 singbox_bin**)+ bypass SSH 源。管理员跑:
```
.\bx.exe run --test-timeout 2m -c config.yaml
```
Expected(读日志):
- bx 自动在 exe 旁生成 `wintun.dll`、DataDir 释放 `sing-box.exe`;
- **企业 MITM 网络下零 github 下载**(日志无「下载 sing-box/brook」、无 `x509` 错误);
- reality 隧道健康、整机出口==VPS、死手还原干净。

- [ ] **Step 3: 便携性**

把 bx.exe 拷到另一个空目录双击 `bx run` 同样跑通(自动释放 wintun.dll 到该目录)。

- [ ] **Step 4: 清理真机**(删 bx.exe + 自动生成的 wintun.dll + DataDir + config)。

---

## Self-Review

- **Spec coverage**:①内嵌矩阵→Task 4/5/6;②wintun 释放 exe 目录→Task 3/4;③singbox/brook 内嵌免下载→Task 5/6(靠现有 EnsureSingbox/EnsureBrook 内嵌优先)+ Task 2(.exe 目标);④CI→Task 8;⑤删随行→Task 7;⑥验收→Task 9。覆盖齐。
- **Placeholder scan**:无 TBD;各步含真实代码/命令。arm64 brook 若 upstream 缺资产→Task 6 Step 1 + Task 8 风险注明走 nil 兜底,非占位。
- **Type consistency**:`execName`(Task 1)被 Task 2 用;`EnsureWintun(exeDir, bytes, version)(string,error)`(Task 3)被 Task 4 用;`embedded.Wintun()/WintunVersion()`(Task 4)在 OpenTUN 用——签名一致。
