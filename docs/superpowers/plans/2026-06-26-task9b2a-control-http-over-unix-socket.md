# Task 9b-2a — 控制面 HTTP over unix socket Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 bx 守护进程的只读 stats socket 升级成 HTTP-over-unix-socket 控制面(Tailscale LocalAPI 范式):`GET /v0/status` / `POST /v0/commit` / `POST /v0/rollback`;把 9b-1 引擎挂进 `bx run` 并补 onRevert 日志 seam;POST 走 peer-cred 鉴权;`liveOps.Status()` 迁到 HTTP 客户端。全 Mac 原生可测。

**Architecture:** `net/http` mux 跑在现有 `SockPath` 的 unix listener 上(替换 serveStats 的推送)。handler 经 `controlEngine` 接口调 9b-1 引擎,control 级 mutex 串行化。peer-cred 经 `http.Server.ConnContext` 把 net.Conn 塞进 ctx,POST handler 取出做 SO_PEERCRED(Linux)。不接真 mutation(`/v0/transport`、`/v0/rehijack` = 9b-2b/9b-3)。

**Tech Stack:** Go 1.26.3,纯 stdlib `net/http`(零新依赖);复用 9b-1 `mutationEngine`、`internal/confirm`、`internal/stats`、现有 `SockPath`。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD,全 Mac 原生可测(httptest/unix 往返,免 root)。
- 提交信息中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- **不接真 mutation**:9b-2a 只 status/commit/rollback。引擎挂进守护进程但只被这三者驱动,brick-safe。
- 复用的真实 API(已存在,实现前打开核对):9b-1 `newMutationEngine(snapper confirm.Snapshotter, window time.Duration, now func() time.Time) *mutationEngine`(本 plan Task 1 给它加 onRevert 参数)、`(*mutationEngine).Commit/Rollback/State()/tick()/Run(ctx)`;`confirm.State`(StateIdle/Armed/Committed/Reverted)、`confirm.ErrNotArmed`;现有 `serveStats(c *stats.Counters, t *tunnel.Tunnel, server, udpMode string) (io.Closer, error)`(run.go:332,调用点 run.go:252)及其 Report 构造;`SockPath`(paths_*.go);`internal/mcp` 的 `liveOps.Status()`(读 socket 解码 Report)。
- peer-cred:Linux 用 `golang.org/x/sys/unix`(已有依赖)的 `unix.GetsockoptUcred(fd, SOL_SOCKET, SO_PEERCRED)`。

---

### Task 1: 引擎 onRevert seam(关 9b-1 carry-forward M3/N1)

**Files:**
- Modify: `internal/supervisor/mutationengine.go`(`newMutationEngine` 加 onRevert;加 `tickOnce`;`Run` 调 `tickOnce`)
- Modify: `internal/supervisor/mutationengine_test.go`(更新现有调用 + 加 onRevert/Run 测试)

**Interfaces:**
- Produces:
  - `func newMutationEngine(snapper confirm.Snapshotter, window time.Duration, now func() time.Time, onRevert func(reverted bool, err error)) *mutationEngine`(新增末位参数,可为 nil)
  - `func (e *mutationEngine) tickOnce()`(tick 一次;若 reverted 或 err 非空且 onRevert 非 nil 则调之)
  - `Run` 改为每 2s 调 `tickOnce`。
- Consumes: 9b-1 引擎(同文件)。

- [ ] **Step 1: 写失败测试(更新现有 + 新增)**

在 `mutationengine_test.go`:把测试用的 `newMutationEngine(...)` / `newTestEngine` 调用都补上末位 onRevert 实参(现有用 nil 或 recording)。`newTestEngine` 改为:
```go
func newTestEngine(snapper confirm.Snapshotter, clk *engClock) *mutationEngine {
	return newMutationEngine(snapper, 240*time.Second, clk.now, nil)
}
```
新增:
```go
func TestEngineTickOnceFiresOnRevert(t *testing.T) {
	snapper := &engFakeSnapper{}
	clk := &engClock{t: time.Unix(0, 0)}
	var gotRev bool
	var gotErr error
	called := 0
	e := newMutationEngine(snapper, 240*time.Second, clk.now, func(rev bool, err error) {
		called++
		gotRev, gotErr = rev, err
	})
	if err := e.Arm(func() error { return nil }, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(241 * time.Second)
	e.tickOnce()
	if called != 1 || !gotRev || gotErr != nil {
		t.Fatalf("到点 tickOnce 应调 onRevert(reverted=true,err=nil);called=%d rev=%v err=%v", called, gotRev, gotErr)
	}
}

func TestEngineTickOnceNoFireBeforeDeadline(t *testing.T) {
	clk := &engClock{t: time.Unix(0, 0)}
	called := 0
	e := newMutationEngine(&engFakeSnapper{}, 240*time.Second, clk.now, func(bool, error) { called++ })
	if err := e.Arm(func() error { return nil }, nil); err != nil {
		t.Fatal(err)
	}
	clk.t = clk.t.Add(100 * time.Second)
	e.tickOnce()
	if called != 0 {
		t.Fatalf("未到点不应调 onRevert,called=%d", called)
	}
}

func TestEngineRunExitsOnCtxCancel(t *testing.T) {
	e := newMutationEngine(&engFakeSnapper{}, 240*time.Second, (&engClock{t: time.Unix(0, 0)}).now, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run 未在 ctx 取消后退出")
	}
}
```
(测试文件需 import `context`、`time`。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run 'Engine' -v`
Expected: 编译失败(newMutationEngine 参数数不符 / `tickOnce` undefined)。

- [ ] **Step 3: 改实现**

`mutationengine.go`:加 `onRevert` 字段,构造加参数,加 `tickOnce`,`Run` 调它:
```go
type mutationEngine struct {
	guard    *confirm.Guard
	snapper  confirm.Snapshotter
	onRevert func(reverted bool, err error)
}

func newMutationEngine(snapper confirm.Snapshotter, window time.Duration, now func() time.Time, onRevert func(reverted bool, err error)) *mutationEngine {
	return &mutationEngine{guard: confirm.New(window, now), snapper: snapper, onRevert: onRevert}
}

// tickOnce:tick 一次;到点自动 revert(或 revert 出错)时通知 onRevert。
func (e *mutationEngine) tickOnce() {
	rev, err := e.guard.Tick()
	if (rev || err != nil) && e.onRevert != nil {
		e.onRevert(rev, err)
	}
}

// Run:每 2s tickOnce,直到 ctx 取消。
func (e *mutationEngine) Run(ctx context.Context) {
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.tickOnce()
		}
	}
}
```
(删掉旧的 `tick()` 若不再被测试引用;若 9b-1 测试仍用 `e.tick()`,保留 `tick()` 并让 `tickOnce` 调用它:`rev, err := e.tick()`。检查 `mutationengine_test.go` 现有用法决定。)

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/supervisor/ -run Engine -v && go vet ./internal/supervisor/ && go build ./...`
Expected: 全部 Engine 测试(含 3 新)PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/mutationengine.go internal/supervisor/mutationengine_test.go
git commit -m "feat(supervisor): 引擎 onRevert 日志 seam + tickOnce(关 9b-1 carry-forward)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: peer-cred helper + 鉴权决策

**Files:**
- Create: `internal/supervisor/peercred_linux.go`(`//go:build linux`)
- Create: `internal/supervisor/peercred_other.go`(`//go:build !linux`)
- Create: `internal/supervisor/peercred.go`(无 tag:纯鉴权决策)
- Create: `internal/supervisor/peercred_test.go`(无 tag:决策单测)

**Interfaces:**
- Produces:
  - `func peerCredUID(conn net.Conn) (uid uint32, known bool)` —— Linux 经 SO_PEERCRED 取 uid(known=true);其它平台 known=false。
  - `func authorizeMutation(uid uint32, known bool) bool` —— 纯决策:known 且 uid==0 放行;**!known(darwin 开发态)宽松放行**(注释标真机待收紧)。

- [ ] **Step 1: 写失败测试**

Create `internal/supervisor/peercred_test.go`:
```go
package supervisor

import "testing"

func TestAuthorizeMutation(t *testing.T) {
	cases := []struct {
		uid   uint32
		known bool
		want  bool
	}{
		{0, true, true},    // root 放行
		{1000, true, false}, // 非 root 拒绝
		{0, false, true},   // 平台无 peer-cred(darwin 开发态)宽松放行
	}
	for _, c := range cases {
		if got := authorizeMutation(c.uid, c.known); got != c.want {
			t.Fatalf("authorizeMutation(%d,%v)=%v want %v", c.uid, c.known, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run Authorize -v`
Expected: `authorizeMutation` undefined。

- [ ] **Step 3: 写实现**

Create `internal/supervisor/peercred.go`:
```go
package supervisor

// authorizeMutation 是 POST(改动类)路由的鉴权决策(纯函数,便于测试)。
// 拿到 peer uid(known)时仅 root 放行;平台取不到 peer-cred(known=false,如 darwin)
// 时开发态宽松放行 —— 真机/生产应收紧(见 peercred_other.go)。
func authorizeMutation(uid uint32, known bool) bool {
	if !known {
		return true // TODO(真机): darwin 收紧为 LOCAL_PEERCRED
	}
	return uid == 0
}
```

Create `internal/supervisor/peercred_linux.go`:
```go
//go:build linux

package supervisor

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerCredUID 经 SO_PEERCRED 取 unix 连接对端进程的 uid。
func peerCredUID(conn net.Conn) (uint32, bool) {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return 0, false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, false
	}
	var cred *unix.Ucred
	var serr error
	if err := raw.Control(func(fd uintptr) {
		cred, serr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || serr != nil || cred == nil {
		return 0, false
	}
	return cred.Uid, true
}
```

Create `internal/supervisor/peercred_other.go`:
```go
//go:build !linux

package supervisor

import "net"

// peerCredUID 在非 Linux 平台暂不取 peer-cred(known=false → 开发态宽松)。
// darwin 真机应实现 LOCAL_PEERCRED(getsockopt + xucred)后收紧。
func peerCredUID(conn net.Conn) (uint32, bool) { return 0, false }
```

- [ ] **Step 4: 跑绿**

Run:
```bash
go test ./internal/supervisor/ -run Authorize -v && go vet ./internal/supervisor/
GOOS=linux go vet ./internal/supervisor/
```
Expected: 决策测试 PASS;linux + darwin 都编得过。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/peercred.go internal/supervisor/peercred_linux.go internal/supervisor/peercred_other.go internal/supervisor/peercred_test.go
git commit -m "feat(supervisor): peer-cred 鉴权(SO_PEERCRED)+ authorizeMutation 决策

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: 控制面 HTTP server(mux + handlers + serveControl)

**Files:**
- Create: `internal/supervisor/control.go`(无 tag)
- Create: `internal/supervisor/control_test.go`(无 tag)

**Interfaces:**
- Produces:
  - `type controlEngine interface { Commit() error; Rollback() error; State() confirm.State }`
  - `func newControlMux(eng controlEngine, report func() stats.Report) http.Handler`
  - `func serveControl(c *stats.Counters, t *tunnel.Tunnel, server, udpMode string, eng controlEngine) (io.Closer, error)`(替换 serveStats:建 reportFn → mux → unix listener → http.Server,ConnContext 塞 conn)
- Consumes: `peerCredUID`/`authorizeMutation`(Task 2)、`controlEngine`(本任务)、`SockPath`、`stats.Report`/`tunnel.Tunnel`/`stats.Counters`(现有)。

- [ ] **Step 1: 写失败测试**

Create `internal/supervisor/control_test.go`:
```go
package supervisor

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/stats"
)

type fakeControlEngine struct {
	commitErr   error
	rollbackErr error
	state       confirm.State
}

func (f *fakeControlEngine) Commit() error      { return f.commitErr }
func (f *fakeControlEngine) Rollback() error    { return f.rollbackErr }
func (f *fakeControlEngine) State() confirm.State { return f.state }

func testMux(eng controlEngine) http.Handler {
	return newControlMux(eng, func() stats.Report { return stats.Report{Server: "test-node"} })
}

func TestControlStatus(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{}))
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/v0/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status code=%d", resp.StatusCode)
	}
	var rep stats.Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		t.Fatal(err)
	}
	if rep.Server != "test-node" {
		t.Fatalf("got %+v", rep)
	}
}

func TestControlCommitOK(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{state: confirm.StateCommitted}))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v0/commit", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("commit ok 应 200,得 %d", resp.StatusCode)
	}
}

func TestControlCommitNotArmed(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{commitErr: confirm.ErrNotArmed}))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v0/commit", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("nothing to commit 应 409,得 %d", resp.StatusCode)
	}
}

func TestControlRollbackError(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{rollbackErr: errors.New("回滚也失败")}))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v0/rollback", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("rollback 出错应 500,得 %d", resp.StatusCode)
	}
}

func TestControlStatusRejectsPost(t *testing.T) {
	srv := httptest.NewServer(testMux(&fakeControlEngine{}))
	defer srv.Close()
	resp, _ := http.Post(srv.URL+"/v0/status", "", nil)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("status POST 应 405,得 %d", resp.StatusCode)
	}
}
```
注:`httptest.NewServer` 用 TCP,**不经 unix-socket peer-cred**,故 POST 在测试里默认放行——这恰好测 handler 主逻辑;peer-cred 门控走 `authorizeMutation` 单测(Task 2)+ ConnContext 接线(Step 3,真 unix 才生效)。

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run Control -v`
Expected: `newControlMux`/`controlEngine` undefined。

- [ ] **Step 3: 写实现**

Create `internal/supervisor/control.go`:
```go
// control.go 是 bx 守护进程的本地控制面:HTTP/1.1 over unix socket(Tailscale LocalAPI 范式)。
// GET /v0/status 返回 Report;POST /v0/commit|rollback 驱动 commit-confirmed 引擎(peer-cred 仅 root)。
// 取代旧的"连上就推 Report"私有协议。真 mutation 路由(/v0/transport、/v0/rehijack)留 9b-2b/9b-3。
package supervisor

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/getbx/bx/internal/confirm"
	"github.com/getbx/bx/internal/stats"
)

type controlEngine interface {
	Commit() error
	Rollback() error
	State() confirm.State
}

type controlResponse struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
	State  string `json:"state,omitempty"`
}

type ctxConnKey struct{}

type controlServer struct {
	mu     sync.Mutex // 串行化命令(满足并发契约)
	eng    controlEngine
	report func() stats.Report
}

func stateName(s confirm.State) string {
	switch s {
	case confirm.StateArmed:
		return "armed"
	case confirm.StateCommitted:
		return "committed"
	case confirm.StateReverted:
		return "reverted"
	default:
		return "idle"
	}
}

func newControlMux(eng controlEngine, report func() stats.Report) http.Handler {
	cs := &controlServer{eng: eng, report: report}
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", cs.handleStatus)
	mux.HandleFunc("/v0/commit", cs.handleCommit)
	mux.HandleFunc("/v0/rollback", cs.handleRollback)
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (cs *controlServer) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	cs.mu.Lock()
	rep := cs.report()
	cs.mu.Unlock()
	writeJSON(w, http.StatusOK, rep)
}

// requireRoot 对 POST 做 peer-cred 鉴权(unix 连接时);非 unix(如 httptest TCP)放行。
func requireRoot(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	conn, _ := r.Context().Value(ctxConnKey{}).(net.Conn)
	if conn == nil {
		return true // 无 conn(测试 TCP):放行,鉴权决策由 authorizeMutation 单测覆盖
	}
	uid, known := peerCredUID(conn)
	if !authorizeMutation(uid, known) {
		writeJSON(w, http.StatusForbidden, controlResponse{Status: "error", Error: "改动类命令需 root"})
		return false
	}
	return true
}

func (cs *controlServer) handleCommit(w http.ResponseWriter, r *http.Request) {
	if !requireRoot(w, r) {
		return
	}
	cs.mu.Lock()
	err := cs.eng.Commit()
	state := stateName(cs.eng.State())
	cs.mu.Unlock()
	if err != nil {
		if errors.Is(err, confirm.ErrNotArmed) {
			writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "nothing to commit", State: state})
			return
		}
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: err.Error(), State: state})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "committed", State: state})
}

func (cs *controlServer) handleRollback(w http.ResponseWriter, r *http.Request) {
	if !requireRoot(w, r) {
		return
	}
	cs.mu.Lock()
	err := cs.eng.Rollback()
	state := stateName(cs.eng.State())
	cs.mu.Unlock()
	if err != nil {
		if errors.Is(err, confirm.ErrNotArmed) {
			writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "nothing to rollback", State: state})
			return
		}
		writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: err.Error(), State: state})
		return
	}
	writeJSON(w, http.StatusOK, controlResponse{Status: "reverted", State: state})
}

// serveControl 在 SockPath 上跑控制面 HTTP server(替换 serveStats)。
func serveControl(c *stats.Counters, t tunnelStatser, server, udpMode string, eng controlEngine) (io.Closer, error) {
	report := func() stats.Report {
		ts := t.Stats()
		return stats.Report{
			Snapshot:      c.Snapshot(),
			Server:        server,
			SocksAddr:     t.SocksAddr(),
			TunnelHealthy: ts.Up,
			LatencyMS:     ts.LatencyMS,
			Restarts:      ts.Restarts,
			UDPMode:       udpMode,
			UDPNote:       udpNote(udpMode),
		}
	}
	_ = os.MkdirAll(filepath.Dir(SockPath), 0o755)
	_ = os.Remove(SockPath)
	ln, err := net.Listen("unix", SockPath)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(SockPath, 0o660)
	srv := &http.Server{
		Handler: newControlMux(eng, report),
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, ctxConnKey{}, conn)
		},
	}
	go srv.Serve(ln)
	return ln, nil
}
```
注:`tunnelStatser` 是为解耦 serveControl 与具体 `*tunnel.Tunnel` 的小接口(`Stats()` 返回隧道统计、`SocksAddr() string`)。**打开 `internal/supervisor/run.go` 的 serveStats(约 332 行)核对 `t.Stats()` 返回类型与字段(Up/LatencyMS/Restarts)、`t.SocksAddr()`,据此定义 `tunnelStatser` 接口**(放 control.go),让 `*tunnel.Tunnel` 自动满足。若嫌接口麻烦,直接用 `t *tunnel.Tunnel` 具体类型亦可(与 serveStats 一致)——二选一,以编译通过为准,在报告里说明选了哪种。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/supervisor/ -run Control -v && go vet ./internal/supervisor/ && go build ./...`
Expected: 6 个 Control 测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/control.go internal/supervisor/control_test.go
git commit -m "feat(supervisor): 控制面 HTTP over unix socket(status/commit/rollback)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: 迁移 `liveOps.Status()` 到 HTTP 客户端

**Files:**
- Modify: `internal/mcp/liveops.go`(`Status()` 改用 unix-socket HTTP 客户端打 `GET /v0/status`)
- Create/Modify: `internal/mcp/liveops_test.go`(对真控制 server 的 unix 往返回归测)

**Interfaces:**
- Consumes: 控制面 `GET /v0/status`(Task 3);`supervisor.SockPath`。
- Produces: 不变(`Status() (StatusOut, error)` 签名不变)。

- [ ] **Step 1: 读现有 `Status()` 并写失败回归测试**

打开 `internal/mcp/liveops.go` 的 `Status()`(现为 dial SockPath + decode `stats.Report`)。新增/改 `internal/mcp/liveops_test.go`:起一个真控制 server(用 `supervisor` 不可导出,故测试改在 `package supervisor` 起 server + 用 `mcp` 客户端逻辑——**或**把 `Status()` 的 HTTP 拨号逻辑抽成可注入 socket 路径的小函数,在 mcp 测试里指向一个临时 unix HTTP server)。推荐后者:抽 `func statusOverSocket(sockPath string) (stats.Report, error)`,测试起一个临时 `net.Listen("unix", tmp)` + 一个返回固定 Report 的 `GET /v0/status` handler,断言 `statusOverSocket` 解析正确。
```go
func TestStatusOverSocket(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/bx.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil { t.Fatal(err) }
	defer ln.Close()
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/status", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(stats.Report{TunnelHealthy: true, LatencyMS: 42})
	})
	go http.Serve(ln, mux)
	rep, err := statusOverSocket(sock)
	if err != nil { t.Fatalf("statusOverSocket: %v", err) }
	if !rep.TunnelHealthy || rep.LatencyMS != 42 {
		t.Fatalf("got %+v", rep)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run StatusOverSocket -v`
Expected: `statusOverSocket` undefined。

- [ ] **Step 3: 实现 `statusOverSocket` + 改 `Status()` 调它**

在 `liveops.go` 加:
```go
func statusOverSocket(sockPath string) (stats.Report, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 1 * time.Second}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	resp, err := client.Get("http://local/v0/status")
	if err != nil {
		return stats.Report{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return stats.Report{}, fmt.Errorf("控制面 /v0/status 返回 %d", resp.StatusCode)
	}
	var rep stats.Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return stats.Report{}, err
	}
	return rep, nil
}
```
把 `Status()` 里"dial+decode"那段替换为 `rep, err := statusOverSocket(supervisor.SockPath)`(socket 不可达 → 现有的 `CodeTunnelUnhealthy` ToolError 逻辑保留)。调整 import(`net`/`net/http`/`context`/`time`/`fmt`)。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/mcp/ -v && go vet ./internal/mcp/ && go build ./...`
Expected: 全 mcp 测试(含 StatusOverSocket)PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/mcp/liveops.go internal/mcp/liveops_test.go
git commit -m "feat(mcp): liveOps.Status() 迁到控制面 HTTP 客户端(GET /v0/status)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: 把引擎 + 控制面挂进 `bx run` 守护进程

**Files:**
- Modify: `internal/supervisor/run.go`(实例化引擎 + onRevert log + go Run + serveStats 调用换 serveControl)

**Interfaces:**
- Consumes: `newMutationEngine`(Task 1)、`NewSystemSnapshotter()`(9a)、`serveControl`(Task 3)。

- [ ] **Step 1: 改 run.go**

打开 `internal/supervisor/run.go`。在创建 stats socket 之前(serveStats 调用点约 run.go:252 附近),加:
```go
	// commit-confirmed 引擎:挂进守护进程,接 9a 真快照器;onRevert 大声记日志。
	eng := newMutationEngine(NewSystemSnapshotter(), 240*time.Second, time.Now, func(reverted bool, err error) {
		if err != nil {
			log.Printf("死手自动回滚失败(系统可能半改动): %v", err)
		} else if reverted {
			log.Printf("死手自动回滚:已还原到 last-known-good")
		}
	})
	go eng.Run(ctx)
```
把
```go
	if closer, err := serveStats(counters, tun0, serverHost, cfg.UDP.Mode); err != nil {
```
改为
```go
	if closer, err := serveControl(counters, tun0, serverHost, cfg.UDP.Mode, eng); err != nil {
```
(其余 defer closer.Close() / os.Remove(SockPath) 不变。)删除旧 `serveStats` 函数(已被 serveControl 取代;`udpNote` 仍被 serveControl 用,保留)。确认 `ctx`/`log`/`time` 在 run.go 已 import(`log` 已用;`ctx` 是 Run 的参数;`time` 已用)。
**注意**:`NewSystemSnapshotter()` 是 `//go:build linux`(9a)。run.go 无 build tag,在 darwin 上编译会找不到它 → 需要一个 darwin 占位。打开看 `internal/mcp/server.go` 现有的 `newSystemSnapshotter()` nop(返回 nopSnapshotter)是怎么跨平台的,照搬:加 `systemsnapshot_other.go`(`//go:build !linux`)提供一个返回 nop `confirm.Snapshotter` 的 `NewSystemSnapshotter()`,使 run.go 在 darwin 也编得过。**这是必需的跨平台缝合,在报告里说明。**

- [ ] **Step 2: 全量验证**

Run:
```bash
cd /Users/nategu_mac_company/Documents/bx
go build ./... && go vet ./... && go test ./...
GOOS=linux go build -o /dev/null ./... && GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 全绿;linux 与 darwin 都编得过(darwin 靠 `NewSystemSnapshotter()` nop 占位)。
注:`bx run` 守护进程实跑收命令执行是 9b-2b 的 e2e(需 Linux 守护进程),本任务只验证编译 + 接线正确 + 既有套件不回归。

- [ ] **Step 3: 手测控制面 server 起得来(可选,Linux/Colima)**

如有 Linux:跑一个最小 harness 或在 netns 内起 serveControl + `curl --unix-socket`。本机 macOS 仅验证 `go test`/编译。报告注明。

- [ ] **Step 4: 提交**

```bash
git add internal/supervisor/run.go internal/supervisor/systemsnapshot_other.go
git commit -m "feat(supervisor): 引擎+控制面挂进 bx run 守护进程(serveControl 取代 serveStats)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- HTTP over unix socket(GET status / POST commit / rollback)→ Task 3。
- 引擎挂 run.go + onRevert log seam(关 9b-1 M3)→ Task 1(seam)+ Task 5(挂载+log)。
- Run tickLoop 测(关 9b-1 N1)→ Task 1(TestEngineRunExitsOnCtxCancel + tickOnce 测)。
- peer-cred(GET 开放、POST 仅 root,SO_PEERCRED)+ socket 0o660 → Task 2(决策+helper)+ Task 3(requireRoot+ConnContext+Chmod)。
- control mutex 串行化 → Task 3 `controlServer.mu`。
- liveOps.Status() 迁移 → Task 4。
- 全 Mac 原生可测 → Task 1-4 全 Mac 测;Task 5 编译+回归。
- 不接真 mutation → 无 transport/rehijack 路由。

**占位扫描:** 无 TBD;新文件代码完整。Task 3 的 `tunnelStatser` 接口 vs 具体类型、Task 4 的 `statusOverSocket` 抽取、Task 5 的 darwin nop 占位 —— 均为"打开现有文件核对真实签名后二选一/照搬"的明确实现指令(因涉及现有 run.go/tunnel 的真实类型,不臆造),非占位。

**类型一致性:** `controlEngine`(T3)由 `*mutationEngine`(T1,有 Commit/Rollback/State)满足;`newMutationEngine` 加 onRevert(T1)被 T5 调用一致;`peerCredUID`/`authorizeMutation`(T2)被 T3 `requireRoot` 调用一致;`serveControl`(T3)被 T5 调用一致(签名镜像 serveStats + eng);`statusOverSocket`(T4)+ `SockPath` 一致。
