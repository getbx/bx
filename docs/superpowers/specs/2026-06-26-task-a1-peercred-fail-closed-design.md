# A1 — peer-cred 鉴权 fail-closed 设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-26)。实现走单独的 plan。

## 背景与定位

9b-2a 最终 review 的 R1(安全,接真 mutation 前阻塞级):控制面 POST(改动类)路由的鉴权 `authorizeMutation(uid, known)` 在 `!known` 时**放行**(fail-open)。`known=false` 混了两种情况——darwin 平台拿不到 peer-cred(开发态)与 Linux 提取失败(bug/异常 conn)。后者在 Linux 上放行 = **CWE-636「Not Failing Securely」**。控制面 socket 在 9b-2a 已收为 `0o666`(开放 status),故 peer-cred 是改动类的**唯一闸**,必须稳。本任务是 9b-2b(真 mutation 路由)的安全前置。

**成熟产品调研结论**:CWE-636 规范、systemd/polkit、Tailscale、Docker —— **无一在拿不到凭证时 fail-open**;凭证验证不了即拒绝。故本设计比初版更严:**fail-closed 一视同仁(含 darwin)**。

## 目标 / 非目标

**目标**:把 `authorizeMutation` 改为 fail-closed —— 凭证验证不了就拒绝改动类命令,无论平台。纯 Mac 可测。

**非目标**:darwin 的 `LOCAL_PEERCRED` 真实现(留到 macOS 守护进程真做时);真 mutation 路由(9b-2b);任何 9b-2b/9b-3 内容。

## 设计

### 鉴权决策(fail-closed everywhere)

```go
// authorizeMutation:凭证验证不了就拒绝(CWE-636 fail-secure)。
// Linux 提取成功且 uid==0 → 放行;Linux 提取失败 → 拒;darwin 拿不到 peer-cred → 拒。
func authorizeMutation(uid uint32, gotUID bool) bool {
    return gotUID && uid == 0
}
```
签名从 `(uid, known)` 改为 `(uid, gotUID)`,语义同 `peerCredUID` 的第二返回值(是否成功提到 uid)。删掉旧的"`!known → true`"宽松分支。

### `peerCredSupported` 平台常量(仅供错误信息,不参与决策)

- `peercred_linux.go`:`const peerCredSupported = true`
- `peercred_other.go`:`const peerCredSupported = false`

用途:`requireRoot` 拒绝时据此给清晰 403 信息——
- `!peerCredSupported`(darwin):`"此平台暂不支持 peer-cred,改动类已拒绝;macOS daemon 待实现 LOCAL_PEERCRED"`
- 否则(linux 非 root / 提取失败):`"改动类命令需 root"`

### 调用点(control.go `requireRoot`)

`peerCredUID(conn)` 返回 `(uid, gotUID)`;`if !authorizeMutation(uid, gotUID) { 写 403(信息按 peerCredSupported 选); return false }`。**保留现有 `conn == nil → 放行`**(httptest TCP 旁路,仅测试路径;生产 serveControl 始终经 unix conn 设 ConnContext)。

## 错误处理

- 改动类被拒 → 403 + 上面两种信息之一。
- status(GET)不受影响(无鉴权)。

## 测试策略(全 Mac 原生,免 root)

`authorizeMutation` 表驱动纯单测,覆盖四组:
- `(0, true) → true`(linux root,提取成功)
- `(1000, true) → false`(linux 非 root)
- `(0, false) → false`(**Linux 提取失败 fail-closed —— 本次核心**)
- `(1000, false) → false`(darwin / 提取失败,uid 被忽略)

(9b-2a 旧测试 `TestAuthorizeMutation` 三参签名作废,改成两参 + 上述用例。)

## 决策记录

- fail-closed everywhere(含 darwin),非"darwin 宽松"——对齐 CWE-636 + Tailscale/systemd/Docker。
- `authorizeMutation(uid, gotUID)` = `gotUID && uid == 0`,删宽松分支。
- `peerCredSupported` 平台常量仅供 403 错误信息,不参与决策。
- 保留 `conn==nil` 测试旁路(httptest)。
- darwin LOCAL_PEERCRED 真实现留后;在此之前 darwin 守护进程改动类一律拒(正确的安全默认)。

## 范围自检

单一可实现小改(`peercred.go` 决策 + `peercred_*.go` 常量 + `control.go` 调用点 + 测试),全 Mac 可测。适合一份小 plan(1-2 任务)。
