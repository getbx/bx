# bx MCP agent-control surface — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给 bx 加一个 `bx mcp`(stdio)MCP server,把 setup/verify/diagnose/repair 暴露成 MCP tools,所有改动类操作走 commit-confirmed 死手(默认 240s)永久可回滚。

**Architecture:** 新增两个纯包 —— `internal/confirm`(commit-confirmed 死手状态机 + last-known-good 快照,纯逻辑、免 root)与 `internal/mcp`(MCP server + tool handlers + 错误 taxonomy)。MCP 层只依赖一个在本 plan 中定义的 `Ops` port 接口;`liveOps` 把它绑到现有 `internal/`(setup/supervisor/tunnel/doctor),测试用 `fakeOps`。CLI 加 `bx mcp` 子命令。

**Tech Stack:** Go 1.26.3;`github.com/modelcontextprotocol/go-sdk/mcp`(官方 MCP SDK,纯 Go);`github.com/urfave/cli/v2`(现有)。

## Global Constraints

- 模块路径 `github.com/getbx/bx`,Go 1.26.3,`CGO_ENABLED=0` 静态二进制。
- 验证命令:`go build ./... && go vet ./... && go test ./...`;跨平台 `GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...`。
- TDD:先写失败测试→跑红→最小实现→跑绿→提交。纯逻辑测试免 root(用 `t.TempDir()`,不碰真实路由/设备)。
- 提交信息:中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- 安全不变量不可破:kill-switch fail-closed、私网恒直连、服务器/自身出站防环。MCP 改动类操作出错期间不得漏 IP;灾难级由死手兜底。
- 死手默认窗口 **240s**,可配。
- 边界:bx 给"数据 + 安全机械动作",agent 给"判断 + 编排";bx 不做传输自动选、不设含糊 `bx_repair`。

---

### Task 1: 钉住 MCP SDK + 最小 `bx mcp` server 骨架

**Files:**
- Modify: `go.mod`(加依赖)
- Create: `internal/mcp/server.go`
- Create: `internal/mcp/server_test.go`

**Interfaces:**
- Produces: `mcp.Serve(ctx context.Context, ops Ops) error`(占位:本任务先只注册一个 ping tool);`func newServer(ops Ops) *mcpsdk.Server`(供测试用内存 transport 连接)。
- Consumes: 无(`Ops` 在 Task 5 定义;本任务用空接口占位,见下)。

- [ ] **Step 1: 加依赖并确认体积影响**

Run:
```bash
cd /Users/nategu_mac_company/Documents/bx
go get github.com/modelcontextprotocol/go-sdk/mcp@latest
go build -o /tmp/bx-before ./... 2>/dev/null; ls -l /tmp/bx-before 2>/dev/null || true
```
Expected: `go.mod` 出现 `github.com/modelcontextprotocol/go-sdk`。记录当前 `cmd` 二进制大小作基线(下一步建好 server 后再比)。

- [ ] **Step 2: 写失败测试(内存 transport 调 ping)**

Create `internal/mcp/server_test.go`:
```go
package mcp

import (
	"context"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerPing(t *testing.T) {
	ctx := context.Background()
	srv := newServer(nil)
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "test", Version: "v0"}, nil)

	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: "bx_ping", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call bx_ping: %v", err)
	}
	if res.IsError {
		t.Fatalf("bx_ping returned error result")
	}
}
```

- [ ] **Step 3: 跑红**

Run: `go test ./internal/mcp/ -run TestServerPing -v`
Expected: 编译失败 / `newServer` undefined。

- [ ] **Step 4: 写最小实现**

Create `internal/mcp/server.go`:
```go
// Package mcp 暴露 bx 的 agent 可操作控制面(MCP server over stdio)。
package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// version 由构建期注入也可;先硬编码占位。
const serverVersion = "v0.1.0"

type pingIn struct{}
type pingOut struct {
	OK bool `json:"ok" jsonschema:"always true if the server is alive"`
}

// newServer 构造已注册 tool 的 MCP server(不连 transport,供测试与 Serve 共用)。
func newServer(ops Ops) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "bx", Version: serverVersion}, nil)
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "bx_ping",
		Description: "liveness probe; returns ok=true",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ pingIn) (*mcpsdk.CallToolResult, pingOut, error) {
		return nil, pingOut{OK: true}, nil
	})
	return s
}

// Serve 在 stdio 上运行 MCP server,直到客户端断开。
func Serve(ctx context.Context, ops Ops) error {
	return newServer(ops).Run(ctx, &mcpsdk.StdioTransport{})
}
```

注意:`Ops` 类型本任务尚未定义。为让本任务独立编译,**临时**在文件顶部加 `type Ops interface{}`,并在 Task 5 替换为真实接口。

- [ ] **Step 5: 跑绿 + 体积复核**

Run:
```bash
go test ./internal/mcp/ -run TestServerPing -v
go vet ./internal/mcp/
go build -o /tmp/bx-after ./... && ls -l /tmp/bx-after
```
Expected: 测试 PASS;记录二进制大小相对基线的增量(预期数 MB 级,可接受;若异常膨胀在此暴露)。

- [ ] **Step 6: 提交**

```bash
git add go.mod go.sum internal/mcp/server.go internal/mcp/server_test.go
git commit -m "feat(mcp): 钉住官方 MCP SDK + bx mcp server 骨架(bx_ping)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: 错误 taxonomy

**Files:**
- Create: `internal/mcp/errors.go`
- Create: `internal/mcp/errors_test.go`

**Interfaces:**
- Produces:
  - `type Code string` 及常量 `CodeLinkInvalid`, `CodePrivilegeRequired`, `CodeTunnelUnhealthy`, `CodeLeakDetected`, `CodeLockoutRisk`, `CodeDeadmanReverted`, `CodeAlreadyCommitted`, `CodeNothingToRollback`。
  - `type ToolError struct { Code Code; Message string; Remediation string; Next []string }` 实现 `error`(`Error() string`)。
  - `func errResult(e ToolError) (*mcpsdk.CallToolResult, any, error)` —— 把结构化错误打进 `CallToolResult`(`IsError=true`,内容含 JSON)。

- [ ] **Step 1: 写失败测试**

Create `internal/mcp/errors_test.go`:
```go
package mcp

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolErrorErrorString(t *testing.T) {
	e := ToolError{Code: CodeTunnelUnhealthy, Message: "443 握手超时"}
	if !strings.Contains(e.Error(), "TUNNEL_UNHEALTHY") {
		t.Fatalf("Error() 应含错误码,得到 %q", e.Error())
	}
}

func TestErrResultIsError(t *testing.T) {
	res, _, err := errResult(ToolError{
		Code: CodeLinkInvalid, Message: "bad link",
		Remediation: "检查 vless:// 链接", Next: []string{"bx_diagnose"},
	})
	if err != nil {
		t.Fatalf("errResult 不应返回 Go error(工具错误走 IsError),得到 %v", err)
	}
	if !res.IsError {
		t.Fatalf("应 IsError=true")
	}
	// 内容里应能解析出结构化负载
	found := false
	for _, c := range res.Content {
		if tc, ok := c.(*textContentString); ok && strings.Contains(tc.s, "LINK_INVALID") {
			found = true
		}
	}
	_ = found // 实际断言见 Step 3 的实现(用 SDK 的 TextContent)
	var probe map[string]any
	if e := json.Unmarshal([]byte("{}"), &probe); e != nil {
		t.Fatal(e)
	}
}
```
注意:上面 `textContentString` 是占位,Step 3 用 SDK 的 `*mcpsdk.TextContent` 真实类型,测试断言改为遍历 `res.Content` 找 `*mcpsdk.TextContent` 且 `.Text` 含 `LINK_INVALID`。先按真实类型写,见 Step 3。

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run TestToolError -v`
Expected: 编译失败,`ToolError`/`errResult` undefined。

- [ ] **Step 3: 写实现**

Create `internal/mcp/errors.go`:
```go
package mcp

import (
	"encoding/json"
	"fmt"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// Code 是有限枚举的机器错误码,agent 据此分支(不解析自然语言)。
type Code string

const (
	CodeLinkInvalid       Code = "LINK_INVALID"
	CodePrivilegeRequired Code = "PRIVILEGE_REQUIRED"
	CodeTunnelUnhealthy   Code = "TUNNEL_UNHEALTHY"
	CodeLeakDetected      Code = "LEAK_DETECTED"
	CodeLockoutRisk       Code = "LOCKOUT_RISK"
	CodeDeadmanReverted   Code = "DEADMAN_REVERTED"
	CodeAlreadyCommitted  Code = "ALREADY_COMMITTED"
	CodeNothingToRollback Code = "NOTHING_TO_ROLLBACK"
)

// ToolError 是返回给 agent 的结构化错误,带"下一步该干嘛"。
type ToolError struct {
	Code        Code     `json:"code"`
	Message     string   `json:"message"`
	Remediation string   `json:"remediation,omitempty"`
	Next        []string `json:"next,omitempty"`
}

func (e ToolError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// errResult 把结构化错误打进 CallToolResult(工具错误,非协议错误)。
func errResult(e ToolError) (*mcpsdk.CallToolResult, any, error) {
	payload := map[string]any{"status": "error", "code": e.Code, "message": e.Message}
	if e.Remediation != "" {
		payload["remediation"] = e.Remediation
	}
	if len(e.Next) > 0 {
		payload["next"] = e.Next
	}
	b, _ := json.Marshal(payload)
	return &mcpsdk.CallToolResult{
		IsError: true,
		Content: []mcpsdk.Content{&mcpsdk.TextContent{Text: string(b)}},
	}, nil, nil
}
```

并把 Step 1 测试里的占位断言改为真实类型:
```go
	found := false
	for _, c := range res.Content {
		if tc, ok := c.(*mcpsdk.TextContent); ok && strings.Contains(tc.Text, "LINK_INVALID") {
			found = true
		}
	}
	if !found {
		t.Fatalf("错误内容应含 LINK_INVALID")
	}
```
(在 `errors_test.go` 顶部 import `mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"`,删掉无用的 json 探针代码与 `textContentString` 占位。)

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/mcp/ -v && go vet ./internal/mcp/`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/errors.go internal/mcp/errors_test.go
git commit -m "feat(mcp): 结构化错误 taxonomy(code+remediation+next)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: commit-confirmed 死手状态机(纯逻辑,免 root)

**Files:**
- Create: `internal/confirm/deadman.go`
- Create: `internal/confirm/deadman_test.go`

**Interfaces:**
- Produces:
  - `type State int` 及 `StateIdle`, `StateArmed`, `StateCommitted`, `StateReverted`。
  - `type Guard struct { ... }`;`func New(window time.Duration, now func() time.Time) *Guard`。
  - `func (g *Guard) Arm(restore func() error) error` —— 进入 Armed,记录 restore 回调与到期时刻。已 Armed 再 Arm 返回错误。
  - `func (g *Guard) Commit() error` —— Armed→Committed,清除回调;非 Armed 返回错误(供上层映射 `ALREADY_COMMITTED`/`NOTHING_TO_ROLLBACK`)。
  - `func (g *Guard) Rollback() error` —— Armed→Reverted,执行 restore。
  - `func (g *Guard) Tick() (reverted bool, err error)` —— 若已过期且仍 Armed,执行 restore 转 Reverted 返回 `true`。
  - `func (g *Guard) State() State`。

- [ ] **Step 1: 写失败测试(用假时钟)**

Create `internal/confirm/deadman_test.go`:
```go
package confirm

import (
	"errors"
	"testing"
	"time"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

func TestCommitDisarms(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	restored := false
	if err := g.Arm(func() error { restored = true; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := g.Commit(); err != nil {
		t.Fatal(err)
	}
	c.advance(300 * time.Second)
	if rev, _ := g.Tick(); rev {
		t.Fatal("已 commit 不应再回滚")
	}
	if restored {
		t.Fatal("已 commit 不应触发 restore")
	}
	if g.State() != StateCommitted {
		t.Fatalf("state=%v want Committed", g.State())
	}
}

func TestNoCommitAutoReverts(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	restored := false
	g.Arm(func() error { restored = true; return nil })

	c.advance(239 * time.Second)
	if rev, _ := g.Tick(); rev {
		t.Fatal("未到期不应回滚")
	}
	c.advance(2 * time.Second) // 越过 240s
	rev, err := g.Tick()
	if err != nil {
		t.Fatal(err)
	}
	if !rev || !restored {
		t.Fatalf("到期应自动回滚(rev=%v restored=%v)", rev, restored)
	}
	if g.State() != StateReverted {
		t.Fatalf("state=%v want Reverted", g.State())
	}
}

func TestDoubleArmRejected(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	g.Arm(func() error { return nil })
	if err := g.Arm(func() error { return nil }); err == nil {
		t.Fatal("重复 Arm 应报错")
	}
}

func TestCommitWhenIdleRejected(t *testing.T) {
	g := New(240*time.Second, (&clock{t: time.Unix(0, 0)}).now)
	if err := g.Commit(); !errors.Is(err, ErrNotArmed) {
		t.Fatalf("idle 时 Commit 应返回 ErrNotArmed,得到 %v", err)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/confirm/ -v`
Expected: 编译失败,`New`/`Guard` undefined。

- [ ] **Step 3: 写实现**

Create `internal/confirm/deadman.go`:
```go
// Package confirm 实现 commit-confirmed 死手:改动类操作 Arm 后须在窗口内
// Commit,否则 Tick 到期自动 restore 到 last-known-good。纯逻辑,免 root,
// 时钟可注入。
package confirm

import (
	"errors"
	"sync"
	"time"
)

type State int

const (
	StateIdle State = iota
	StateArmed
	StateCommitted
	StateReverted
)

var (
	ErrNotArmed     = errors.New("guard 未处于 armed 状态")
	ErrAlreadyArmed = errors.New("guard 已 armed,先 Commit 或 Rollback")
)

type Guard struct {
	mu       sync.Mutex
	window   time.Duration
	now      func() time.Time
	state    State
	deadline time.Time
	restore  func() error
}

func New(window time.Duration, now func() time.Time) *Guard {
	return &Guard{window: window, now: now, state: StateIdle}
}

func (g *Guard) Arm(restore func() error) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state == StateArmed {
		return ErrAlreadyArmed
	}
	g.state = StateArmed
	g.restore = restore
	g.deadline = g.now().Add(g.window)
	return nil
}

func (g *Guard) Commit() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != StateArmed {
		return ErrNotArmed
	}
	g.state = StateCommitted
	g.restore = nil
	return nil
}

func (g *Guard) Rollback() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != StateArmed {
		return ErrNotArmed
	}
	err := g.restore()
	g.state = StateReverted
	g.restore = nil
	return err
}

// Tick 由后台循环周期调用;到期且仍 Armed 时自动 restore。
func (g *Guard) Tick() (bool, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.state != StateArmed || g.now().Before(g.deadline) {
		return false, nil
	}
	err := g.restore()
	g.state = StateReverted
	g.restore = nil
	return true, err
}

func (g *Guard) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.state
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/confirm/ -v && go vet ./internal/confirm/`
Expected: 四个测试全 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/confirm/deadman.go internal/confirm/deadman_test.go
git commit -m "feat(confirm): commit-confirmed 死手状态机(注入时钟,纯逻辑)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: last-known-good 快照接口 + 内存 fake

**Files:**
- Create: `internal/confirm/snapshot.go`
- Create: `internal/confirm/snapshot_test.go`

**Interfaces:**
- Produces:
  - `type Snapshot interface { ID() string }`。
  - `type Snapshotter interface { Capture() (Snapshot, error); Restore(Snapshot) error }`。
  - `func ArmWithSnapshot(g *Guard, s Snapshotter) (Snapshot, error)` —— 先 Capture 得 last-known-good,再 `g.Arm(func() error { return s.Restore(snap) })`;Capture 失败则不 Arm 不改动(宁可不改也不留半截)。
- Consumes: `Guard`(Task 3)。

- [ ] **Step 1: 写失败测试**

Create `internal/confirm/snapshot_test.go`:
```go
package confirm

import (
	"errors"
	"testing"
	"time"
)

type fakeSnap struct{ id string }

func (f fakeSnap) ID() string { return f.id }

type fakeSnapper struct {
	captureErr error
	restored   []string
}

func (f *fakeSnapper) Capture() (Snapshot, error) {
	if f.captureErr != nil {
		return nil, f.captureErr
	}
	return fakeSnap{id: "good-1"}, nil
}
func (f *fakeSnapper) Restore(s Snapshot) error { f.restored = append(f.restored, s.ID()); return nil }

func TestArmWithSnapshotCapturesThenArms(t *testing.T) {
	c := &clock{t: time.Unix(0, 0)}
	g := New(240*time.Second, c.now)
	fs := &fakeSnapper{}
	snap, err := ArmWithSnapshot(g, fs)
	if err != nil || snap.ID() != "good-1" {
		t.Fatalf("snap=%v err=%v", snap, err)
	}
	if g.State() != StateArmed {
		t.Fatal("应已 Armed")
	}
	c.advance(241 * time.Second)
	g.Tick()
	if len(fs.restored) != 1 || fs.restored[0] != "good-1" {
		t.Fatalf("到期应 Restore 到 good-1,得到 %v", fs.restored)
	}
}

func TestCaptureFailDoesNotArm(t *testing.T) {
	g := New(240*time.Second, (&clock{t: time.Unix(0, 0)}).now)
	fs := &fakeSnapper{captureErr: errors.New("boom")}
	if _, err := ArmWithSnapshot(g, fs); err == nil {
		t.Fatal("Capture 失败应报错")
	}
	if g.State() != StateIdle {
		t.Fatal("Capture 失败不应 Arm")
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/confirm/ -run Snapshot -v`
Expected: 编译失败,`Snapshotter`/`ArmWithSnapshot` undefined。

- [ ] **Step 3: 写实现**

Create `internal/confirm/snapshot.go`:
```go
package confirm

// Snapshot 是一次 last-known-good 状态快照的句柄。
type Snapshot interface {
	ID() string
}

// Snapshotter 抓取/还原系统状态(路由/config/unit/nft)。
// 真实实现是平台特定的(后续任务);本包只定义接口,便于纯逻辑测试。
type Snapshotter interface {
	Capture() (Snapshot, error)
	Restore(Snapshot) error
}

// ArmWithSnapshot 先抓 last-known-good,再武装死手;Capture 失败不武装、不改动。
func ArmWithSnapshot(g *Guard, s Snapshotter) (Snapshot, error) {
	snap, err := s.Capture()
	if err != nil {
		return nil, err
	}
	if err := g.Arm(func() error { return s.Restore(snap) }); err != nil {
		return nil, err
	}
	return snap, nil
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/confirm/ -v && go vet ./internal/confirm/`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/confirm/snapshot.go internal/confirm/snapshot_test.go
git commit -m "feat(confirm): last-known-good 快照接口 + ArmWithSnapshot

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: `Ops` port + 只读 tools(capabilities/status/diagnose/logs/plan)

**Files:**
- Create: `internal/mcp/ops.go`(定义 `Ops` 接口 + 输入/输出类型)
- Create: `internal/mcp/tools_readonly.go`(注册只读 tools)
- Modify: `internal/mcp/server.go`(`newServer` 改为注册真实 tools;把临时 `type Ops interface{}` 删除)
- Create: `internal/mcp/tools_readonly_test.go`
- Create: `internal/mcp/fakeops_test.go`(测试用 `fakeOps`)

**Interfaces:**
- Produces (`internal/mcp/ops.go`):
```go
type Ops interface {
    Capabilities() (CapabilitiesOut, error)
    Status() (StatusOut, error)
    Diagnose() (DiagnoseOut, error)
    Logs(LogsIn) (LogsOut, error)
    Plan(PlanIn) (PlanOut, error)
    Verify() (VerifyOut, error)          // Task 6 使用
    Setup(SetupIn) error                 // Task 7 使用
    SetTransport(SetTransportIn) error   // Task 7 使用
    RestartTunnel() error                // Task 7 使用
    Rehijack() error                     // Task 7 使用
}
```
  以及对应的 `*In`/`*Out` 结构(本任务先定义只读相关的;Task 6/7 复用同文件追加字段)。
- Consumes: 无外部;`liveOps`(绑定现有 internal 包)留到 Task 8 接 CLI 时实现。

- [ ] **Step 1: 写 `Ops` 接口与只读类型**

Create `internal/mcp/ops.go`:
```go
package mcp

// Ops 是 MCP tools 依赖的操作 port。liveOps(Task 8)绑到现有 internal 包,
// 测试用 fakeOps。这样 tool handler 可纯逻辑测试,免 root。
type Ops interface {
	Capabilities() (CapabilitiesOut, error)
	Status() (StatusOut, error)
	Diagnose() (DiagnoseOut, error)
	Logs(LogsIn) (LogsOut, error)
	Plan(PlanIn) (PlanOut, error)
	Verify() (VerifyOut, error)
	Setup(SetupIn) error
	SetTransport(SetTransportIn) error
	RestartTunnel() error
	Rehijack() error
}

type CapabilitiesOut struct {
	Platform   string   `json:"platform" jsonschema:"linux or darwin"`
	Transports []string `json:"transports" jsonschema:"supported transport schemes, e.g. brook,reality"`
	Installed  bool     `json:"installed" jsonschema:"whether bx is installed on this host"`
}

type StatusOut struct {
	TunnelHealthy bool   `json:"tunnel_healthy"`
	LatencyMS     int64  `json:"latency_ms"`
	Mode          string `json:"mode" jsonschema:"host or router"`
	UDPMode       string `json:"udp_mode"`
}

type Finding struct {
	Severity    string `json:"severity" jsonschema:"info|warn|error"`
	Title       string `json:"title"`
	Remediation string `json:"remediation,omitempty"`
}
type DiagnoseOut struct {
	Findings []Finding `json:"findings"`
}

type LogsIn struct {
	Lines int    `json:"lines,omitempty" jsonschema:"how many trailing lines (default 100)"`
	Since string `json:"since,omitempty" jsonschema:"optional time filter, e.g. 10m"`
}
type LogsOut struct {
	Text string `json:"text"`
}

type PlanIn struct {
	Link string `json:"link,omitempty" jsonschema:"optional server link to plan a setup/transport change for"`
}
type PlanOut struct {
	Steps []string `json:"steps" jsonschema:"the route/firewall steps that WOULD run, not applied"`
}
```

- [ ] **Step 2: 写 fakeOps 与失败测试**

Create `internal/mcp/fakeops_test.go`:
```go
package mcp

type fakeOps struct {
	caps     CapabilitiesOut
	status   StatusOut
	diagnose DiagnoseOut
	logs     LogsOut
	plan     PlanOut
	verify   VerifyOut
	calls    []string
	setupErr error
}

func (f *fakeOps) Capabilities() (CapabilitiesOut, error) { return f.caps, nil }
func (f *fakeOps) Status() (StatusOut, error)             { return f.status, nil }
func (f *fakeOps) Diagnose() (DiagnoseOut, error)         { return f.diagnose, nil }
func (f *fakeOps) Logs(LogsIn) (LogsOut, error)           { return f.logs, nil }
func (f *fakeOps) Plan(PlanIn) (PlanOut, error)           { return f.plan, nil }
func (f *fakeOps) Verify() (VerifyOut, error)             { return f.verify, nil }
func (f *fakeOps) Setup(SetupIn) error                    { f.calls = append(f.calls, "setup"); return f.setupErr }
func (f *fakeOps) SetTransport(SetTransportIn) error      { f.calls = append(f.calls, "set_transport"); return nil }
func (f *fakeOps) RestartTunnel() error                   { f.calls = append(f.calls, "restart"); return nil }
func (f *fakeOps) Rehijack() error                        { f.calls = append(f.calls, "rehijack"); return nil }
```

Create `internal/mcp/tools_readonly_test.go`:
```go
package mcp

import (
	"context"
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func callTool(t *testing.T, ops Ops, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx := context.Background()
	srv := newServer(ops)
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "t", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}

func TestCapabilitiesTool(t *testing.T) {
	ops := &fakeOps{caps: CapabilitiesOut{Platform: "linux", Transports: []string{"brook", "reality"}, Installed: true}}
	res := callTool(t, ops, "bx_capabilities", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
	var out CapabilitiesOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if out.Platform != "linux" || !out.Installed {
		t.Fatalf("got %+v", out)
	}
}

func TestDiagnoseTool(t *testing.T) {
	ops := &fakeOps{diagnose: DiagnoseOut{Findings: []Finding{{Severity: "warn", Title: "v6 enabled"}}}}
	res := callTool(t, ops, "bx_diagnose", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
}
```

- [ ] **Step 3: 跑红**

Run: `go test ./internal/mcp/ -run 'Capabilities|Diagnose' -v`
Expected: 编译失败(`bx_capabilities` 未注册 / `VerifyOut`/`SetupIn` 等未定义)。
注意:`VerifyOut`/`SetupIn`/`SetTransportIn` 在 Task 6/7 定义;为让本任务编译,在 `ops.go` 末尾先加最小占位:
```go
type VerifyOut struct{ Pass bool `json:"pass"` }
type SetupIn struct{ Link string `json:"link"` }
type SetTransportIn struct{ Link string `json:"link"` }
```
Task 6/7 再扩展这些类型的字段。

- [ ] **Step 4: 注册只读 tools**

Create `internal/mcp/tools_readonly.go`:
```go
package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type emptyIn struct{}

func registerReadOnly(s *mcpsdk.Server, ops Ops) {
	ro := &mcpsdk.ToolAnnotations{ReadOnlyHint: true}

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_capabilities", Description: "host platform, supported transports, installed?", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, CapabilitiesOut, error) {
			out, err := ops.Capabilities()
			if err != nil {
				return errResultTyped[CapabilitiesOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_status", Description: "tunnel health, latency, mode", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, StatusOut, error) {
			out, err := ops.Status()
			if err != nil {
				return errResultTyped[StatusOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_diagnose", Description: "structured findings with remediation", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, DiagnoseOut, error) {
			out, err := ops.Diagnose()
			if err != nil {
				return errResultTyped[DiagnoseOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_logs", Description: "tail client logs for self-diagnosis", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in LogsIn) (*mcpsdk.CallToolResult, LogsOut, error) {
			out, err := ops.Logs(in)
			if err != nil {
				return errResultTyped[LogsOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, out, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_plan", Description: "dry-run the route/firewall steps for a change", Annotations: ro},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in PlanIn) (*mcpsdk.CallToolResult, PlanOut, error) {
			out, err := ops.Plan(in)
			if err != nil {
				return errResultTyped[PlanOut](ToolError{Code: CodeLinkInvalid, Message: err.Error()})
			}
			return nil, out, nil
		})
}
```

在 `errors.go` 追加泛型辅助(让带类型 Out 的 handler 能返回错误结果——错误时 Out 用零值):
```go
// errResultTyped 同 errResult,但适配 AddTool 的 (Out) 返回位:错误走 IsError,Out 用零值。
func errResultTyped[T any](e ToolError) (*mcpsdk.CallToolResult, T, error) {
	res, _, _ := errResult(e)
	var zero T
	return res, zero, nil
}
```

Modify `internal/mcp/server.go` 的 `newServer`:删掉临时 `type Ops interface{}`(现由 `ops.go` 定义),并把 ping 之外加上只读注册:
```go
func newServer(ops Ops) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "bx", Version: serverVersion}, nil)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_ping", Description: "liveness probe; returns ok=true", Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ pingIn) (*mcpsdk.CallToolResult, pingOut, error) {
			return nil, pingOut{OK: true}, nil
		})
	if ops != nil {
		registerReadOnly(s, ops)
	}
	return s
}
```

- [ ] **Step 5: 跑绿**

Run: `go test ./internal/mcp/ -v && go vet ./internal/mcp/`
Expected: 全 PASS(ping、capabilities、diagnose 等)。

- [ ] **Step 6: 提交**

```bash
git add internal/mcp/ops.go internal/mcp/tools_readonly.go internal/mcp/tools_readonly_test.go internal/mcp/fakeops_test.go internal/mcp/server.go internal/mcp/errors.go
git commit -m "feat(mcp): Ops port + 只读 tools(capabilities/status/diagnose/logs/plan)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 6: `bx_verify` tool(主机可测维度)

**Files:**
- Modify: `internal/mcp/ops.go`(扩展 `VerifyOut`)
- Create: `internal/mcp/tools_verify.go`
- Create: `internal/mcp/tools_verify_test.go`

**Interfaces:**
- Produces:
```go
type VerifyOut struct {
    Pass         bool   `json:"pass"`
    ExitIP       string `json:"exit_ip,omitempty" jsonschema:"observed egress IP; should be the VPS"`
    DNSLeak      bool   `json:"dns_leak"`
    IPv6Leak     bool   `json:"ipv6_leak"`
    SelfReach    bool   `json:"self_reach" jsonschema:"agent control channel (SSH) still reachable"`
    KillSwitchOK bool   `json:"killswitch_ok"`
    Note         string `json:"note,omitempty" jsonschema:"e.g. WebRTC requires a LAN-client browser test, not automated here"`
}
```
- Consumes: `Ops.Verify()`(Task 5 已在接口声明)。

- [ ] **Step 1: 扩展 VerifyOut + 写失败测试**

把 `ops.go` 里 Task 5 的占位 `type VerifyOut struct{ Pass bool ... }` 替换为上面的完整结构。

Create `internal/mcp/tools_verify_test.go`:
```go
package mcp

import (
	"encoding/json"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestVerifyToolPass(t *testing.T) {
	ops := &fakeOps{verify: VerifyOut{Pass: true, ExitIP: "203.0.113.9", SelfReach: true, KillSwitchOK: true,
		Note: "WebRTC 需 LAN 客户端浏览器测,未自动化"}}
	res := callTool(t, ops, "bx_verify", map[string]any{})
	if res.IsError {
		t.Fatal("不应错误")
	}
	var out VerifyOut
	if err := json.Unmarshal([]byte(res.Content[0].(*mcpsdk.TextContent).Text), &out); err != nil {
		t.Fatal(err)
	}
	if !out.Pass || out.ExitIP != "203.0.113.9" {
		t.Fatalf("got %+v", out)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run Verify -v`
Expected: 编译失败,`bx_verify` 未注册 / `VerifyOut` 字段不全。

- [ ] **Step 3: 注册 verify tool**

Create `internal/mcp/tools_verify.go`:
```go
package mcp

import (
	"context"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func registerVerify(s *mcpsdk.Server, ops Ops) {
	mcpsdk.AddTool(s, &mcpsdk.Tool{
		Name:        "bx_verify",
		Description: "leak audit: exit IP, DNS leak, IPv6 leak, self-reachability, kill-switch",
		Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true},
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, VerifyOut, error) {
		out, err := ops.Verify()
		if err != nil {
			return errResultTyped[VerifyOut](ToolError{Code: CodeLeakDetected, Message: err.Error(),
				Remediation: "检查隧道健康与路由劫持", Next: []string{"bx_diagnose", "bx_status"}})
		}
		return nil, out, nil
	})
}
```

在 `server.go` 的 `newServer` 里,`registerReadOnly(s, ops)` 之后加 `registerVerify(s, ops)`。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/mcp/ -v && go vet ./internal/mcp/`
Expected: 全 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/ops.go internal/mcp/tools_verify.go internal/mcp/tools_verify_test.go internal/mcp/server.go
git commit -m "feat(mcp): bx_verify 泄漏审计 tool(主机可测维度;WebRTC 标注未自动化)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 7: 改动类 tools 走 commit-confirmed + bx_commit/bx_rollback

**Files:**
- Modify: `internal/mcp/ops.go`(扩展 `SetupIn`/`SetTransportIn`)
- Create: `internal/mcp/tools_mutating.go`
- Create: `internal/mcp/tools_mutating_test.go`
- Modify: `internal/mcp/server.go`(注册改动类 + 控制类 tools;`newServer` 接入一个 `*confirm.Guard` + `Snapshotter`)

**Interfaces:**
- Produces:
  - `newServer` 改签名为 `newServerWithGuard(ops Ops, g *confirm.Guard, snap confirm.Snapshotter) *mcpsdk.Server`;`newServer(ops)` 保留为便捷封装(用一个默认 in-process guard + nopSnapshotter,供只读测试)。
  - 改动类 tools `bx_setup`/`bx_set_transport`/`bx_restart_tunnel`/`bx_rehijack`:每个先 `confirm.ArmWithSnapshot(g, snap)` 抓 last-known-good 并武装死手,再调对应 `ops.*`,返回提示"已武装 240s,verify 通过后调 bx_commit"。
  - 控制类 `bx_commit`(`g.Commit()`)、`bx_rollback`(`g.Rollback()`),按 `confirm` 的 `ErrNotArmed` 映射 `ALREADY_COMMITTED`/`NOTHING_TO_ROLLBACK`。
- Consumes: `confirm.Guard`/`Snapshotter`/`ArmWithSnapshot`(Task 3/4);`Ops`(Task 5)。

- [ ] **Step 1: 扩展类型 + 写失败测试**

把 `ops.go` 的占位 `SetupIn`/`SetTransportIn` 替换为:
```go
type SetupIn struct {
	Link string `json:"link" jsonschema:"server link: brook:// or vless://"`
}
type SetTransportIn struct {
	Link string `json:"link" jsonschema:"new server link to switch transport to"`
}
```

Create `internal/mcp/tools_mutating_test.go`:
```go
package mcp

import (
	"testing"
	"time"

	"github.com/getbx/bx/internal/confirm"
)

type memSnap struct{ id string }

func (m memSnap) ID() string { return m.id }

type memSnapper struct{ restored int }

func (m *memSnapper) Capture() (confirm.Snapshot, error) { return memSnap{id: "lkg"}, nil }
func (m *memSnapper) Restore(confirm.Snapshot) error     { m.restored++; return nil }

type tclock struct{ t time.Time }

func (c *tclock) now() time.Time { return c.t }

func TestSetupArmsThenCommit(t *testing.T) {
	clk := &tclock{t: time.Unix(0, 0)}
	g := confirm.New(240*time.Second, clk.now)
	snap := &memSnapper{}
	ops := &fakeOps{}
	srv := newServerWithGuard(ops, g, snap)
	res := callToolOn(t, srv, "bx_setup", map[string]any{"link": "vless://x@h:443"})
	if res.IsError {
		t.Fatal("setup 不应错误")
	}
	if g.State() != confirm.StateArmed {
		t.Fatal("setup 后应 Armed")
	}
	// 提交转正
	res = callToolOn(t, srv, "bx_commit", map[string]any{})
	if res.IsError {
		t.Fatal("commit 不应错误")
	}
	if g.State() != confirm.StateCommitted {
		t.Fatalf("state=%v want Committed", g.State())
	}
}

func TestSetupNoCommitWouldRevert(t *testing.T) {
	clk := &tclock{t: time.Unix(0, 0)}
	g := confirm.New(240*time.Second, clk.now)
	snap := &memSnapper{}
	srv := newServerWithGuard(&fakeOps{}, g, snap)
	callToolOn(t, srv, "bx_setup", map[string]any{"link": "vless://x@h:443"})
	clk.t = clk.t.Add(241 * time.Second)
	if rev, _ := g.Tick(); !rev || snap.restored != 1 {
		t.Fatalf("未 commit 到期应回滚(rev=%v restored=%d)", rev, snap.restored)
	}
}

func TestRollbackWhenIdle(t *testing.T) {
	g := confirm.New(240*time.Second, (&tclock{t: time.Unix(0, 0)}).now)
	srv := newServerWithGuard(&fakeOps{}, g, &memSnapper{})
	res := callToolOn(t, srv, "bx_rollback", map[string]any{})
	if !res.IsError {
		t.Fatal("idle 时 rollback 应返回错误结果(NOTHING_TO_ROLLBACK)")
	}
}
```

并在 `tools_readonly_test.go` 里把 `callTool` 重构出一个 `callToolOn(t, srv, name, args)`(接收已建好的 `*mcpsdk.Server`),`callTool` 改为 `callToolOn(t, newServer(ops), ...)`。给出 `callToolOn`:
```go
func callToolOn(t *testing.T, srv *mcpsdk.Server, name string, args map[string]any) *mcpsdk.CallToolResult {
	t.Helper()
	ctx := context.Background()
	st, ct := mcpsdk.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "t", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	res, err := cs.CallTool(ctx, &mcpsdk.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	return res
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run 'Setup|Rollback' -v`
Expected: 编译失败,`newServerWithGuard`/改动类 tool 未定义。

- [ ] **Step 3: 写实现**

Create `internal/mcp/tools_mutating.go`:
```go
package mcp

import (
	"context"
	"errors"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/getbx/bx/internal/confirm"
)

type armedOut struct {
	Status string `json:"status" jsonschema:"armed"`
	Note   string `json:"note"`
}

const armedNote = "改动已应用并武装 240s 死手;请立即 bx_verify,通过后调 bx_commit,否则将自动回滚"

func armThen(g *confirm.Guard, snap confirm.Snapshotter, apply func() error) (*mcpsdk.CallToolResult, armedOut, error) {
	if _, err := confirm.ArmWithSnapshot(g, snap); err != nil {
		return errResultTyped[armedOut](ToolError{Code: CodeLockoutRisk, Message: "抓取 last-known-good 失败,已中止改动: " + err.Error()})
	}
	if err := apply(); err != nil {
		_ = g.Rollback() // apply 失败立即回滚,不留半截
		return errResultTyped[armedOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error(),
			Remediation: "已自动回滚到改动前;查 bx_diagnose", Next: []string{"bx_diagnose", "bx_logs"}})
	}
	return nil, armedOut{Status: "armed", Note: armedNote}, nil
}

func registerMutating(s *mcpsdk.Server, ops Ops, g *confirm.Guard, snap confirm.Snapshotter) {
	dx := &mcpsdk.ToolAnnotations{DestructiveHint: ptrue()}

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_setup", Description: "install+configure from a link; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in SetupIn) (*mcpsdk.CallToolResult, armedOut, error) {
			if in.Link == "" {
				return errResultTyped[armedOut](ToolError{Code: CodeLinkInvalid, Message: "link 不能为空"})
			}
			return armThen(g, snap, func() error { return ops.Setup(in) })
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_set_transport", Description: "switch transport to a new link; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in SetTransportIn) (*mcpsdk.CallToolResult, armedOut, error) {
			if in.Link == "" {
				return errResultTyped[armedOut](ToolError{Code: CodeLinkInvalid, Message: "link 不能为空"})
			}
			return armThen(g, snap, func() error { return ops.SetTransport(in) })
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_restart_tunnel", Description: "restart the transport subprocess; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, armedOut, error) {
			return armThen(g, snap, ops.RestartTunnel)
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_rehijack", Description: "reinstall route hijack; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, armedOut, error) {
			return armThen(g, snap, ops.Rehijack)
		})

	// 控制类
	type ctlOut struct {
		State string `json:"state"`
	}
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_commit", Description: "confirm the armed change; disarms the deadman"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, ctlOut, error) {
			if err := g.Commit(); err != nil {
				if errors.Is(err, confirm.ErrNotArmed) {
					return errResultTyped[ctlOut](ToolError{Code: CodeAlreadyCommitted, Message: "没有待确认的改动"})
				}
				return errResultTyped[ctlOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, ctlOut{State: "committed"}, nil
		})

	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_rollback", Description: "immediately revert to last-known-good"},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, _ emptyIn) (*mcpsdk.CallToolResult, ctlOut, error) {
			if err := g.Rollback(); err != nil {
				if errors.Is(err, confirm.ErrNotArmed) {
					return errResultTyped[ctlOut](ToolError{Code: CodeNothingToRollback, Message: "没有可回滚的改动"})
				}
				return errResultTyped[ctlOut](ToolError{Code: CodeTunnelUnhealthy, Message: "回滚出错: " + err.Error()})
			}
			return nil, ctlOut{State: "reverted"}, nil
		})
}

func ptrue() *bool { b := true; return &b }
```

Modify `internal/mcp/server.go`:
```go
import (
	"context"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/getbx/bx/internal/confirm"
)

// nopSnapshotter 用于只读/单元场景:不抓真实状态。
type nopSnapshotter struct{}
type nopSnap struct{}

func (nopSnap) ID() string                                 { return "nop" }
func (nopSnapshotter) Capture() (confirm.Snapshot, error)  { return nopSnap{}, nil }
func (nopSnapshotter) Restore(confirm.Snapshot) error      { return nil }

func newServer(ops Ops) *mcpsdk.Server {
	g := confirm.New(240*time.Second, time.Now)
	return newServerWithGuard(ops, g, nopSnapshotter{})
}

func newServerWithGuard(ops Ops, g *confirm.Guard, snap confirm.Snapshotter) *mcpsdk.Server {
	s := mcpsdk.NewServer(&mcpsdk.Implementation{Name: "bx", Version: serverVersion}, nil)
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_ping", Description: "liveness probe; returns ok=true", Annotations: &mcpsdk.ToolAnnotations{ReadOnlyHint: true}},
		func(ctx context.Context, _ *mcpsdk.CallToolRequest, _ pingIn) (*mcpsdk.CallToolResult, pingOut, error) {
			return nil, pingOut{OK: true}, nil
		})
	if ops != nil {
		registerReadOnly(s, ops)
		registerVerify(s, ops)
		registerMutating(s, ops, g, snap)
	}
	return s
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/mcp/ ./internal/confirm/ -v && go vet ./internal/mcp/`
Expected: 全 PASS(含 setup→commit、未 commit→回滚、idle rollback 报错)。

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/ops.go internal/mcp/tools_mutating.go internal/mcp/tools_mutating_test.go internal/mcp/tools_readonly_test.go internal/mcp/server.go
git commit -m "feat(mcp): 改动类 tools 走 commit-confirmed + bx_commit/bx_rollback

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 8: CLI 接线 `bx mcp` + `liveOps` 绑定现有 internal + 死手后台 Tick

**Files:**
- Create: `internal/mcp/liveops.go`(`liveOps` 实现 `Ops`,绑定现有 internal 包)
- Create: `internal/mcp/liveops_test.go`(只测可纯逻辑验证的部分:权限门控)
- Modify: `internal/cli/cli.go`(注册 `{Name: "mcp", ...}` + `mcpAction`)
- Modify: `internal/mcp/server.go`(导出 `Serve`,内部起一个后台 goroutine 周期 `g.Tick()` 驱动死手到期回滚)

**Interfaces:**
- Produces:
  - `func NewLiveOps(configPath string) Ops` —— 构造绑定现有逻辑的 Ops。
  - `func Serve(ctx context.Context, ops Ops) error` —— 已存在(Task 1);本任务内部改为 `newServer` + 起 `tickLoop(ctx, g)` 后台循环(每 2s `g.Tick()`),并把 `g`/`snap` 暴露给 `Serve` 内部持有。
- Consumes: 现有 `internal/setup`、`internal/supervisor`(控制 socket)、`internal/install`、`internal/config`、capabilities/doctor 逻辑。**实现者须打开这些文件读真实函数签名**(本 plan 不臆造其签名)。

- [ ] **Step 1: 权限门控失败测试**

Create `internal/mcp/liveops_test.go`:
```go
package mcp

import "testing"

func TestMutatingRequiresRoot(t *testing.T) {
	// requireRoot 为纯函数:isRoot=false 且 mutating 时返回 PRIVILEGE_REQUIRED。
	if err := requireRoot(false); err == nil {
		t.Fatal("非 root 调改动类应报 PRIVILEGE_REQUIRED")
	}
	if te, ok := err.(ToolError); !ok || te.Code != CodePrivilegeRequired {
		t.Fatalf("应为 PRIVILEGE_REQUIRED,得到 %v", err)
	}
	if err := requireRoot(true); err != nil {
		t.Fatalf("root 时不应报错,得到 %v", err)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run RequiresRoot -v`
Expected: `requireRoot` undefined。

- [ ] **Step 3: 写 liveOps 骨架 + requireRoot**

Create `internal/mcp/liveops.go`:
```go
package mcp

import "os"

// requireRoot:改动类操作的权限门控(纯函数,便于测试)。
func requireRoot(isRoot bool) error {
	if !isRoot {
		return ToolError{Code: CodePrivilegeRequired, Message: "改动类操作需 root",
			Remediation: "用 `sudo bx mcp` 或 `ssh root@host bx mcp` 启动 server"}
	}
	return nil
}

func isRoot() bool { return os.Geteuid() == 0 }

// liveOps 把 Ops 绑到现有 internal 逻辑。
// 注意:每个方法的具体实现需打开对应 internal 包读真实签名后填入。
type liveOps struct {
	configPath string
}

// NewLiveOps 构造绑定现有逻辑的 Ops。
func NewLiveOps(configPath string) Ops { return &liveOps{configPath: configPath} }

// 下列方法的实现:绑定现有逻辑(读真实签名),改动类先 requireRoot(isRoot())。
// 例(伪,实现时替换为真实调用):
//   func (o *liveOps) Status() (StatusOut, error) {
//       // 读 supervisor 控制 socket(见 internal/supervisor/run.go serveStats + stats.Report),
//       // 映射成 StatusOut 返回。
//   }
//   func (o *liveOps) Setup(in SetupIn) error {
//       if err := requireRoot(isRoot()); err != nil { return err }
//       // 调 internal/setup 的现有 setup 流程(见 internal/cli setupAction / internal/setup)。
//   }
```

实现者据此补全 `liveOps` 全部 `Ops` 方法:**只读**方法绑 capabilities/status(控制 socket)/doctor/logs/router-plan;**改动**方法首行 `requireRoot(isRoot())`,再调现有 setup/supervisor 控制动词。每补一个方法,补一个绑定层的集成测试(门控在需要 root 的用 build tag,见 Task 9)。

- [ ] **Step 4: 跑绿(权限门控)**

Run: `go test ./internal/mcp/ -run RequiresRoot -v`
Expected: PASS。

- [ ] **Step 5: 后台 Tick 驱动死手 + 接 CLI**

Modify `internal/mcp/server.go`:把 `Serve` 改为持有 guard 并起后台 tick:
```go
func Serve(ctx context.Context, ops Ops) error {
	g := confirm.New(240*time.Second, time.Now)
	srv := newServerWithGuard(ops, g, newSystemSnapshotter())
	go tickLoop(ctx, g)
	return srv.Run(ctx, &mcpsdk.StdioTransport{})
}

func tickLoop(ctx context.Context, g *confirm.Guard) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			_, _ = g.Tick()
		}
	}
}
```
`newSystemSnapshotter()` 先返回 `nopSnapshotter{}`(真实路由/config 快照实现留 Task 9/后续 spec,此处不 brick、退化为"无快照可还原"——并在 `bx_diagnose` 报告 snapshot 能力未就绪)。在文件加:
```go
func newSystemSnapshotter() confirm.Snapshotter { return nopSnapshotter{} }
```

Modify `internal/cli/cli.go`,在 Commands 列表加(放在 `run`/`serve` 附近):
```go
{Name: "mcp", Usage: "启动 agent 控制面 MCP server(stdio)", Hidden: false, Flags: mcpFlags(), Action: mcpAction},
```
并新增:
```go
func mcpFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "config", Aliases: []string{"c"}, Value: defaultConfigPath, Usage: "客户端配置路径"},
	}
}

func mcpAction(c *cli.Context) error {
	ops := mcp.NewLiveOps(c.String("config"))
	return mcp.Serve(c.Context, ops)
}
```
(在 `cli.go` import 加 `"github.com/getbx/bx/internal/mcp"`。)

- [ ] **Step 6: 全量验证**

Run:
```bash
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
```
Expected: 全绿;darwin 交叉编译通过。

- [ ] **Step 7: 手测 stdio server 起得来**

Run:
```bash
printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"smoke","version":"0"}}}' | go run . mcp 2>/dev/null | head -c 400
```
Expected: 输出一行 JSON-RPC `initialize` 结果(含 serverInfo `bx`)。说明 stdio server 正常握手。

- [ ] **Step 8: 提交**

```bash
git add internal/mcp/liveops.go internal/mcp/liveops_test.go internal/mcp/server.go internal/cli/cli.go
git commit -m "feat(cli): bx mcp 子命令 + liveOps 绑定 + 死手后台 Tick

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 9(门控/集成,需 root): netns 端到端 + 死手真实还原

**Files:**
- Create: `internal/mcp/integration_linux_test.go`(`//go:build integration`)
- Create: `internal/confirm/systemsnapshot_linux.go`(真实路由/config/unit 快照与还原)
- Create: `internal/confirm/systemsnapshot_linux_test.go`(`//go:build integration`)

**Interfaces:**
- Produces: `func NewSystemSnapshotter() confirm.Snapshotter`(真实实现:Capture 记录 `ip rule`/`ip route`/相关 config 文件,Restore 还原);替换 Task 8 的 `newSystemSnapshotter()` nop。
- Consumes: `confirm.Snapshotter`(Task 4)。

- [ ] **Step 1: 写真实快照(Linux)的失败集成测试**

Create `internal/confirm/systemsnapshot_linux_test.go`(`//go:build integration`):测试在一个 netns 内 Capture → 人为改一条 `ip rule` → Restore → 断言规则集回到 Capture 时刻。给出测试骨架(实现者按 netns 习惯补全 setup/teardown):
```go
//go:build integration

package confirm

import "testing"

func TestSystemSnapshotRoundTrip(t *testing.T) {
	// 需 root + netns。Capture 当前 ip rule/route 快照;
	// 改动一条规则;Restore;断言与 Capture 时一致。
	t.Skip("实现者在 netns 内补全:Capture→mutate→Restore→assert")
}
```

- [ ] **Step 2: 写真实快照实现**

Create `internal/confirm/systemsnapshot_linux.go`:Capture 执行 `ip rule show`/`ip route show table all`(及 bx 相关 config 路径)存快照;Restore 据快照逆向。复用 bx 现有的 `e736351 pre-clean own policy-routing` / `62d17a2 self-heal foreign ip rules` 的还原思路(读 `internal/supervisor/router_linux.go` 现有还原逻辑作参考)。

- [ ] **Step 3: 端到端 MCP 集成测试**

Create `internal/mcp/integration_linux_test.go`(`//go:build integration`):在 netns 内用真实 `liveOps` + `NewSystemSnapshotter()` 跑 `bx_setup`(假/本地 socks 服务器)→ `bx_verify` → `bx_commit`;再跑一例"`bx_rehijack` 后不 commit、推进时间 → 自动还原、控制通道恢复"。

- [ ] **Step 4: 跑集成测试(CI/手动,需 root)**

Run: `sudo go test -tags integration ./internal/confirm/ ./internal/mcp/ -v`
Expected: round-trip 与端到端 PASS。

- [ ] **Step 5: 把 Serve 切到真实快照**

Modify `internal/mcp/server.go`:`newSystemSnapshotter()` 在 Linux 改为返回 `confirm.NewSystemSnapshotter()`(用 build tag 或运行时按 GOOS)。darwin 暂留 nop(真实快照随 darwin 真机验证另开)。

- [ ] **Step 6: 提交**

```bash
git add internal/confirm/systemsnapshot_linux.go internal/confirm/systemsnapshot_linux_test.go internal/mcp/integration_linux_test.go internal/mcp/server.go
git commit -m "feat(confirm): Linux 真实 last-known-good 快照 + netns 端到端集成测试(门控)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- §2 架构(`bx mcp` stdio / SSH-stdio / 权限)→ Task 1(stdio server)、Task 8(CLI + requireRoot 权限门控);SSH-stdio 无需代码(运行方式,文档)。
- §3 commit-confirmed 死手 → Task 3(状态机)、Task 4(快照接口)、Task 7(改动类接入)、Task 8(后台 Tick)、Task 9(真实快照)。
- §4 工具集 12 个 → bx_ping(T1)、capabilities/status/diagnose/logs/plan(T5)、verify(T6)、setup/set_transport/restart_tunnel/rehijack/commit/rollback(T7)。注解 readOnly/destructive → T5/T6/T7。
- §5 错误 taxonomy(8 码 + remediation + next)→ Task 2(全部码 + 结构);各 tool 错误分支用对应码(T5–T7)。
- §6 边界 → 体现在 `Ops` 只暴露机械动作、无"自动选传输"、无含糊 repair(已拆成 set_transport/restart/rehijack,T7)。
- §7 数据流 → Task 8 手测(T7)闭环可跑;Task 9 真实端到端。
- §8 错误处理(link 非法早失败 / 缺权限 / 切断自身 / 重复 commit / 快照失败中止)→ T7 armThen(快照失败 CodeLockoutRisk、apply 失败自动回滚)、T7 commit/rollback 幂等码、T8 requireRoot。
- §9 测试分层(纯逻辑死手/状态机 + MCP mock + 门控集成 + 回归)→ T3/T4 纯逻辑、T5–T7 MCP 内存 transport、T9 门控集成;回归靠 Global Constraints 的 `go test ./...`。

**占位扫描:** Task 8 的 `liveOps` 各方法体是"绑定现有签名"的实现指令(非 TBD)——已明确"打开 internal 包读真实签名后填入",并给出 Status/Setup 范例;这是诚实处理"不臆造现有签名"的必要做法,非占位失败。Task 9 测试骨架带 `t.Skip` 是门控集成测试的标准占位,已注明实现者补全点。

**类型一致性:** `Ops` 接口(T5)与 `fakeOps`(T5)/`liveOps`(T8)方法签名一致;`*In/*Out` 类型在 T5 定义、T6/T7 扩展同名结构(VerifyOut/SetupIn/SetTransportIn);`confirm.Guard`/`Snapshotter`/`ArmWithSnapshot`/`ErrNotArmed`(T3/T4)被 T7 一致引用;`errResultTyped`(T5)被 T5–T7 一致使用;`newServer`/`newServerWithGuard`(T7 改签名)与测试 `callToolOn`(T7)一致。
