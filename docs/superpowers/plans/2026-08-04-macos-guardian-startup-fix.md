# macOS Guardian 启动阻断修复(自有运行时目录 / 网关延迟发现 / 启动可见性)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 修掉「Guardian 在任何 stock Mac 上都起不来」这一阻断缺陷及其两个放大器——① socket 从共享的 `/var/run` 搬进 bx 自有的 `/var/run/bx/`(root:wheel 0755,fd 锚定校验),让既有加固第一次真正成立;② 默认网关从守护进程启动前提降级为「用时才取」,并让缺网关时的报错说人话;③ `bx up` 能看见 Guardian 死活(等 socket、必要时强制 kickstart、失败时贴守护日志),并对「legacy plist 存在但没在跑」走直接清理而非整套迁移事务。

**Architecture:** 新增 `internal/secdir`(一个跨平台的「安全确保 root 自有目录」原语,darwin 用 `x/sys/unix` fd 锚定 + `O_NOFOLLOW`,移植自 `internal/update/macos_install_darwin.go` 已验证的手法);Guardian 与 supervisor 两个 socket 创建点都改用它并把 darwin 路径搬进 `/var/run/bx/`。网关不再进 `RunDaemon`,改由 Manager 在真正要装屏障时经既有 `gatewayProvider` 惰性取。`bx up` 的生命周期编排加一步 socket 存活探测。**不动**:Guardian 状态机/事件序/屏障语义/kill-switch/数据面;linux 与 windows 的运行时路径(那里没有该缺陷,不做无谓改动)。

**Tech Stack:** Go 1.26(`golang.org/x/sys/unix`)、Swift 5.9(一个常量)、launchd。

## Global Constraints

- **不执行 bx / sudo / launchctl / 网络变更**;实现期只写代码与测试。真机验证由用户在文末验收门执行。
- **既有安全不变量一个不松**:socket 仍 `chmod 0666` + peer-credential 鉴权;`removeStaleSocket` 的 ECONNREFUSED 证明逻辑不动;屏障/健康门/fail-closed 语义不动。本计划只让「父目录不可被他人写」这个前提**由构造成立**,而不是放宽检查。
- **`sun_path` ≤ 104 字节**:新路径 `/var/run/bx/guardian.sock`(25)与 `/var/run/bx/core.sock`(21)均安全;测试里的 `shortSocketDir` 约定保留。
- **仅 darwin 改路径**:`internal/supervisor/paths_linux.go`、`paths_windows.go` 与 `/run/bx.sock` 语义保持原样。
- **版本一致性**:统一安装一次性原子替换 App+CLI+runtime,新旧 socket 路径不会跨版本混用;无需保留旧路径兼容(README 注明)。
- **验证**:`go build ./... && go vet ./... && go test ./...`;`GOOS=linux GOARCH=amd64 go build -o /dev/null ./...` + `GOOS=windows GOARCH=amd64 go build -o /dev/null ./...`;`gofumpt -l <改动目录>` 空;Swift 改动跑 `bash scripts/test-macos-menu.sh` 与 `cd apps/macos/BxMenu && swift build`。
- **提交**:中文 conventional commits,直接 master,结尾 `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`。

---

## File Structure

**创建:**
- `internal/secdir/secdir.go`(无 tag,公共 API + 纯逻辑校验)、`secdir_unix.go`(`//go:build unix`,fd 锚定实现)、`secdir_other.go`(`//go:build !unix`)、`secdir_test.go`。

**修改:**
- `internal/guardian/daemon.go` — `SocketPath` 常量;`prepareSocketDirectory` 改调 `secdir`;`RunDaemon` 删除启动期网关发现。
- `internal/guardian/manager.go` — 屏障上下文取网关惰性化(`contextForRuntime` 附近 :951-956、`recordBarrierAttempt` :898-908)。
- `internal/guardian/barrier.go` — `parseDefaultGateway` 的缺网关错误带上接口名。
- `internal/supervisor/paths_darwin.go` — `SockPath`/`PidPath` 搬进 `/var/run/bx/`。
- `internal/supervisor/control.go:414-416` — 目录创建改 `secdir`。
- `internal/cli/guardian.go` — legacy 未加载快速路径 + Guardian 存活探测(`ensureGuardianOwnership`、`macOSLifecycleDeps`、`defaultMacOSLifecycleDeps`)。
- `internal/install/guardian_darwin.go` — `EnableGuardian` 在「已加载但 socket 不在」时强制 kickstart;新增 `GuardianLogTail`。
- `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift:6` — socket 常量。
- `README.md` — 路径表更新 + 故障排查一段。

---

### Task 1: `internal/secdir` — 安全确保 root 自有运行时目录

**Files:**
- Create: `internal/secdir/secdir.go`、`internal/secdir/secdir_unix.go`、`internal/secdir/secdir_other.go`、`internal/secdir/secdir_test.go`

**Interfaces:**
- Produces:

```go
// Package secdir 确保一个由本进程属主拥有、他人不可写的目录存在——用于存放
// unix socket 等「路径本身即信任边界」的运行期对象。
// darwin/unix 实现全程 fd 锚定(openat + O_NOFOLLOW),父链上的符号链接一律拒绝,
// 因此即便父目录(如 macOS 的 /var/run,出厂 0775 root:daemon)可被组写,
// 也无法通过预置符号链接把我们的目录劫持到别处。
package secdir

// Ensure 确保 path 存在、是真目录、属主为 ownerUID、且 group/other 不可写。
// 已存在则校验并按 mode 归一化权限(不受 umask 影响);不存在则创建。
// mode 必须不含 group/other 写位,否则返回错误。
func Ensure(path string, ownerUID int, mode os.FileMode) error

// ValidateMode 是纯逻辑校验(供调用方与测试复用):mode 不得含 0o022。
func ValidateMode(mode os.FileMode) error
```

`secdir_unix.go` 实现要点(照抄 `internal/update/macos_install_darwin.go` 的手法,不共用其未导出符号):
- 从 `/` 起逐段 `unix.Openat(fd, comp, O_RDONLY|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC, 0)` 打开父链;macOS 上先把 `/var/...` 规范成 `/private/var/...`(否则 `/var` 这个符号链接会被 `O_NOFOLLOW` 顶掉)——这一步与 `openDarwinAbsoluteDir` 同源。
- 末段:先 `unix.Mkdirat(parent, name, uint32(mode.Perm()))`;`EEXIST` 时改为 `openDirAt` 打开;两条路径最后统一 `unix.Fstat` 校验 `S_IFDIR`、`Uid == ownerUID`,并 `unix.Fchmod(fd, mode)` 归一化权限(压掉 umask 与历史遗留的组写位),再复查 `Perm()&0o022 == 0`。
- 非目录(文件/符号链接)占位 → 返回错误,**不**自动删除(留给人工判断)。

`secdir_other.go`(windows):`os.MkdirAll(path, mode)` + `os.Stat` 校验是目录即可(NTFS 无 uid 语义),签名一致。

- [ ] **Step 1: 写失败测试**(`secdir_test.go`,全部 `t.TempDir()` 免 root;`ownerUID` 传 `os.Geteuid()`)

```go
package secdir

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEnsureCreatesWithExactMode(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := Ensure(path, os.Geteuid(), 0o755); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		t.Fatalf("stat: %v dir=%v", err, info.IsDir())
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm = %o want 0755", info.Mode().Perm())
	}
}

func TestEnsureIsIdempotentAndNormalizesPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := os.Mkdir(path, 0o777); err != nil { // 组/其他可写的历史目录
		t.Fatal(err)
	}
	if err := Ensure(path, os.Geteuid(), 0o755); err != nil {
		t.Fatalf("Ensure on existing dir: %v", err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("perm not normalized: %o", info.Mode().Perm())
	}
	if err := Ensure(path, os.Geteuid(), 0o755); err != nil {
		t.Fatalf("second Ensure: %v", err)
	}
}

func TestEnsureSucceedsUnderGroupWritableParent(t *testing.T) {
	// 复刻 macOS /var/run(0775):父目录可被组写,不应妨碍我们创建自有子目录。
	root := t.TempDir()
	parent := filepath.Join(root, "run")
	if err := os.Mkdir(parent, 0o775); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(filepath.Join(parent, "bx"), os.Geteuid(), 0o755); err != nil {
		t.Fatalf("Ensure under group-writable parent: %v", err)
	}
}

func TestEnsureRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "bx")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(link, os.Geteuid(), 0o755); err == nil {
		t.Fatal("symlink placeholder must be rejected")
	}
}

func TestEnsureRejectsNonDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path, os.Geteuid(), 0o755); err == nil {
		t.Fatal("regular file placeholder must be rejected")
	}
}

func TestEnsureRejectsForeignOwner(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "bx")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := Ensure(path, os.Geteuid()+4242, 0o755); err == nil {
		t.Fatal("owner mismatch must be rejected")
	}
}

func TestEnsureRejectsRelativeAndWritableMode(t *testing.T) {
	if err := Ensure("relative/path", os.Geteuid(), 0o755); err == nil {
		t.Fatal("relative path must be rejected")
	}
	if err := Ensure(filepath.Join(t.TempDir(), "bx"), os.Geteuid(), 0o775); err == nil {
		t.Fatal("group-writable mode must be rejected")
	}
	if err := ValidateMode(0o707); err == nil {
		t.Fatal("other-writable mode must be rejected")
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/secdir/`(包不存在,编译失败)。
- [ ] **Step 3: 实现**(`Ensure` 在 unix 上走 fd 锚定;`ValidateMode` 与路径绝对性校验放 `secdir.go` 无 tag 文件,两平台共用)。
- [ ] **Step 4: 跑绿** — `go test ./internal/secdir/ -v && go vet ./internal/secdir/`;两个交叉编译;`gofumpt -l internal/secdir/` 空。
- [ ] **Step 5: Commit**

```bash
git add internal/secdir/
git commit -m "feat(secdir): 安全确保 root 自有运行时目录(fd 锚定,拒符号链接)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Guardian socket 搬进 `/var/run/bx/`

**Files:**
- Modify: `internal/guardian/daemon.go`(`SocketPath` :21、`prepareSocketDirectory` :220-239、`StartDaemon` :84-161 中的调用)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift:6`
- Test: `internal/guardian/localapi_test.go`(既有 `StartDaemon` 用例需适配新的目录校验)

**Interfaces:**
- Consumes: `secdir.Ensure`(Task 1)。
- Produces:

```go
// RuntimeDir 是 bx 自有的运行期目录:socket 等「路径即信任边界」的对象都放这里。
// 不能用 /var/run 本身——macOS 出厂 /var/run 是 0775 root:daemon(组可写),
// 任何「父目录不可被他人写」的加固在那里都不可能成立。
const RuntimeDir = "/var/run/bx"

const SocketPath = RuntimeDir + "/guardian.sock"
```

- `prepareSocketDirectory(path string, ownerUID uint32) error` 内部改为 `secdir.Ensure(path, int(ownerUID), 0o755)`,保留同名函数与错误包裹(`create Guardian socket directory: %w`),使 `StartDaemon` 与既有测试的调用形态不变。
- **既有测试适配**:`localapi_test.go` 用 `t.TempDir()`(0700)与 `shortSocketDir`(`/tmp/bxg-*`)作 socket 父目录,`Ensure` 会把它们归一化成 0755 且校验属主为当前用户——`OwnerUID: uint32(os.Geteuid())` 已满足,预期无需改测试;若 `Ensure` 因 `/tmp` 是符号链接(macOS `/tmp → /private/tmp`)而失败,则在 `secdir` 的路径规范化里一并处理 `/tmp`(与 `/var` 同款 `/private` 前缀规则),**不要**为此放宽校验。
- Swift:`private let guardianSocketPath = "/var/run/bx/guardian.sock"`。

- [ ] **Step 1: 写失败测试**(追加到 `internal/guardian/localapi_test.go`)

```go
func TestGuardianSocketLivesInOwnedRuntimeDir(t *testing.T) {
	if filepath.Dir(SocketPath) != RuntimeDir {
		t.Fatalf("SocketPath %q must live under %q", SocketPath, RuntimeDir)
	}
	if RuntimeDir == "/var/run" {
		t.Fatal("Guardian must not put its socket directly in the shared /var/run")
	}
	if len(SocketPath) >= 104 {
		t.Fatalf("SocketPath %q exceeds sun_path limit", SocketPath)
	}
}

func TestStartDaemonAcceptsGroupWritableParentOfOwnedDir(t *testing.T) {
	// 复刻 macOS:/var/run 组可写,但我们只要求自有子目录本身干净。
	root := t.TempDir()
	shared := filepath.Join(root, "run")
	if err := os.Mkdir(shared, 0o775); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(shared, "bx")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	daemon, err := StartDaemon(ctx, DaemonOptions{
		SocketPath: filepath.Join(owned, "guardian.sock"),
		Handler:    http.NewServeMux(),
		OwnerUID:   uint32(os.Geteuid()),
	})
	if err != nil {
		t.Fatalf("StartDaemon under group-writable grandparent: %v", err)
	}
	defer daemon.Close()
	if _, err := os.Stat(filepath.Join(owned, "guardian.sock")); err != nil {
		t.Fatalf("socket not created: %v", err)
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/guardian/ -run 'OwnedRuntimeDir|GroupWritableParent'`(常量未改 + 旧 `prepareSocketDirectory` 在 0775 祖父目录下…注意:旧实现只看直接父目录,该用例可能已通过;**必须先确认第一条常量断言是红的**,它是本任务的核心)。
- [ ] **Step 3: 实现**(常量、`prepareSocketDirectory` 换 `secdir`、Swift 常量)。全仓 `grep -rn "bx-guard.sock"` 确认只剩文档/历史计划里的引用。
- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/guardian/ ./internal/cli/ && go vet ./...`;两个交叉编译;`bash scripts/test-macos-menu.sh && (cd apps/macos/BxMenu && swift build)`。
- [ ] **Step 5: Commit**

```bash
git add internal/guardian/ apps/macos/BxMenu/
git commit -m "fix(guardian): socket 搬进自有 /var/run/bx(macOS 共享 /var/run 组可写,加固前提不可能成立)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Core 控制 socket 同迁 `/var/run/bx/`(darwin)

**Files:**
- Modify: `internal/supervisor/paths_darwin.go`
- Modify: `internal/supervisor/control.go:414-416`
- Test: `internal/supervisor/control_test.go` 或新增(纯常量断言 + 既有 socket 用例)

**Interfaces:**
- Produces(仅 darwin;linux/windows 不动):

```go
const (
	RuntimeDir = "/var/run/bx"
	SockPath   = RuntimeDir + "/core.sock"
	PidPath    = RuntimeDir + "/core.pid"
)
```

- `control.go` 的 `_ = os.MkdirAll(filepath.Dir(SockPath), 0o755)` 换成 `secdir.Ensure(filepath.Dir(SockPath), os.Geteuid(), 0o755)` 并**检查错误**(失败即返回,socket 起不来本就该 fail:`return nil, fmt.Errorf("准备控制 socket 目录: %w", err)`)。其余(`os.Remove` + `net.Listen` + `chmod 0666`)保持不变——Core 的鉴权模型(peer-cred 门控 mutation)不改。
- 注意 `internal/cli/cli.go:4415`、`preset.go:132/134`、`direct.go:165/167`、`internal/mcp/liveops.go` 多处直接用 `supervisor.SockPath` 常量,改常量即自动跟随;`BX_STATUS_SOCKET` 覆盖语义不变。

- [ ] **Step 1: 写失败测试**

```go
func TestDarwinCoreSocketLivesInOwnedRuntimeDir(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("darwin 专属路径约定")
	}
	if filepath.Dir(SockPath) != RuntimeDir || filepath.Dir(PidPath) != RuntimeDir {
		t.Fatalf("core socket/pid must live under %q: %q %q", RuntimeDir, SockPath, PidPath)
	}
	if len(SockPath) >= 104 {
		t.Fatalf("SockPath %q exceeds sun_path limit", SockPath)
	}
}
```

- [ ] **Step 2: 跑红** — `go test ./internal/supervisor/ -run DarwinCoreSocket`。
- [ ] **Step 3: 实现**;`grep -rn '"/var/run/bx.sock"' --include='*.go' .` 应无残留。
- [ ] **Step 4: 跑绿** — `go build ./... && go test ./internal/supervisor/ ./internal/cli/ ./internal/guardian/ ./internal/mcp/ && go vet ./...` + 两个交叉编译。
- [ ] **Step 5: Commit**

```bash
git add internal/supervisor/
git commit -m "fix(supervisor): darwin 控制 socket 同迁自有运行时目录并校验目录安全

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: 网关从启动前提降级为「用时才取」

**Files:**
- Modify: `internal/guardian/daemon.go`(`RunDaemon` :293-331 删除 `discoverDaemonGateway` 调用与 `BarrierContext.Gateway` 种子)
- Modify: `internal/guardian/daemon_darwin.go` / `daemon_other.go`(删除已无调用方的 `discoverDaemonGateway`)
- Modify: `internal/guardian/manager.go`(屏障上下文取网关惰性化)
- Modify: `internal/guardian/barrier.go`(`parseDefaultGateway` 错误带接口名)
- Test: `internal/guardian/daemon_test.go`、`manager_test.go`、`barrier_test.go`

**Interfaces:**
- `RunDaemon` 中 `BarrierContext{Gateway: gateway, BlockIPv6: true}` → `BarrierContext{BlockIPv6: true}`;`GatewayProvider: GatewayProviderFunc(DiscoverDefaultGateway)` 保留不动(它就是惰性入口)。
- Manager 侧新增(照 `contextForRuntime`(manager.go:951-956)现有形态扩展,实现者先读该函数与 `recordBarrierAttempt` :898-908):

```go
// barrierContextForRuntime 在真正要装屏障时才解析默认网关:守护进程启动、
// 以及一切不装屏障的路径都不该依赖它(另一条 VPN 占着默认路由时网关不存在,
// 但那不该阻止 Guardian 启动或响应 status)。
func (m *Manager) barrierContextForRuntime(ctx context.Context, state supervisor.RuntimeState) (BarrierContext, error)
```
语义:先 `contextForRuntime(state)`;若结果 `Gateway == ""` 且 `len(ServerBypass) > 0`(即这次真要装带 bypass 的屏障),调 `m.gatewayProvider.DefaultGateway(ctx)` 填上,失败则返回错误。`blockOnly` 场景(`blockOnlyRecoveryContext`)不需要网关,保持不碰。把 Up/意外退出重启等会装屏障的调用点从 `contextForRuntime` 切到本函数(实现者 grep `contextForRuntime` 全部调用点逐一判断:装屏障的切,纯读的不切)。
- `parseDefaultGateway`(barrier.go:177-190):扫描时记录 `interface:` 值;走到末尾没有 `gateway:` 时返回

```go
if iface != "" {
	return "", fmt.Errorf("default route via %s has no gateway (point-to-point tunnel: another VPN likely owns the default route)", iface)
}
return "", errors.New("default gateway not found")
```

- [ ] **Step 1: 写失败测试**

```go
// barrier_test.go
func TestParseDefaultGatewayNamesPointToPointInterface(t *testing.T) {
	_, err := parseDefaultGateway([]byte("   route to: default\ndestination: default\n  interface: utun16\n"))
	if err == nil {
		t.Fatal("want error")
	}
	for _, want := range []string{"utun16", "point-to-point"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q missing %q", err, want)
		}
	}
}

// daemon_test.go —— 守护进程启动不得依赖网关
func TestRunDaemonDoesNotDiscoverGatewayAtStartup(t *testing.T) {
	// 纯静态断言:RunDaemon 不再引用 discoverDaemonGateway。
	// (行为层由 manager 测试覆盖;此处防回归。)
	if _, err := os.Stat("daemon_darwin.go"); err == nil {
		data, readErr := os.ReadFile("daemon_darwin.go")
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.Contains(string(data), "discoverDaemonGateway") {
			t.Fatal("discoverDaemonGateway must be gone: gateway is an operation-time dependency")
		}
	}
}

// manager_test.go —— 惰性取网关
func TestBarrierContextForRuntimeResolvesGatewayLazily(t *testing.T) {
	// 用既有 Manager 测试骨架(grep newTestManager/managerFixture)构造:
	// seed BarrierContext{BlockIPv6:true}(无 Gateway)+ fakeGatewayProvider{gateway:"192.168.1.1"}
	// runtime state 带 ServerBypass ["203.0.113.20/32"]
	// 期望:返回的 ctx.Gateway == "192.168.1.1",且 provider 恰好被调用一次。
}

func TestBarrierContextForRuntimeFailsWhenGatewayUnavailable(t *testing.T) {
	// fakeGatewayProvider{err: errors.New("no gateway")} + 非空 ServerBypass
	// 期望:返回错误(fail-closed),不返回空网关的上下文。
}
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — `go build ./... && go test ./internal/guardian/ -count=1 && go test ./... && go vet ./...` + 两个交叉编译。
- [ ] **Step 5: Commit**

```bash
git add internal/guardian/
git commit -m "fix(guardian): 网关改为装屏障时惰性发现,不再阻断守护进程启动

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: legacy plist 存在但未加载 → 直接清理,不走迁移事务

**Files:**
- Modify: `internal/cli/guardian.go`(`macOSLifecycleDeps` :39-50、`defaultMacOSLifecycleDeps` :57-75、`ensureGuardianOwnership` :234-267)
- Test: `internal/cli/macos_lifecycle_test.go`(既有事件序用例需扩展)

**Interfaces:**
- `macOSLifecycleDeps` 新增字段:

```go
removeLegacyUnit func() error // 默认 install.RemoveLegacyCoreUnit(它自身在服务仍加载时会拒绝)
```

- `ensureGuardianOwnership` 逻辑改为:

```go
legacyLoaded, err := deps.legacyLoaded()
...
switch {
case legacyLoaded:
	// 真有活着的 legacy Core:必须走带屏障的迁移事务(需要网关)。
	request, err = deps.migrationRequest(ctx, configPath)
	...
case deps.legacyInstalled():
	// 只剩一份没在跑的 plist:没有流量要接管,直接删掉即可,
	// 不该为此要求默认网关、更不该跑一次迁移事务。
	if err := deps.removeLegacyUnit(); err != nil {
		return guardian.Status{}, false, fmt.Errorf("清理 legacy Core 服务: %w", err)
	}
}
```
随后 `enableGuardian()`;`legacyLoaded` 为真时才 `client.Migrate`,否则走 `client.Up`(即 `migrated=false`)。

- [ ] **Step 1: 写失败测试**(`macos_lifecycle_test.go`,仿既有事件序用例)

```go
func TestMacOSUpLifecycleRemovesOrphanLegacyPlistWithoutMigrating(t *testing.T) {
	// legacyInstalled=true, legacyLoaded=false
	// 期望事件序:legacy.loaded|legacy.remove|guardian.enable|guardian.up|guardian.status|console.uid|menu.ensure
	// 且 migrationRequest 从未被调用(否则会要求网关)。
}

func TestMacOSUpLifecycleStillMigratesWhenLegacyLoaded(t *testing.T) {
	// legacyLoaded=true → 事件序保持既有:legacy.loaded|metadata|guardian.enable|guardian.migrate|...
	// (回归保护:活着的 legacy Core 仍必须走迁移事务)
}
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**(既有 `TestMacOSUpLifecycleMigratesBeforeMenuAndWaitsForProtected` :104-134 与 `TestMacOSUpLifecycleMetadataFailureLeavesGuardianAndLegacyUntouched` :160-180 的 fixture 需把 `legacyLoaded` 显式设为 true 才能保持原语义——按上面第二个新用例的意图同步修正,不要削弱断言);**Step 4: 跑绿** — `go test ./internal/cli/ -count=1 && go build ./... && go vet ./...`。
- [ ] **Step 5: Commit**

```bash
git add internal/cli/
git commit -m "fix(cli): 孤儿 legacy plist 直接清理,不再为它跑需要网关的迁移事务

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: `bx up` 看得见 Guardian 死活

**Files:**
- Modify: `internal/install/guardian_darwin.go`(`EnableGuardian`/`enableGuardianWithControl` :108-129、`guardianEnableCommands` :251-261;新增 `GuardianLogTail`)
- Modify: `internal/install/guardian_other.go`(stub 同步)
- Modify: `internal/cli/guardian.go`(`macOSLifecycleDeps` 加存活探测;`ensureGuardianOwnership` 在 enable 后等待)
- Test: `internal/install/guardian_darwin_test.go`、`internal/cli/macos_lifecycle_test.go`

**Interfaces:**
- Produces:

```go
// install
// EnableGuardianWithProbe 在服务已加载但探测显示未就绪时强制 kickstart 一次,
// 避免「launchd 认为已加载、进程实则在崩溃循环」这种静默失败。
func EnableGuardianWithProbe(ready func() bool) error

// guardianEnableCommands 增加参数:already-loaded 且 !ready 时也要发 kickstart
func guardianEnableCommands(active, ready bool) [][]string

// GuardianLogTail 读 /var/log/bx-guard.err.log 末尾 n 行(读不到返回空串,绝不报错)
func GuardianLogTail(lines int) string

// cli
// waitGuardianSocket 轮询 socket 可连通,超时返回 false。
func waitGuardianSocket(ctx context.Context, socketPath string, timeout, interval time.Duration) bool
```

- `guardianEnableCommands(active, ready bool)`:`active && ready` → nil;`active && !ready` → `[]{{"kickstart", "-k", "system/com.getbx.bx.guard"}}`;`!active` → 原三条。
- `ensureGuardianOwnership`:`deps.enableGuardian()` 之后、任何 `client.*` 调用之前插入

```go
if !deps.guardianReady(ctx) {
	return guardian.Status{}, false, fmt.Errorf(
		"Guardian 服务未能启动(socket %s 未就绪)。最近的守护日志:\n%s\n排查:sudo launchctl print system/com.getbx.bx.guard;完整日志 /var/log/bx-guard.err.log",
		guardian.SocketPath, install.GuardianLogTail(10))
}
```
`macOSLifecycleDeps.guardianReady func(context.Context) bool`,默认实现 = `waitGuardianSocket(ctx, guardian.SocketPath, 10*time.Second, 200*time.Millisecond)`;测试注入。

- [ ] **Step 1: 写失败测试**

```go
// internal/install/guardian_darwin_test.go
func TestGuardianEnableCommandsKickstartsLoadedButNotReady(t *testing.T) {
	if got := guardianEnableCommands(true, true); got != nil {
		t.Fatalf("loaded+ready 应为 no-op,got %v", got)
	}
	got := guardianEnableCommands(true, false)
	want := [][]string{{"kickstart", "-k", "system/com.getbx.bx.guard"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("loaded+not-ready = %v want %v", got, want)
	}
	if len(guardianEnableCommands(false, false)) != 3 {
		t.Fatal("未加载时仍应 enable+bootstrap+kickstart")
	}
}

// internal/cli/macos_lifecycle_test.go
func TestMacOSUpLifecycleFailsWithGuardianLogWhenSocketNeverReady(t *testing.T) {
	// guardianReady 注入 false;期望:返回的 error 含 "Guardian 服务未能启动" 与 socket 路径,
	// 且 client.Up / client.Migrate 从未被调用(不再吐裸 dial 错误)。
}
```

- [ ] **Step 2: 跑红**;**Step 3: 实现**;**Step 4: 跑绿** — `go build ./... && go test ./internal/install/ ./internal/cli/ -count=1 && go vet ./...` + 两个交叉编译。
- [ ] **Step 5: Commit**

```bash
git add internal/install/ internal/cli/
git commit -m "feat(cli): bx up 等待并校验 Guardian 就绪,失败时贴出守护日志

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: 文档

**Files:** `README.md`

- [ ] **Step 1: 改写路径描述** — macOS 段里凡提到运行期 socket 的地方改为 `/var/run/bx/`(guardian.sock / core.sock);在 macOS 安装节后追加一小段「排查:保护起不来时」:
  - `sudo launchctl print system/com.getbx.bx.guard`(看是否 loaded / 退出码)
  - `tail -20 /var/log/bx-guard.err.log`(守护进程自身的拒绝原因)
  - 说明 bx 不会与其它全局 VPN 争抢默认路由:另一条隧道占着默认路由时 `bx up` 会明确报「默认路由由 utunN 占用、无网关」,先关掉对方再试。
- [ ] **Step 2: 自查** — `grep -n "bx-guard.sock\|/var/run/bx.sock" README.md` 无残留。
- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs(macos): 运行时目录改 /var/run/bx 并补保护启动排查

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Manual macOS Acceptance Gate

自动任务全绿后停下,由用户在本机执行(开发 agent 不代跑):

1. **重装以带上修复**:`sudo bash scripts/darwin-unified-install-check.sh --execute --yes`。
2. **关掉 brook**(避免两条全局隧道争默认路由),然后 `sudo bx up`:
   - 期望 Guardian 起来、socket 出现在 `/var/run/bx/guardian.sock`(`ls -l /var/run/bx/`,目录应为 `drwxr-xr-x root wheel`);
   - `bx status` 显示 Protected、出口 == VPS;
   - `tail /var/log/bx-guard.err.log` 不再有 `group/other writable` 或 `default gateway not found` 刷屏;
   - 旧的 `/Library/LaunchDaemons/com.getbx.bx.plist`(孤儿 legacy)应已被自动删除(`ls` 确认)。
3. **故障可见性验证**(可选):`sudo launchctl bootout system/com.getbx.bx.guard` 后立刻 `sudo bx up` —— 期望它自己把服务 kickstart 起来并成功;若人为把 plist 改坏,期望报「Guardian 服务未能启动」并附日志尾巴,而不是裸 dial 错误。
4. **共存回归**:brook 开着时 `sudo bx up` —— 期望明确报「默认路由由 utunN 占用(无网关)」这类可读错误,而不是守护进程崩溃循环。
5. 完成后跑一次 `sudo bash scripts/darwin-unified-update-check.sh --execute --yes` 验证更新/回滚仍正常。
