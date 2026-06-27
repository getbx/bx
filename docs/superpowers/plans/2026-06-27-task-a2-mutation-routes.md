# A2 — 控制面真 mutation 路由 + MCP 改接 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 给守护进程控制面加 `POST /v0/transport`+`/v0/rehijack` 真 mutation 路由(经 `mutator` 拿 apply/undo → `engine.Arm`),控制客户端 + MCP 工具改接守护进程;生产挂 nopMutator(brick-safe)。全 Mac 原生可测。

**Architecture:** `mutator` 接口把"set_transport/rehijack"翻译成 `(apply, undo)` 给 9b-1 引擎 `Arm`;routes 复用 A1 peer-cred + control mutex。MCP `bx_set_transport`/`bx_rehijack` 改接守护进程(对齐已做的 commit/rollback)。真 apply impl 留硬件刀,本 plan 生产挂 nopMutator。

**Tech Stack:** Go 1.26.3,纯 stdlib;复用现有 `internal/supervisor`(control.go/control_client.go/mutationengine.go)、`internal/mcp`、`internal/confirm`。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD,全 Mac 原生可测(httptest/unix 往返,免 root)。
- 提交信息中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- **不接真 apply**:本 plan 生产挂 `nopMutator`(apply/undo 返回 nil)。真 mutator impl(run.go 捕获 tun0/teardown/plat/cfg)是硬件刀,不在本 plan。
- **现有真实代码(实现前打开核对)**:
  - `control.go`:`controlEngine interface { Commit() error; Rollback() error; State() confirm.State }`(约 :25);`newControlMux(eng controlEngine, report func() stats.Report) http.Handler`(约 :69);`serveControl(c *stats.Counters, t tunnelStatser, server, udpMode string, eng controlEngine) (io.Closer, error)`(约 :165);`requireRoot`(peer-cred,A1 已 fail-closed);`controlResponse{Status,Error,State}`;`stateName(confirm.State) string`;`writeJSON`。
  - `mutationengine.go`:`(*mutationEngine).Arm(apply, undo func() error) error`、`Commit/Rollback/State`。
  - `control_client.go`:`controlHTTPClient(sockPath) *http.Client`、`postControl(sockPath, path) (string, error)`、`CommitControl/RollbackControl`。
  - `run.go`:`serveControl(counters, tun0, serverHost, cfg.UDP.Mode, mutEng)` 调用点(约 :262)。
  - `internal/mcp/tools_mutating.go`:`bx_commit`/`bx_rollback` 已改为 `ops.Commit()`/`ops.Rollback()` + `errors.As(ToolError)` 透传(**这是 set_transport/rehijack 改接的模板**);`bx_set_transport`/`bx_rehijack` 仍用 `armThen(g, snap, …)`。`SetTransportIn{Link string}`。
  - `internal/mcp/liveops.go`:`SetTransport(in SetTransportIn) error` / `Rehijack() error` 现返回 `CodeNotImplemented` ToolError;`Commit/Rollback` 已调 `supervisor.CommitControl/RollbackControl(supervisor.SockPath)`(**模板**)。

---

### Task 1: mutator 接口 + nopMutator

**Files:**
- Create: `internal/supervisor/mutator.go`
- Create: `internal/supervisor/mutator_test.go`

**Interfaces:**
- Produces:
  - `type mutator interface { SetTransport(link string) (apply func() error, undo func() error, err error); Rehijack() (apply func() error, undo func() error, err error) }`
  - `type nopMutator struct{}` 实现 mutator,apply/undo 返回 nil 的闭包,err nil。

- [ ] **Step 1: 写失败测试**

Create `internal/supervisor/mutator_test.go`:
```go
package supervisor

import "testing"

func TestNopMutator(t *testing.T) {
	var m mutator = nopMutator{}
	apply, undo, err := m.SetTransport("vless://x@h:443")
	if err != nil {
		t.Fatalf("SetTransport err: %v", err)
	}
	if apply == nil || undo == nil {
		t.Fatal("apply/undo 不应为 nil 闭包")
	}
	if err := apply(); err != nil {
		t.Fatalf("nop apply 应 nil: %v", err)
	}
	if err := undo(); err != nil {
		t.Fatalf("nop undo 应 nil: %v", err)
	}
	a2, u2, err := m.Rehijack()
	if err != nil || a2() != nil || u2() != nil {
		t.Fatalf("Rehijack nop 应全 nil: err=%v", err)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run NopMutator -v`
Expected: `mutator`/`nopMutator` undefined。

- [ ] **Step 3: 写实现**

Create `internal/supervisor/mutator.go`:
```go
// mutator 把一次改动翻译成 commit-confirmed 引擎要的 (apply, undo)。
// fake 测、nopMutator 生产(A2)、真 impl 留硬件刀(run.go 捕获 tun0/teardown/plat/cfg)。
package supervisor

// mutator:改动类操作的执行器。apply 执行改动;undo 语义回滚(路由还原另有 9a 快照网兜底)。
type mutator interface {
	SetTransport(link string) (apply func() error, undo func() error, err error)
	Rehijack() (apply func() error, undo func() error, err error)
}

// nopMutator:不做任何真实改动(A2 生产挂载)。full commit-confirmed 回路仍真实跑,
// 只是 apply/undo 为空 —— brick-safe。真 mutator 接入是硬件刀。
type nopMutator struct{}

func nop() error { return nil }

func (nopMutator) SetTransport(string) (func() error, func() error, error) { return nop, nop, nil }
func (nopMutator) Rehijack() (func() error, func() error, error)           { return nop, nop, nil }
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/supervisor/ -run NopMutator -v && go vet ./internal/supervisor/`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/mutator.go internal/supervisor/mutator_test.go
git commit -m "feat(supervisor): mutator 接口 + nopMutator(改动类执行器接缝)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: 守护进程 mutation 路由(/v0/transport、/v0/rehijack)

**Files:**
- Modify: `internal/supervisor/control.go`(controlEngine 加 Arm;controlServer 持 mut;两 handler;newControlMux/serveControl +mut 参数)
- Modify: `internal/supervisor/control_test.go`(fakeControlEngine 加 Arm;路由测试)
- Modify: `internal/supervisor/run.go`(serveControl 调用点传 `nopMutator{}`,保持编译)

**Interfaces:**
- Consumes: `mutator`/`nopMutator`(Task 1);`(*mutationEngine).Arm`、`confirm.ErrAlreadyArmed`(现有)。
- Produces:
  - `controlEngine` 接口增 `Arm(apply func() error, undo func() error) error`。
  - `newControlMux(eng controlEngine, report func() stats.Report, mut mutator) http.Handler`。
  - `serveControl(c, t, server, udpMode string, eng controlEngine, mut mutator) (io.Closer, error)`。
  - 路由 `POST /v0/transport`(body `{"link":"..."}`)、`POST /v0/rehijack`。

- [ ] **Step 1: 写失败测试**

打开 `control_test.go`,给 `fakeControlEngine` 加字段与 `Arm`:
```go
// 在 fakeControlEngine 结构里加:
//   armErr  error
//   armed   bool
//   applied bool
func (f *fakeControlEngine) Arm(apply, undo func() error) error {
	if f.armErr != nil {
		return f.armErr
	}
	if apply != nil {
		_ = apply()
		f.applied = true
	}
	f.armed = true
	return nil
}
```
并把所有 `testMux(...)`/`newControlMux(...)` 调用补上第三参 `mut`(传一个 `nopMutator{}` 或下面的 fakeMutator)。新增 fakeMutator + 路由测试:
```go
type fakeMutator struct {
	gotLink   string
	setErr    error
	setCalled bool
	rehCalled bool
}

func (f *fakeMutator) SetTransport(link string) (func() error, func() error, error) {
	f.setCalled = true
	f.gotLink = link
	if f.setErr != nil {
		return nil, nil, f.setErr
	}
	return func() error { return nil }, func() error { return nil }, nil
}
func (f *fakeMutator) Rehijack() (func() error, func() error, error) {
	f.rehCalled = true
	return func() error { return nil }, func() error { return nil }, nil
}

func testMuxMut(eng controlEngine, mut mutator) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} }, mut)
}

func TestControlSetTransportArmed(t *testing.T) {
	mut := &fakeMutator{}
	eng := &fakeControlEngine{state: confirm.StateArmed}
	srv := httptest.NewServer(testMuxMut(eng, mut))
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/v0/transport", "application/json",
		strings.NewReader(`{"link":"vless://x@h:443"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("armed 应 200,得 %d", resp.StatusCode)
	}
	if !mut.setCalled || mut.gotLink != "vless://x@h:443" || !eng.armed {
		t.Fatalf("应调 mut.SetTransport(link) 且 engine.Arm;mut=%+v armed=%v", mut, eng.armed)
	}
}

func TestControlSetTransportEmptyLink(t *testing.T) {
	mut := &fakeMutator{}
	srv := httptest.NewServer(testMuxMut(&fakeControlEngine{}, mut))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v0/transport", "application/json", strings.NewReader(`{"link":""}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 link 应 400,得 %d", resp.StatusCode)
	}
	if mut.setCalled {
		t.Fatal("空 link 不应调 mut")
	}
}

func TestControlSetTransportAlreadyArmed(t *testing.T) {
	eng := &fakeControlEngine{armErr: confirm.ErrAlreadyArmed}
	srv := httptest.NewServer(testMuxMut(eng, &fakeMutator{}))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v0/transport", "application/json", strings.NewReader(`{"link":"vless://x@h:443"}`))
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("已 armed 应 409,得 %d", resp.StatusCode)
	}
}

func TestControlRehijackArmed(t *testing.T) {
	mut := &fakeMutator{}
	eng := &fakeControlEngine{state: confirm.StateArmed}
	srv := httptest.NewServer(testMuxMut(eng, mut))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v0/rehijack", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !mut.rehCalled || !eng.armed {
		t.Fatalf("rehijack 应 200 + mut.Rehijack + Arm;code=%d mut=%+v armed=%v", resp.StatusCode, mut, eng.armed)
	}
}
```
(测试文件需 import `strings`。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run 'Control(SetTransport|Rehijack)' -v`
Expected: 编译失败(`Arm` 不在 controlEngine / `newControlMux` 三参未定义)。

- [ ] **Step 3: 写实现**

`control.go`:
1. `controlEngine` 接口加方法:
```go
type controlEngine interface {
	Arm(apply func() error, undo func() error) error
	Commit() error
	Rollback() error
	State() confirm.State
}
```
2. `controlServer` 加 `mut mutator` 字段;`newControlMux` 签名加 `mut mutator`,存入 `cs.mut`,并注册两路由:
```go
func newControlMux(eng controlEngine, report func() stats.Report, mut mutator) http.Handler {
	cs := &controlServer{eng: eng, report: report, mut: mut}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", cs.handleStatus)
	mux.HandleFunc("/v0/commit", cs.handleCommit)
	mux.HandleFunc("/v0/rollback", cs.handleRollback)
	mux.HandleFunc("/v0/transport", cs.handleSetTransport)
	mux.HandleFunc("/v0/rehijack", cs.handleRehijack)
	return mux
}
```
3. 两 handler(复用 `requireRoot` + `cs.mu`):
```go
type setTransportReq struct {
	Link string `json:"link"`
}

func (cs *controlServer) handleSetTransport(w http.ResponseWriter, r *http.Request) {
	if !requireRoot(w, r) {
		return
	}
	var req setTransportReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Link == "" {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: "缺 link"})
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	apply, undo, err := cs.mut.SetTransport(req.Link)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: err.Error()})
		return
	}
	cs.armAndRespond(w, apply, undo)
}

func (cs *controlServer) handleRehijack(w http.ResponseWriter, r *http.Request) {
	if !requireRoot(w, r) {
		return
	}
	cs.mu.Lock()
	defer cs.mu.Unlock()
	apply, undo, err := cs.mut.Rehijack()
	if err != nil {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: err.Error()})
		return
	}
	cs.armAndRespond(w, apply, undo)
}

// armAndRespond 调 engine.Arm 并映射状态码(调用方须持 cs.mu)。
func (cs *controlServer) armAndRespond(w http.ResponseWriter, apply, undo func() error) {
	err := cs.eng.Arm(apply, undo)
	state := stateName(cs.eng.State())
	if err != nil {
		if errors.Is(err, confirm.ErrAlreadyArmed) {
			writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "已有待确认的改动", State: state})
			return
		}
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: err.Error(), State: state})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "armed", State: state})
}
```
注:`requireRoot` 已含 method gate(非 POST → 405);若现有 `requireRoot` 不 gate method,在两 handler 开头加 `if r.Method != http.MethodPost { writeJSON(w, 405, …); return }`。**打开核对 requireRoot 真实行为**。`errors`/`encoding/json` 应已 import。
4. `serveControl` 签名加 `mut mutator`,传给 `newControlMux(... , mut)`:
```go
func serveControl(c *stats.Counters, t tunnelStatser, server, udpMode string, eng controlEngine, mut mutator) (io.Closer, error) {
	// ... 现有 report 闭包不变 ...
	srv := &http.Server{
		Handler:           newControlMux(eng, report, mut),
		ReadHeaderTimeout: 5 * time.Second,
		ConnContext:       func(ctx context.Context, conn net.Conn) context.Context { return context.WithValue(ctx, ctxConnKey{}, conn) },
	}
	// ... 其余不变 ...
}
```
(以现有 serveControl 真实 body 为准,只加 mut 参数 + 传参。)

`run.go` 调用点(约 :262)改为:
```go
		return serveControl(counters, tun0, serverHost, cfg.UDP.Mode, mutEng, nopMutator{})
```

- [ ] **Step 4: 跑绿 + 全量**

Run:
```bash
go test ./internal/supervisor/ -run Control -v
go test ./internal/supervisor/ ./internal/mcp/ ./internal/cli/ && go vet ./... && go build ./...
GOOS=linux go build -o /dev/null ./... && GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 新 4 路由测试 + 既有 Control 测试 PASS;全套件绿;两平台编译。
注:`fakeControlEngine` 之前没 Arm,现加上后既有 commit/rollback 测试不受影响(它们不调 Arm)。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/control.go internal/supervisor/control_test.go internal/supervisor/run.go
git commit -m "feat(supervisor): /v0/transport + /v0/rehijack 路由(经 mutator → engine.Arm),生产挂 nopMutator

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: 控制客户端 SetTransportControl / RehijackControl

**Files:**
- Modify: `internal/supervisor/control_client.go`(加带 body 的 POST + 两函数)
- Modify: `internal/supervisor/control_client_test.go`(往返测)

**Interfaces:**
- Consumes: 现有 `controlHTTPClient`、`controlResponse`。
- Produces:
  - `func SetTransportControl(sockPath, link string) (state string, err error)` —— POST `/v0/transport` body `{"link":link}`。
  - `func RehijackControl(sockPath string) (state string, err error)` —— POST `/v0/rehijack`。

- [ ] **Step 1: 写失败测试**

打开 `control_client_test.go`(看现有 `CommitControl` 往返测怎么起临时 socket + handler),追加:
```go
func TestSetTransportControl(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/bx.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/transport", func(w http.ResponseWriter, r *http.Request) {
		var req struct{ Link string `json:"link"` }
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Link == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(controlResponse{Status: "error", Error: "缺 link"})
			return
		}
		_ = json.NewEncoder(w).Encode(controlResponse{Status: "armed", State: "armed"})
	})
	go (&http.Server{Handler: mux}).Serve(ln)
	state, err := SetTransportControl(sock, "vless://x@h:443")
	if err != nil || state != "armed" {
		t.Fatalf("SetTransportControl state=%q err=%v", state, err)
	}
	if _, err := SetTransportControl(sock, ""); err == nil {
		t.Fatal("空 link 服务端 400,客户端应返回错误")
	}
}
```
(若 RehijackControl 想测,仿 CommitControl 的现有测试再加一个;最少覆盖 SetTransportControl。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run SetTransportControl -v`
Expected: `SetTransportControl` undefined。

- [ ] **Step 3: 写实现**

在 `control_client.go` 加一个带 body 的 POST 辅助 + 两函数(复用现有 `controlHTTPClient`,参考现有 `postControl`):
```go
// postControlBody:POST path,带可选 JSON body;返回 controlResponse.State,非 2xx → error(含 Error)。
func postControlBody(sockPath, path string, body any) (string, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		rd = bytes.NewReader(b)
	}
	resp, err := client.Post("http://local"+path, "application/json", rd)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out controlResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", fmt.Errorf("控制面 %s 返回 %d: %s", path, resp.StatusCode, out.Error)
		}
		return "", fmt.Errorf("控制面 %s 返回 %d", path, resp.StatusCode)
	}
	return out.State, nil
}

func SetTransportControl(sockPath, link string) (string, error) {
	return postControlBody(sockPath, "/v0/transport", map[string]string{"link": link})
}

func RehijackControl(sockPath string) (string, error) {
	return postControlBody(sockPath, "/v0/rehijack", nil)
}
```
注:确保 `io`/`bytes` 已 import(现有 `postControl` 已用 `bytes`)。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/supervisor/ -run 'SetTransportControl|Control' -v && go vet ./internal/supervisor/`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/control_client.go internal/supervisor/control_client_test.go
git commit -m "feat(supervisor): SetTransportControl/RehijackControl 控制客户端

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: MCP bx_set_transport/bx_rehijack 改接守护进程 + liveOps un-stub

**Files:**
- Modify: `internal/mcp/tools_mutating.go`(两工具从 armThen 改为 ops.SetTransport/Rehijack)
- Modify: `internal/mcp/liveops.go`(SetTransport/Rehijack 从 CodeNotImplemented 改调控制客户端)
- Modify: `internal/mcp/tools_mutating_test.go` / `fakeops_test.go`(断言改接)

**Interfaces:**
- Consumes: `supervisor.SetTransportControl/RehijackControl`(Task 3)、`supervisor.SockPath`;`Ops.SetTransport(SetTransportIn)`/`Rehijack()`(现有接口)。

- [ ] **Step 1: 看模板 + 写失败测试**

**打开 `tools_mutating.go` 看 `bx_commit`/`bx_rollback` 已经怎么改的**(`ops.Commit()` + `errors.As(ToolError)` 透传)——`bx_set_transport`/`bx_rehijack` 照同模板。在 `tools_mutating_test.go` 加(用 fakeOps;若 fakeOps 缺 SetTransport/Rehijack 调用记录,在 `fakeops_test.go` 加 `calls` 记录,参考已有写法):
```go
func TestSetTransportToolForwardsToOps(t *testing.T) {
	ops := &fakeOps{}
	res := callTool(t, ops, "bx_set_transport", map[string]any{"link": "vless://x@h:443"})
	if res.IsError {
		t.Fatalf("不应错误")
	}
	// 断言 fakeOps.SetTransport 被调(用 fakeOps 的 calls 或 lastLink 字段)
	if ops.lastSetTransportLink != "vless://x@h:443" {
		t.Fatalf("应转发 link 给 ops.SetTransport,得 %q", ops.lastSetTransportLink)
	}
}
```
(在 `fakeops_test.go` 给 fakeOps 加 `lastSetTransportLink string`,其 `SetTransport(in SetTransportIn) error` 记 `f.lastSetTransportLink = in.Link; return f.setTransportErr`;`Rehijack()` 记 `f.rehijackCalled=true`。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run 'SetTransportTool|Rehijack' -v`
Expected: FAIL(bx_set_transport 仍走 armThen,没调 ops.SetTransport)。

- [ ] **Step 3: 改实现**

`tools_mutating.go` —— `bx_set_transport` 工具改为(对齐 bx_commit 模板):
```go
	mcpsdk.AddTool(s, &mcpsdk.Tool{Name: "bx_set_transport", Description: "switch transport to a new link; armed under commit-confirmed", Annotations: dx},
		func(_ context.Context, _ *mcpsdk.CallToolRequest, in SetTransportIn) (*mcpsdk.CallToolResult, armedOut, error) {
			if in.Link == "" {
				return errResultTyped[armedOut](ToolError{Code: CodeLinkInvalid, Message: "link 不能为空"})
			}
			if err := ops.SetTransport(in); err != nil {
				var te ToolError
				if errors.As(err, &te) {
					return errResultTyped[armedOut](te)
				}
				return errResultTyped[armedOut](ToolError{Code: CodeTunnelUnhealthy, Message: err.Error()})
			}
			return nil, armedOut{Status: "armed", Note: armedNote}, nil
		})
```
`bx_rehijack` 同理改为调 `ops.Rehijack()`(无 link 参数)。**删掉这两工具原来的 `armThen(g, snap, …)` 调用**;若 `g`/`snap` 在 `registerMutating` 里只剩被这两个用,确认 commit/rollback 已不用它们后按编译需要清理(只删不再引用的;不动别的)。

`liveops.go` —— `SetTransport`/`Rehijack` 改为:
```go
func (o *liveOps) SetTransport(in SetTransportIn) error {
	if _, err := supervisor.SetTransportControl(supervisor.SockPath, in.Link); err != nil {
		return ToolError{Code: CodeTunnelUnhealthy, Message: "set_transport 失败: " + err.Error(),
			Remediation: "确认 bx 守护进程在跑(bx up)且本机有权限"}
	}
	return nil
}

func (o *liveOps) Rehijack() error {
	if _, err := supervisor.RehijackControl(supervisor.SockPath); err != nil {
		return ToolError{Code: CodeTunnelUnhealthy, Message: "rehijack 失败: " + err.Error(),
			Remediation: "确认 bx 守护进程在跑(bx up)"}
	}
	return nil
}
```

- [ ] **Step 4: 跑绿 + 全量**

Run:
```bash
go test ./internal/mcp/ -v
go test ./... && go vet ./... && go build ./...
GOOS=linux go build -o /dev/null ./... && GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
```
Expected: 全绿(注:`internal/tunnel` 的 `/bin/true` macOS 预存失败无关,忽略)。

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/tools_mutating.go internal/mcp/liveops.go internal/mcp/tools_mutating_test.go internal/mcp/fakeops_test.go
git commit -m "feat(mcp): bx_set_transport/bx_rehijack 改接守护进程控制面(对齐 commit/rollback)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- mutator 接口 + nopMutator → Task 1。
- /v0/transport + /v0/rehijack 路由(mutator → engine.Arm,peer-cred,mutex)→ Task 2。
- controlEngine 加 Arm / newControlMux+serveControl +mut / run.go 挂 nopMutator → Task 2。
- SetTransportControl/RehijackControl 客户端 → Task 3。
- MCP 两工具改接 + liveOps un-stub → Task 4。
- 错误处理(空 link 400 / 已 armed 409 / 非 root 403 / apply 失败)→ Task 2 handler + armAndRespond。
- 全 Mac 可测(httptest/unix 往返/fakeOps)→ Task 1-4 全 Mac 测。
- 不接真 apply(nopMutator)→ Task 1 + Task 2 run.go。

**占位扫描:** 无 TBD;新代码完整。Task 2/3/4 多处"打开核对现有 requireRoot/postControl/commit 模板"是改 9b-2a/用户现有码、不臆造其确切上下文,非占位。

**类型一致性:** `mutator`/`nopMutator`(T1)被 T2 `serveControl`/run.go 引用;`controlEngine.Arm`+`newControlMux(eng,report,mut)`+`serveControl(...,mut)`(T2)一致;`fakeControlEngine.Arm`/`fakeMutator`(T2 测)一致;`SetTransportControl/RehijackControl`(T3)被 T4 liveOps 调用一致;`Ops.SetTransport(SetTransportIn)/Rehijack()`(现有接口)被 T4 工具与 liveOps 实现一致;复用 `(*mutationEngine).Arm`、`confirm.ErrAlreadyArmed`、`requireRoot`、`controlResponse`、`stateName`、`SockPath` 均现状。
