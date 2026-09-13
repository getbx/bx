# 计划:Core 起不来时,说出它为什么起不来

设计 `docs/superpowers/specs/2026-09-13-core-start-failure-reason-design.md`。
起因是 2026-09-12 真机事故:VPS 连 ssh 与 ping 都不通,而 bx 对着 `sudo bx up` 反复回答
`core_ownership_uncertain`。**所有者原话:「vps 之前不通,但 bx 不会告诉我是 vps 不通,
用户会以为是 bx 自己的问题。」**

## 已核实的前提(动笔前不必重查,改动它们要回来改这份计划)

- `waitTunnelHealthy` 在 `run.go:305`,`OpenTUN` 在 469,控制 socket 在 788。
  ⇒ **卡在隧道健康那步的 Core 没开过 TUN、没装过路由、没碰过 DNS。**
- `waitTunnelHealthy` 的错误已经过 `withTunnelStderr` 带上 sing-box 的 stderr 环形缓冲
  ⇒ 那句 `i/o timeout` 一直在错误值里,缺的只是出口。
- `Manager.cleanupStartedCore` → `runner.Stop` → `shutdown(ctx, controlSocket, pid)`
  (`internal/guardian/process.go:544`),**失败即 return,从不回落 kill**。
- `ExecCoreRunner.cleanupStartedProcess` 用的是 `Terminate()` = `Kill()`,不碰 socket。
- `coreArgs` 拼 `["run","-c",cfg,"--listen-dns",dns]`;`looksLikeCore` 只认 `argv[1]=="run"`
  ⇒ 追加一个 flag 不影响进程扫描。
- `runAction` 有**两个** `supervisor.Run(...)` 出口(`cli.go:3554` windows service、`:3557` 常规)。

---

## Task 1 — 停止路径不再吊在一个按构造不存在的 socket 上

`Manager.cleanupStartedCore` 收拾的是**一个从没健康过的 Core**。给 runner 加一条明确的
「强行收掉」路径(复用既有的 `Terminate()`/`Wait()`,即 `cleanupStartedProcess` 那套),
Manager 清理走它,不走 `Stop` 的协作关闭。

**承重理由写进代码注释**:那个 Core 什么都没装,没有任何东西需要优雅还原;而
「先请它自己退出」依赖的正是它没能建出来的那个 socket。这是 2026-08-04 那次 71 分钟事故
立下的规矩 —— **停止路径不许依赖别的先成功**。

`Stop` 本身**一个字不改**(它收拾的是验明过身份、正在正常服务的 Core,协作关闭是对的)。

**守卫**:注入一个「socket 永远拨不通」的 shutdown,断言失败的 Core 仍被收干净、
`Manager` **不产出 `core_ownership_uncertain`**、且失败码是 `core_health_failed`。
变异:把回落 kill 去掉 ⇒ 必须转红。

## Task 2 — 启动失败的分类:哨兵错误,不碰字符串

`internal/supervisor` 导出启动失败哨兵并在各自产地 wrap:
`ErrTunnelUnhealthy` / `ErrTUNOpen` / `ErrHijack` / `ErrProvision` / `ErrConfig`。

判据是 `errors.Is`,**全程一条字符串匹配都不许有**。既有错误文案一个字不改
(它们进 Core 日志,是人读的)。

**守卫**:每个产地各一条,断言 `errors.Is` 认得出;另一条穷举断言**每个哨兵都有产地**
(加了哨兵没接上产地 = 一个永远不会出现的码)。

## Task 3 — 「隧道没起来」一分为二,靠观测不靠猜

`waitTunnelHealthy` 超时之后,对 `serverHostFromLink` 给出的 host:port **直连拨一次**
(5 秒上限,经 `DirectDialer()`):

- 拨不通 ⇒ `tunnel_unreachable`
- 拨得通 ⇒ `tunnel_handshake_failed`
- 判别本身失败/超时 ⇒ `tunnel_unhealthy_undetermined`,**不许挑一个**

理由见 spec §4.5:两者处置完全相反,而本仓库为第二种付过大代价(reality 全挂的真因是
默认 SNI 证书过大,当时先误归因成 sing-box 同机问题与 MITM)。

**守卫三条**:三种结局各一;**判别只在失败路径上发生**(白盒:注入的拨号器在成功启动那条路上
被调用即红 —— 这条守的是「不后台定时探测」那条边界不被顺手破坏);判别失败必须落
undetermined(注入恒超时的拨号器)。

## Task 4 — Core 自报:一条不含自由文本的启动失败记录

`bx run` 新增 `--start-failure-file <path>`(**只由 Guardian 传**;手敲的 `sudo bx run` 不传 ⇒
一个字都不写,从构造上断掉陈旧文件串味)。`runAction` 的**两个** Run 出口都要盖到 ——
抽一个薄壳包住,别在两处各写一遍。

Run 返回错误时,原子写:

```json
{"schema_version": 1, "pid": 26158, "at": "2026-09-12T18:01:51Z", "code": "tunnel_unreachable"}
```

**只有码,没有 detail 串** —— 按构造漏不出路径/链接/凭据。细节照旧进 Core 日志。

**守卫**:不传 flag 一个字节都不写;写的内容里不含链接与配置路径(逐字节断言);
写盘失败**不改变 Run 的返回错误**(诊断不许把故障换成另一个故障)。

## Task 5 — Guardian 读它:PID + 时间双重匹配,对不上就是「没说」

- **spawn 之前先删**那个文件(第一层),`coreArgs` 带上 `--start-failure-file`。
- `waitHealthy` 失败后读它:`pid` 必须等于本次 fork 的 PID,`at` 必须落在本次健康窗口内
  (第二层)。任何一项对不上、读不动、根本没有 ⇒ **「这一次没说」**,回落
  `core_health_failed`,**绝不猜**。
- 读完即删;删不掉只记日志,**不升级成失败**(停止/诊断路径不许因为别的事没做成而失败)。

**守卫**:三种陈旧形状各一条(PID 对不上 / `at` 在窗口外 / schema 认不出),一律回落
`core_health_failed`。变异:去掉 PID 匹配 ⇒ 上一次 spawn 的码会被这一次采信 ⇒ 必须转红。

## Task 6 — 两个客户端各自把那句可行动的话拼出来

**应答体仍然只带码,发布面一寸不扩**(spec §5):服务器地址与「你还有哪几台」两个客户端
本来就合法持有 —— `bx up` 以 root 读得到配置;菜单经 `/v1/servers` 拿到的条目本来就带
host/port。

- CLI(`guardianCodeHints`):三种隧道结局三句**措辞相反**的话 + 点名另一台可用服务器。
- 菜单:同样三句,英文;它从已有的 servers 清单里取候选。

**守卫**:三种结局在**渲染出来的整句话**上两两不同(不是比枚举值 —— 少了这条,三个分支
映射到同一句「隧道没起来」照样全绿,而那正是这次要消灭的东西);另一台服务器那句话
**只在真有另一台时出现**,且**绝不出现链接**(照 `TestServerListNeverShipsTheLinkItself`)。

## Task 7 — CLAUDE.md 记录

记这次事故的因果链、两条修法前提(为什么 kill 是安全的、为什么信息一直都在)、
以及**刻意没做**的那条(让 Core 先开控制 socket 再等隧道健康 —— `core_socket=true`
被观测层、调谐环准入、所有权判定到处在用,单独立项)。

---

## 不做

- 不自动切服务器(所有者定死的边界)。
- 不改「先等隧道健康再开控制 socket」的顺序。
- 不改 fail-closed。
- 不读 sing-box 的 stderr 做文本分类。

## 真机验收

把 `current` 指向一个不通的地址(或等下次 VPS 出事),`sudo bx up` 应当**一次**就说出
「连不上服务器 <host:port>」并点名另一台,**而不是**七次 `core_ownership_uncertain`。
