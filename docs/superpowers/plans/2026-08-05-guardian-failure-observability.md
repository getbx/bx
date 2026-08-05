# Guardian 故障可观测性与启动韧性 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 Guardian 的失败能被诊断——失败原因必须同时出现在日志和调用方返回里;并消除一类会卡死 `bx up` 的陈旧运行时状态。

**Architecture:** 四处独立修复,共享一条原则——**失败必须留下可操作的线索**。前两项修可观测性(日志 + 错误码回传),第三项修陈旧 core 记录导致的启动失败,第四项修 root 向用户 GUI 域 bootstrap 必然失败的问题。

**Tech Stack:** Go 1.26,既有 `internal/guardian` 控制面与 `internal/cli` macOS 生命周期。

## 事故背景(修复依据)

2026-08-05 真机验证时 `sudo bx up` 失败,用户只看到:

```
Guardian /v1/up returned 500: guardian operation failed
```

排查耗时四轮才定位真因(`/var/lib/bx/core-process.json` 是 8/04 旧事故遗留、指向已死的 PID 5129,
而 `bx uninstall` 保留 `/var/lib/bx` 使它活过卸载重装)。期间发现:

- `/var/log/bx-guard.err.log` 对本次失败**一个字都没写**(最后记录停在 8/04 19:05)
- bx 自动归档的诊断包里收集到的也是**陈旧日志**
- Guardian 内部其实**已经知道**失败码(`needsAttention` 把它写进 `status.LastError`),但既不落日志、也不回给调用方

清除该陈旧记录后 `bx up` 立即成功,`DNS Wi-Fi managed`、`传输 reality@166 UDP→hysteria2@166` 全部正常。

同时观察到 `bx up` 稳定打印:
`launchctl bootstrap gui/501 …: exit status 5: Bootstrap failed: 5: Input/output error`
——root 上下文向用户 GUI 域 bootstrap 在 macOS 上必然失败。

## Global Constraints

- **不得削弱 fail-closed**,不得改动数据面(`internal/dialer`/`route`/`tun`)。
- **不得泄漏敏感信息**:回传给调用方的只能是**失败码**(如 `core_ownership_uncertain`),
  绝不能是原始错误串(可能含路径、链接、凭据)。Guardian 日志可记完整错误(该文件 root-only)。
- 纯逻辑测试免 root(假 runner/假 client/`t.TempDir()`),不碰真实路由、DNS、launchctl 或设备。
- **本轮不重新构建、不重装、不重启用户机器上正在运行的 bx。** 实现与验证止于源码层。
- TDD:先写失败测试→跑红→最小实现→跑绿。中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。
- 验证:`go build ./... && go vet ./... && go test ./...`;`go test -race ./internal/guardian ./internal/cli`;
  跨平台 `GOOS=linux/windows GOARCH=amd64 go build -o /dev/null ./...`。

---

### Task 1: Guardian 失败时记录真实原因并回传失败码

**Files:**
- Modify: `internal/guardian/manager.go`(`needsAttention`)
- Modify: `internal/guardian/localapi.go`(`mutationHandler`,两处 500 分支)
- Test: `internal/guardian/localapi_test.go`(追加)、`internal/guardian/manager_test.go`(追加)

**Interfaces:**
- Consumes: 既有 `Controller.Status()`(含 `LastError`)、`writeGuardianJSON`
- Produces: 500 响应体新增 `code` 字段(值为 `status.LastError`,可能为空)

**根因:** `mutationHandler`(`localapi.go:180`、`:254`)把 `err` 整个丢弃,只回固定文案
`{"error":"guardian operation failed"}`;`needsAttention`(`manager.go`)把失败码写进
`status.LastError` 后**从不记日志**。于是失败码在进程内存在,却既不落盘也不外传。

- [ ] **Step 1: 写失败测试**

在 `internal/guardian/manager_test.go` 追加(复用同包既有的 Manager 构造辅助):

```go
// 失败码只存在内存里等于没有——排查时既看不到日志也拿不到返回。
func TestNeedsAttentionLogsTheFailureCode(t *testing.T) {
	var buf bytes.Buffer
	restore := swapGuardianLogOutput(&buf)
	defer restore()

	m := newTestManagerForStatus(t) // 复用同包既有辅助;没有则实现最小版本
	m.needsAttention(DesiredOn, "core_ownership_uncertain")

	logged := buf.String()
	if !strings.Contains(logged, "core_ownership_uncertain") {
		t.Errorf("失败码必须写进日志,实际日志:%q", logged)
	}
	if m.Status().LastError != "core_ownership_uncertain" {
		t.Error("失败码仍应保留在 status.LastError")
	}
}
```

`swapGuardianLogOutput` 是本测试需要的小辅助:替换 `log` 的输出目标并返回还原函数。
若同包已有等价物则复用。

在 `internal/guardian/localapi_test.go` 追加:

```go
// 调用方只看到 "guardian operation failed" 时无从下手;必须回传失败码。
func TestMutationHandlerReturnsFailureCodeAndLogs(t *testing.T) {
	var buf bytes.Buffer
	restore := swapGuardianLogOutput(&buf)
	defer restore()

	controller := &fakeController{status: Status{LastError: "core_ownership_uncertain"}}
	handler := mutationHandler(controller,
		func(context.Context) error { return errors.New("inspect recorded Core PID 5129: boom") },
		newAcceptedMutations())

	rec := httptest.NewRecorder()
	handler(rec, rootMutationRequest(t)) // 复用同包既有的带 root peer 凭据的请求构造

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("状态码 = %d, want 500", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["code"] != "core_ownership_uncertain" {
		t.Errorf("响应必须带失败码,实际 body = %v", body)
	}
	// 原始错误可能含路径/链接/凭据,绝不能回传给调用方。
	if strings.Contains(rec.Body.String(), "boom") ||
		strings.Contains(rec.Body.String(), "5129") {
		t.Errorf("响应不得包含原始错误内容,实际 = %s", rec.Body.String())
	}
	// 但 Guardian 自己的日志(root-only)必须记全,否则排查无据。
	if !strings.Contains(buf.String(), "boom") {
		t.Errorf("Guardian 日志必须记录完整错误,实际 = %q", buf.String())
	}
}
```

`fakeController`、`newAcceptedMutations`、`rootMutationRequest` 若同包已有等价物则复用;
没有就在测试文件内实现最小版本(注意 `mutationHandler` 要求 peer uid==0)。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/guardian/ -run 'TestNeedsAttentionLogs|TestMutationHandlerReturnsFailureCode' -count=1`
Expected: FAIL(日志为空 / 响应无 `code` 字段)

- [ ] **Step 3: 实现**

`needsAttention` 末尾加日志(沿用同包既有的 `log.Printf` 惯例,参考 `path_recovery.go:772`):

```go
	status.LastError = code
	m.setStatus(status)
	// 失败码只存在内存里等于没有:真机事故中 Guardian 已经知道原因,
	// 却既不落日志也不外传,排查耗时四轮。
	log.Printf("guardian_needs_attention code=%s desired=%s protection=%s", code, desired, status.Protection)
```

`mutationHandler` 的两处 500 分支改为:记录完整错误到 Guardian 日志、只把失败码回给调用方:

```go
		if err := mutate(mutationCtx); err != nil {
			// 完整错误只进 root-only 的 Guardian 日志;响应只带失败码,
			// 避免把路径/链接/凭据经 socket 外传。
			log.Printf("guardian_mutation_failed err=%v", err)
			writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{
				"error": "guardian operation failed",
				"code":  controller.Status().LastError,
			})
			return
		}
```

`:254` 那处(update)同样处理,注意它的 controller 变量名可能不同,请按实际改。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/guardian/ -run 'TestNeedsAttentionLogs|TestMutationHandlerReturnsFailureCode' -count=1
go test ./internal/guardian/ -count=1
```
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add internal/guardian/manager.go internal/guardian/localapi.go internal/guardian/*_test.go
git commit -m "fix(guardian): 失败时记录真实原因并回传失败码"
```

---

### Task 2: CLI 展示 Guardian 回传的失败码

**Files:**
- Modify: `internal/cli/guardian.go`(解析 Guardian 错误响应处)
- Test: `internal/cli/guardian_test.go` 或 `macos_lifecycle_test.go`(追加)

**Interfaces:**
- Consumes: Task 1 新增的响应 `code` 字段
- Produces: 无新导出接口

**根因:** 用户看到的是 `Guardian /v1/up returned 500: guardian operation failed`,
没有任何可操作信息。Task 1 已让 Guardian 回传 `code`,本任务让 CLI 用起来。

- [ ] **Step 1: 写失败测试**

先在 `internal/cli/` 下找到解析 Guardian 错误响应的位置(搜索 `returned %d` 或
`guardian operation failed` 的构造处),复用其既有测试脚手架。测试要表达:

```go
// 用户看到 "guardian operation failed" 时无从下手;必须把失败码和下一步给出来。
func TestGuardianErrorSurfacesFailureCode(t *testing.T) {
	err := guardianHTTPError(http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed","code":"core_ownership_uncertain"}`))

	msg := err.Error()
	if !strings.Contains(msg, "core_ownership_uncertain") {
		t.Errorf("必须展示失败码,实际:%s", msg)
	}
	if !strings.Contains(msg, "bx logs") && !strings.Contains(msg, "bx doctor") {
		t.Errorf("必须给出下一步排查动作,实际:%s", msg)
	}
}

// 旧版 Guardian 不回传 code 时不得崩溃或输出空码。
func TestGuardianErrorWithoutCodeStaysReadable(t *testing.T) {
	err := guardianHTTPError(http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed"}`))
	if strings.Contains(err.Error(), "code=") {
		t.Errorf("无 code 时不应输出空码,实际:%s", err.Error())
	}
}
```

函数名 `guardianHTTPError` 是示意——请按仓库实际的错误构造函数命名与签名对齐。
若现有实现没有这样一个可测的函数,先把解析逻辑抽成纯函数再测。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/cli/ -run TestGuardianError -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**

解析响应体的 `code` 字段;非空时把它并入错误信息,并附一条可操作提示,例如:

```
Guardian 操作失败(code=core_ownership_uncertain)。
排查:sudo bx doctor;详细日志 sudo bx logs
```

无 `code` 时保持原有文案,不要输出 `code=` 空值。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/cli/ -run TestGuardianError -count=1
go test ./internal/cli/ -count=1
```

- [ ] **Step 5: 提交**

```bash
git add internal/cli/
git commit -m "fix(cli): 展示 Guardian 失败码与排查指引"
```

---

### Task 3: 陈旧 core 记录不再卡死 `bx up`

**Files:**
- Modify: `internal/guardian/process.go`(`ExecCoreRunner.Existing`,约 212 行起)
- Modify: `internal/cli/uninstall_plan.go`(运行时状态与配置数据分离)
- Test: `internal/guardian/process_test.go`、`internal/cli/uninstall_plan_test.go`(追加)

**Interfaces:**
- Consumes: 既有 `loadProcessRecord`、`removeRecordIfGeneration`、`ErrProcessNotRunning`
- Produces: 无新导出接口

**根因(两层):**

1. `Existing()` 发现记录里的 PID 已死时会尝试清除该记录,**清除失败则直接判定
   `uncertainOwnership`** → `core_ownership_uncertain` → `bx up` 失败。真机事故中
   记录停留在磁盘上且 up 失败,与该分支吻合。进程都已经死了,清不掉一个陈旧文件
   不应该等价于"所有权不确定"——那是给"进程还在但身份存疑"准备的语义。
2. `bx uninstall` 保留 `/var/lib/bx`(为了保住用户配置),但该目录同时存放**运行时状态**
   (`core-process.json`、`guardian-state.json`)。于是陈旧运行时状态活过卸载重装。
   两类数据不该同命运。

- [ ] **Step 1: 写失败测试**

`internal/guardian/process_test.go` 追加(复用同包既有的假 operations/临时 statePath 脚手架):

```go
// 进程已死却清不掉陈旧记录时,不应等价于"所有权不确定"——那会卡死 bx up。
// 真机事故:core-process.json 指向早已死亡的 PID 5129,导致 up 持续失败。
func TestExistingTreatsUnremovableDeadRecordAsNoCore(t *testing.T) {
	runner := newTestRunnerWithRecord(t, processRecord{
		PID: 5129, State: processRecordOwned, Generation: "darwin:1785895536:393862",
	})
	runner.ops.inspectErr = ErrProcessNotRunning
	runner.removeErr = errors.New("remove record: permission denied")

	process, err := runner.Existing(context.Background())
	if err != nil {
		t.Fatalf("死进程的陈旧记录不应让 Existing 报错: %v", err)
	}
	if process.PID != 0 || process.Uncertain {
		t.Errorf("应视为无既有 Core,实际 = %+v", process)
	}
}
```

`internal/cli/uninstall_plan_test.go` 追加:

```go
// uninstall 保留 /var/lib/bx 是为了保住用户配置,但运行时状态不该跟着活下来——
// 陈旧的 core-process.json 会让重装后的 bx up 失败。
func TestUninstallPlanRemovesRuntimeStateButKeepsConfig(t *testing.T) {
	plan := buildDarwinUninstallPlan(501, "/Users/test", true)

	joined := strings.Join(plan.RemovePaths, " ")
	for _, runtime := range []string{"core-process.json", "guardian-state.json"} {
		if !strings.Contains(joined, runtime) {
			t.Errorf("运行时状态必须清除:%s;RemovePaths = %v", runtime, plan.RemovePaths)
		}
	}
	keep := strings.Join(plan.KeepPaths, " ")
	if !strings.Contains(keep, "/etc/bx") {
		t.Errorf("用户配置必须保留,KeepPaths = %v", plan.KeepPaths)
	}
	for _, p := range plan.RemovePaths {
		if p == "/etc/bx" || p == "/var/lib/bx" {
			t.Errorf("不得整目录删除配置数据:%s", p)
		}
	}
}
```

请先读 `buildDarwinUninstallPlan` 的实际签名与 `RemovePaths`/`KeepPaths` 语义再对齐测试。

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./internal/guardian/ -run TestExistingTreatsUnremovableDeadRecord -count=1
go test ./internal/cli/ -run TestUninstallPlanRemovesRuntimeState -count=1
```
Expected: 均 FAIL

- [ ] **Step 3: 实现**

`Existing()` 中,清除失败的分支改为:记日志 + 当作无既有 Core 继续,不再 `uncertainOwnership`:

```go
		if errors.Is(err, ErrProcessNotRunning) {
			if clearErr := r.removeRecordIfGeneration(record.PID, record.Generation); clearErr != nil {
				// 进程已经死了。清不掉一个陈旧文件不等于"所有权不确定"——
				// 后者是给"进程还在但身份存疑"准备的。把它当成不确定会卡死 bx up。
				log.Printf("guardian_stale_core_record pid=%d clear_failed=%v", record.PID, clearErr)
			}
			return Process{}, nil
		}
```

`buildDarwinUninstallPlan` 把运行时状态文件加入 `RemovePaths`(逐个文件,**不要**整目录删
`/var/lib/bx`,那会连同 brook/sing-box/china 列表一起删掉):至少
`/var/lib/bx/core-process.json` 与 `/var/lib/bx/guardian-state.json`。

- [ ] **Step 4: 跑测试确认通过**

```bash
go test ./internal/guardian/ ./internal/cli/ -count=1
```

- [ ] **Step 5: 提交**

```bash
git add internal/guardian/process.go internal/cli/uninstall_plan.go internal/guardian/*_test.go internal/cli/*_test.go
git commit -m "fix(guardian): 陈旧 core 记录不再卡死 bx up,卸载清运行时状态"
```

---

### Task 4: 菜单栏 bootstrap 改用 `launchctl asuser`

**Files:**
- Modify: `internal/cli/` 中执行 `launchctl bootstrap gui/<uid>` 的位置(搜索 `bootstrap gui/`)
- Test: 同目录测试文件(追加,纯命令构造断言)

**根因:** `bx up` 以 root 运行,直接 `launchctl bootstrap gui/501 …` 在 macOS 上必然
返回 `EIO(5): Input/output error`——root 上下文没有用户 GUI 域的 session。真机每次
`bx up` 都会打印该错误。正确做法是 `launchctl asuser <uid> launchctl bootstrap gui/<uid> …`。

- [ ] **Step 1: 写失败测试**

先定位命令构造处并把它抽成纯函数(若尚未),再测:

```go
// root 直接 bootstrap 用户 GUI 域在 macOS 上必然 EIO;必须经 asuser 进入用户上下文。
func TestMenuBootstrapUsesAsuserWhenRoot(t *testing.T) {
	args := menuBootstrapCommand(501, "/Users/test/Library/LaunchAgents/com.getbx.bx.menu.plist")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "asuser 501") {
		t.Errorf("必须经 launchctl asuser 进入用户上下文,实际:%s", joined)
	}
	if !strings.Contains(joined, "bootstrap gui/501") {
		t.Errorf("仍应 bootstrap 到用户 GUI 域,实际:%s", joined)
	}
}
```

函数名按实际命名对齐。

- [ ] **Step 2: 跑测试确认失败** → **Step 3: 实现** → **Step 4: 跑测试确认通过**

实现即把命令改为 `launchctl asuser <uid> launchctl bootstrap gui/<uid> <plist>`。
失败时的提示文案保持既有的降级语义(保护已起、仅菜单栏未起),并补一句用户可自行执行的命令。

- [ ] **Step 5: 提交**

```bash
git commit -m "fix(cli): 菜单栏 bootstrap 经 launchctl asuser 进入用户上下文"
```

---

### Task 5: 文档与全量验证

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 记录不变量**

在 macOS 段落补记本轮确立的原则:**失败必须留下可操作线索**——Guardian 失败码同时
落日志与回传(完整错误只进 root-only 日志,响应只带码);陈旧 core 记录不再等价于
所有权不确定;卸载清运行时状态但保配置;root 经 `launchctl asuser` 进用户 GUI 域。
注明依据是 2026-08-05 真机事故(`bx up` 500、排查四轮、Guardian 零日志)。

- [ ] **Step 2: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/guardian ./internal/cli -count=1
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...
scripts/test-macos-menu.sh
git diff --check
```
Expected: 全部 rc=0

- [ ] **Step 3: 提交**

```bash
git add CLAUDE.md
git commit -m "docs: 记录 Guardian 故障可观测性不变量"
```

- [ ] **Step 4: 停在重建之前**

**不要重新构建、不要重装、不要重启用户机器上正在运行的 bx**——用户明确要求保持现有
dev 构建运行。向用户报告自动化验证结果,并说明下次重建后可验证的点:

```bash
# 重建安装后,制造一次 up 失败(例如塞一个指向死 PID 的 core-process.json),
# 确认现在能直接看到失败码而不是 "guardian operation failed"
sudo bx up                     # 期望:错误信息含 code=... 与排查指引
sudo tail -5 /var/log/bx-guard.err.log   # 期望:有 guardian_needs_attention / guardian_mutation_failed 记录

# 确认菜单栏不再报 EIO
sudo bx up                     # 期望:无 "Bootstrap failed: 5" 字样
```
