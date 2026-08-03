# macOS 统一 Bx.app(Phase 1:统一无签名产品包)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 macOS 产品收敛为「安装并打开 `/Applications/Bx.app`」一个对象:App 内嵌该 release 的 CLI 资产,一次管理员授权装出 root-owned runtime(`/Library/Application Support/bx/runtime/<version>/`)+ 稳定 CLI bridge(`/usr/local/bin/bx`)+ Guardian LaunchDaemon + 登录项,单实例、可迁移旧副本,root 永不执行用户可写二进制。

**Architecture:** 新增三个纯 Go 包(`internal/release` release.json 元数据、`internal/runtimedir` 版本化 runtime 目录、`internal/bridge` bridge 解析校验)+ 一个小 main(`cmd/bx-bridge`);`bx app-install`(hidden,root)是唯一特权安装入口,从经过 digest 验证的 Bx.app bundle 原子安装全部组件;Guardian plist 与 Core 子进程改为钉在 runtime 版本目录上;BxMenu 增加单实例守护与「Install bx」状态。**不动数据面、不动 Guardian 契约**(socket/labels/`/var/lib/bx` 全不变)。统一更新事务(Guardian `/v1/update` 接线、三版本 Repair)是 Phase 2,另写计划;本计划对旧 `bx update` 路径加护栏防止其破坏新布局。

**Tech Stack:** Go 1.26(urfave/cli v2、x/sys)、Swift 5.9(AppKit,无第三方依赖)、shell 打包脚本、launchd。

## Global Constraints

- **绝不擅自启动 bx / 改路由 / 动真实 launchd**:实现期间不执行 `bx up/down/reconnect/update`、`sudo`、`launchctl` 变更、DNS/路由修改;涉及真机系统状态的验证一律先 dry-run 打印计划,真实执行只在文末「Manual macOS Acceptance Gate」由用户亲自授权跑。
- **既有路径/label 一个不动**:`/var/run/bx-guard.sock`、`/var/run/bx.sock`、`/var/lib/bx/**`、`/etc/bx/config.yaml`、`com.getbx.bx.guard`、`com.getbx.bx.menu`、legacy `com.ggshr9.*` 迁移语义,全部保持。
- **新增固定路径(本计划唯一新增)**:`/Library/Application Support/bx/runtime/<version>/{bx,release.json}` + `current -> <version>`(root:wheel,目录 0755、bx 0755、release.json 0644);bridge 恒为 `/usr/local/bin/bx`;App 恒为 `/Applications/Bx.app`(root:wheel)。
- **root 不执行用户可写二进制**:Guardian plist 与 Core 只指向 runtime 目录;bridge 校验 runtime 逃逸/属主/权限后才 exec;`/usr/local/bin/bx` 绝不软链到用户可写路径。
- **安装不启动保护**:`app-install` 只写文件与 plist,不 `bootstrap` Guardian、不执行 `bx up`、不改用户配置;Guardian 的 enable 仍由既有 `sudo bx up`(`ensureGuardianOwnership`)完成。
- **Quit Menu ≠ Turn Off**:菜单退出不停止保护(现状已如此,勿回退)。
- **无秘密泄漏**:argv、日志、release.json、错误信息不含 client link/token。
- **兼容 legacy 布局**:runtime 未安装时,darwin 上 `bx setup`/`bx up` 走现有 `SelfInstall + BinPath` 流程不变;两种布局由 `runtimedir.Installed` 区分。
- **验证命令**:`go build ./... && go vet ./... && go test ./...`(本机即 darwin)+ `GOOS=linux GOARCH=amd64 go build -o /dev/null ./...` + `GOOS=windows GOARCH=amd64 go build -o /dev/null ./...`;`gofumpt -l <改动目录>` 无输出;Swift 改动跑 `bash scripts/test-macos-menu.sh`。
- **Swift 测试双跑器同步**:新增 Swift 测试必须同时登记到 `apps/macos/BxMenu/run-swift-tests.sh` 与 `scripts/test-macos-menu.sh`(两份编译清单人工保持一致)。
- **提交**:中文 conventional commits,直接提交 master,结尾 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。

---

## File Structure

**创建(Go):**
- `internal/release/release.go` + `release_test.go` — release.json schema、Load/Parse/Write、digest/平台校验。
- `internal/runtimedir/runtimedir.go` + `runtimedir_test.go` — 版本化 runtime 目录:InstallVersion/SwitchCurrent/Current/Installed。
- `internal/bridge/bridge.go` + `bridge_test.go` — bridge 的 runtime 解析与安全校验(纯逻辑,注入 stat/owner)。
- `cmd/bx-bridge/main_darwin.go` / `main_other.go` — bridge 可执行入口(darwin exec,其他平台报错)。
- `internal/install/unified_darwin.go` + `unified_darwin_test.go` — `UnifiedInstall`:bundle 验证→App 落位→runtime→bridge→Guardian plist→登录项→legacy 迁移。
- `internal/cli/appinstall_darwin.go` / `appinstall_other.go` + `appinstall_test.go` — hidden `bx app-install` 命令。
- `internal/cli/uninstall_darwin.go` / (改 `uninstallAction` 分派) + `uninstall_darwin_test.go` — darwin 完整卸载计划(纯计划函数 TDD)。
- `scripts/darwin-unified-install-check.sh` — 统一安装验收梯度脚本(默认 dry-run)。
- `apps/macos/BxMenu/Sources/BxMenu/InstanceGate.swift` + `Tests/InstanceGateTests.swift` — 单实例决策 + 登录项 plist 文本(纯逻辑)。
- `apps/macos/BxMenu/Sources/BxMenu/InstallPresentation.swift` + `Tests/InstallPresentationTests.swift` — runtime 版本读取、Install 动作标题(纯逻辑)。

**修改:**
- `internal/install/guardian_darwin.go`(+ 其测试)— `GuardianPlistText/WriteGuardianUnit` 增加 executable 参数;新增 `GuardianExecutable()`。
- `internal/install/install.go:218-260` — `WriteUnit` darwin 分支改用 `parseGuardianExecStart`(接受 BinPath 与 runtime 两种 ExecStart)。
- `internal/guardian/daemon.go:277 附近` — Core 子进程可执行路径钉到 `os.Executable()` 解析结果(新 `coreExecutable` 纯函数 + 测试)。
- `internal/cli/guardian.go:232 ensureGuardianOwnership` — `WriteGuardianUnit(install.GuardianExecutable(), configPath)`。
- `internal/cli/cli.go` — 注册 `app-install`;`setupAction`(约 :3483)darwin 分支跳过 SelfInstall;`uninstallAction`(约 :5056)darwin 分派。
- `internal/cli/update.go:188 updateAction` — 统一布局下拒绝就地更新(`--check` 除外)。
- `scripts/package-macos-release.sh` / `scripts/verify-macos-release.sh` — bundle 内嵌 `bx-cli`/`bx-bridge`/`release.json`,新 tar 布局与 install.sh,verify 断言全套更新。
- `apps/macos/BxMenu/Sources/BxMenu/main.swift` — 单实例、登录项、`notInstalled` 状态 + Install bx 动作、Turn Off bx 动作。
- `apps/macos/BxMenu/run-swift-tests.sh`、`scripts/test-macos-menu.sh` — 登记两组新测试。
- `README.md`、`apps/macos/BxMenu/README.md` — 统一安装故事。

---

### Task 1: `internal/release` — release.json 元数据

**Files:**
- Create: `internal/release/release.go`
- Test: `internal/release/release_test.go`

**Interfaces:**
- Produces(后续任务用到的精确签名):
  - `const FileName = "release.json"`、`const SchemaVersion = 1`
  - `type Info struct { SchemaVersion int; Version string; Platform string; Assets map[string]string }`(JSON tag 均为 snake_case:`schema_version/version/platform/assets`;Assets 键为资产名 `bx-cli`/`bx-bridge`,值为 64 位 hex sha256)
  - `func Load(path string) (Info, error)`、`func Parse(data []byte) (Info, error)`、`func Write(path string, info Info) error`
  - `func (i Info) MatchesPlatform(goos, goarch string) error`(Platform 形如 `darwin/arm64`)
  - `func (i Info) VerifyAsset(name string, data []byte) error`

- [ ] **Step 1: 写失败测试**

```go
package release

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func validInfo(t *testing.T, payload []byte) Info {
	t.Helper()
	sum := sha256.Sum256(payload)
	return Info{
		SchemaVersion: SchemaVersion,
		Version:       "1.2.3",
		Platform:      "darwin/arm64",
		Assets:        map[string]string{"bx-cli": hex.EncodeToString(sum[:])},
	}
}

func TestWriteLoadRoundTrip(t *testing.T) {
	payload := []byte("fake-cli-binary")
	info := validInfo(t, payload)
	path := filepath.Join(t.TempDir(), FileName)
	if err := Write(path, info); err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != "1.2.3" || loaded.Platform != "darwin/arm64" ||
		loaded.Assets["bx-cli"] != info.Assets["bx-cli"] {
		t.Fatalf("roundtrip mismatch: %+v", loaded)
	}
	if err := loaded.VerifyAsset("bx-cli", payload); err != nil {
		t.Fatalf("VerifyAsset: %v", err)
	}
}

func TestVerifyAssetMismatch(t *testing.T) {
	info := validInfo(t, []byte("real"))
	if err := info.VerifyAsset("bx-cli", []byte("tampered")); err == nil {
		t.Fatal("want digest mismatch error")
	}
	if err := info.VerifyAsset("absent", []byte("x")); err == nil {
		t.Fatal("want unknown asset error")
	}
}

func TestParseRejects(t *testing.T) {
	cases := map[string]string{
		"unknown field": `{"schema_version":1,"version":"v","platform":"darwin/arm64","assets":{"a":"` + hex64() + `"},"extra":1}`,
		"bad schema":    `{"schema_version":2,"version":"v","platform":"darwin/arm64","assets":{"a":"` + hex64() + `"}}`,
		"empty version": `{"schema_version":1,"version":"","platform":"darwin/arm64","assets":{"a":"` + hex64() + `"}}`,
		"no assets":     `{"schema_version":1,"version":"v","platform":"darwin/arm64","assets":{}}`,
		"bad digest":    `{"schema_version":1,"version":"v","platform":"darwin/arm64","assets":{"a":"zz"}}`,
	}
	for name, raw := range cases {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("%s: want error", name)
		}
	}
}

func TestMatchesPlatform(t *testing.T) {
	info := validInfo(t, []byte("x"))
	if err := info.MatchesPlatform("darwin", "arm64"); err != nil {
		t.Fatalf("match: %v", err)
	}
	if err := info.MatchesPlatform("darwin", "amd64"); err == nil {
		t.Fatal("want platform mismatch error")
	}
}

func hex64() string {
	sum := sha256.Sum256([]byte("h"))
	return hex.EncodeToString(sum[:])
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "nope.json")); !os.IsNotExist(err) {
		t.Fatalf("want IsNotExist, got %v", err)
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/release/` 期望编译失败(包不存在)。

- [ ] **Step 3: 最小实现**

```go
// Package release 描述一个 bx macOS release 的自描述元数据(release.json)。
package release

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

const (
	FileName      = "release.json"
	SchemaVersion = 1
)

type Info struct {
	SchemaVersion int               `json:"schema_version"`
	Version       string            `json:"version"`
	Platform      string            `json:"platform"`
	Assets        map[string]string `json:"assets"`
}

func Load(path string) (Info, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Info{}, err
	}
	return Parse(data)
}

func Parse(data []byte) (Info, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var info Info
	if err := decoder.Decode(&info); err != nil {
		return Info{}, fmt.Errorf("release metadata invalid: %w", err)
	}
	if info.SchemaVersion != SchemaVersion {
		return Info{}, fmt.Errorf("release schema_version %d unsupported", info.SchemaVersion)
	}
	if info.Version == "" {
		return Info{}, errors.New("release version empty")
	}
	if info.Platform == "" {
		return Info{}, errors.New("release platform empty")
	}
	if len(info.Assets) == 0 {
		return Info{}, errors.New("release assets empty")
	}
	for name, digest := range info.Assets {
		if name == "" {
			return Info{}, errors.New("release asset name empty")
		}
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != sha256.Size {
			return Info{}, fmt.Errorf("release asset %q digest invalid", name)
		}
	}
	return info, nil
}

func Write(path string, info Info) error {
	if _, err := Parse(mustMarshal(info)); err != nil {
		return err
	}
	return os.WriteFile(path, mustMarshal(info), 0o644)
}

func mustMarshal(info Info) []byte {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

func (i Info) MatchesPlatform(goos, goarch string) error {
	want := goos + "/" + goarch
	if i.Platform != want {
		return fmt.Errorf("release platform %q does not match %q", i.Platform, want)
	}
	return nil
}

func (i Info) VerifyAsset(name string, data []byte) error {
	digest, ok := i.Assets[name]
	if !ok {
		return fmt.Errorf("release has no asset %q", name)
	}
	sum := sha256.Sum256(data)
	want, err := hex.DecodeString(digest)
	if err != nil {
		return fmt.Errorf("release asset %q digest invalid", name)
	}
	if subtle.ConstantTimeCompare(sum[:], want) != 1 {
		return fmt.Errorf("release asset %q digest mismatch", name)
	}
	return nil
}
```

- [ ] **Step 4: 跑绿** — `go test ./internal/release/ -v` 全 PASS;`go vet ./internal/release/`。

- [ ] **Step 5: Commit**

```bash
git add internal/release/
git commit -m "feat(release): macOS release.json 元数据与 digest 校验

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `internal/runtimedir` — root-owned 版本化 runtime 目录

**Files:**
- Create: `internal/runtimedir/runtimedir.go`
- Test: `internal/runtimedir/runtimedir_test.go`

**Interfaces:**
- Consumes: `release.Info` / `release.Write` / `release.Load`(Task 1)。
- Produces:
  - `const Root = "/Library/Application Support/bx/runtime"`
  - `func CurrentLink(root string) string` → `<root>/current`
  - `func ExecutablePath(root string) string` → `<root>/current/bx`
  - `var ErrNotInstalled = errors.New(...)`
  - `func Installed(root string) bool`
  - `func Current(root string) (release.Info, string, error)`(返回 info 与版本目录绝对路径)
  - `type Payload struct { CLI []byte; Info release.Info }`
  - `func InstallVersion(root string, payload Payload) (string, error)`(返回版本目录;staging+rename;幂等)
  - `func SwitchCurrent(root, version string) error`(临时符号链 rename 原子切换;相对 target)

- [ ] **Step 1: 写失败测试**

```go
package runtimedir

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/release"
)

func payloadFor(t *testing.T, version string, cli []byte) Payload {
	t.Helper()
	sum := sha256.Sum256(cli)
	return Payload{CLI: cli, Info: release.Info{
		SchemaVersion: release.SchemaVersion,
		Version:       version,
		Platform:      "darwin/arm64",
		Assets:        map[string]string{"bx-cli": hex.EncodeToString(sum[:])},
	}}
}

func TestInstallSwitchCurrent(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	dir, err := InstallVersion(root, payloadFor(t, "1.0.0", []byte("cli-v1")))
	if err != nil {
		t.Fatalf("InstallVersion: %v", err)
	}
	if dir != filepath.Join(root, "1.0.0") {
		t.Fatalf("version dir = %q", dir)
	}
	if Installed(root) {
		t.Fatal("Installed should be false before SwitchCurrent")
	}
	if err := SwitchCurrent(root, "1.0.0"); err != nil {
		t.Fatalf("SwitchCurrent: %v", err)
	}
	if !Installed(root) {
		t.Fatal("Installed should be true")
	}
	info, versionDir, err := Current(root)
	if err != nil {
		t.Fatalf("Current: %v", err)
	}
	if info.Version != "1.0.0" || versionDir != dir {
		t.Fatalf("Current = %q %q", info.Version, versionDir)
	}
	data, err := os.ReadFile(ExecutablePath(root))
	if err != nil || string(data) != "cli-v1" {
		t.Fatalf("executable content: %q err=%v", data, err)
	}
	fi, err := os.Stat(filepath.Join(dir, "bx"))
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("bx perm = %v err=%v", fi.Mode(), err)
	}
}

func TestSwitchCurrentReplacesAtomically(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	for _, v := range []string{"1.0.0", "1.1.0"} {
		if _, err := InstallVersion(root, payloadFor(t, v, []byte("cli-"+v))); err != nil {
			t.Fatalf("install %s: %v", v, err)
		}
	}
	if err := SwitchCurrent(root, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := SwitchCurrent(root, "1.1.0"); err != nil {
		t.Fatalf("re-switch: %v", err)
	}
	target, err := os.Readlink(CurrentLink(root))
	if err != nil || target != "1.1.0" {
		t.Fatalf("current -> %q err=%v", target, err)
	}
}

func TestInstallVersionIdempotentAndReplacing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	p := payloadFor(t, "1.0.0", []byte("same"))
	if _, err := InstallVersion(root, p); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallVersion(root, p); err != nil {
		t.Fatalf("idempotent reinstall: %v", err)
	}
	changed := payloadFor(t, "1.0.0", []byte("different-bytes"))
	if _, err := InstallVersion(root, changed); err != nil {
		t.Fatalf("replacing reinstall: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(root, "1.0.0", "bx"))
	if string(data) != "different-bytes" {
		t.Fatalf("content not replaced: %q", data)
	}
}

func TestInstallVersionRejects(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	bad := payloadFor(t, "1.0.0", []byte("cli"))
	bad.Info.Assets["bx-cli"] = hex.EncodeToString(make([]byte, sha256.Size)) // digest 不匹配
	if _, err := InstallVersion(root, bad); err == nil {
		t.Fatal("want digest mismatch error")
	}
	evil := payloadFor(t, "../evil", []byte("cli"))
	if _, err := InstallVersion(root, evil); err == nil {
		t.Fatal("want version path error")
	}
	if err := SwitchCurrent(root, "missing"); err == nil {
		t.Fatal("want missing version error")
	}
}

func TestCurrentRejectsAbsoluteOrEscapingLink(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", CurrentLink(root)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Current(root); err == nil {
		t.Fatal("want escaping link rejected")
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/runtimedir/`。

- [ ] **Step 3: 最小实现**

```go
// Package runtimedir 管理 root-owned 的版本化 bx runtime 目录
// (/Library/Application Support/bx/runtime/<version> + current 符号链)。
package runtimedir

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/getbx/bx/internal/release"
)

const Root = "/Library/Application Support/bx/runtime"

var ErrNotInstalled = errors.New("bx runtime not installed")

func CurrentLink(root string) string   { return filepath.Join(root, "current") }
func ExecutablePath(root string) string { return filepath.Join(root, "current", "bx") }

func Installed(root string) bool {
	_, _, err := Current(root)
	return err == nil
}

func Current(root string) (release.Info, string, error) {
	target, err := os.Readlink(CurrentLink(root))
	if err != nil {
		return release.Info{}, "", ErrNotInstalled
	}
	if err := validVersionName(target); err != nil {
		return release.Info{}, "", fmt.Errorf("runtime current link invalid: %w", err)
	}
	versionDir := filepath.Join(root, target)
	info, err := release.Load(filepath.Join(versionDir, release.FileName))
	if err != nil {
		return release.Info{}, "", fmt.Errorf("runtime metadata unreadable: %w", err)
	}
	if _, err := os.Stat(filepath.Join(versionDir, "bx")); err != nil {
		return release.Info{}, "", fmt.Errorf("runtime executable missing: %w", err)
	}
	return info, versionDir, nil
}

type Payload struct {
	CLI  []byte
	Info release.Info
}

func InstallVersion(root string, payload Payload) (string, error) {
	if err := validVersionName(payload.Info.Version); err != nil {
		return "", err
	}
	if err := payload.Info.VerifyAsset("bx-cli", payload.CLI); err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	versionDir := filepath.Join(root, payload.Info.Version)
	staging := filepath.Join(root, ".staging-"+payload.Info.Version)
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(staging, "bx"), payload.CLI, 0o755); err != nil {
		return "", err
	}
	if err := release.Write(filepath.Join(staging, release.FileName), payload.Info); err != nil {
		return "", err
	}
	discard := filepath.Join(root, ".discard-"+payload.Info.Version)
	if err := os.RemoveAll(discard); err != nil {
		return "", err
	}
	if _, err := os.Stat(versionDir); err == nil {
		if err := os.Rename(versionDir, discard); err != nil {
			return "", err
		}
	}
	if err := os.Rename(staging, versionDir); err != nil {
		return "", err
	}
	_ = os.RemoveAll(discard)
	return versionDir, nil
}

func SwitchCurrent(root, version string) error {
	if err := validVersionName(version); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(root, version, "bx")); err != nil {
		return fmt.Errorf("runtime version %q not installed: %w", version, err)
	}
	next := filepath.Join(root, ".current-next")
	if err := os.Remove(next); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Symlink(version, next); err != nil {
		return err
	}
	return os.Rename(next, CurrentLink(root))
}

func validVersionName(version string) error {
	if version == "" || version == "current" ||
		strings.ContainsAny(version, "/\\") || strings.Contains(version, "..") ||
		strings.HasPrefix(version, ".") {
		return fmt.Errorf("runtime version name %q invalid", version)
	}
	return nil
}
```

- [ ] **Step 4: 跑绿** — `go test ./internal/runtimedir/ -v && go vet ./internal/runtimedir/`。

- [ ] **Step 5: Commit**

```bash
git add internal/runtimedir/
git commit -m "feat(runtimedir): 版本化 root runtime 目录与 current 原子切换

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `internal/bridge` + `cmd/bx-bridge` — 稳定 CLI 入口

**Files:**
- Create: `internal/bridge/bridge.go`
- Create: `cmd/bx-bridge/main_darwin.go`、`cmd/bx-bridge/main_other.go`
- Test: `internal/bridge/bridge_test.go`

**Interfaces:**
- Consumes: `runtimedir.Root/CurrentLink`(Task 2)。
- Produces:
  - `const RepairHint = "bx runtime is missing or damaged. Open Bx.app to install or repair bx.\n(bx 运行时缺失或损坏:请打开 Bx.app 完成安装或修复。)"`
  - `type Sys struct { Stat func(string) (os.FileInfo, error); EvalSymlinks func(string) (string, error); OwnerUID func(os.FileInfo) (int, bool) }`
  - `func RealSys() Sys`
  - `func ResolveExecutable(root string, sys Sys) (string, error)` — 校验:current 解析后不逃逸 root、版本目录与 `bx` 属主 uid 0、group/other 不可写、`bx` 为常规可执行文件。

安全要点(即自动测试第 1、8 条「路径逃逸/错误 owner/mode」):bridge 是用户与 agent 的固定入口,必须在 exec 前证明目标是 root 安装的 runtime,不能被 `current -> /tmp/evil` 或用户可写目录劫持。

- [ ] **Step 1: 写失败测试**

```go
package bridge

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeInfo struct {
	mode os.FileMode
	dir  bool
}

func (f fakeInfo) Name() string       { return "x" }
func (f fakeInfo) Size() int64        { return 1 }
func (f fakeInfo) Mode() os.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return f.dir }
func (f fakeInfo) Sys() any           { return nil }

type fixture struct {
	resolved string
	uid      map[string]int
	mode     map[string]os.FileMode
	dirs     map[string]bool
}

func (f fixture) sys() Sys {
	return Sys{
		Stat: func(path string) (os.FileInfo, error) {
			mode, ok := f.mode[path]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return fakeInfo{mode: mode, dir: f.dirs[path]}, nil
		},
		EvalSymlinks: func(path string) (string, error) {
			if f.resolved == "" {
				return "", errors.New("dangling")
			}
			return f.resolved, nil
		},
		OwnerUID: func(info os.FileInfo) (int, bool) {
			// 测试中按 mode 独一无二地反查路径不可行;用统一 uid 表:
			// fixture 用同一个 uid 值覆盖所有路径即可满足用例。
			for _, uid := range f.uid {
				return uid, true
			}
			return 0, false
		},
	}
}

const root = "/Library/Application Support/bx/runtime"

func healthy() fixture {
	versionDir := filepath.Join(root, "1.0.0")
	return fixture{
		resolved: versionDir,
		uid:      map[string]int{versionDir: 0},
		mode: map[string]os.FileMode{
			versionDir:                        0o755 | os.ModeDir,
			filepath.Join(versionDir, "bx"):   0o755,
		},
		dirs: map[string]bool{versionDir: true},
	}
}

func TestResolveHealthy(t *testing.T) {
	exe, err := ResolveExecutable(root, healthy().sys())
	if err != nil {
		t.Fatalf("ResolveExecutable: %v", err)
	}
	want := filepath.Join(root, "1.0.0", "bx")
	if exe != want {
		t.Fatalf("exe = %q want %q", exe, want)
	}
}

func TestResolveRejectsEscape(t *testing.T) {
	f := healthy()
	f.resolved = "/private/tmp/evil"
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want escape rejected")
	}
}

func TestResolveRejectsNonRootOwner(t *testing.T) {
	f := healthy()
	f.uid = map[string]int{filepath.Join(root, "1.0.0"): 501}
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want non-root owner rejected")
	}
}

func TestResolveRejectsWritableMode(t *testing.T) {
	f := healthy()
	f.mode[filepath.Join(root, "1.0.0", "bx")] = 0o775 // group 可写
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want group-writable rejected")
	}
}

func TestResolveRejectsMissingRuntime(t *testing.T) {
	f := fixture{}
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want missing runtime error")
	}
}

func TestResolveRejectsNonExecutable(t *testing.T) {
	f := healthy()
	f.mode[filepath.Join(root, "1.0.0", "bx")] = 0o644
	if _, err := ResolveExecutable(root, f.sys()); err == nil {
		t.Fatal("want non-executable rejected")
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/bridge/`。

- [ ] **Step 3: 最小实现**

```go
// Package bridge 实现 /usr/local/bin/bx 稳定入口对 root runtime 的解析与安全校验。
package bridge

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const RepairHint = "bx runtime is missing or damaged. Open Bx.app to install or repair bx.\n(bx 运行时缺失或损坏:请打开 Bx.app 完成安装或修复。)"

type Sys struct {
	Stat         func(string) (os.FileInfo, error)
	EvalSymlinks func(string) (string, error)
	OwnerUID     func(os.FileInfo) (int, bool)
}

func RealSys() Sys {
	return Sys{
		Stat:         os.Stat,
		EvalSymlinks: filepath.EvalSymlinks,
		OwnerUID: func(info os.FileInfo) (int, bool) {
			stat, ok := info.Sys().(*syscall.Stat_t)
			if !ok {
				return 0, false
			}
			return int(stat.Uid), true
		},
	}
}

func ResolveExecutable(root string, sys Sys) (string, error) {
	resolvedRoot, err := sys.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("runtime root missing: %w", err)
	}
	versionDir, err := sys.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil {
		return "", fmt.Errorf("runtime current missing: %w", err)
	}
	if !strings.HasPrefix(versionDir+string(filepath.Separator), resolvedRoot+string(filepath.Separator)) {
		return "", fmt.Errorf("runtime current escapes %s", root)
	}
	if err := requireRootOwned(sys, versionDir, true, false); err != nil {
		return "", err
	}
	executable := filepath.Join(versionDir, "bx")
	if err := requireRootOwned(sys, executable, false, true); err != nil {
		return "", err
	}
	return executable, nil
}

func requireRootOwned(sys Sys, path string, wantDir, wantExecutable bool) error {
	info, err := sys.Stat(path)
	if err != nil {
		return fmt.Errorf("runtime path %s missing: %w", path, err)
	}
	if info.IsDir() != wantDir {
		return fmt.Errorf("runtime path %s wrong type", path)
	}
	if !wantDir && !info.Mode().IsRegular() {
		return fmt.Errorf("runtime path %s not a regular file", path)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("runtime path %s is group/other writable", path)
	}
	if wantExecutable && info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("runtime path %s not executable", path)
	}
	uid, ok := sys.OwnerUID(info)
	if !ok || uid != 0 {
		return fmt.Errorf("runtime path %s not owned by root", path)
	}
	return nil
}
```

`cmd/bx-bridge/main_darwin.go`:

```go
//go:build darwin

// bx-bridge 是 /usr/local/bin/bx 的稳定入口:定位 root-owned runtime 并 exec 同版本 CLI。
package main

import (
	"fmt"
	"os"
	"syscall"

	"github.com/getbx/bx/internal/bridge"
	"github.com/getbx/bx/internal/runtimedir"
)

func main() {
	executable, err := bridge.ResolveExecutable(runtimedir.Root, bridge.RealSys())
	if err != nil {
		fmt.Fprintln(os.Stderr, bridge.RepairHint)
		fmt.Fprintf(os.Stderr, "bx-bridge: %v\n", err)
		os.Exit(1)
	}
	argv := append([]string{executable}, os.Args[1:]...)
	if err := syscall.Exec(executable, argv, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "bx-bridge: exec %s: %v\n", executable, err)
		os.Exit(1)
	}
}
```

`cmd/bx-bridge/main_other.go`:

```go
//go:build !darwin

package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stderr, "bx-bridge is only used on macOS")
	os.Exit(1)
}
```

注意 `fakeInfo.Sys()` 返回 nil,所以测试注入 `OwnerUID`;`RealSys` 的 `syscall.Stat_t` 断言在 linux/darwin 都编译(windows 交叉编译:`internal/bridge` 会被 `go build ./...` 编译——`syscall.Stat_t` 在 windows 不存在,故把 `RealSys` 拆到 `bridge_unix.go`(`//go:build !windows`),`bridge.go` 保留纯逻辑;windows 侧无人调用 `RealSys`,不需要 stub)。

- [ ] **Step 4: 跑绿 + 三平台构建**

```bash
go test ./internal/bridge/ -v && go vet ./internal/bridge/ ./cmd/bx-bridge/
GOOS=linux GOARCH=amd64 go build -o /dev/null ./... && GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
```

- [ ] **Step 5: Commit**

```bash
git add internal/bridge/ cmd/bx-bridge/
git commit -m "feat(bridge): 稳定 CLI 入口 bx-bridge(校验 root runtime 后 exec)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Guardian plist 指向 runtime(executable 参数化)

**Files:**
- Modify: `internal/install/guardian_darwin.go`(`GuardianPlistText` :23、`WriteGuardianUnit` :72、`writeGuardianUnitAt` :76 及其既有测试)
- Modify: `internal/install/install.go`(`WriteUnit` :218、`guardianConfigPathFromExecStart` :233 → 改名 `parseGuardianExecStart`)
- Modify: `internal/cli/guardian.go`(`ensureGuardianOwnership` :232 的 `WriteGuardianUnit` 调用)
- Modify: `internal/cli/cli.go`(`buildExecStartForGOOS` :5043 darwin 分支)
- Test: `internal/install/guardian_darwin_test.go`(既有文件补用例)

**Interfaces:**
- Consumes: `runtimedir.Installed/ExecutablePath/Root`(Task 2)。
- Produces:
  - `func GuardianPlistText(executable, configPath string) string`(原单参数版删除,所有调用点同步)
  - `func WriteGuardianUnit(executable, configPath string) error`
  - `func GuardianExecutable() string` — `runtimedir.Installed(runtimedir.Root)` 时返回 `runtimedir.ExecutablePath(runtimedir.Root)`,否则 `BinPath`。
  - `func parseGuardianExecStart(execStart string) (executable, configPath string, err error)` — 接受两种前缀:`BinPath` 或 `runtimedir.ExecutablePath(runtimedir.Root)`(runtime 路径含空格,**不能用 `strings.Fields` 整串切**——先按已知前缀剥离,再 Fields 余下参数)。

- [ ] **Step 1: 写失败测试**(追加到 `guardian_darwin_test.go`;若既有测试用单参数 `GuardianPlistText`,同步改为双参数)

```go
func TestGuardianPlistTextRuntimeExecutable(t *testing.T) {
	text := GuardianPlistText("/Library/Application Support/bx/runtime/current/bx", "/etc/bx/config.yaml")
	for _, want := range []string{
		"<string>/Library/Application Support/bx/runtime/current/bx</string>",
		"<string>guardian</string>",
		"<string>/etc/bx/config.yaml</string>",
		"<string>com.getbx.bx.guard</string>",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("plist missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "<string>/usr/local/bin/bx</string>") {
		t.Fatal("runtime plist must not reference the bridge path")
	}
}

func TestParseGuardianExecStart(t *testing.T) {
	runtimeExec := runtimedir.ExecutablePath(runtimedir.Root)
	cases := []struct{ execStart, wantExec, wantConfig string }{
		{BinPath + " guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53", BinPath, "/etc/bx/config.yaml"},
		{runtimeExec + " guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53", runtimeExec, "/etc/bx/config.yaml"},
	}
	for _, tc := range cases {
		gotExec, gotConfig, err := parseGuardianExecStart(tc.execStart)
		if err != nil || gotExec != tc.wantExec || gotConfig != tc.wantConfig {
			t.Fatalf("parse(%q) = %q %q %v", tc.execStart, gotExec, gotConfig, err)
		}
	}
	if _, _, err := parseGuardianExecStart("/opt/evil/bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53"); err == nil {
		t.Fatal("want unknown executable rejected")
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/install/ -run 'GuardianPlist|GuardianExecStart'`。

- [ ] **Step 3: 实现**

`guardian_darwin.go`:模板 `ProgramArguments` 第一个 `<string>` 由 `executable` 参数填充(其余 XML 原样);签名改为:

```go
func GuardianPlistText(executable, configPath string) string
func WriteGuardianUnit(executable, configPath string) error {
	return writeGuardianUnitAt(guardianLaunchdPlistPath, executable, configPath, os.Chown)
}
func writeGuardianUnitAt(path, executable, configPath string, chown func(string, int, int) error) error
// writeGuardianUnitAt 增加校验:executable 必须为绝对路径,否则报错(config 校验保持不变)。

func GuardianExecutable() string {
	if runtimedir.Installed(runtimedir.Root) {
		return runtimedir.ExecutablePath(runtimedir.Root)
	}
	return BinPath
}
```

`install.go` `WriteUnit` darwin 分支:

```go
case "darwin":
	executable, configPath, err := parseGuardianExecStart(execStart)
	if err != nil {
		return err
	}
	return WriteGuardianUnit(executable, configPath)
```

`parseGuardianExecStart`(替换 `guardianConfigPathFromExecStart`,保留原有 6-token 严格性):

```go
func parseGuardianExecStart(execStart string) (string, string, error) {
	var executable string
	for _, candidate := range []string{runtimedir.ExecutablePath(runtimedir.Root), BinPath} {
		if strings.HasPrefix(execStart, candidate+" ") {
			executable = candidate
			break
		}
	}
	if executable == "" {
		return "", "", fmt.Errorf("guardian ExecStart %q does not start with a trusted bx executable", execStart)
	}
	rest := strings.Fields(strings.TrimPrefix(execStart, executable+" "))
	if len(rest) != 5 || rest[0] != "guardian" || rest[1] != "--config" ||
		rest[3] != "--listen-dns" || rest[4] != "127.0.0.1:53" || !filepath.IsAbs(rest[2]) {
		return "", "", fmt.Errorf("guardian ExecStart %q has unexpected arguments", execStart)
	}
	return executable, rest[2], nil
}
```

`internal/cli/guardian.go` `ensureGuardianOwnership`:`install.WriteGuardianUnit(configPath)` → `install.WriteGuardianUnit(install.GuardianExecutable(), configPath)`。
`internal/cli/cli.go` `buildExecStartForGOOS` darwin 分支:`fmt.Sprintf("%s guardian --config %s --listen-dns %s", install.GuardianExecutable(), configPath, darwinDNSListen)`(忽略传入的 `bin` 参数,加注释说明 darwin 由 GuardianExecutable 决定)。全仓 `grep -rn 'GuardianPlistText\|WriteGuardianUnit'` 同步所有调用点与测试。

- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/install/ ./internal/cli/ && go vet ./...`。

- [ ] **Step 5: Commit**

```bash
git add internal/install/ internal/cli/
git commit -m "feat(macos): Guardian plist 可执行路径参数化并优先指向统一 runtime

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Guardian 以自身可执行路径拉起 Core(版本钉死)

**Files:**
- Modify: `internal/guardian/daemon.go`(`RunDaemon` :277 附近构造 `NewExecCoreRunner(install.BinPath, …)` 处)
- Test: `internal/guardian/daemon_test.go`(追加)

**Interfaces:**
- Produces: `func coreExecutable(selfExecutable func() (string, error), evalSymlinks func(string) (string, error)) string`(包内)。

理由:统一布局下 `/usr/local/bin/bx` 是 bridge;Guardian 若仍用 `install.BinPath` 拉 Core,会经 bridge 再 exec,破坏进程身份验证(`processRecord.Executable`)。Guardian 与 Core 同一二进制,用 `os.Executable()` 解析符号链后的路径拉起 Core,天然把 Core 钉在 Guardian 自己的版本目录;legacy 布局(直接 `/usr/local/bin/bx guardian`)下解析结果就是 BinPath,行为不变。

- [ ] **Step 1: 写失败测试**

```go
func TestCoreExecutablePinsResolvedSelf(t *testing.T) {
	got := coreExecutable(
		func() (string, error) { return "/Library/Application Support/bx/runtime/current/bx", nil },
		func(path string) (string, error) { return "/Library/Application Support/bx/runtime/1.2.3/bx", nil },
	)
	if got != "/Library/Application Support/bx/runtime/1.2.3/bx" {
		t.Fatalf("coreExecutable = %q", got)
	}
}

func TestCoreExecutableFallsBackToBinPath(t *testing.T) {
	got := coreExecutable(
		func() (string, error) { return "", errors.New("no self") },
		func(path string) (string, error) { return path, nil },
	)
	if got != install.BinPath {
		t.Fatalf("fallback = %q", got)
	}
}

func TestCoreExecutableKeepsPathWhenResolveFails(t *testing.T) {
	got := coreExecutable(
		func() (string, error) { return "/usr/local/bin/bx", nil },
		func(path string) (string, error) { return "", errors.New("eval failed") },
	)
	if got != "/usr/local/bin/bx" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/guardian/ -run CoreExecutable`。

- [ ] **Step 3: 实现**

```go
func coreExecutable(selfExecutable func() (string, error), evalSymlinks func(string) (string, error)) string {
	path, err := selfExecutable()
	if err != nil || path == "" {
		return install.BinPath
	}
	if resolved, err := evalSymlinks(path); err == nil && resolved != "" {
		return resolved
	}
	return path
}
```

`RunDaemon` 中 `NewExecCoreRunner(install.BinPath, configPath, options.DNSListen)` → `NewExecCoreRunner(coreExecutable(os.Executable, filepath.EvalSymlinks), configPath, options.DNSListen)`(实际参数名以现场签名为准,只换第一个参数)。

- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/guardian/ && go vet ./internal/guardian/`。

- [ ] **Step 5: Commit**

```bash
git add internal/guardian/
git commit -m "fix(guardian): Core 子进程钉在 Guardian 自身解析路径,统一布局不经 bridge

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: `install.UnifiedInstall` — 特权统一安装(文件层)

**Files:**
- Create: `internal/install/unified_darwin.go`(`//go:build darwin`)
- Test: `internal/install/unified_darwin_test.go`

**Interfaces:**
- Consumes: `release.Load/VerifyAsset`、`runtimedir.InstallVersion/SwitchCurrent/ExecutablePath`、`ReplaceBinary`、`WriteGuardianUnit(executable, configPath)`。
- Produces:

```go
type UnifiedInstallOptions struct {
	BundlePath     string // 源 Bx.app(必填,绝对路径,basename == "Bx.app")
	AppDestination string // 默认 /Applications/Bx.app
	ConfigPath     string // 默认 /etc/bx/config.yaml
	RuntimeRoot    string // 默认 runtimedir.Root(测试注入)
	BridgePath     string // 默认 BinPath(测试注入)
	ConsoleHome    string // 控制台用户家目录(登录项/legacy 清理;空则跳过这两步)
	ConsoleUID     int
	ConsoleGID     int
	Chown          func(string, int, int) error            // 默认 os.Chown(测试注入)
	WriteGuardian  func(executable, configPath string) error // 默认 WriteGuardianUnit(测试注入)
}
type UnifiedInstallResult struct {
	Version           string
	AppPath           string
	RuntimeExecutable string
	BridgeReplaced    bool
	RemovedLegacyApp  bool
}
func UnifiedInstall(options UnifiedInstallOptions) (UnifiedInstallResult, error)
func MenuAgentPlistText(executable, logDir string) string
```

安装顺序(**先全量验证,后落盘;任何验证失败时文件系统零改动**):
1. 读 `<Bundle>/Contents/Resources/release.json`(`release.Load`)→ `MatchesPlatform(runtime.GOOS, runtime.GOARCH)`;读 `bx-cli`、`bx-bridge` 字节并 `VerifyAsset`;确认 `<Bundle>/Contents/MacOS/BxMenu` 与 `Contents/Info.plist` 存在,且 Info.plist 的 `CFBundleIdentifier` == `com.getbx.bx.menu`。
2. App 落位:`BundlePath` 与 `AppDestination` 相同(EvalSymlinks 后比较)则跳过;否则 copyTree(拒绝符号链接,保留权限位)到 `<destDir>/.Bx.app.install-stage` → 整树 `Chown(0,0)` → 旧 App rename 到 `.Bx.app.install-old` → stage rename 落位 → RemoveAll 旧树。
3. `runtimedir.InstallVersion` + `SwitchCurrent`。
4. bridge:现有 `BridgePath` 内容 sha256 != bx-bridge sha256 时 `ReplaceBinary(BridgePath, bridgeBytes)` + `Chown(BridgePath, 0, 0)`。
5. `WriteGuardian(runtimedir.ExecutablePath(RuntimeRoot), ConfigPath)`(**不 enable**)。
6. `ConsoleHome != ""` 时:写 `<home>/Library/LaunchAgents/com.getbx.bx.menu.plist`(`MenuAgentPlistText(<AppDestination>/Contents/MacOS/BxMenu, <home>/Library/Logs/bx)`,0644,`Chown(ConsoleUID, ConsoleGID)`,LaunchAgents 目录若新建同样 chown);删除 legacy `com.ggshr9.bx.menu.plist`。
7. legacy App:`<home>/Applications/Bx.app` 存在、不是 `AppDestination`、且其 Info.plist `CFBundleIdentifier` == `com.getbx.bx.menu` 时 `os.RemoveAll`。

`MenuAgentPlistText` 输出与 `scripts/install-macos-menu.sh:119-138` 现模板逐键一致(Label `com.getbx.bx.menu`、单元素 ProgramArguments、RunAtLoad true、StandardOut/ErrPath 指向 `<logDir>/menu.log|menu.err.log`,无 KeepAlive)。

- [ ] **Step 1: 写失败测试**(核心用例;fake bundle 用 `t.TempDir()` 搭)

```go
func writeFakeBundle(t *testing.T, dir string, version string) (bundle string, cli, bridgeBin []byte) {
	t.Helper()
	cli = []byte("cli-" + version)
	bridgeBin = []byte("bridge-" + version)
	bundle = filepath.Join(dir, "Bx.app")
	resources := filepath.Join(bundle, "Contents", "Resources")
	macos := filepath.Join(bundle, "Contents", "MacOS")
	for _, d := range []string{resources, macos} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	must := func(err error) { t.Helper(); if err != nil { t.Fatal(err) } }
	must(os.WriteFile(filepath.Join(macos, "BxMenu"), []byte("menu"), 0o755))
	must(os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"),
		[]byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.getbx.bx.menu</string></dict></plist>`), 0o644))
	must(os.WriteFile(filepath.Join(resources, "bx-cli"), cli, 0o755))
	must(os.WriteFile(filepath.Join(resources, "bx-bridge"), bridgeBin, 0o755))
	cliSum, bridgeSum := sha256.Sum256(cli), sha256.Sum256(bridgeBin)
	must(release.Write(filepath.Join(resources, release.FileName), release.Info{
		SchemaVersion: release.SchemaVersion, Version: version,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Assets: map[string]string{
			"bx-cli":    hex.EncodeToString(cliSum[:]),
			"bx-bridge": hex.EncodeToString(bridgeSum[:]),
		},
	}))
	return bundle, cli, bridgeBin
}

func testOptions(t *testing.T, bundle string) (UnifiedInstallOptions, *[]string) {
	t.Helper()
	base := t.TempDir()
	var guardianCalls []string
	opts := UnifiedInstallOptions{
		BundlePath:     bundle,
		AppDestination: filepath.Join(base, "Applications", "Bx.app"),
		ConfigPath:     "/etc/bx/config.yaml",
		RuntimeRoot:    filepath.Join(base, "runtime"),
		BridgePath:     filepath.Join(base, "bin", "bx"),
		ConsoleHome:    filepath.Join(base, "home"),
		ConsoleUID:     501, ConsoleGID: 20,
		Chown: func(string, int, int) error { return nil },
		WriteGuardian: func(executable, configPath string) error {
			guardianCalls = append(guardianCalls, executable+" "+configPath)
			return nil
		},
	}
	if err := os.MkdirAll(filepath.Dir(opts.BridgePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.AppDestination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(opts.ConsoleHome, 0o755); err != nil {
		t.Fatal(err)
	}
	return opts, &guardianCalls
}

func TestUnifiedInstallFresh(t *testing.T) {
	bundle, cli, bridgeBin := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, guardianCalls := testOptions(t, bundle)
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatalf("UnifiedInstall: %v", err)
	}
	if result.Version != "1.0.0" || !result.BridgeReplaced {
		t.Fatalf("result = %+v", result)
	}
	gotCLI, _ := os.ReadFile(filepath.Join(opts.RuntimeRoot, "current", "bx"))
	if string(gotCLI) != string(cli) {
		t.Fatal("runtime cli mismatch")
	}
	gotBridge, _ := os.ReadFile(opts.BridgePath)
	if string(gotBridge) != string(bridgeBin) {
		t.Fatal("bridge mismatch")
	}
	if _, err := os.Stat(filepath.Join(opts.AppDestination, "Contents", "MacOS", "BxMenu")); err != nil {
		t.Fatalf("app not installed: %v", err)
	}
	agent := filepath.Join(opts.ConsoleHome, "Library", "LaunchAgents", "com.getbx.bx.menu.plist")
	agentText, err := os.ReadFile(agent)
	if err != nil || !strings.Contains(string(agentText), opts.AppDestination+"/Contents/MacOS/BxMenu") {
		t.Fatalf("agent plist: %v %q", err, agentText)
	}
	if len(*guardianCalls) != 1 || !strings.Contains((*guardianCalls)[0], filepath.Join(opts.RuntimeRoot, "current", "bx")) {
		t.Fatalf("guardian calls = %v", *guardianCalls)
	}
}

func TestUnifiedInstallIdempotent(t *testing.T) {
	bundle, _, _ := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, _ := testOptions(t, bundle)
	if _, err := UnifiedInstall(opts); err != nil {
		t.Fatal(err)
	}
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if result.BridgeReplaced {
		t.Fatal("unchanged bridge must not be replaced")
	}
}

func TestUnifiedInstallDigestMismatchTouchesNothing(t *testing.T) {
	dir := t.TempDir()
	bundle, _, _ := writeFakeBundle(t, dir, "1.0.0")
	// 篡改 bx-cli,digest 不再匹配
	if err := os.WriteFile(filepath.Join(bundle, "Contents", "Resources", "bx-cli"), []byte("tampered"), 0o755); err != nil {
		t.Fatal(err)
	}
	opts, guardianCalls := testOptions(t, bundle)
	if _, err := UnifiedInstall(opts); err == nil {
		t.Fatal("want digest error")
	}
	if _, err := os.Stat(opts.RuntimeRoot); !os.IsNotExist(err) {
		t.Fatal("runtime must be untouched")
	}
	if _, err := os.Stat(opts.BridgePath); !os.IsNotExist(err) {
		t.Fatal("bridge must be untouched")
	}
	if _, err := os.Stat(opts.AppDestination); !os.IsNotExist(err) {
		t.Fatal("app must be untouched")
	}
	if len(*guardianCalls) != 0 {
		t.Fatal("guardian plist must be untouched")
	}
}

func TestUnifiedInstallMigratesLegacyUserApp(t *testing.T) {
	bundle, _, _ := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, _ := testOptions(t, bundle)
	legacy := filepath.Join(opts.ConsoleHome, "Applications", "Bx.app", "Contents")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "Info.plist"),
		[]byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.getbx.bx.menu</string></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyAgent := filepath.Join(opts.ConsoleHome, "Library", "LaunchAgents", "com.ggshr9.bx.menu.plist")
	if err := os.MkdirAll(filepath.Dir(legacyAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAgent, []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemovedLegacyApp {
		t.Fatal("want legacy app removed")
	}
	if _, err := os.Stat(filepath.Join(opts.ConsoleHome, "Applications", "Bx.app")); !os.IsNotExist(err) {
		t.Fatal("legacy app still present")
	}
	if _, err := os.Stat(legacyAgent); !os.IsNotExist(err) {
		t.Fatal("legacy agent plist still present")
	}
}

func TestUnifiedInstallKeepsForeignUserApp(t *testing.T) {
	bundle, _, _ := writeFakeBundle(t, t.TempDir(), "1.0.0")
	opts, _ := testOptions(t, bundle)
	foreign := filepath.Join(opts.ConsoleHome, "Applications", "Bx.app", "Contents")
	if err := os.MkdirAll(foreign, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(foreign, "Info.plist"),
		[]byte(`<plist><dict><key>CFBundleIdentifier</key><string>com.example.other</string></dict></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := UnifiedInstall(opts)
	if err != nil {
		t.Fatal(err)
	}
	if result.RemovedLegacyApp {
		t.Fatal("foreign bundle id must not be removed")
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/install/ -run UnifiedInstall`。

- [ ] **Step 3: 实现** `unified_darwin.go`(骨架;helper 全部包内私有)

```go
//go:build darwin

package install

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/getbx/bx/internal/release"
	"github.com/getbx/bx/internal/runtimedir"
)

var bundleIdentifierPattern = regexp.MustCompile(`(?s)<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)

const menuBundleIdentifier = "com.getbx.bx.menu"

func UnifiedInstall(options UnifiedInstallOptions) (UnifiedInstallResult, error) {
	options = options.withDefaults() // AppDestination /Applications/Bx.app、ConfigPath、RuntimeRoot、BridgePath、Chown=os.Chown、WriteGuardian=WriteGuardianUnit
	// 1. 全量验证(只读)
	verified, err := loadVerifiedBundle(options.BundlePath) // {Info release.Info; CLI, Bridge []byte}
	if err != nil {
		return UnifiedInstallResult{}, err
	}
	result := UnifiedInstallResult{Version: verified.Info.Version}
	// 2. App 落位
	appPath, err := installAppBundle(options)
	if err != nil {
		return UnifiedInstallResult{}, err
	}
	result.AppPath = appPath
	// 3. runtime
	if _, err := runtimedir.InstallVersion(options.RuntimeRoot, runtimedir.Payload{CLI: verified.CLI, Info: verified.Info}); err != nil {
		return UnifiedInstallResult{}, err
	}
	if err := runtimedir.SwitchCurrent(options.RuntimeRoot, verified.Info.Version); err != nil {
		return UnifiedInstallResult{}, err
	}
	result.RuntimeExecutable = runtimedir.ExecutablePath(options.RuntimeRoot)
	// 4. bridge(仅内容变化时替换)
	replaced, err := installBridge(options, verified.Bridge)
	if err != nil {
		return UnifiedInstallResult{}, err
	}
	result.BridgeReplaced = replaced
	// 5. Guardian plist(不 enable)
	if err := options.WriteGuardian(result.RuntimeExecutable, options.ConfigPath); err != nil {
		return UnifiedInstallResult{}, err
	}
	// 6+7. 登录项与 legacy 迁移
	if options.ConsoleHome != "" {
		if err := writeMenuAgent(options, appPath); err != nil {
			return UnifiedInstallResult{}, err
		}
		removed, err := removeLegacyUserApp(options, appPath)
		if err != nil {
			return UnifiedInstallResult{}, err
		}
		result.RemovedLegacyApp = removed
	}
	return result, nil
}
```

实现细节要求:
- `loadVerifiedBundle`:release.Load → `MatchesPlatform(runtime.GOOS, runtime.GOARCH)` → 读 `bx-cli`/`bx-bridge` → `VerifyAsset` → 校验 `Contents/MacOS/BxMenu`、`Contents/Info.plist` 存在且 bundle id 匹配 `menuBundleIdentifier`(`bundleIdentifierPattern` 提取)。
- `installAppBundle`:源与目的 `filepath.EvalSymlinks` 相等(或目的不存在且源==目的字面量)则原样返回目的;否则 `copyBundleTree`(`filepath.WalkDir`;遇 symlink 返回错误;文件按源 `Perm()` 写出)到 `<destDir>/.Bx.app.install-stage`(先 RemoveAll),整树 `chownTreeWith(options.Chown, 0, 0)`,旧目的 rename 到 `.Bx.app.install-old`,stage rename 落位,`os.RemoveAll(.Bx.app.install-old)`。
- `installBridge`:现文件不存在或 sha256 不同 → `ReplaceBinary` + `Chown(path,0,0)`;相同返回 false。
- `writeMenuAgent`:`MkdirAll(<home>/Library/LaunchAgents, 0o755)` 后对新建目录 `Chown(ConsoleUID, ConsoleGID)`,写 plist 0644 + chown;随后 `os.Remove(<home>/Library/LaunchAgents/com.ggshr9.bx.menu.plist)`(IsNotExist 忽略)。
- `removeLegacyUserApp`:目标 `<home>/Applications/Bx.app`;与 appPath EvalSymlinks 相同则跳过;Info.plist bundle id 必须匹配才 RemoveAll。
- `MenuAgentPlistText(executable, logDir string) string`:XML 与现 `install-macos-menu.sh` 模板一致。

- [ ] **Step 4: 跑绿** — `go test ./internal/install/ -run UnifiedInstall -v && go vet ./internal/install/ && gofumpt -l internal/install/`。

- [ ] **Step 5: Commit**

```bash
git add internal/install/unified_darwin.go internal/install/unified_darwin_test.go
git commit -m "feat(macos): UnifiedInstall 一次事务安装 App/runtime/bridge/Guardian/登录项

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: `bx app-install` hidden 命令

**Files:**
- Create: `internal/cli/appinstall_darwin.go`(`//go:build darwin`)、`internal/cli/appinstall_other.go`(`//go:build !darwin`)
- Modify: `internal/cli/cli.go`(`New()` 命令表注册)
- Test: `internal/cli/appinstall_test.go`(无 build tag,纯逻辑)

**Interfaces:**
- Consumes: `install.UnifiedInstall`(Task 6)、`consoleUserUID()`(menu_darwin.go 既有)、`ensureMacOSMenuRunning(uid)`(既有)。
- Produces:
  - `func appInstallCommand() *urfavecli.Command` — `Name: "app-install"`,`Hidden: true`,Flags:`--app-source`(源 Bx.app 路径,缺省从自身可执行路径推导)、`--config`(默认 `defaultConfigPath`)。
  - `func bundleRootFromExecutable(executable string) (string, error)`(纯函数,darwin/other 共用文件)。

- [ ] **Step 1: 写失败测试**(`appinstall_test.go`)

```go
func TestBundleRootFromExecutable(t *testing.T) {
	got, err := bundleRootFromExecutable("/Applications/Bx.app/Contents/Resources/bx-cli")
	if err != nil || got != "/Applications/Bx.app" {
		t.Fatalf("got %q err=%v", got, err)
	}
	if _, err := bundleRootFromExecutable("/usr/local/bin/bx"); err == nil {
		t.Fatal("want error for non-bundle executable")
	}
	got, err = bundleRootFromExecutable("/Users/a/Downloads/Bx.app/Contents/Resources/bx-cli")
	if err != nil || got != "/Users/a/Downloads/Bx.app" {
		t.Fatalf("got %q err=%v", got, err)
	}
}
```

- [ ] **Step 2: 跑红**。

- [ ] **Step 3: 实现**

`bundleRootFromExecutable`(放 `appinstall.go`,无 tag):

```go
func bundleRootFromExecutable(executable string) (string, error) {
	const marker = "/Bx.app/Contents/Resources/"
	idx := strings.Index(executable, marker)
	if idx < 0 {
		return "", fmt.Errorf("executable %q is not inside a Bx.app bundle; pass --app-source", executable)
	}
	return executable[:idx+len("/Bx.app")], nil
}
```

`appinstall_darwin.go` action 逻辑:

```go
func appInstallAction(c *urfavecli.Context) error {
	if os.Geteuid() != 0 {
		return errors.New("app-install 需要 root:请通过 Bx.app 的 Install bx 或 sudo 执行")
	}
	source := c.String("app-source")
	if source == "" {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		if source, err = bundleRootFromExecutable(executable); err != nil {
			return err
		}
	}
	uid, err := consoleUserUID()
	if err != nil {
		return fmt.Errorf("定位控制台用户失败: %w", err)
	}
	account, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return fmt.Errorf("查找控制台用户失败: %w", err)
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	result, err := install.UnifiedInstall(install.UnifiedInstallOptions{
		BundlePath:  source,
		ConfigPath:  c.String("config"),
		ConsoleHome: account.HomeDir,
		ConsoleUID:  uid,
		ConsoleGID:  gid,
	})
	if err != nil {
		return err
	}
	fmt.Printf("✓ 已安装 Bx.app %s → %s\n", result.Version, result.AppPath)
	fmt.Printf("✓ runtime → %s\n", result.RuntimeExecutable)
	fmt.Println("✓ 命令行入口 /usr/local/bin/bx 与 Guardian 服务已就绪(未启动保护)")
	if err := ensureMacOSMenuRunning(uid); err != nil {
		fmt.Printf("! 菜单栏未能自动启动(可手动打开 Bx.app): %v\n", err)
	}
	fmt.Println("下一步:菜单栏 Set Up bx,或 sudo bx setup <client-link> && sudo bx up")
	return nil
}
```

`appinstall_other.go`:`appInstallAction` 返回 `errors.New("app-install 仅支持 macOS")`。`cli.go` 注册(与其他 hidden 命令并列):

```go
{
	Name:   "app-install",
	Usage:  "从 Bx.app 安装统一 runtime/CLI bridge/Guardian(macOS,root)",
	Hidden: true,
	Flags: []urfavecli.Flag{
		&urfavecli.StringFlag{Name: "app-source", Usage: "源 Bx.app 路径(默认从自身位置推导)"},
		&urfavecli.StringFlag{Name: "config", Value: defaultConfigPath, Usage: "Guardian 配置路径"},
	},
	Action: appInstallAction,
},
```

- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/cli/ -run BundleRoot && go vet ./internal/cli/`;三平台交叉编译。

- [ ] **Step 5: Commit**

```bash
git add internal/cli/appinstall*.go internal/cli/cli.go
git commit -m "feat(cli): bx app-install 统一安装命令(hidden,root)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: darwin `setup` 与 `update` 适配统一布局

**Files:**
- Modify: `internal/cli/cli.go`(`setupAction` :3483 的 SelfInstall/WriteUnit 段)
- Modify: `internal/cli/update.go`(`updateAction` :188 开头)
- Test: `internal/cli/cli_test.go` 或新 `internal/cli/unifiedlayout_test.go`(纯逻辑)

**Interfaces:**
- Produces: `func unifiedLayoutActive() bool`(darwin 且 `runtimedir.Installed(runtimedir.Root)`;其余 GOOS 恒 false;放无 tag 文件,内部用 `runtime.GOOS` 判断)。

行为变化:
1. `setupAction`:统一布局下**跳过 `install.SelfInstall()`**(bridge 不能被整二进制覆盖),Guardian plist 直接 `install.WriteGuardianUnit(install.GuardianExecutable(), abs)`;legacy 布局(runtime 未装)保持原 SelfInstall + `install.WriteUnit(buildExecStart(bin, abs))` 不变。
2. `updateAction`:统一布局且非 `--check` → 直接报错指引(旧就地替换会用整二进制覆盖 bridge、绕过 Guardian 事务)。`--check`/`--json --check` 照常可用。

- [ ] **Step 1: 写失败测试**

```go
func TestDarwinUpdateGuardMessage(t *testing.T) {
	err := unifiedUpdateGuard(true, false)
	if err == nil || !strings.Contains(err.Error(), "Bx.app") {
		t.Fatalf("want guidance error, got %v", err)
	}
	if err := unifiedUpdateGuard(true, true); err != nil {
		t.Fatalf("--check must pass: %v", err)
	}
	if err := unifiedUpdateGuard(false, false); err != nil {
		t.Fatalf("legacy layout must pass: %v", err)
	}
}
```

- [ ] **Step 2: 跑红**。

- [ ] **Step 3: 实现**

```go
// update.go
func unifiedUpdateGuard(unifiedLayout, checkOnly bool) error {
	if !unifiedLayout || checkOnly {
		return nil
	}
	return errors.New("统一安装(Bx.app)模式暂不支持 bx update 就地更新:请下载新版 bx-macos 包,打开新版 Bx.app(或运行其 install.sh)完成整体升级;经 Guardian 的统一在线更新将在下一阶段提供")
}
```

`updateAction` 开头(解析 flags 后):`if err := unifiedUpdateGuard(unifiedLayoutActive(), c.Bool("check")); err != nil { return err }`。

`unifiedLayoutActive`(新文件 `internal/cli/unifiedlayout.go`,无 tag):

```go
func unifiedLayoutActive() bool {
	return runtime.GOOS == "darwin" && runtimedir.Installed(runtimedir.Root)
}
```

`setupAction` 中原第 5-6 步(`install.SelfInstall()` + `install.WriteUnit(...)`)改为:

```go
if unifiedLayoutActive() {
	if err := install.WriteGuardianUnit(install.GuardianExecutable(), abs); err != nil {
		return fmt.Errorf("写入 Guardian 服务失败: %w", err)
	}
	fmt.Println("✓ 统一布局:保留 CLI bridge,Guardian 指向 runtime")
} else {
	bin, err := install.SelfInstall()
	if err != nil {
		return fmt.Errorf("安装 bx 失败: %w", err)
	}
	if err := install.WriteUnit(buildExecStart(bin, abs)); err != nil {
		return fmt.Errorf("写入服务失败: %w", err)
	}
}
```

(保留原两步的既有打印/错误文案结构,以现场代码为准微调。)

- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/cli/ && go vet ./...`。

- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): setup/update 适配统一布局(保护 bridge,拦截就地更新)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: darwin `bx uninstall` 完整卸载

**Files:**
- Create: `internal/cli/uninstall_darwin.go`(`//go:build darwin`)+ `internal/cli/uninstall_darwin_test.go`
- Modify: `internal/cli/cli.go`(`uninstallAction` :5056 增加 darwin 分派)

**Interfaces:**
- Produces(纯计划函数,TDD 主体;执行器薄):

```go
type darwinUninstallPlan struct {
	LaunchctlCommands [][]string // bootout guard + gui agent
	RemovePaths       []string   // plists、bridge、runtime root、app、agent plists
	KeepPaths         []string   // /etc/bx、/var/lib/bx(说明用)
}
func buildDarwinUninstallPlan(consoleUID int, consoleHome string, unifiedLayout bool) darwinUninstallPlan
```

- [ ] **Step 1: 写失败测试**

```go
func TestBuildDarwinUninstallPlanUnified(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", true)
	wantCommands := [][]string{
		{"launchctl", "bootout", "system/com.getbx.bx.guard"},
		{"launchctl", "bootout", "gui/501/com.getbx.bx.menu"},
	}
	if !reflect.DeepEqual(plan.LaunchctlCommands, wantCommands) {
		t.Fatalf("commands = %v", plan.LaunchctlCommands)
	}
	for _, want := range []string{
		"/Library/LaunchDaemons/com.getbx.bx.guard.plist",
		"/usr/local/bin/bx",
		"/Library/Application Support/bx/runtime",
		"/Applications/Bx.app",
		"/Users/alice/Library/LaunchAgents/com.getbx.bx.menu.plist",
		"/Users/alice/Library/LaunchAgents/com.ggshr9.bx.menu.plist",
	} {
		if !slices.Contains(plan.RemovePaths, want) {
			t.Fatalf("RemovePaths missing %s: %v", want, plan.RemovePaths)
		}
	}
	for _, keep := range []string{"/etc/bx", "/var/lib/bx"} {
		if slices.Contains(plan.RemovePaths, keep) {
			t.Fatalf("must keep %s", keep)
		}
		if !slices.Contains(plan.KeepPaths, keep) {
			t.Fatalf("KeepPaths missing %s", keep)
		}
	}
}

func TestBuildDarwinUninstallPlanLegacySkipsRuntime(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/alice", false)
	if slices.Contains(plan.RemovePaths, "/Library/Application Support/bx/runtime") {
		t.Fatal("legacy layout must not remove runtime root")
	}
	if slices.Contains(plan.RemovePaths, "/Applications/Bx.app") {
		t.Fatal("legacy layout must not remove /Applications/Bx.app")
	}
}
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**——`buildDarwinUninstallPlan` 按测试构造;执行器 `uninstallDarwinAction`:
1. Guardian 状态守卫:`guardian.NewClient()`(既有 client)`Status(ctx)` 可达且 `Protection` ∈ {`protected`,`starting`,`recovering`,`blocked`} → 返回错误 `"保护仍在运行:先执行 sudo bx down 再卸载"`(Guardian 不可达视为未运行,继续)。
2. 依计划执行:launchctl 逐条 `exec.Command`(错误仅打印警告,不中断——服务可能本就未加载);`install.Uninstall()`(清 legacy Core plist);`RemovePaths` 逐个 `os.RemoveAll`;打印保留说明(`KeepPaths` + "如需彻底清除配置数据请手动删除")。
3. `uninstallAction` 开头:`if runtime.GOOS == "darwin" { return uninstallDarwinAction(c) }`(`uninstall_other` 不需要——非 darwin 走原逻辑)。consoleUID/home 取法同 Task 7;取不到时跳过 gui 域与用户文件并打印提示。

- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/cli/ -run DarwinUninstall && go vet ./...`。

- [ ] **Step 5: Commit**

```bash
git add internal/cli/uninstall_darwin.go internal/cli/uninstall_darwin_test.go internal/cli/cli.go
git commit -m "feat(cli): darwin 完整卸载(guard/runtime/bridge/App/登录项,保留配置)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: 打包脚本 — bundle 内嵌资产 + 新 tar 布局 + verify 全套

**Files:**
- Modify: `scripts/package-macos-release.sh`
- Modify: `scripts/verify-macos-release.sh`
- (不动 `scripts/package-macos-menu.sh` 的 App 构建;资产内嵌发生在 release 脚本层)

**Interfaces:**
- Produces(新 release 布局,Task 12/14 与 Phase 2 依赖):

```
dist/release/bx-macos-<arch>/
├── Bx.app/Contents/{Info.plist, MacOS/BxMenu, Resources/{bx-cli, bx-bridge, release.json}}
├── install.sh    # sudo "<dir>/Bx.app/Contents/Resources/bx-cli" app-install --app-source "<dir>/Bx.app"
├── uninstall.sh  # 指引 sudo bx uninstall
└── README.txt
```

顶层裸 `bx` 从包里移除(CLI 经 app-install 安装;`bx_darwin_*.tar.gz` 纯 CLI 资产仍由 release.yml build job 单独发布,不受影响)。旧客户端 `bx update --package` 对新包会解包失败并干净报错——pre-1.0 可接受,README 注明。

- [ ] **Step 1: 修改 `package-macos-release.sh`**

在现有「build bx + 打包 App」之后、写 install.sh 之前插入:

```bash
# 内嵌本 release 的 CLI 资产到 App bundle
RESOURCES="$RELEASE_DIR/Bx.app/Contents/Resources"
install -m 0755 "$RELEASE_DIR/bx" "$RESOURCES/bx-cli"
GOOS=darwin GOARCH="$ARCH" go build -trimpath -ldflags "-X github.com/getbx/bx/internal/version.Version=$VERSION" \
  -o "$RESOURCES/bx-bridge" ./cmd/bx-bridge
CLI_SHA=$(shasum -a 256 "$RESOURCES/bx-cli" | awk '{print $1}')
BRIDGE_SHA=$(shasum -a 256 "$RESOURCES/bx-bridge" | awk '{print $1}')
cat > "$RESOURCES/release.json" <<EOF
{
  "schema_version": 1,
  "version": "$VERSION",
  "platform": "darwin/$ARCH",
  "assets": {
    "bx-cli": "$CLI_SHA",
    "bx-bridge": "$BRIDGE_SHA"
  }
}
EOF
rm "$RELEASE_DIR/bx"   # 顶层裸 bx 不再进包
```

install.sh heredoc 整体替换为(保留 arch/darwin/非 root 预检结构):

```bash
#!/bin/bash
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
[ "$(uname -s)" = "Darwin" ] || { echo "仅支持 macOS" >&2; exit 1; }
[ "$(id -u)" -ne 0 ] || { echo "请勿用 root 运行本脚本(安装时会请求一次 sudo)" >&2; exit 1; }
MACHINE="$(uname -m)"
case "__BX_RELEASE_ARCH__:$MACHINE" in
  arm64:arm64|amd64:x86_64) ;;
  *) echo "架构不匹配:包为 __BX_RELEASE_ARCH__,机器为 $MACHINE" >&2; exit 1 ;;
esac
[ -x "$DIR/Bx.app/Contents/Resources/bx-cli" ] || { echo "包不完整:缺少 bx-cli" >&2; exit 1; }
echo "即将安装 Bx.app 到 /Applications 并配置 bx(需要一次管理员授权)。"
echo "安装不会启动保护、不修改你的连接配置。"
sudo "$DIR/Bx.app/Contents/Resources/bx-cli" app-install --app-source "$DIR/Bx.app"
echo "完成。打开菜单栏的 bx 图标继续 Set Up。"
```

uninstall.sh 替换为提示 `sudo bx uninstall`(说明其會停用 Guardian 服务、移除 App/CLI/runtime/登录项、保留 /etc/bx 与 /var/lib/bx)。README.txt 相应改写(安装两种方式:双击拖 /Applications 后打开 App 点 Install bx;或运行 ./install.sh)。tar 打包段与 SHA256SUMS 逻辑不变。

- [ ] **Step 2: 更新 `verify-macos-release.sh` 断言**(旧断言全面对照替换)

新增/替换检查:
- `Bx.app/Contents/Resources/bx-cli`、`bx-bridge` 存在、可执行、`file` 为对应 arch Mach-O;
- `Resources/release.json` 存在,且用 `python3 -c 'import json,sys; json.load(open(sys.argv[1]))'` 校验合法 JSON,`version` 字段 == `$BX_VERSION`;
- 重算 `shasum -a 256` 与 release.json 中 `bx-cli`/`bx-bridge` digest 一致;
- 顶层 `bx` **不存在**(`[ ! -e "$RELEASE_DIR/bx" ]`);
- install.sh 含 `app-install --app-source`、含「不会启动保护」字样、不含 `bx setup`/`bx up`/`networksetup`;
- uninstall.sh 含 `bx uninstall`;
- 保留既有:Info.plist lint、NSAppleEventsUsageDescription、tar 完整性、SHA256SUMS 校验、arch 预检与非 root guard 断言(引用行随新文案更新)。

- [ ] **Step 3: 本机验证(staging-only,不装任何东西)**

```bash
STAGE=$(mktemp -d)
BX_ARCH=arm64 BX_VERSION=dev BX_RELEASE_DIR="$STAGE/release" scripts/package-macos-release.sh
BX_ARCH=arm64 BX_VERSION=dev BX_RELEASE_DIR="$STAGE/release" scripts/verify-macos-release.sh
```

Expected: 两脚本全绿;verify 输出含 release.json digest 校验通过。

- [ ] **Step 4: Commit**

```bash
git add scripts/package-macos-release.sh scripts/verify-macos-release.sh
git commit -m "build(macos): Bx.app 内嵌 bx-cli/bx-bridge/release.json,包安装走 app-install

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: BxMenu 单实例 + 登录项自愈(Swift)

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/InstanceGate.swift`
- Create: `apps/macos/BxMenu/Tests/InstanceGateTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(applicationDidFinishLaunching 最前)
- Modify: `apps/macos/BxMenu/run-swift-tests.sh`、`scripts/test-macos-menu.sh`(登记编译组 `instance-gate`: `InstanceGate.swift` + 测试)

**Interfaces:**
- Produces:

```swift
enum InstanceDecision: Equatable {
    case keepSelf(terminatePeer: Bool)
    case yieldToPeer
}
func resolveInstanceConflict(selfPath: String, peerPath: String?, canonicalPath: String) -> InstanceDecision
func menuLaunchAgentPlist(executablePath: String, logDirectory: String) -> String
```

- [ ] **Step 1: 写失败测试**(`InstanceGateTests.swift`,沿用仓库 `@main struct` + `expect` 风格)

```swift
import Foundation

@main
struct InstanceGateTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition { failures += 1; FileHandle.standardError.write(Data(("FAIL: " + message + "\n").utf8)) }
    }

    static func main() {
        let canonical = "/Applications/Bx.app"
        expect(resolveInstanceConflict(selfPath: canonical, peerPath: nil, canonicalPath: canonical)
            == .keepSelf(terminatePeer: false), "no peer keeps self")
        expect(resolveInstanceConflict(selfPath: canonical, peerPath: "/Users/a/Downloads/Bx.app", canonicalPath: canonical)
            == .keepSelf(terminatePeer: true), "canonical self terminates stray peer")
        expect(resolveInstanceConflict(selfPath: "/Users/a/Downloads/Bx.app", peerPath: canonical, canonicalPath: canonical)
            == .yieldToPeer, "stray self yields to canonical peer")
        expect(resolveInstanceConflict(selfPath: "/Users/a/Desktop/Bx.app", peerPath: "/Users/a/Downloads/Bx.app", canonicalPath: canonical)
            == .yieldToPeer, "newcomer yields when neither canonical")

        let plist = menuLaunchAgentPlist(executablePath: "/Applications/Bx.app/Contents/MacOS/BxMenu",
                                         logDirectory: "/Users/a/Library/Logs/bx")
        expect(plist.contains("<string>com.getbx.bx.menu</string>"), "agent label present")
        expect(plist.contains("<string>/Applications/Bx.app/Contents/MacOS/BxMenu</string>"), "agent points at canonical app")
        expect(plist.contains("<string>/Users/a/Library/Logs/bx/menu.log</string>"), "stdout log path")
        expect(!plist.contains("KeepAlive"), "menu agent must not keep-alive")

        if failures > 0 { exit(1) }
        print("InstanceGateTests passed")
    }
}
```

- [ ] **Step 2: 登记两个测试跑器**(新增编译组 `instance-gate`,源文件 `InstanceGate.swift` + 该测试)→ `bash scripts/test-macos-menu.sh` 跑红(编译失败)。

- [ ] **Step 3: 实现 `InstanceGate.swift`**

```swift
import Foundation

enum InstanceDecision: Equatable {
    case keepSelf(terminatePeer: Bool)
    case yieldToPeer
}

func resolveInstanceConflict(selfPath: String, peerPath: String?, canonicalPath: String) -> InstanceDecision {
    guard let peerPath else { return .keepSelf(terminatePeer: false) }
    if selfPath == canonicalPath { return .keepSelf(terminatePeer: true) }
    if peerPath == canonicalPath { return .yieldToPeer }
    return .yieldToPeer
}

func menuLaunchAgentPlist(executablePath: String, logDirectory: String) -> String {
    """
    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
    <plist version="1.0">
    <dict>
      <key>Label</key>
      <string>com.getbx.bx.menu</string>
      <key>ProgramArguments</key>
      <array>
        <string>\(executablePath)</string>
      </array>
      <key>RunAtLoad</key>
      <true/>
      <key>StandardOutPath</key>
      <string>\(logDirectory)/menu.log</string>
      <key>StandardErrorPath</key>
      <string>\(logDirectory)/menu.err.log</string>
    </dict>
    </plist>
    """
}
```

main.swift 的 `applicationDidFinishLaunching` 最前插入(AppKit 接线,不进测试):

```swift
enforceSingleInstance()
ensureLoginItemIfCanonical()
```

```swift
private func enforceSingleInstance() {
    guard let bundleID = Bundle.main.bundleIdentifier else { return }
    let selfPID = NSRunningApplication.current.processIdentifier
    let peers = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
        .filter { $0.processIdentifier != selfPID }
    guard let peer = peers.first else { return }
    switch resolveInstanceConflict(selfPath: Bundle.main.bundleURL.path,
                                   peerPath: peer.bundleURL?.path,
                                   canonicalPath: "/Applications/Bx.app") {
    case .keepSelf(terminatePeer: true):
        peer.terminate()
    case .keepSelf(terminatePeer: false):
        break
    case .yieldToPeer:
        peer.activate(options: [])
        NSApp.terminate(nil)
    }
}

private func ensureLoginItemIfCanonical() {
    let canonical = "/Applications/Bx.app"
    guard Bundle.main.bundleURL.path == canonical else { return }
    let agentDir = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/LaunchAgents")
    let agentURL = agentDir.appendingPathComponent("com.getbx.bx.menu.plist")
    let logDir = FileManager.default.homeDirectoryForCurrentUser
        .appendingPathComponent("Library/Logs/bx").path
    let desired = menuLaunchAgentPlist(executablePath: canonical + "/Contents/MacOS/BxMenu",
                                       logDirectory: logDir)
    if (try? String(contentsOf: agentURL, encoding: .utf8)) == desired { return }
    try? FileManager.default.createDirectory(at: agentDir, withIntermediateDirectories: true)
    try? desired.write(to: agentURL, atomically: true, encoding: .utf8)
}
```

- [ ] **Step 4: 跑绿** — `bash scripts/test-macos-menu.sh`(全部组,含新组)与 `cd apps/macos/BxMenu && swift build`。

- [ ] **Step 5: Commit**

```bash
git add apps/macos/BxMenu/ scripts/test-macos-menu.sh
git commit -m "feat(macos): BxMenu 单实例守护与登录项自愈

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: BxMenu「Not Installed → Install bx」与「Turn Off bx」(Swift)

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/InstallPresentation.swift`
- Create: `apps/macos/BxMenu/Tests/InstallPresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(BxState、loadState、菜单、动作)
- Modify: 两个测试跑器(新组 `install-presentation`)

**Interfaces:**
- Consumes: `runPrivileged`、`shellSingleQuoted`、`runBx`、`addHeader/addInfo/addAction`(main.swift 既有);`/Library/Application Support/bx/runtime` 布局(Task 2)。
- Produces:

```swift
struct RuntimeRelease: Decodable, Equatable {
    let version: String
    private enum CodingKeys: String, CodingKey { case version }
}
func decodeRuntimeVersion(_ data: Data) -> String?
func unifiedRuntimeVersion(root: String) -> String?   // 读 <root>/current/release.json
func installActionTitle(runtimeInstalled: Bool, cliUsable: Bool) -> String?  // 均 false → "Install bx…"
let turnOffActionTitle = "Turn Off bx"
```

- [ ] **Step 1: 写失败测试**

```swift
import Foundation

@main
struct InstallPresentationTests {
    static var failures = 0
    static func expect(_ c: Bool, _ m: String) {
        if !c { failures += 1; FileHandle.standardError.write(Data(("FAIL: " + m + "\n").utf8)) }
    }

    static func main() {
        expect(decodeRuntimeVersion(Data(#"{"schema_version":1,"version":"1.2.3","platform":"darwin/arm64","assets":{"bx-cli":"ab"}}"#.utf8)) == "1.2.3",
               "decode version from release.json")
        expect(decodeRuntimeVersion(Data("not json".utf8)) == nil, "garbage yields nil")

        expect(installActionTitle(runtimeInstalled: false, cliUsable: false) == "Install bx…", "fresh mac offers install")
        expect(installActionTitle(runtimeInstalled: false, cliUsable: true) == nil, "legacy CLI install shows no install action")
        expect(installActionTitle(runtimeInstalled: true, cliUsable: true) == nil, "healthy unified layout shows no install action")
        expect(installActionTitle(runtimeInstalled: true, cliUsable: false) == "Install bx…", "runtime present but cli broken offers repair-install")

        expect(turnOffActionTitle == "Turn Off bx", "turn off title pinned")

        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("bx-install-presentation-\(getpid())")
        let current = dir.appendingPathComponent("current")
        try? FileManager.default.createDirectory(at: current, withIntermediateDirectories: true)
        try? Data(#"{"schema_version":1,"version":"9.9.9","platform":"darwin/arm64","assets":{"bx-cli":"ab"}}"#.utf8)
            .write(to: current.appendingPathComponent("release.json"))
        expect(unifiedRuntimeVersion(root: dir.path) == "9.9.9", "reads version via current link dir")
        expect(unifiedRuntimeVersion(root: dir.path + "-missing") == nil, "missing root yields nil")

        if failures > 0 { exit(1) }
        print("InstallPresentationTests passed")
    }
}
```

- [ ] **Step 2: 登记两个跑器 → 跑红。**

- [ ] **Step 3: 实现**

`InstallPresentation.swift`:

```swift
import Foundation

struct RuntimeRelease: Decodable, Equatable {
    let version: String
    private enum CodingKeys: String, CodingKey { case version }
}

func decodeRuntimeVersion(_ data: Data) -> String? {
    (try? JSONDecoder().decode(RuntimeRelease.self, from: data))?.version
}

func unifiedRuntimeVersion(root: String = "/Library/Application Support/bx/runtime") -> String? {
    let path = root + "/current/release.json"
    guard let data = FileManager.default.contents(atPath: path) else { return nil }
    return decodeRuntimeVersion(data)
}

func installActionTitle(runtimeInstalled: Bool, cliUsable: Bool) -> String? {
    if runtimeInstalled && cliUsable { return nil }
    if !runtimeInstalled && cliUsable { return nil } // legacy 布局:沿用既有指引,不递归安装
    return "Install bx…"
}

let turnOffActionTitle = "Turn Off bx"
```

main.swift 变更:
1. `BxState` 增加 `case notInstalled(bundleVersion: String?)`;`statusIndicatorState()` 映射到 `.missing`(灰)。
2. `loadState()` 首段改为:

```swift
let runtimeInstalled = unifiedRuntimeVersion() != nil
let cliUsable = FileManager.default.isExecutableFile(atPath: bxPath) && runBx(["--version"]).exitCode == 0
if installActionTitle(runtimeInstalled: runtimeInstalled, cliUsable: cliUsable) != nil {
    state = .notInstalled(bundleVersion: bundleReleaseVersion())
    return
}
```

`bundleReleaseVersion()` 读 `Bundle.main.url(forResource: "release", withExtension: "json", subdirectory: nil)`(即 `Contents/Resources/release.json`)→ `decodeRuntimeVersion`。`runBx` 若现返回类型无 exitCode 字段则以现场 `CommandResult` 为准取既有状态字段。
3. 菜单 `notInstalled` 分支:header `"bx"` / subtitle `"Not Installed"`;info `Version: <bundleVersion>`(有则显);action `Install bx…`(symbol `arrow.down.circle`)→ `installBx()`;保留 `View Logs`、`Quit Menu`。既有 `.missing` 渲染保持(legacy 提示路径仍会用到)。
4. `installBx()`:

```swift
@objc private func installBx() {
    let alert = NSAlert()
    alert.messageText = "Install bx?"
    alert.informativeText = "bx will install its command line tool and background protection service. macOS will ask for administrator authorization. Protection is not started until you set up and turn it on."
    alert.addButton(withTitle: "Install")
    alert.addButton(withTitle: "Cancel")
    guard alert.runModal() == .alertFirstButtonReturn else { return }
    let bundlePath = Bundle.main.bundleURL.path
    let installer = bundlePath + "/Contents/Resources/bx-cli"
    guard FileManager.default.isExecutableFile(atPath: installer) else {
        showFailure("Install Failed", "This copy of Bx.app has no embedded installer. Download the full bx-macos package.")
        return
    }
    let command = "\(shellSingleQuoted(installer)) app-install --app-source \(shellSingleQuoted(bundlePath))"
    if runPrivileged(command) {
        if bundlePath != "/Applications/Bx.app" {
            NSWorkspace.shared.open(URL(fileURLWithPath: "/Applications/Bx.app"))
            NSApp.terminate(nil)
        } else {
            refresh()
        }
    } else {
        showFailure("Install Failed", "bx could not complete the installation.")
    }
}
```

5. 「Turn Off bx」:connected/warning 菜单在 `Quit bx…` 之前加 `addAction(turnOffActionTitle, symbol: "pause.circle", …)` → `turnOffBx()`:确认弹窗 messageText `"Turn off bx?"`、informativeText `"bx will stop protecting system traffic and restore managed DNS settings. The menu stays open."`,确认后 `runPrivileged("'\(bxPath)' down")` + `refresh()`(App 保持打开显示 Off)。既有 `Quit bx…`(down + 退出)不动。

- [ ] **Step 4: 跑绿** — `bash scripts/test-macos-menu.sh && (cd apps/macos/BxMenu && swift build)`。

- [ ] **Step 5: Commit**

```bash
git add apps/macos/BxMenu/ scripts/test-macos-menu.sh
git commit -m "feat(macos): 菜单 Install bx 一键安装与 Turn Off bx(保护与退出解耦)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 13: 文档 — 统一安装故事

**Files:**
- Modify: `README.md`(macOS 安装/更新/卸载段,含 :334 附近旧「更新不打断保护」表述)
- Modify: `apps/macos/BxMenu/README.md`

**Steps:**

- [ ] **Step 1: README macOS 段改写**,要点(以现有文风续写,中文):
  - 安装:下载 `bx-macos-<arch>.tar.gz` → 解压 → 把 `Bx.app` 拖入 `/Applications` → 打开 → 菜单栏点 `Install bx…`(一次管理员授权;或运行包内 `./install.sh`)→ `Set Up bx` 粘贴链接 → `Start Protection`。
  - 布局说明:`/Applications/Bx.app`(唯一产品)、`/usr/local/bin/bx`(稳定入口,自动指向当前版本)、`/Library/Application Support/bx/runtime/<version>/`(root-owned 运行时);终端 `bx --version` 与 App 版本一致。
  - 更新:统一布局暂不支持 `bx update` 就地更新——下载新包重复安装即可(幂等、保留配置、不打断已停的状态;Guardian 统一在线更新在下一阶段)。删除/改写「`sudo bx update` 不打断当前保护会话」旧句。
  - 卸载:`sudo bx uninstall`(移除服务/CLI/runtime/App/登录项,保留 `/etc/bx` 与 `/var/lib/bx`)。
  - 开发模式:仓库 `./bx` 照常可跑测试与 `bx run`;不会注册生产服务或覆盖统一 runtime。
- [ ] **Step 2: BxMenu README** 更新手动安装节(LaunchAgent 由安装器/App 自愈生成,指向 `/Applications`;删掉指引用户手装 `dist/macos/com.getbx.bx.menu.plist` 的段落)。
- [ ] **Step 3: 自查** — `grep -n "usr/local/bin/bx\|Bx.app\|update" README.md` 通读一遍,确认无与新布局矛盾的句子。
- [ ] **Step 4: Commit**

```bash
git add README.md apps/macos/BxMenu/README.md
git commit -m "docs(macos): 统一 Bx.app 安装/更新/卸载故事

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 14: 统一安装验收脚本(dry-run 默认)

**Files:**
- Create: `scripts/darwin-unified-install-check.sh`(0755)

**Interfaces:**
- Consumes: Task 10 的包布局、`bx app-install`、`bx uninstall`。

**Steps:**

- [ ] **Step 1: 写脚本**,行为:
  - 默认 **dry-run**:`mktemp -d` staging → `BX_ARCH/BX_VERSION=dev BX_RELEASE_DIR=$STAGE scripts/package-macos-release.sh` + `verify-macos-release.sh` → 打印「将会写入」的完整清单(`/Applications/Bx.app`、`/Library/Application Support/bx/runtime/dev/{bx,release.json}` + `current`、`/usr/local/bin/bx`(bridge)、`/Library/LaunchDaemons/com.getbx.bx.guard.plist`、`~console/Library/LaunchAgents/com.getbx.bx.menu.plist`)与「不会做」清单(不 `bx up`、不 bootstrap guardian、不改 DNS/路由)后退出 0。
  - `--execute` 需 root + `--yes` 双确认:执行 `"$STAGE/release/Bx.app/Contents/Resources/bx-cli" app-install --app-source "$STAGE/release/Bx.app"`,随后断言并输出:`/usr/local/bin/bx --version` 成功且经 bridge(`readlink /Library/Application\ Support/bx/runtime/current` == `dev`);runtime 目录 `stat -f '%Su %Sp'` 为 root 且非组可写;guard plist `ProgramArguments` 含 runtime 路径(`grep 'runtime/current/bx'`);agent plist 存在;`launchctl print system/com.getbx.bx.guard` **未加载**(证明安装未启动服务;若机器上本就有旧 Guardian 在跑则跳过该断言并提示);全程零 `bx up`。
  - `--execute` 结束打印回滚指引:`sudo bx uninstall`。
- [ ] **Step 2: 本机 dry-run 验证** — `bash scripts/darwin-unified-install-check.sh` 全绿(仅 staging,零系统改动)。
- [ ] **Step 3: Commit**

```bash
git add scripts/darwin-unified-install-check.sh
git commit -m "test(macos): 统一安装验收脚本(默认 dry-run,execute 需显式授权)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Manual macOS Acceptance Gate

自动任务全绿后**停下**,把 staged 包路径与下述矩阵呈给用户,由用户在本机逐条执行(涉及 sudo/launchctl/网络的步骤开发 agent 一律不代跑):

1. `bash scripts/darwin-unified-install-check.sh --execute --yes`(本机已有 legacy 安装:此步即真实迁移)→ 全部断言绿。
2. `bx --version`(经 bridge)与 `/Applications/Bx.app` 菜单显示版本一致;`readlink '/Library/Application Support/bx/runtime/current'` 指向该版本。
3. 打开 Bx.app 十次:始终只有一个菜单图标、一个 BxMenu 进程;`~/Applications/Bx.app` 与 `com.ggshr9.bx.menu` 已被迁移清理。
4. 菜单 `Set Up bx`(或沿用既有 `/etc/bx/config.yaml`)→ `Start Protection` → 出口 == VPS(用 icanhazip.com / api.ipify.org,勿用 china 列表域名)。
5. `sudo bx up` 能唤醒菜单且不产生第二实例;Quit Menu 后保护继续(出口仍 == VPS),重开 App 状态恢复。
6. `Turn Off bx` → 状态变 Off、App 保持打开、DNS 还原;再 `Start Protection` 恢复。
7. 删除 `/Applications/Bx.app`(保护运行中)→ Core 不中断;重装 App → 状态无损恢复。
8. `sudo bx uninstall`(先 `sudo bx down`)→ guard/agent plist、runtime、bridge、App 全清,`/etc/bx`、`/var/lib/bx` 保留。

## Follow-On Plan Boundary

本计划完成后,为 Phase 2「统一更新与 Repair」另写并执行独立计划,范围已在本次侦察中钉死:
- `bx update`(darwin)改走 Guardian:下载验证后 stage 到 `/var/lib/bx/update/staging/<txid>/`,以 root POST `/v1/update`(`guardian.Client.Update` 目前零调用方);
- Guardian 更新事务的 activation 改为「装 `runtime/<newver>`(屏障前)→ 新 Core 从新版本目录起 → 健康门 → 原子换 App/flip current/换 bridge → committed」;`ExtractMacOSPackage` v2 适配新包布局;`validateInstallOptions` 的 `CLIDestination == BinPath` 钉死点放开为 runtime 布局;
- `guardian.Status` 增加 `guardian_version`/`runtime_version`,App 做三版本一致性 + `Repair bx`;
- 菜单更新 UX 换成 07-16 规范文案(「Internet access may pause briefly…」),废除 `--package/--app-path/--app-owner` AppleScript 路径;
- 失败注入 + 真机更新验收矩阵(Off/Protected/激活失败回滚/App 替换失败回滚)。
禁止在 Phase 2 重新引入 AppleScript reconnect 或第二条 CLI 生命周期。
