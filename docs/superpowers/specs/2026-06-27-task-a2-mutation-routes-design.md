# A2 — 控制面真 mutation 路由 + MCP 改接(set_transport / rehijack)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-27)。实现走单独的 plan。

## 背景与定位

9b 控制面收口的下一步:让 agent 经控制面**操作传输**(换 brook↔REALITY / 重劫持)。现状(含用户并行成果):
- 守护进程已有 commit-confirmed 引擎(`mutEng`,9a 真快照器 + 死手)+ 控制面 HTTP(`GET /v0/status`、`POST /v0/commit|rollback`)。
- `bx_commit`/`bx_rollback` MCP 工具已转发到守护进程(`ops.Commit/Rollback`→`CommitControl/RollbackControl`);status 透出 `mutation_state`;`bx inspect` 诊断包已成型。
- **缺**:守护进程 `POST /v0/transport`、`/v0/rehijack` **真 mutation 路由**;`bx_set_transport`/`bx_rehijack` MCP 工具仍打 MCP 本地 Guard(未接守护进程);liveOps 的 `SetTransport`/`Rehijack` 仍是 `CodeNotImplemented` 桩。

**范围决策**:A2 = 协议路由 + 控制客户端 + MCP 改接 + **nop mutator**,全 Mac 原生可测;**真 apply 执行(真换隧道/重劫持)留硬件那一刀**(B 阶段)。理由:真 apply 碰 `tun0`/`teardown`/真实路由,只能真机验;但 agent 经 socket 驱动 arm→commit→revert 的**整条回路用 nop mutator 即可端到端验**(回路真,只那一下"改"为空)。brick-safe,与现有 liveOps 桩 / nopSnapshotter 同模式。

## 目标 / 非目标

**目标**:守护进程 `/v0/transport`+`/v0/rehijack` 路由(经 `mutator` 拿 apply/undo → `engine.Arm`);控制客户端 `SetTransportControl`/`RehijackControl`;MCP `bx_set_transport`/`bx_rehijack` 改接守护进程(删本地 Guard armThen);liveOps 对应 ops un-stub;生产挂 nop mutator。全 Mac 原生可测。

**非目标(留 B 阶段硬件刀)**:真 mutator impl(run.go 捕获 tun0/teardown/plat/cfg,真换隧道/重劫持);守护进程 e2e(真的执行 mutation);serveControl-before-Hijack 的真实排序解决。

## 架构

### mutator 接口(control 依赖,可 fake)

```go
// mutator 把一次改动翻译成 commit-confirmed 引擎要的 (apply, undo)。
// fake 测、nop 生产(本 A2)、真 impl 留硬件刀。
type mutator interface {
    SetTransport(link string) (apply func() error, undo func() error, err error)
    Rehijack()               (apply func() error, undo func() error, err error)
}
```
- `SetTransport`:link 非法 → 立即 err(不 Arm);否则返回 (换隧道 apply, 用旧 link 重启的 undo)。
- `Rehijack`:返回 (重装劫持 apply, nil undo)——**undo 靠 9a 路由快照网**(`engine.Arm` 的 restore 已含 `snapshotter.Restore`,兜路由还原)。
- **nopMutator**(本 A2 生产用):apply/undo 均为 `func() error { return nil }`,err nil。full loop 真、改动为空 → brick-safe。

### 守护进程路由(control.go)

- `controlServer` 增持 `mut mutator` + `engine *mutationEngine`(现已有 `eng controlEngine` 供 commit/rollback;Arm 需具体引擎或扩接口含 `Arm`)。
- `POST /v0/transport`:peer-cred root → 读 body `{link}`(空 link → 400)→ `apply, undo, err := mut.SetTransport(link)`(err → 400/500)→ `engine.Arm(apply, undo)`(ErrAlreadyArmed → 409)→ 200 `{status:"armed", state}`。
- `POST /v0/rehijack`:peer-cred root → `mut.Rehijack()` → `engine.Arm` → 200 armed / 409。
- 复用现有 control mutex(串行)+ peer-cred(`requireRoot`,A1 已 fail-closed)+ `controlResponse`。
- `controlEngine` 接口扩 `Arm(apply, undo func() error) error`(`*mutationEngine` 已实现);或路由直接持 `*mutationEngine`。以编译/可 fake 为准。

### 控制客户端(control_client.go,对齐 CommitControl)

```go
func SetTransportControl(sockPath, link string) (state string, err error) // POST /v0/transport {link}
func RehijackControl(sockPath string)          (state string, err error) // POST /v0/rehijack
```
(复用现有 `controlHTTPClient` + `postControl`;set_transport 需带 JSON body。)

### MCP 改接(对齐已做的 commit/rollback)

- `internal/mcp` 的 `bx_set_transport`/`bx_rehijack` 工具:**删除现在的本地 Guard `armThen`**,改为调 `ops.SetTransport(in)`/`ops.Rehijack()`,错误经 `errors.As(ToolError)` 透传(同 commit/rollback 已有写法)。
- liveOps:`SetTransport`/`Rehijack` 从 `CodeNotImplemented` 桩改成调 `supervisor.SetTransportControl(SockPath, in.Link)`/`RehijackControl(SockPath)`,成功 nil、失败映射 ToolError。

### run.go 挂载

`serveControl(..., eng, mut)` 增 `mut` 参数,生产传 `nopMutator{}`(真 mutator impl 留硬件刀)。

## 错误处理

- link 非法/空 → 400(不 Arm)。
- 已 armed → 409(ErrAlreadyArmed)。
- 非 root POST → 403(A1 peer-cred)。
- apply 失败 → 引擎 `Arm` 内已 Rollback + 返回(沿用 9b-1);路由回 500 含信息。
- nop mutator:apply/undo 永 nil → Arm 成功 → armed。

## 测试策略(全 Mac 原生,免 root)

- **路由 handler 单测**(httptest + fakeMutator + 真 `*mutationEngine`(注入 fakeSnapshotter+fake 时钟)):`POST /v0/transport`(armed 200 / 空 link 400 / 已 armed 409)、`POST /v0/rehijack`(armed 200)。fakeMutator 记录 apply/undo 被调。
- **控制客户端往返**:真临时 unix socket + 路由 → `SetTransportControl`/`RehijackControl` 解析 state(对齐现有 control_client 测试)。
- **MCP 工具测**:fakeOps 断言 `bx_set_transport`/`bx_rehijack` 调 `ops.SetTransport/Rehijack` 且透传 ToolError(对齐已做的 commit/rollback 工具测)。
- **nopMutator**:apply/undo 返回 nil 的纯单测。
- **回归**:既有 status/commit/rollback 路由 + MCP 不受影响;两平台编译;`GOOS=linux go vet -tags integration`。

## 决策记录

- A2 = 协议/客户端/MCP 改接 + nop mutator(Mac 可测);真 apply 留硬件刀。
- `mutator` 接口 `(apply, undo, err)`;rehijack undo 靠 9a 快照网(nil undo);set_transport undo = 旧 link 重启(真 impl)。
- MCP set_transport/rehijack 改接守护进程(删本地 Guard armThen),对齐已做的 commit/rollback。
- 生产挂 nopMutator(brick-safe,full loop 真、改动空)。
- 复用 A1 peer-cred(fail-closed)+ control mutex + controlResponse + control_client。

## 范围自检

单一可实现增量(mutator 接口 + 2 路由 + 2 客户端 + MCP 2 工具改接 + liveOps un-stub + nopMutator),全 Mac 可测,不接真 apply / 不做守护进程 e2e。适合一份 plan(4-6 任务)。
