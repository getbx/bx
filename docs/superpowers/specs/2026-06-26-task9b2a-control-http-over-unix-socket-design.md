# Task 9b-2a — 控制面命令协议(HTTP over unix socket)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-26)。实现走单独的 plan。

## 背景与定位

Task 9b(让 MCP 改动类 ops 真正操作 bx)拆:9b-1 守护进程 commit-confirmed 引擎(已交付 + CI 真机验)· **9b-2a 控制面命令协议 + 引擎挂 run.go** · 9b-2b rehijack 真 mutation + MCP socket 客户端(需守护进程 Linux e2e)· 9b-3 真隧道 mutation(需真机)。本 spec 只做 **9b-2a**——命令通道的协议层,**全 Mac 原生可测**,不接真 mutation、不需守护进程 e2e。

**设计依据(成熟产品调研)**:本地控制面收敛模式 = **HTTP/1.1 over unix domain socket**。Tailscale `tailscaled` 的 LocalAPI(unix socket 上跑 HTTP 服务器,CLI 是 HTTP 客户端,SO_PEERCRED 鉴权)是 Go 单静态二进制守护进程的范式;Docker 同理;clash/mihomo 的 RESTful API,且 Clash Verge Rev 2026 已从 TCP HTTP 迁到 IPC(unix socket)承载同一套 HTTP——为安全。故 bx 采同款,取代现有"连上就推 Report"的私有 socket 协议。

## 目标 / 非目标

**目标**:把现有只读 stats socket 升级成 HTTP-over-unix-socket 控制面(`GET /v0/status`、`POST /v0/commit`、`POST /v0/rollback`);把 9b-1 引擎挂进 `bx run` 守护进程并补 onRevert 日志 seam;POST 路由 peer-cred 鉴权。全 Mac 原生可测(httptest/unix 往返,免 root)。

**非目标(留 9b-2b/9b-3)**:真 mutation 路由(`/v0/transport`、`/v0/rehijack`);MCP 改动 tool 变 socket 客户端;守护进程 e2e(`bx run` 实跑收命令执行真 mutation)。9b-2a 引擎挂进守护进程但只被 status/commit/rollback 驱动,brick-safe。

## 架构

### 控制面 = HTTP over unix socket(取代 serveStats 的推送协议)

- 新 `internal/supervisor/control.go`:`http.ServeMux` + handlers,跑在现有 `SockPath` 的 unix listener 上(`net.Listen("unix", SockPath)` → `http.Server{Handler: mux}.Serve(ln)`)。替换现有 `serveStats` 的"每连接推一份 Report"。
- 路由(9b-2a):
  - `GET /v0/status` → 200 `stats.Report` JSON(取代旧推送)。**不鉴权**(只读)。
  - `POST /v0/commit` → 引擎 `Commit()`;成功 200 `{status:"committed"}`;`ErrNotArmed` → 409 `{status:"error", error:"nothing to commit"}`。
  - `POST /v0/rollback` → 引擎 `Rollback()`;成功 200 `{status:"reverted"}`;`ErrNotArmed` → 409 `{status:"error", error:"nothing to rollback"}`;revert 报错 → 500 含 message。
  - 未知路由 → 404。
- **并发**:`http.Server` 每请求一 goroutine,故 control 持一把 `sync.Mutex` **串行化所有 handler**,满足 9b-1 review 的"命令串行"契约(9b-2b 的 Arm 副作用也被这把锁罩住)。

### 引擎挂进守护进程(run.go)

- `Run()` 实例化 `newMutationEngine(NewSystemSnapshotter(), 240*time.Second, time.Now, onRevert)`,`go eng.Run(ctx)`,引擎引用传给 control server。
- **关掉 9b-1 阻塞级 carry-forward(M3)**:`newMutationEngine` 增 `onRevert func(reverted bool, err error)` 参数;`Run` 每 tick 后,若 reverted 或 err 非空则调 `onRevert`。run.go 传 log 回调:`log.Printf("死手自动回滚: reverted=%v err=%v", ...)`——revert(尤其 revert 失败)大声记录。

### 鉴权(Tailscale 同款 peer-cred)

- `GET /v0/status` 不鉴权(只读)。
- `POST` 路由走 **SO_PEERCRED(Linux),仅 uid==0 放行**,否则 403。小平台 helper `peerCredUID(conn) (uint32, error)`:`peercred_linux.go`(SO_PEERCRED via `unix.GetsockoptUcred`)+ `peercred_other.go`(darwin 等:返回 unknown,开发态宽松放行 + 注释标真机待做)。
- socket 文件权限从 `0o666` 收紧到 `0o660`。

### 客户端迁移(强制)

- `internal/mcp` 的 `liveOps.Status()`:从"dial socket 直接 decode Report"改为 unix-socket 上的 `http.Client` 打 `GET /v0/status` 解码 Report。这是协议改动强制的一处迁移(否则 `bx status` 断)。`http.Client{Transport:{DialContext: 拨 unix}}` + `GET http://local/v0/status`(host 占位)。

## 数据结构

```go
type controlResponse struct {
    Status string        `json:"status"`           // "committed"|"reverted"|"error"
    Error  string        `json:"error,omitempty"`
    State  string        `json:"state,omitempty"`  // 引擎死手状态
}
// GET /v0/status 直接返回 stats.Report(沿用现有类型)。
```

## 错误处理

- `Commit`/`Rollback` 遇 `confirm.ErrNotArmed` → 409 + 明确 message;其它 error → 500。
- revert 内部失败(undo/Restore)→ Rollback 返回 joined error → 500 含"回滚也失败"(沿用 9b-1 I1 的上浮)。
- 非 root POST → 403。
- 未知路由/方法 → 404/405。
- handler panic → `http.Server` 自带 recover,不挂守护进程(另可加 recover middleware 记日志)。

## 测试策略(全 Mac 原生,免 root)

- **handler 单测**:`mux` + fakeEngine(Commit/Rollback/State 可控返回)+ fake reportFn。用 `net/http/httptest`(`httptest.NewServer` 或直接调 handler),断言 `GET /v0/status`(200+Report)、`POST /v0/commit`(200 / 409 ErrNotArmed)、`POST /v0/rollback`(200 / 409 / 500)、未知路由(404)。
- **真 unix-socket 往返**:`net.Listen("unix", t.TempDir()+"/sock")` + control server + `http.Client` 拨 unix → 端到端验协议(Mac 上 unix socket 正常,免 root)。覆盖 `liveOps.Status()` 迁移后的回归。
- **`Run` tickLoop 测(关 9b-1 N1)**:cancellable ctx + fake 时钟 + fake snapshotter,Arm→推进时钟→`Run` 触发 revert→断言 onRevert 被调(reverted=true)。
- **peer-cred**:uid 判定纯逻辑单测;真 SO_PEERCRED 往返门控(Linux,需要时)。
- **不需 root/netns**:9b-2a 全部在 Mac 上跑绿。真 mutation e2e 是 9b-2b/9b-3。

## 决策记录

- 控制面 = HTTP/1.1 over unix socket(Tailscale LocalAPI 范式),取代私有推送协议;`net/http` 纯 stdlib,零新依赖。
- 路由 `GET /v0/status` / `POST /v0/commit` / `POST /v0/rollback`;真 mutation 路由留 9b-2b/9b-3。
- control 级 mutex 串行化 handler(并发契约)。
- 引擎挂 run.go + onRevert log seam(关 9b-1 M3);`newMutationEngine` 增 onRevert 参数。
- peer-cred:GET 开放,POST 仅 root(Linux SO_PEERCRED);socket 0o660。
- `liveOps.Status()` 迁移到 HTTP 客户端(协议改动强制)。
- 全 Mac 原生可测;不接真 mutation、不需守护进程 e2e。

## 范围自检

单一可实现增量(HTTP 控制面 + 引擎挂载 + onRevert + peer-cred + status 迁移),全 Mac 可测,不接真 mutation / 不做守护进程 e2e。适合一份 plan。
