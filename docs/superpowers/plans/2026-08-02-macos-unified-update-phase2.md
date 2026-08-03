# macOS 统一更新与 Repair(Phase 2)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `bx update` 与菜单更新在统一 Bx.app 布局下真正可用:Protected 时走 Guardian 的 fail-closed 更新事务(屏障→换版本→健康门→原子提交,失败完整回滚),Off 时直接文件级升级;App 显示三版本一致性并提供 Repair;真机可用本地包做更新与回滚演练。

**Architecture:** 复用 Guardian 既有更新事务引擎(`Manager.Update`/`updatePreparedLocked`/crash recovery,一行状态机不改),只替换**activation 的内容物**:`macOSUpdatePreparer` 改为统一布局——Prepare 阶段(屏障外)解包 v2 包、把新版本装进 `runtime/<toVersion>/`(不 flip)、用 `PrepareMacOSInstall`(payload.CLI=**bx-bridge 字节**,`CLIDestination` 仍是 `/usr/local/bin/bx`,钉死点无需放开)做 bridge+App 快照;Activate= 原快照机制 + `SwitchCurrent` flip;新 Core 从 `runtime/<toVersion>/bx` 起(`CoreRunner` 增加 `Executable()/SetExecutable`)。CLI 侧:root 下载验证→stage 到 `/var/lib/bx/update/staging/<txid>/`→POST `/v1/update`;Off 态解包后直接复用 `install.UnifiedInstall`。菜单换 07-16 规范文案、经 `bx update --json` 单次授权、解析 UpdateResult 呈现结果;新增 Updating(黄)呈现与 Repair。

**Tech Stack:** Go 1.26(既有 guardian/update/runtimedir/release 包)、Swift 5.9、bash 验收脚本。

## Global Constraints

- **绝不擅自启动/停止 bx、改路由、动 launchd**:实现期间不执行 `bx up/down/update/reconnect`、sudo、launchctl 变更;涉及真机的验证全部 dry-run,真实执行只在「Manual macOS Acceptance Gate」由用户授权。
- **Guardian 更新事务事件序不可变**(07-16 spec 原文):`prepare+verify → journal(prepared) → barrier install → journal(barrier_active) → stop old → reassert bypass → activate files → start new → complete health gate → journal(committed) → barrier remove → receipt → cleanup`。本计划只改 activate 的内容与新 Core 的可执行路径,不改序。
- **屏障语义不可变**:屏障期间 DNS 保持 127.0.0.1(fail-closed,绝不临时恢复 ISP DNS);V1 要求至少一个已解析 IPv4 `/32` server bypass;双失败 → 保持 blocked + `needs_attention`。
- **60 秒预算**:`/v1/update` 受 `guardianMutationTimeout = time.Minute`(localapi.go:13)约束。所有大 I/O(解包、runtime 版本目录写入、快照复制)都发生在 `Prepare`(屏障安装前);屏障窗口内只允许 rename/symlink-flip/进程起停/健康门(≤20s)。任何任务不得把大拷贝挪进屏障窗口。
- **`currentGuardianProtocol` 保持 1**:`/v1/update` 此前零生产调用方,无兼容压力;不引入协议升级。
- **无秘密泄漏**:descriptor/journal/receipt/argv/日志/UpdateResult 不含 client link、token、服务密码(`safeLastErrorPattern` 只存稳定错误码的约束不变)。
- **固定路径/label 全部沿用**:`/var/lib/bx/update/{staging,snapshots}`、`/var/run/bx-guard.sock`、`runtimedir.Root`、`install.BinPath`、`com.getbx.bx.*`。
- **验证命令**:`go build ./... && go vet ./... && go test ./...` + `GOOS=linux GOARCH=amd64 go build -o /dev/null ./...` + `GOOS=windows GOARCH=amd64 go build -o /dev/null ./...`;`gofumpt -l <改动目录>` 空;Swift 改动跑 `bash scripts/test-macos-menu.sh`(新测试同步登记 `apps/macos/BxMenu/run-swift-tests.sh` 与 `scripts/test-macos-menu.sh` 两个跑器)。
- **提交**:中文 conventional commits,直接提交 master,结尾 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。

---

## File Structure

**修改(Go):**
- `internal/update/macos_package.go` + `macos_package_test.go` — `ExtractMacOSPackage` v2(Bx.app-only 布局,返回 `MacOSPackage`)。
- `internal/runtimedir/runtimedir.go` + 测试 — 新增 `GC(root string, keep []string) error`。
- `internal/guardian/manager.go` — `CoreRunner` 接口加 `Executable()/SetExecutable`。
- `internal/guardian/process.go` — `ExecCoreRunner` 实现两方法(mutex 保护)。
- `internal/guardian/update.go` + 测试 — `macOSUpdatePreparer` 统一化、`preparedMacOSUpdate` 增 `CoreExecutable/PreviousCoreExecutable`、descriptor v2、`updatePreparedLocked`/`rollbackUpdate`/recovery 路径接可执行切换与 current flip。
- `internal/guardian/types.go` / `localapi.go` / `daemon.go` / `client.go` — Status 增 `guardian_version`/`runtime_version`;`LocalAPIOptions` 扩展;`NewClientWithTimeout`。
- `internal/cli/update.go` + 测试 — 统一布局 update 主路径(On→Guardian 事务 / Off→直接升级)、`--package-file`、删 `unifiedUpdateGuard` 与 legacy `updateMacOSPackage`。
- `internal/cli/update_package.go` — 删除 legacy 直接替换路径(保留仍被引用的辅助函数,grep 确认)。
- `internal/cli/cli.go` — `clientStatusReport` 增 `phase`/`core_version`/`guardian_version`/`runtime_version` 透传;`bx status` 人类输出加更新中提示。

**修改(Swift):**
- `apps/macos/BxMenu/Sources/BxMenu/UpdatePresentation.swift` + `Tests/UpdatePresentationTests.swift` — `UpdateResultJSON` 解码、`parseUpdateOutcome`、新文案常量。
- `apps/macos/BxMenu/Sources/BxMenu/InstallPresentation.swift` + 测试 — 取消 `menuUpdateActionTitle` 的统一布局压制;新增 `repairActionNeeded`/`updatingBanner`。
- `apps/macos/BxMenu/Sources/BxMenu/main.swift` — updateBx 新流程与文案、Updating 呈现、Repair 动作。
- 两个 Swift 测试跑器 — 编译组源列表更新。

**创建:**
- `scripts/darwin-unified-update-check.sh` — 更新/回滚真机演练(默认 dry-run)。

**文档:** `README.md`(更新章节重写)、`apps/macos/BxMenu/README.md`。

---

### Task 1: `ExtractMacOSPackage` v2 — Bx.app-only 包格式

**Files:**
- Modify: `internal/update/macos_package.go`(整体替换 Extract 逻辑)
- Test: `internal/update/macos_package_test.go`(改写既有用例 + 新增)

**Interfaces:**
- Produces:

```go
type MacOSPackage struct {
	CLI     []byte            // Contents/Resources/bx-cli(runtime 用)
	Bridge  []byte            // Contents/Resources/bx-bridge(/usr/local/bin/bx 用)
	Release release.Info      // 解析自 Contents/Resources/release.json
	App     map[string][]byte // Bx.app/ 下全部文件,键为相对路径(含上述三个)
}
func ExtractMacOSPackage(data []byte, arch string) (MacOSPackage, error)
func (p MacOSPackage) VerifyAssets(goos, goarch string) error
```

- 旧 `MacOSPayload{CLI, Menu}` **保留不动**(`PrepareMacOSInstall` 的输入);preparer 负责由 `MacOSPackage` 构造它。
- 解包规则:root `bx-macos-<arch>/`;只接受 `<root>/Bx.app/` 下的常规文件(其余条目含旧 `<root>/bx` 一律忽略,与现行为一致);沿用 `validateMacOSPackagePath`、128 MiB 总量上限、重复名拒绝、`LimitReader` 截断拒绝(均已存在,行号见 macos_package.go:30-75)。
- 必需文件(缺一 error):`Contents/MacOS/BxMenu`、`Contents/Info.plist`、`Contents/Resources/bx-cli`、`Contents/Resources/bx-bridge`、`Contents/Resources/release.json`(release.json 用 `release.Parse` 解析,失败即 error)。
- `VerifyAssets`:`Release.MatchesPlatform(goos,goarch)` + `Release.VerifyAsset("bx-cli", p.CLI)` + `Release.VerifyAsset("bx-bridge", p.Bridge)`。

- [ ] **Step 1: 改写测试**。用 helper 构造合法 tar.gz(gzip+tar 内存构造,root `bx-macos-arm64/Bx.app/...` 五必需文件,release.json 由 `release.Write` 风格 JSON 生成、digest 与内容一致):

```go
func buildV2Package(t *testing.T, mutate func(files map[string][]byte)) []byte {
	t.Helper()
	cli := []byte("cli-bytes")
	bridgeBin := []byte("bridge-bytes")
	cliSum, bridgeSum := sha256.Sum256(cli), sha256.Sum256(bridgeBin)
	releaseJSON := fmt.Sprintf(`{"schema_version":1,"version":"1.2.3","platform":"darwin/arm64","assets":{"bx-cli":"%x","bx-bridge":"%x"}}`, cliSum, bridgeSum)
	files := map[string][]byte{
		"bx-macos-arm64/Bx.app/Contents/MacOS/BxMenu":              []byte("menu"),
		"bx-macos-arm64/Bx.app/Contents/Info.plist":                []byte("<plist/>"),
		"bx-macos-arm64/Bx.app/Contents/Resources/bx-cli":          cli,
		"bx-macos-arm64/Bx.app/Contents/Resources/bx-bridge":       bridgeBin,
		"bx-macos-arm64/Bx.app/Contents/Resources/release.json":    []byte(releaseJSON),
	}
	if mutate != nil {
		mutate(files)
	}
	// gzip+tar 打包 files → 返回字节(实现留给测试 helper,顺序稳定遍历)
	...
}
```

用例:
- `TestExtractV2Roundtrip`:CLI/Bridge 字节正确、`Release.Version=="1.2.3"`、`App["Contents/MacOS/BxMenu"]` 存在、`VerifyAssets("darwin","arm64")` nil、`VerifyAssets("darwin","amd64")` 错误。
- `TestExtractV2MissingRequired`:分别删除五个必需文件各得 error。
- `TestExtractV2IgnoresTopLevelBx`:mutate 添加 `bx-macos-arm64/bx` → 解包成功且不影响结果。
- `TestExtractV2BadReleaseJSON`:release.json 换成垃圾 → error。
- 保留既有 path-traversal/尺寸/重复名用例(改为新布局)。
- [ ] **Step 2: 跑红** — `go test ./internal/update/ -run ExtractV2`。
- [ ] **Step 3: 实现**。改写 `ExtractMacOSPackage`:walk 中把 `name == root+"/bx"` 分支删掉(落入 ignore),`payload.Menu` 改为 `pkg.App`;收尾必需文件检查换成五项 + `release.Parse(App["Contents/Resources/release.json"])` + 填充 CLI/Bridge 便捷字段;新增 `VerifyAssets`。同步修 `validateMacOSPayload` 的调用方(它检查 `MacOSPayload`,不动)。**grep 全部 `ExtractMacOSPackage` 调用方**(`internal/guardian/update.go:878`、`internal/cli/update_package.go:40`)——本任务先让它们编译通过:guardian 侧临时改为 `payload := updatepkg.MacOSPayload{CLI: pkg.Bridge, Menu: pkg.App}`(Task 3 会重写该函数,此处最小适配);cli 侧 `extractMacOSPackage` wrapper 及其唯一调用方 `updateMacOSPackage` 属 legacy,将在 Task 7 删除——本任务同步把 wrapper 改为返回 `MacOSPackage` 或直接内联适配,保证 `go build ./...` 绿。
- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/update/ ./internal/guardian/ && go vet ./...`(guardian 既有更新测试若因 payload 构造变化需要同步 fixture,按同布局改)。
- [ ] **Step 5: Commit**

```bash
git add internal/update/ internal/guardian/ internal/cli/
git commit -m "feat(update): macOS 更新包 v2(Bx.app 内嵌资产布局)解包与资产校验

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `runtimedir.GC` + `CoreRunner` 可执行路径热切换

**Files:**
- Modify: `internal/runtimedir/runtimedir.go` + `runtimedir_test.go`
- Modify: `internal/guardian/manager.go`(`CoreRunner` 接口,~:44-50)
- Modify: `internal/guardian/process.go`(`ExecCoreRunner`)
- Test: `internal/guardian/process_test.go`(或既有 runner 测试文件追加)

**Interfaces:**
- Produces:

```go
// runtimedir
func GC(root string, keep []string) error
// 删除 <root> 下不在 keep 且非 "current" 的版本目录,以及所有 .staging-* / .discard-* 残留;
// keep 中的名字经 validVersionName 校验;current 符号链指向的版本即使不在 keep 也不删(防御)。

// guardian
type CoreRunner interface {
	Existing(context.Context) (Process, error)
	Watch(Process) Process
	Verify(Process) error
	Start(context.Context, CoreStartOptions) (Process, error)
	Stop(context.Context, Process) error
	Executable() string
	SetExecutable(string) error // 必须绝对路径,否则 error;并发安全
}
```

- `ExecCoreRunner`:两方法用 `r.mu` 保护;`Start`(process.go:146)与 `Verify`(process.go:258)内部读取 `r.Executable` 字段处全部改经私有 `r.executablePath()`(锁内读)。字段保留导出(构造用)。
- **grep 更新所有 `CoreRunner` fake**:`grep -rn 'CoreRunner\|Start(ctx' internal/guardian/*_test.go`,给每个 fake 补 `Executable()/SetExecutable`(fake 存字段即可)。

- [ ] **Step 1: 写失败测试**

```go
// runtimedir_test.go
func TestGC(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	for _, v := range []string{"1.0.0", "1.1.0", "1.2.0"} {
		if _, err := InstallVersion(root, payloadFor(t, v, []byte("cli-"+v))); err != nil {
			t.Fatal(err)
		}
	}
	if err := SwitchCurrent(root, "1.2.0"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".staging-leftover"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := GC(root, []string{"1.1.0", "1.2.0"}); err != nil {
		t.Fatalf("GC: %v", err)
	}
	for path, want := range map[string]bool{
		filepath.Join(root, "1.0.0"):            false,
		filepath.Join(root, "1.1.0"):            true,
		filepath.Join(root, "1.2.0"):            true,
		filepath.Join(root, ".staging-leftover"): false,
	} {
		_, err := os.Stat(path)
		if got := err == nil; got != want {
			t.Fatalf("%s exists=%v want %v", path, got, want)
		}
	}
	if _, _, err := Current(root); err != nil {
		t.Fatalf("current must survive GC: %v", err)
	}
}

func TestGCNeverRemovesCurrentTarget(t *testing.T) {
	root := filepath.Join(t.TempDir(), "runtime")
	if _, err := InstallVersion(root, payloadFor(t, "1.0.0", []byte("x"))); err != nil {
		t.Fatal(err)
	}
	if err := SwitchCurrent(root, "1.0.0"); err != nil {
		t.Fatal(err)
	}
	if err := GC(root, []string{"9.9.9"}); err != nil {
		t.Fatal(err)
	}
	if !Installed(root) {
		t.Fatal("GC must not remove the version current points to")
	}
}
```

```go
// guardian process 测试(追加)
func TestExecCoreRunnerSetExecutable(t *testing.T) {
	runner := NewExecCoreRunner("/a/bx", "/etc/bx/config.yaml", "127.0.0.1:53")
	if runner.Executable() != "/a/bx" {
		t.Fatalf("initial executable = %q", runner.Executable())
	}
	if err := runner.SetExecutable("relative/bx"); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if err := runner.SetExecutable("/b/bx"); err != nil {
		t.Fatal(err)
	}
	if runner.Executable() != "/b/bx" {
		t.Fatalf("swapped executable = %q", runner.Executable())
	}
}
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**(GC:ReadDir root,目录名以 `.staging-`/`.discard-` 前缀 → RemoveAll;其余目录名若 `validVersionName` 通过且不在 keep 集合且 != `os.Readlink(current)` 目标 → RemoveAll;`current` 符号链与文件跳过)。ExecCoreRunner 按接口实现;所有 fake 补齐。
- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/runtimedir/ ./internal/guardian/ && go vet ./...`。
- [ ] **Step 5: Commit**

```bash
git add internal/runtimedir/ internal/guardian/
git commit -m "feat(guardian): CoreRunner 可执行路径热切换 + runtimedir GC

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Guardian 统一 preparer(Prepare/Activate/Restore/Commit + descriptor v2)

**Files:**
- Modify: `internal/guardian/update.go`(`macOSUpdatePreparer.Prepare` :871-912、`preparedMacOSUpdate` :947-972、`updateRecoveryDescriptor` :979-999、`buildUpdateRecoveryDescriptorTemplate` :1001-1031、`validateUpdateRecoveryDescriptorStatic` :1108-1141、`PreparedUpdate` 接口 :60-67)
- Test: `internal/guardian/update_test.go`(既有 fixture 同步 + 新用例)

**Interfaces:**
- `PreparedUpdate` 接口追加两方法(所有实现与 fake 同步):

```go
CoreExecutable() string         // 新 Core 应从哪个绝对路径启动;"" = 不切换(保持 runner 现值)
PreviousCoreExecutable() string // 回滚时应切回的绝对路径;"" = 不切换
```

- `updateRecoveryDescriptor` 追加字段(SchemaVersion 仍 1;新字段 omitempty,旧 descriptor 读入时为空即走 legacy 语义):

```go
RuntimeRoot        string `json:"runtime_root,omitempty"`
FromRuntimeVersion string `json:"from_runtime_version,omitempty"`
ToRuntimeVersion   string `json:"to_runtime_version,omitempty"`
```

- `macOSUpdatePreparer.Prepare` 新逻辑(**全部发生在屏障安装前**,替换 :871-912 主体):

```go
func (macOSUpdatePreparer) Prepare(_ context.Context, request UpdateRequest, packageData []byte, paths Paths) (PreparedUpdate, error) {
	requiredProtocol, err := requiredGuardianProtocol(packageData, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	pkg, err := updatepkg.ExtractMacOSPackage(packageData, runtime.GOARCH)
	if err != nil {
		return nil, err
	}
	if err := pkg.VerifyAssets(runtime.GOOS, runtime.GOARCH); err != nil {
		return nil, err
	}
	if pkg.Release.Version != request.ToVersion {
		return nil, fmt.Errorf("package release version %q does not match to_version %q", pkg.Release.Version, request.ToVersion)
	}
	currentInfo, _, err := runtimedir.Current(runtimedir.Root)
	if err != nil {
		return nil, fmt.Errorf("unified runtime not installed: %w", err)
	}
	if currentInfo.Version != request.FromVersion {
		return nil, fmt.Errorf("runtime current %q does not match from_version %q", currentInfo.Version, request.FromVersion)
	}
	// 屏障外的大 I/O:新版本目录落盘(不 flip,不影响旧 Core)
	if _, err := runtimedir.InstallVersion(runtimedir.Root, runtimedir.Payload{CLI: pkg.CLI, Info: pkg.Release}); err != nil {
		return nil, err
	}
	transactionRoot := filepath.Join(paths.Staging, request.TransactionID)
	if err := os.RemoveAll(transactionRoot); err != nil {
		return nil, fmt.Errorf("reset staging: %w", err)
	}
	appPath := request.AppPath
	if appPath == "" {
		appPath = "/Applications/Bx.app"
	}
	prepared, err := updatepkg.PrepareMacOSInstall(updatepkg.InstallOptions{
		CLIDestination: install.BinPath, // bridge 的家;钉死点保持
		AppDestination: appPath,
		AppUID:         request.AppUID,
		AppGID:         request.AppGID,
		SnapshotDir:    filepath.Join(paths.Snapshots, request.TransactionID),
		StagingDir:     transactionRoot,
	}, updatepkg.MacOSPayload{CLI: pkg.Bridge, Menu: pkg.App}) // CLI 槽位装 bridge 字节
	if err != nil {
		return nil, err
	}
	descriptor, err := buildUpdateRecoveryDescriptorTemplate(request, paths, appPath, requiredProtocol)
	if err != nil {
		_ = prepared.Commit()
		return nil, err
	}
	descriptor.RuntimeRoot = runtimedir.Root
	descriptor.FromRuntimeVersion = request.FromVersion
	descriptor.ToRuntimeVersion = request.ToVersion
	return &preparedMacOSUpdate{
		PreparedInstall:  prepared,
		requiredProtocol: requiredProtocol,
		paths:            paths,
		descriptor:       descriptor,
	}, nil
}
```

- `preparedMacOSUpdate` 方法重写/追加:

```go
func (p *preparedMacOSUpdate) Activate() error {
	if err := p.PreparedInstall.Activate(); err != nil { // bridge + App 原子换
		return err
	}
	return runtimedir.SwitchCurrent(p.descriptor.RuntimeRoot, p.descriptor.ToRuntimeVersion)
}

func (p *preparedMacOSUpdate) Restore() error {
	// 先把 current 指回旧版本(旧版本目录不可变、始终存在),再还原 bridge+App
	if err := runtimedir.SwitchCurrent(p.descriptor.RuntimeRoot, p.descriptor.FromRuntimeVersion); err != nil {
		return err
	}
	return p.PreparedInstall.Restore()
}

func (p *preparedMacOSUpdate) Commit() error {
	if err := p.PreparedInstall.Commit(); err != nil {
		return err
	}
	// 只保留 from/to 两个版本,清 staging/discard 残留;失败不致命
	_ = runtimedir.GC(p.descriptor.RuntimeRoot, []string{p.descriptor.FromRuntimeVersion, p.descriptor.ToRuntimeVersion})
	return nil
}

func (p *preparedMacOSUpdate) CoreExecutable() string {
	return filepath.Join(p.descriptor.RuntimeRoot, p.descriptor.ToRuntimeVersion, "bx")
}

func (p *preparedMacOSUpdate) PreviousCoreExecutable() string {
	return filepath.Join(p.descriptor.RuntimeRoot, p.descriptor.FromRuntimeVersion, "bx")
}
```

- `validateUpdateRecoveryDescriptorStatic` 追加规则:三个新字段要么全空(legacy descriptor)要么全非空;非空时 `FromRuntimeVersion`/`ToRuntimeVersion` 必须匹配 `updateVersionPattern`,且 `requireSystemDestinations` 时 `RuntimeRoot == runtimedir.Root`。
- `recoveredMacOSUpdate`(crash-recovery 侧,:1230 附近):实现新接口两方法(从 descriptor 取值,legacy 空字段 → 返回 "");其 `Restore()` 前置同样先 `SwitchCurrent(FromRuntimeVersion)`(字段非空时)。

- [ ] **Step 1: 写失败测试**(在 update_test.go 追加;fixture 用 Task 1 的 `buildV2Package` 风格 helper 构造包,`runtimedir` 根用 t.TempDir 注入——**注意 Prepare 硬编码 `runtimedir.Root`,为可测性把 preparer 改为带根字段**:`type macOSUpdatePreparer struct{ runtimeRoot string }`,零值时 `Prepare` 内取 `runtimedir.Root`;`NewManager` 默认构造不变):

```go
func TestUnifiedPrepareInstallsVersionWithoutFlip(t *testing.T)
// Prepare 后:<root>/<to>/bx 存在且内容==pkg.CLI;current 仍指 <from>;BinPath staging(.bx-update-<txid>)内容==pkg.Bridge
func TestUnifiedActivateFlipsAndRestoreUnflips(t *testing.T)
// Activate 后 current→to、bridge 文件==Bridge 字节;Restore 后 current→from、bridge 还原
func TestUnifiedPrepareRejectsVersionMismatch(t *testing.T)
// pkg.Release.Version != ToVersion → error;runtime current != FromVersion → error
func TestUnifiedCoreExecutablePaths(t *testing.T)
// CoreExecutable()==<root>/<to>/bx;PreviousCoreExecutable()==<root>/<from>/bx
func TestDescriptorRuntimeFieldsValidated(t *testing.T)
// 三字段半空 → validate 错;全空(legacy)→ 过;全非空+版本合法 → 过
```

(App/CLI destination 用 t.TempDir 下的假路径 + 非 production paths,绕过 `requireSystemDestinations`;fixture 参考既有 update_test.go 的做法。)
- [ ] **Step 2: 跑红**;**Step 3: 实现**(上述代码 + 接口方法补齐所有 PreparedUpdate fake/实现,`grep -rn 'PreparedUpdate' internal/guardian/`)。既有 legacy-shape 的 Prepare 测试改为新包格式 fixture。
- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/guardian/ -count=1 && go vet ./...`。
- [ ] **Step 5: Commit**

```bash
git add internal/guardian/
git commit -m "feat(guardian): 更新事务统一布局化(runtime 版本目录+current flip+bridge 快照)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Manager 接线 — 新 Core 从新版本目录启动,回滚切回

**Files:**
- Modify: `internal/guardian/update.go`(`updatePreparedLocked` :628-638 之间、`rollbackUpdate` :702-744)
- Test: `internal/guardian/update_test.go`(事务级用例,复用既有 Manager+fake 测试骨架)

**Interfaces:**
- Consumes: Task 2 的 `CoreRunner.Executable()/SetExecutable`、Task 3 的 `CoreExecutable()/PreviousCoreExecutable()`。

改动点(保持事件序不变):

1. `updatePreparedLocked`:`prepared.Activate()` 成功之后、`startUpdateCore(ctx, request.ToVersion)` 之前插入:

```go
if next := prepared.CoreExecutable(); next != "" {
	if err := m.runner.SetExecutable(next); err != nil {
		return m.rollbackUpdate(ctx, &transaction, request, prepared, barrierContext, "update_activate_failed")
	}
}
```

2. `rollbackUpdate`:`prepared.Restore()` 成功之后、`startUpdateCore(ctx, request.FromVersion)` 之前插入:

```go
if prev := prepared.PreviousCoreExecutable(); prev != "" {
	if err := m.runner.SetExecutable(prev); err != nil {
		return m.failUpdate(*transaction, request, "update_restore_failed", true, false)
	}
}
```

(`failUpdate` 参数以现场签名为准 :746;语义:回滚中连可执行路径都切不回 → needs_attention 保持屏障,fail-closed。)

- [ ] **Step 1: 写失败测试**(Manager 级,fake runner 记录 SetExecutable 调用序列;fake prepared 返回非空 CoreExecutable/PreviousCoreExecutable):

```go
func TestUpdateSwitchesRunnerExecutableBeforeNewCore(t *testing.T)
// 成功路径:SetExecutable(<to>/bx) 发生在 fake runner.Start 之前;结束后 runner.Executable()==<to>/bx
func TestRollbackSwitchesExecutableBack(t *testing.T)
// 新 Core 健康门失败 → rollback:SetExecutable 调用序 [<to>/bx, <from>/bx];最终 Executable()==<from>/bx
func TestLegacyPreparedSkipsExecutableSwitch(t *testing.T)
// CoreExecutable()=="" → SetExecutable 从未被调用
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — `go test ./internal/guardian/ -count=1 && go vet ./internal/guardian/`。
- [ ] **Step 5: Commit**

```bash
git add internal/guardian/
git commit -m "feat(guardian): 更新事务按版本目录切换 Core 可执行路径(回滚对称)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: crash-recovery 路径接 runtime flip 与可执行切换

**Files:**
- Modify: `internal/guardian/update.go`(`recoverUpdateLocked` :243-、`recoverUpdateRollback` :489-、`recoverCommittedUpdate` :537-、`recoveredMacOSUpdate` :1230 附近)
- Test: `internal/guardian/update_test.go`(recovery 用例追加)

要求(在既有 recovery 逻辑上最小插入,不改相位分派):
1. `recoverUpdateRollback`(相位 `barrier_active|activating|rolling_back`):在调用 `prepared.Restore()` 的路径上,`recoveredMacOSUpdate.Restore()` 已含 `SwitchCurrent(FromRuntimeVersion)`(Task 3);此处补:重启旧 Core 前,若 descriptor `FromRuntimeVersion != ""` → `m.runner.SetExecutable(filepath.Join(RuntimeRoot, FromRuntimeVersion, "bx"))`(失败 → 保持 needs_attention,不放屏障)。
2. `recoverCommittedUpdate`(相位 `committed`):若 descriptor `ToRuntimeVersion != ""` → 幂等 `runtimedir.SwitchCurrent(RuntimeRoot, ToRuntimeVersion)` + 需要重启 Core 时 `SetExecutable(<to>/bx)`。
3. Guardian 崩溃后由 launchd 以**新版本**二进制重启(current 已 flip)执行回滚是合法路径——测试里用注释钉住这个语义(代码无需分辨自身版本)。

- [ ] **Step 1: 写失败测试**(复用既有 recovery 测试骨架——先 `grep -n 'recoverUpdate' internal/guardian/update_test.go` 找现有用例仿写):

```go
func TestRecoveryRollbackRestoresRuntimeCurrentAndExecutable(t *testing.T)
// 构造 activating 相位 journal + 统一 descriptor + current 已指 to:Recover 后 current→from、runner.Executable()==<from>/bx、相位 rolled_back
func TestRecoveryCommittedEnsuresCurrentFlipped(t *testing.T)
// committed 相位 + current 仍指 from(flip 前崩溃不可能到 committed,但防御):Recover 后 current→to
func TestRecoveryLegacyDescriptorUntouchedByRuntimeLogic(t *testing.T)
// 旧 descriptor(runtime 字段空)走原路径,SetExecutable 未被调用
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — `go test ./internal/guardian/ -count=1 && go vet ./...`;`gofumpt -l internal/guardian/` 空。
- [ ] **Step 5: Commit**

```bash
git add internal/guardian/
git commit -m "fix(guardian): 崩溃恢复同步还原 runtime current 与 Core 可执行路径

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Status 三版本可观测(guardian_version / runtime_version)

**Files:**
- Modify: `internal/guardian/types.go`(Status :51-61)、`internal/guardian/localapi.go`(`LocalAPIOptions`、`observableStatus` :106-130)、`internal/guardian/daemon.go`(`startRecoveredDaemon` 的 NewLocalAPI 调用)
- Test: `internal/guardian/localapi_test.go`(追加)

**Interfaces:**
- `Status` 追加:

```go
GuardianVersion string `json:"guardian_version,omitempty"`
RuntimeVersion  string `json:"runtime_version,omitempty"`
```

- `LocalAPIOptions` 追加:

```go
GuardianVersion string
RuntimeVersion  func() string // 每次 status 请求惰性读;nil 安全
```

- `observableStatus` 在现有后处理末尾填充两字段(RuntimeVersion 为 nil 或返回空则 omit)。
- `daemon.go` 接线:

```go
localAPIOptions := LocalAPIOptions{
	OwnerUID:        options.OwnerUID,
	GuardianVersion: version.Version,
	RuntimeVersion: func() string {
		info, _, err := runtimedir.Current(runtimedir.Root)
		if err != nil {
			return ""
		}
		return info.Version
	},
}
```

(找到现 `NewLocalAPI(controller, LocalAPIOptions{OwnerUID: …})` 处替换;`observableStatus` 若不直接收 options,按现函数签名把两个值传进去——以现场为准,保持一处填充。)

- [ ] **Step 1: 写失败测试**:GET /v1/status 走 `httptest` + fake controller(仿既有 localapi 测试),`LocalAPIOptions{GuardianVersion: "9.9.9", RuntimeVersion: func() string { return "8.8.8" }}` → 响应 JSON 含 `"guardian_version":"9.9.9"` 与 `"runtime_version":"8.8.8"`;RuntimeVersion 为 nil → 两字段可缺省不 panic。
- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — `go test ./internal/guardian/ && go vet ./...`;linux/windows 交叉编译(daemon.go 引 runtimedir——它无 tag,可编译)。
- [ ] **Step 5: Commit**

```bash
git add internal/guardian/
git commit -m "feat(guardian): status 暴露 guardian_version 与 runtime_version

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: CLI `bx update` 统一主路径(On→Guardian 事务 / Off→直接升级)

**Files:**
- Modify: `internal/cli/update.go`(删 `unifiedUpdateGuard`;`updateAction` :199-281 分流;新增统一路径函数)
- Modify: `internal/cli/update_package.go`(删 legacy `updateMacOSPackage` 及仅被它引用的 helper——先 `grep -rn 'applyMacOSPackage\|restartMacOSMenu\|replaceMacOSMenuApp\|parseMacOSAppOwner' internal/` 确认无他用)
- Modify: `internal/cli/unifiedlayout_test.go`(删守卫测试,换新用例)
- Modify: `internal/guardian/client.go`(新增 `NewClientWithTimeout`)
- Test: `internal/cli/update_unified_test.go`(新)

**Interfaces:**
- Produces:

```go
// guardian/client.go
func NewClientWithTimeout(socketPath string, timeout time.Duration) *Client
// 与 NewClient 同,但 HTTPClient 超时为给定值(update 事务上限 60s,客户端给 90s 余量)

// cli/update.go
func updateUnifiedMacOS(c *urfavecli.Context, client *http.Client, manifest updatepkg.Manifest, latest string) error
func buildGuardianUpdateRequest(txid, fromVersion string, pkg updatepkg.MacOSPackage, packageSHA, packagePath string) guardian.UpdateRequest // 纯函数,TDD
func newUpdateTransactionID(now int64, random []byte) string // "update-<unix>-<hex>",匹配 updateTransactionIDPattern,纯函数 TDD
func decideUnifiedUpdateRoute(status guardian.Status, statusErr error, guardianLoaded bool) (route string, err error) // 纯函数 TDD:
// statusErr==nil && Protection=="protected" → "guardian"
// statusErr==nil && Protection=="off"       → "direct"
// statusErr==nil && 其他 Protection         → err 指引(starting/recovering→稍后再试;blocked/needs_attention→先 bx doctor)
// statusErr!=nil && guardianLoaded          → err "Guardian 状态不明,先 sudo bx doctor"
// statusErr!=nil && !guardianLoaded         → "direct"
```

- flags 变更:新增隐藏 `--package-file <path>`(本地包文件,跳过下载与 manifest,digest 对文件自算——**仅统一布局**,供真机演练);`--package`/`--app-path`/`--app-owner` 保留 hidden 但 action 改为:统一布局下与默认路径同义(忽略),legacy 布局下直接报错「`--package` 已废弃:legacy 布局请用完整包重装(install.sh)」。
- `updateAction` 分流(替换 :200-202 的守卫调用及 :236-238 的 `--package` 分支):`--check`/`--json --check` 逻辑不动;非 check 且 `unifiedLayoutActive()` → `updateUnifiedMacOS`;legacy 布局 → 原 asset 替换路径原样保留。

`updateUnifiedMacOS` 流程(全部实现,含中文输出):
1. `os.Geteuid()==0` 否则报错「sudo bx update」。
2. 取包字节:`--package-file` → `os.ReadFile`;否则 `updatepkg.FindPackage(manifest, GOOS/GOARCH)` + 下载 + size + `verifyChecksum`(复用 :303-311 现逻辑)。
3. `pkg := updatepkg.ExtractMacOSPackage(data, GOARCH)`;`pkg.VerifyAssets(GOOS, GOARCH)`;目标版本 `target := pkg.Release.Version`;非 `--package-file` 时要求 `target == latest`。
4. 路由:`status, statusErr := guardian.NewClient(guardian.SocketPath).Status(ctx2s)`;`route, err := decideUnifiedUpdateRoute(status, statusErr, install.GuardianActive())`。
5. `route=="direct"`(Off):`fmt.Println("1/2 校验并安装新版本…")`;把 `pkg.App` 写到 `os.MkdirTemp("/var/lib/bx", ".bx-update-app-")` 下重建 `<tmp>/Bx.app`(目录 0755、文件按 `Contents/MacOS/`、`Resources/bx-*` 可执行 0755 其余 0644)→ `install.UnifiedInstall(install.UnifiedInstallOptions{BundlePath: <tmp>/Bx.app, ConfigPath: defaultConfigPath, ConsoleHome/UID/GID: 尽力取(同 appInstallAction,取不到跳过用户位并提示)})` → defer RemoveAll(tmp) → `fmt.Printf("2/2 完成 ✅ bx 已更新到 %s(保护未开启,无网络影响)\n", target)`;`--json` → 输出 `guardian.UpdateResult{FromVersion: 旧 runtime 版本, ToVersion: target, Phase: guardian.PhaseCommitted, CoreActivated: false, RolledBack: false, ProtectionState: "off"}`。
6. `route=="guardian"`(Protected):
   - `from := status.CoreVersion`(空 → 报错「无法确认运行版本,先 bx doctor」);`from == target && !--force` → 「已是最新」返回。
   - `txid := newUpdateTransactionID(time.Now().Unix(), 随机4字节)`;`stagingDir := filepath.Join("/var/lib/bx/update/staging", txid)`;`os.MkdirAll(stagingDir, 0o700)`;`packagePath := filepath.Join(stagingDir, "package.tgz")`;`os.WriteFile(packagePath, data, 0o600)`;defer 尽力 `os.RemoveAll(stagingDir)`。
   - `fmt.Println("1/4 准备:包已验证并暂存")`;`fmt.Println("2/4 更新中:网络可能短暂暂停,bx 将自动重连…")`。
   - `result, err := guardian.NewClientWithTimeout(guardian.SocketPath, 90*time.Second).Update(ctx, buildGuardianUpdateRequest(txid, from, pkg, hex(sha256(data)), packagePath))`。
   - err → 「更新失败:%v;运行 sudo bx status 与 bx doctor 检查保护状态」(exit 非 0)。
   - `--json` → 直接编码 `result` 返回。
   - `result.RolledBack` → `fmt.Printf("3/4 新版本未通过健康检查,已自动回滚\n4/4 完成:保持 %s,保护未降级直连 ✅\n", result.FromVersion)`(返回非 nil error 以给非零退出码,message 简短)。
   - 否则 → `fmt.Printf("3/4 已重连(protection_state=%s)\n4/4 完成 ✅ bx 已更新到 %s\n", result.ProtectionState, result.ToVersion)`。

- [ ] **Step 1: 写失败测试**(纯函数三件套):

```go
func TestNewUpdateTransactionID(t *testing.T) // 匹配 ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$;同输入确定性
func TestDecideUnifiedUpdateRoute(t *testing.T) // 上表 6 分支逐一断言(err 消息含关键词 doctor/稍后)
func TestBuildGuardianUpdateRequest(t *testing.T) // 字段齐且过 guardian.ValidateUpdateRequest;AppPath 留空(用服务端默认)
```

- [ ] **Step 2: 跑红**;**Step 3: 实现 + 删除 legacy**(`unifiedUpdateGuard`、`updateMacOSPackage`、`update_package.go` 内孤儿 helper 与其测试;`TestDarwinUpdateGuardMessage` 删除)。
- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/cli/ ./internal/guardian/ && go vet ./...` + 双平台交叉编译;`gofumpt -l internal/cli internal/guardian` 空。
- [ ] **Step 5: Commit**

```bash
git add internal/cli/ internal/guardian/
git commit -m "feat(cli): 统一布局 bx update(Protected 走 Guardian 事务,Off 直接升级)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: `bx status` 透传更新可观测字段

**Files:**
- Modify: `internal/cli/cli.go`(`clientStatusReport` :202-209、`assembleClientStatusReportWithCore` :4160-4180、`statusAction` 人类输出)
- Test: `internal/cli/cli_test.go`(仿既有 assemble 测试追加)

**Interfaces:**
- `clientStatusReport` 追加(全部 omitempty):

```go
Phase           string `json:"phase,omitempty"`
CoreVersion     string `json:"core_version,omitempty"`
GuardianVersion string `json:"guardian_version,omitempty"`
RuntimeVersion  string `json:"runtime_version,omitempty"`
```

- assemble 处从 `guardian.Status` 原样拷贝四字段。
- `statusAction` 人类输出:phase ∈ {`prepared`,`barrier_active`,`activating`,`rolling_back`} 时加一行 `更新中(<phase>):网络可能短暂暂停,完成后自动恢复`。

- [ ] **Step 1: 写失败测试**:assemble 输入带 `Phase: "activating", CoreVersion: "1.2.3", GuardianVersion: "1.2.2", RuntimeVersion: "1.2.3"` 的 guardian.Status → 输出 JSON 四字段在;Phase 空 → 缺省。
- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿**(`go test ./internal/cli/`)。
- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "feat(cli): status 透传 phase 与三版本字段(更新可观测)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: BxMenu 更新 UX(07-16 文案 + UpdateResult 结果呈现)

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/UpdatePresentation.swift` + `Tests/UpdatePresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/InstallPresentation.swift` + `Tests/InstallPresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(`updateBx` :656-684、菜单 :327-329、`loadState`)
- Modify: 两个测试跑器(`update-presentation` 组补 `InstallPresentation.swift` 若相互引用;按最终依赖定)

**Interfaces(纯逻辑,全部进测试):**

```swift
// UpdatePresentation.swift 追加
struct UpdateResultJSON: Decodable, Equatable {
    let fromVersion: String
    let toVersion: String
    let phase: String
    let coreActivated: Bool
    let rolledBack: Bool
    let protectionState: String
    enum CodingKeys: String, CodingKey {
        case fromVersion = "from_version"
        case toVersion = "to_version"
        case phase
        case coreActivated = "core_activated"
        case rolledBack = "rolled_back"
        case protectionState = "protection_state"
    }
}

enum UpdateOutcome: Equatable {
    case succeeded(to: String)
    case rolledBack(from: String)
    case failed
}

func parseUpdateOutcome(_ logData: Data) -> UpdateOutcome
// 从日志字节里自后向前找第一行可解码为 UpdateResultJSON 的行:
// rolledBack==true → .rolledBack(from: fromVersion)
// phase=="committed" && !rolledBack → .succeeded(to: toVersion)
// 其他/找不到 → .failed

let updateConfirmTitle = "Update bx?"
let updateConfirmMessage = "Internet access may pause briefly. bx will reconnect automatically."
let updateSucceededMessage = "bx is up to date"
let updateRolledBackMessage = "Update couldn't be completed. Previous version restored."
```

```swift
// InstallPresentation.swift:取消统一布局压制 + Updating 呈现
func menuUpdateActionTitle(check: UpdateCheck?) -> String? {   // 删 runtimeInstalled 参数
    updateActionTitle(for: check)
}
func updatingBanner(phase: String?) -> String? {
    guard let phase else { return nil }
    switch phase {
    case "prepared", "barrier_active", "activating", "rolling_back":
        return "Updating bx…"
    default:
        return nil
    }
}
```

main.swift 变更:
1. 菜单构建 :327 与 `updateBx` :657 的 `menuUpdateActionTitle(check:runtimeInstalled:)` 调用改为单参数版。
2. `updateBx()`:确认弹窗改用 `updateConfirmTitle/updateConfirmMessage`,按钮 `Update` / `Not Now`;命令改为 `'\(bxPath)' update --json > \(shellSingleQuoted(logPath)) 2>&1`(去掉 `--package --app-path --app-owner`);`runPrivileged` 返回后读 `logPath` 文件 → `parseUpdateOutcome`:`.succeeded` → 信息弹窗 `updateSucceededMessage` + `refresh()` + `refreshUpdateCheck()`;`.rolledBack` → 弹窗 `updateRolledBackMessage`(信息级,非红);`.failed`(或 runPrivileged false)→ `showFailure("Update Failed", "bx could not complete the update. Run Doctor for details.")`。
3. `BxReport` 增加可选 `phase: String?`(CodingKeys 补 `phase`;Task 8 已让 `bx status --json` 携带);`loadState()` 在判定 connected/warning 前:`if let banner = updatingBanner(phase: report.phase) { state = .warning(banner, version: version); return }`——更新窗口内黄色 "Updating bx…",不显示 Blocked 红。

- [ ] **Step 1: 写失败测试**,按文件归组(编译组约束:UpdatePresentationTests 只编 UpdatePresentation.swift;InstallPresentationTests 编 InstallPresentation.swift + UpdatePresentation.swift):
  - **UpdatePresentationTests**:`parseUpdateOutcome` 四用例(committed 成功行、rolled_back 行、混有普通日志行时取最后 JSON 行、垃圾 → .failed);四个文案常量钉死。
  - **InstallPresentationTests**:`updatingBanner` 四相位 → 非 nil、`committed`/nil → nil;`menuUpdateActionTitle(check:)` 单参数真值表(available+verified → 标题,否则 nil);删除旧的 runtimeInstalled 压制用例。(`updatingBanner` 与单参数 `menuUpdateActionTitle` 都实现于 InstallPresentation.swift。)
- [ ] **Step 2: 登记跑器 → 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — `bash scripts/test-macos-menu.sh && (cd apps/macos/BxMenu && swift build)`。
- [ ] **Step 5: Commit**

```bash
git add apps/macos/BxMenu/ scripts/test-macos-menu.sh
git commit -m "feat(macos): 菜单统一更新 UX(短暂断网提示+结果呈现+Updating 状态)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 10: BxMenu 三版本一致性与 Repair

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/InstallPresentation.swift` + `Tests/InstallPresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`

**Interfaces:**

```swift
func repairActionNeeded(bundleVersion: String?, runtimeVersion: String?, coreVersion: String?, phase: String?) -> Bool
// 更新相位(updatingBanner 非 nil)→ false(事务中的短暂不一致合法)
// runtimeVersion==nil → false(未安装走 Install 流程,不归 Repair)
// 取非 nil 的版本集合两两比较,存在不一致 → true(coreVersion nil = Off,允许只比 bundle vs runtime)
let repairActionTitle = "Repair bx…"
```

main.swift:
1. `BxReport` 增可选 `coreVersion/guardianVersion/runtimeVersion`(CodingKeys `core_version`/`guardian_version`/`runtime_version`)。
2. `loadState()` 中(updating 检查之后、正常分支之前):`repairActionNeeded(bundleVersion: bundleReleaseVersion(), runtimeVersion: unifiedRuntimeVersion(), coreVersion: report?.coreVersion, phase: report?.phase)` 为 true → `state = .warning("Repair Required", version: …)`。
3. `.warning("Repair Required", _)` 的菜单分支添加 `repairActionTitle` 动作(symbol `wrench.and.screwdriver`)→ 复用 `installBx()` 的特权 `app-install` 流程(把 `installBx` 的核心抽成 `runEmbeddedInstaller(confirmTitle:confirmMessage:)`,Install 与 Repair 都调它;Repair 确认文案:`"Repair bx?"` / `"bx will reinstall its components from this app. Your connection settings are kept."`)。注意既有 `passiveStatusRecovery(protectionState:)` 对 `.warning("Repair Required", _)` 的字符串匹配(main.swift 现映射 `needs_attention`)——保持该字符串不变即兼容。

- [ ] **Step 1: 写失败测试**:`repairActionNeeded` 六用例(全一致 false;runtime≠bundle true;core≠runtime true;core nil 且 bundle==runtime false;core nil 且 bundle≠runtime true;phase=activating 时任何不一致均 false);`repairActionTitle` 钉死。
- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — 两个跑器 + `swift build`。
- [ ] **Step 5: Commit**

```bash
git add apps/macos/BxMenu/ scripts/test-macos-menu.sh
git commit -m "feat(macos): 三版本一致性检测与 Repair bx

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 11: 真机更新演练脚本(dry-run 默认)

**Files:**
- Create: `scripts/darwin-unified-update-check.sh`(0755)

行为(风格对齐 `darwin-unified-install-check.sh`):
- **默认 dry-run**:staging 里用 `package-macos-release.sh` 构建 `BX_VERSION=dev2` 完整包(与已装 dev 相邻版本)+ 用 `verify-macos-release.sh` 验证;打印将执行的演练步骤(update→断言→坏包回滚演练)与「不会做」清单;零系统改动,退出 0。
- **`--execute`(root + `--yes`,且要求当前为统一布局 + `bx status --json` 显示 protected)**:
  1. 正向:`bx update --package-file "$STAGE/…/bx-macos-<arch>.tar.gz" --json | tee` → 断言:exit 0、输出 `"phase":"committed"`、`readlink current`==dev2、`/usr/local/bin/bx --version` 含 dev2、状态回 `protected`、`/var/lib/bx/update/receipt.json` outcome==committed、`/var/lib/bx/update/transaction.json` 不存在。
  2. 回滚演练:复制 dev2 包解出的 Bx.app 到临时目录,把 `Contents/Resources/bx-cli` 截断为垃圾字节并**重算 release.json 中 bx-cli 的 sha256**(digest 合法但二进制起不来 → 新 Core 启动失败 → Guardian 自动回滚),版本号改 `dev3`,重打 tar → `bx update --package-file <dev3包> --json` → 断言:输出 `"rolled_back":true`、current 仍 dev2、状态回 `protected`、receipt outcome==rolled_back。
  3. 全程只调用 `bx update`/`bx status`/`bx --version`(不碰 up/down/launchctl);结束打印「如需还原 dev:重跑本脚本用 dev 包正向更新」。
- 打包/断言所需 helper(重算 sha、重打 tar)全部内联 bash(shasum/tar/python3 均可用)。

- [ ] **Step 1: 写脚本**;**Step 2: 本机 dry-run 验证** — `bash scripts/darwin-unified-update-check.sh` 退出 0、零系统改动;`bash -n` 过。
- [ ] **Step 3: Commit**

```bash
git add scripts/darwin-unified-update-check.sh
git commit -m "test(macos): 统一更新与回滚真机演练脚本(默认 dry-run)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 12: 文档 — 更新故事收口

**Files:**
- Modify: `README.md`(macOS 更新段:删「暂不支持就地更新/统一在线更新将在下一阶段提供」,写新行为)
- Modify: `apps/macos/BxMenu/README.md`(菜单 Update 行为)

要点:`sudo bx update`(或菜单 Update bx…)在统一布局下:保护开启时经 Guardian 安全事务——网络可能短暂暂停、DNS 保持 fail-closed、绝不回落直连,新版本未通过健康检查自动回滚到旧版本;保护关闭时直接文件级升级,零网络影响。`bx update --check` 只读。更新覆盖 App+CLI+runtime 三组件,完成后 `bx --version` 与 App 版本一致;不一致时菜单出 `Repair bx…`。与代码逐条核对后再写(尤其 4-phase 输出文案与菜单弹窗文案引用一致)。

另:`bx capabilities` 的输出(grep `capabilities` 在 cli.go 的 action 定位现有文案结构)追加一条更新语义声明:「update:更新期间网络可能短暂暂停,全程 fail-closed 不回落直连,失败自动回滚;agent 不应以 down/up 组合模拟更新」——07-16 规范对 capabilities/MCP 的要求。

- [ ] **Step 1: 改写两个 README 相应段落 + capabilities 文案**;**Step 2: 自查** — `grep -n "update\|更新" README.md` 通读无矛盾;**Step 3: Commit**

```bash
git add README.md apps/macos/BxMenu/README.md
git commit -m "docs(macos): 统一在线更新与 Repair 行为说明

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Manual macOS Acceptance Gate

自动任务全绿后停下,呈给用户在真机执行(合并 Phase 1 的验收一次上机;开发 agent 不代跑任何一步):

1. 先完成 Phase 1 验收(`darwin-unified-install-check.sh --execute --yes` 起步的 8 条矩阵,见 Phase 1 计划)。
2. `sudo bash scripts/darwin-unified-update-check.sh --execute --yes`:
   - 正向更新 dev→dev2:期间在第二个终端连续 `curl -m 2 https://api.ipify.org` 采样——**只允许出现代理出口 IP 或超时,绝不出现真实公网 IP**;完成后出口仍 == VPS。
   - 坏包回滚 dev3:自动回滚、current 保持 dev2、保护恢复 protected、采样同样无直连泄漏。
3. 菜单更新:发布/伪造一个更新可见状态(或直接观察 `Update bx…` 在 available+verified 时出现),点更新 → 弹窗文案为「Internet access may pause briefly…」→ 成功后「bx is up to date」;期间菜单显示黄色 `Updating bx…` 而非红色 Blocked。
4. Repair:手动把 `runtime/current` 指向一个旧版本目录(或改 bundle 版本)制造不一致 → 菜单出 `Repair bx…` → 点击修复后三版本一致、保护状态不受影响(Off 时修复不启动保护)。
5. `bx status --json` 含 `phase`/`core_version`/`guardian_version`/`runtime_version` 且三版本一致。
6. 更新后 `sudo bx down && sudo bx up` 一轮正常(新 Guardian 二进制接管生命周期)。

## Follow-On Plan Boundary

Phase 3(Apple 签名/公证/SMAppService/登录项 API)仍按 2026-07-20 规范排期,不在本计划。本计划完成后,统一 App 规范的 Phase 1+2 承诺(产品承诺 1-10、更新事务 6 条)全部落地,仅签名信任链留待 Phase 3。
