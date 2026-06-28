# 业主 uid 授权(③ onboarding Slice ③-1)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-28)。

## 背景与定位

产品方向 ③(onboarding)用户定:**先做 agent 配对**——现状 `bx mcp`(agent 控制面)存在,但**无任何把它接进用户 agent 的路径**(无 `bx pair`、无 MCP 配置写入)。配对的拦路虎:agent(Claude Code/Desktop)以**用户(非 root)**身份起 `bx mcp` 子进程,而 A1 peer-cred 让改动类**仅 root**(status 读放行,`set_transport`/`rehijack`/`commit` 需 uid 0)。故 naive 配对只能读、不能操作 bx,违背"agent 操作 bx"。

用户选 **B 业主 uid 授权**:控制面授权 root **或**「配置的业主 uid」,fail-closed 对其余人。故 ③ 拆两片:**③-1 业主 uid 授权(本设计,基座)** → ③-2 `bx pair`(把 `bx mcp` 写进 agent,无需 sudo)。

## 目标 / 非目标

**目标**:`bx setup`(经 sudo)捕获调用者 `$SUDO_UID` 为业主、写进 `/etc/bx/config.yaml` 的 `owner_uid`;守护进程把 `owner_uid` 喂给控制面;`authorizeMutation` 从 root-only 精化为 **root 或业主 uid**(`owner_uid==0` 退回 root-only,安全默认)。全 Mac 原生可测决策逻辑。

**非目标**:`bx pair` 命令(③-2);darwin `LOCAL_PEERCRED`(peer-cred 在 darwin 仍不支持,业主授权在 Linux 生效,darwin 维持 fail-closed);多业主;owner 之外的细粒度授权。

## 架构

### 授权决策(peercred.go)

```go
// authorizeMutation:改动类路由鉴权(纯函数,fail-closed)。
// 放行 = 成功取到 uid 且(uid 是 root 或 = 配置的业主 uid)。
// ownerUID==0 表示无业主配置 → 退回 root-only(安全默认,如直接 root 跑 setup、或老配置)。
func authorizeMutation(uid uint32, gotUID bool, ownerUID uint32) bool {
    return gotUID && (uid == 0 || (ownerUID != 0 && uid == ownerUID))
}
```
签名加第三参 `ownerUID uint32`。语义:业主与 root 同权(改动类);其余人仍 403。

### 配置(config.go)

`Config` 加 `OwnerUID int `yaml:"owner_uid"``(0 = 无业主/root-only)。`Parse` 无需校验(0 合法 = 默认)。`owner_uid` 是控制面授权用的本机业主,非加密/分流项。

### 捕获业主(setup)

`bx setup` 经 `sudo` 跑时,真实用户在环境变量 `$SUDO_UID`。setup 写配置时:解析 `os.Getenv("SUDO_UID")` 为 int,非空且 >0 则写入 `owner_uid`;为空(直接 root 跑、无 sudo)则 `owner_uid` 留 0(root-only)。纯函数 `ownerUIDFromEnv(getenv func(string) string) int` 便于免环境单测。

### 插线(run.go → control)

- `run.go` 把 `cfg.OwnerUID`(转 `uint32`)传给 `serveControl`(增参 `ownerUID uint32`)。
- `serveControl` → `newControlMux(eng, report, mut, ownerUID)` → `controlServer` 增字段 `ownerUID uint32`。
- `requireRoot` 从自由函数改为方法 `func (cs *controlServer) requireRoot(w, r) bool`,内部 `authorizeMutation(uid, gotUID, cs.ownerUID)`;现有调用 `requireRoot(w, r)` 改 `cs.requireRoot(w, r)`(handlers 已是 `*controlServer` 方法)。保留 `conn==nil → 放行`(httptest 旁路)与 method gate 原样。

## 数据流 / 错误处理

`sudo bx setup` → 捕获 `$SUDO_UID` → 写 `owner_uid` → `sudo bx up` → systemd 跑 `bx run`(root)→ 读 `cfg.OwnerUID` → 控制面 `controlServer.ownerUID`。改动类请求:peer-cred 取 uid → root 或业主 → 放行;其余 → 403(信息沿用 A1)。status(GET)不受影响。`ownerUID==0`(未配置)→ 严格 root-only,fail-closed 不变。

## 测试策略(全 Mac 原生)

- `authorizeMutation` 表驱动(三参):`(0,true,1000)→true`(root)、`(1000,true,1000)→true`(业主)、`(1000,true,0)→false`(无业主→root-only,核心)、`(1001,true,1000)→false`(他人)、`(0,false,1000)→false`(取 uid 失败 fail-closed)。
- `ownerUIDFromEnv`:`SUDO_UID="1000"→1000`、空→0、非法→0、`"0"→0`。
- `config.Parse` 读 `owner_uid`(有/无字段)。
- 回归:既有控制面/peercred 测随签名更新仍绿;两平台编译;`GOOS=linux go vet -tags integration`。
- 真机:业主连 socket 改动获授权,与已验证的守护进程路径同(决策逻辑已单测,本片不另上真机)。

## 决策记录

- B 业主 uid 授权:控制面 root 或业主 uid;`owner_uid==0` 退回 root-only(安全默认)。优于 A(sudoers/NOPASSWD):无需动 sudoers、agent 以业主身份直跑 `bx mcp`、契合单业主产品模型。
- 业主 = `sudo bx setup` 的调用者(`$SUDO_UID`)。
- `authorizeMutation` 加 ownerUID 第三参;`requireRoot` 改 `controlServer` 方法。
- darwin 仍 fail-closed(peer-cred 未实现);业主授权 Linux 生效。
- ③-2 `bx pair` 据此免 sudo 配对(另开 spec)。

## 范围自检

单一可实现增量(config 字段 + setup 捕获 + authorizeMutation 三参 + control 插线 ownerUID + 单测),全 Mac 可测、决策纯函数。适合一份小 plan(2 任务:① authorizeMutation 三参 + control 插线;② config 字段 + setup 捕获)。
