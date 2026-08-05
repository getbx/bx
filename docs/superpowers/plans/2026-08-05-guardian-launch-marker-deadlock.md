# Guardian 启动标记死锁与日志权限收尾 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 消除最后一类会让 `bx up` 永久失败的陈旧运行时状态,并让 Guardian 日志的 0600 权限对既有安装也真正生效。

**Architecture:** 两处独立修复。第一处把「Guardian 崩在写标记与保存记录之间」留下的孤儿 `launching` 标记纳入既有的「陈旧记录自愈」语义;第二处让 Guardian 自己在启动时复述日志权限,不再只依赖安装期。

**Tech Stack:** Go 1.26,既有 `internal/guardian` 进程记录与 `internal/install` 安装层。

## 背景

上一轮(`f141cca..947a56a`)修好了「`owned` 记录 + 已死 PID」这一类卡死。最终复审指出**同一事故形态还有一个未覆盖的分支**:

`processRecordLaunching` 标记在 `process.go:142` 于 spawn **之前**落盘、PID 恒为 0,只在本进程内由 `clearLaunchMarker` 清理。若 Guardian 在「写完标记、还没保存 owned 记录」之间被 SIGKILL 或断电,标记会**永久残留**:

- `Existing()`(`process.go:264`)判 `record.state() != processRecordOwned` → `uncertainOwnership`
- `Start()` 的 `refuseLiveLaunchMarker` 也因 PID≤0 拒绝

→ `bx up` 永久 500 `core_ownership_uncertain`,**只能手删文件或 `bx uninstall`**——与用户 2026-08-05 实际踩到的形态一模一样。

另:`SecureGuardianLogs()` 目前只在 `WriteGuardianUnit` 里调用,而 `bx up` 仅在 `!guardianInstalled()` 时才走该路径。因此**既有安装的日志仍是 0644**(真机实测,内含服务器 IP 与 116 条 bypass 网段),日志被删后 launchd 重建也是 0644。

## Global Constraints

- **不得削弱 fail-closed**。放宽只允许针对「结构上不可能对应任何活进程」的记录;「进程还在但身份存疑」必须继续被拦住。
- 不得改动数据面(`internal/dialer`/`route`/`tun`)。
- 纯逻辑测试免 root(假 operations、`t.TempDir()`),不碰真实 `/var/lib/bx` 或 `/var/log` 文件。
- **本轮不重建、不重装、不重启用户机器上正在运行的 bx 与菜单栏。**
- TDD:先写失败测试→跑红→最小实现→跑绿。中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。
- **文档不得把推断写成已验证。**
- 验证:`go build ./... && go vet ./... && go test ./...`;`go test -race ./internal/guardian ./internal/cli ./internal/install`;跨平台 `GOOS=linux/windows GOARCH=amd64 go build -o /dev/null ./...`。

---

### Task 1: 孤儿 launching 标记不再永久卡死

**Files:**
- Modify: `internal/guardian/process.go`(`Existing()` 约 264 行;`refuseLiveLaunchMarker` 约 198-224 行)
- Test: `internal/guardian/process_test.go`、`internal/guardian/manager_test.go`(追加)

**Interfaces:**
- Consumes: 既有 `processRecord`、`processRecordLaunching`、`clearLaunchMarker`、`uncertainOwnership`
- Produces: 无新导出接口

**关键判据:** `launching` 标记的 `PID` 恒为 0(`process.go:142` 写入 `processRecord{State: processRecordLaunching}`,不带 PID)。**PID==0 意味着结构上不可能对应任何活进程**——没有任何进程可以被"拥有"。因此把它当作陈旧标记清理,不削弱 fail-closed;而 `PID>0` 的记录一律沿用既有语义,不动。

**必须保持拦住的场景(务必在测试中断言):**
- `owned` + 进程活着且身份匹配 → 正常返回该进程(既有行为)
- `owned` + 进程活着但身份不匹配 → 仍 `uncertainOwnership`
- `launching` 但 `PID>0`(理论上不该出现,防御性) → 仍 `uncertainOwnership`

- [ ] **Step 1: 写失败测试**

先阅读 `internal/guardian/process_test.go` 既有脚手架(如 `newRecordedProcessRunner`)并复用。测试要表达:

```go
// Guardian 崩在"写完 launching 标记、还没保存 owned 记录"之间会留下 PID=0 的孤儿标记。
// PID=0 结构上不可能对应任何活进程,不该让 bx up 永久失败——用户实际踩到的正是这个形态。
func TestExistingTreatsOrphanLaunchMarkerAsNoCore(t *testing.T) {
	runner := newRecordedProcessRunner(t) // 复用既有辅助;函数名按实际对齐
	writeRecord(t, runner, processRecord{State: processRecordLaunching}) // PID 为 0

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatalf("孤儿 launching 标记不应让 Existing 报错: %v", err)
	}
	if process.PID != 0 || process.Uncertain {
		t.Errorf("应视为无既有 Core,实际 = %+v", process)
	}
}

// 防御性:launching 标记若带非零 PID(不该出现),仍按不确定处理,不放宽。
func TestExistingStillRefusesLaunchMarkerWithPID(t *testing.T) {
	runner := newRecordedProcessRunner(t)
	writeRecord(t, runner, processRecord{State: processRecordLaunching, PID: 4242})

	if _, err := runner.Existing(context.Background()); !errors.Is(err, ErrProcessOwnershipUncertain) {
		t.Errorf("带 PID 的 launching 标记必须仍判不确定,实际 err = %v", err)
	}
}

// 清除失败也不该卡死——与上一轮"已死 PID 记录"的处理保持一致。
func TestExistingToleratesUnremovableOrphanLaunchMarker(t *testing.T) {
	runner := newRecordedProcessRunner(t)
	writeRecord(t, runner, processRecord{State: processRecordLaunching})
	runner.RemoveProcessRecord = func() error { return errors.New("permission denied") } // 按实际注入方式对齐

	process, err := runner.Existing(context.Background())
	if err != nil || process.Uncertain {
		t.Errorf("清不掉孤儿标记也应视为无 Core,实际 err=%v process=%+v", err, process)
	}
}
```

再在 `manager_test.go` 补一条 **Manager 级端到端**回归(参照上一轮新增的
`TestManagerUpStartsCoreDespiteUnremovableDeadCoreRecord` 的写法):存在孤儿
`launching` 标记时,`Up()` 应能成功起 Core,而不是返回 `core_ownership_uncertain`。
**只覆盖 `Existing()` 一跳是假绿——上一轮就栽在这里。**

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run 'TestExisting.*LaunchMarker|TestManagerUp.*LaunchMarker' -count=1`
Expected: FAIL,错误含 `durable launch marker has no accepted process record` 或 `already exists`

- [ ] **Step 3: 实现**

`Existing()` 中把「非 owned 记录」这一支按 PID 分流:PID==0 视为孤儿标记 → 尝试清除(失败只记日志)→ 返回无 Core;PID!=0 维持既有 `uncertainOwnership`。

`refuseLiveLaunchMarker` 同样:PID==0 的标记允许覆写(否则 `Existing()` 放行后 `Start()` 仍会拒,重蹈上一轮覆辙);PID>0 的分支不动。

日志沿用既有惯例,例如:

```go
	log.Printf("guardian_orphan_launch_marker cleared=%t err=%v", clearErr == nil, clearErr)
```

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/guardian/ -count=1
go test -race ./internal/guardian -count=1
```

- [ ] **Step 5: 提交**

```bash
git add internal/guardian/process.go internal/guardian/process_test.go internal/guardian/manager_test.go
git commit -m "fix(guardian): 孤儿 launching 标记不再让 bx up 永久失败"
```

---

### Task 2: Guardian 启动时复述日志权限

**Files:**
- Modify: `internal/guardian/daemon.go`(daemon 启动路径)或其调用方——**以代码实际为准**
- Modify: `internal/cli/cli.go` 约 4697 行注释(把「0600 root-only」的既成事实表述改为与实际一致)
- Test: 相应包的测试文件

**Interfaces:**
- Consumes: `install.SecureGuardianLogs()`(darwin);非 darwin 需有 no-op 或条件编译处理
- Produces: 无新导出接口

**根因:** `SecureGuardianLogs()` 只在 `WriteGuardianUnit` 里调用,而 `bx up` 仅在
`!guardianInstalled()` 时才走该路径。因此**既有安装的日志仍是 0644**(真机实测),
且日志被删除后 launchd 会以 0644 重建,此后再无人收紧。

Guardian daemon 本身以 root 运行,是复述该权限的自然位置。

**注意跨平台**:`SecureGuardianLogs` 是 darwin-only。接线时不要破坏 linux/windows 构建——
参照 `internal/install` 既有的 `_darwin`/`_other` 分文件惯例。

- [ ] **Step 1: 写失败测试**

测试应验证「daemon 启动路径会调用日志加固」这一接线(用可注入的假函数断言被调用),
而**不是**去改真实 `/var/log` 文件。先读实际 daemon 启动代码决定注入点;若当前不可注入,
先做最小重构使其可测。

- [ ] **Step 2: 跑测试确认失败** → **Step 3: 实现** → **Step 4: 跑测试确认通过**

实现:在 daemon 启动早期调用 `install.SecureGuardianLogs()`,失败**只记日志不中断启动**
(权限收紧失败不应让保护起不来)。

同时把 `internal/cli/cli.go` 约 4697 行注释里「Guardian 日志(0600 root-only)」的表述
改为与实际一致(例如注明「安装/启动时收紧为 0600;此前的安装可能仍是 0644」)。

- [ ] **Step 5: 提交**

```bash
git commit -m "fix(guardian): 启动时复述日志权限,不再只依赖安装期"
```

---

### Task 3: 文档与全量验证

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 更新文档**

在 macOS 段落如实补记:

- 孤儿 `launching` 标记(PID=0)现按陈旧记录自愈;`PID>0` 的记录与「进程活着但身份不匹配」
  仍 fail-closed。**注明由 Manager 级端到端回归覆盖,真机未验。**
- Guardian 启动时会复述日志 0600;**注明 launchd 不 chmod 已存在文件这一前提仍未真机复验**
  (整个日志权限修复架在该假设上)。
- 诊断包会整份收录 Guardian 日志(含服务器 IP 与 bypass 网段),而包内其余部分是脱敏的
  ——这是有意取舍,分享诊断包前需知情。

- [ ] **Step 2: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/guardian ./internal/cli ./internal/install -count=1
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...
scripts/test-macos-menu.sh
git diff --check
```
Expected: 全部 rc=0

- [ ] **Step 3: 提交并停在重建之前**

```bash
git add CLAUDE.md
git commit -m "docs: 记录孤儿启动标记自愈与日志权限复述"
```

**不要重建、不要重装、不要重启用户机器上运行中的 bx。** 向用户报告验证结果,并给出
下次重建时的验收点:

```bash
# 孤儿标记自愈
printf '{"state":"launching"}' | sudo tee /var/lib/bx/core-process.json >/dev/null
sudo bx up                      # 期望:成功起来,而不是 core_ownership_uncertain

# 日志权限
ls -l /var/log/bx-guard*.log    # 期望:-rw-------
sudo rm /var/log/bx-guard.err.log && sudo bx restart && ls -l /var/log/bx-guard.err.log
                                # 期望:重建后仍是 -rw-------(验证"启动时复述"生效)
```
