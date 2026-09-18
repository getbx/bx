# Core 起不来时,说出它为什么起不来

**日期**:2026-09-13
**状态**:设计,待实施
**起因**:2026-09-12 真机事故。所有者的 VPS(203.0.113.92)连 ssh 与 ping 都不通,而 bx 对着
`sudo bx up` 反复回答 `core_ownership_uncertain` —— 一句关于「系统里可能有第二个 Core」的话。
**所有者原话:「vps 之前不通,但 bx 不会告诉我是 vps 不通,用户会以为是 bx 自己的问题。」**

---

## 1. 事故里那条信息丢失链

Core 从第一秒就知道:`dial tcp 203.0.113.92:443: i/o timeout`。它被逐层剥掉:

| 环节 | 手里有什么 | 交出去了什么 |
|---|---|---|
| Core | 到具体 host:port 的连接超时 | 只写进 root-only 的 `/var/log/bx.log`,然后退出 |
| Guardian 等健康 | 「socket 20 秒没出现」 | 不知道 Core 为什么没起来 —— 它从不读 Core 的日志或退出原因 |
| Guardian 清理 | 要收拾这个失败的 Core | **经 `core.sock` 请它自己退出** —— 那个 socket 按构造不存在 ⇒ 清理失败 |
| CLI | `core_ownership_uncertain` | 三百字「系统里可能有第二个 Core」的排查指引 |

两处结构性事实,后面的设计都挂在它们上:

- **`supervisor.Run` 里 `waitTunnelHealthy` 在 305 行,控制 socket 在 788 行。** 隧道不健康 ⇒ Run
  返回错误 ⇒ Core 在建出 socket **之前**退出。这一段是设计如此(fail-closed),**本设计一个字不改**。
- **`OpenTUN` 在 469 行、劫持更靠后。** 卡在隧道健康那步的 Core **没开过 TUN、没装过路由、没碰过
  DNS** —— 身上没有任何东西需要优雅还原。这是「清理改走 kill」能成立的承重前提,已实测核实。

---

## 2. 三个缺陷

### 2.1 停止路径吊在一个按构造不存在的 socket 上(结构性)

`Manager.cleanupStartedCore` → `runner.Stop` → `shutdown(ctx, controlSocket, pid)`
(`internal/guardian/process.go:544`)。**一个「没能起来的 Core」按定义就是「没有 socket 的
Core」** —— 这条清理路在最需要它的时候必定失败。而 `Stop` 在那一步**直接 return,从不回落到
kill**。

违反的是 2026-08-04 那次 71 分钟事故立下的规矩:**停止路径不许依赖别的先成功**。

原语就在隔壁:`Terminate()` = `Kill()`,`ExecCoreRunner.cleanupStartedProcess` 一直在用,
只有 Manager 这条路没用。

**副作用**:被丢下的 Core 要等自己那 20 秒超时才自杀,窗口恰好盖住用户的重试 —— 于是
`guardian_core_still_running_on_release pids=26331` **指着 bx 自己没清干净的孤儿**,
把用户派去杀一个 bx 自己造的进程。

### 2.2 失败码被顶掉

`manager.go:1367` 本来就有 `core_health_failed`,那是真话;清理一失败就被
`core_ownership_uncertain` 覆盖。修掉 2.1 之后这条自动好。

### 2.3 即便码对了,也没说为什么(所有者真正要的那条)

`core_health_failed` 仍然答不出「是 VPS 不通」。而 bx 手里有全部素材:哪台服务器、什么错、
**以及用户配了第二台**(他配了 203.0.113.92 与 203.0.113.123,bx 一个字没提)。

---

## 3. 要做成什么样

`tunnel_unreachable`(这次事故的那一种):

```
bx 起不来:连不上服务器 203.0.113.92:443 —— 那台机器上的这个端口没有应答。
  · 它可能挂了、或者换了 IP。确认:nc -z 203.0.113.92 443
  · 你还配了另一台:203.0.113.123 —— sudo bx server use <名字>
```

`tunnel_handshake_failed`(措辞必须相反,否则等于把人派去修一台好机器):

```
bx 起不来:服务器 203.0.113.92:443 在应答,但隧道 20 秒内没能建起来。
  服务器是活的 —— 问题在这条链接、凭据、或路上的干扰,不在那台机器死没死。
  · 完整原因:sudo tail -50 /var/log/bx.log
  · 你还配了另一台:203.0.113.123 —— sudo bx server use <名字>
```

`tunnel_unhealthy_undetermined`:如实说「隧道没起来,而 bx 没能判断出那台服务器还在不在」,
**两条动作都给,一条都不许断言**。

---

## 4. 机制:Core 自报,Guardian 按 PID 匹配着读

### 4.1 为什么不是「Guardian 去读 Core 的日志」

按日志文本分类是本仓库明确反对的形状(判据不许长在文本匹配上)。而且那份日志是**多次 spawn
共用**的,分不清哪几行属于这一次。

### 4.2 为什么不是「给 Core 开一条管道」

CLAUDE.md 记着一次实打实的死锁:管道的 EOF 被**孙进程**(Core 自己 spawn 的 sing-box)继承,
父进程永远等不到。不再走管道。

### 4.3 采用:一条结构化的启动失败记录

Core 在 `bx run` 的错误路径上,原子写 `/var/lib/bx/core-start-failure.json`:

```json
{"schema_version": 1, "pid": 26158, "at": "2026-09-12T18:01:51Z", "code": "tunnel_unreachable"}
```

- **只有码,没有 detail 串。** 细节照旧进 Core 日志。一个不含自由文本的记录,按构造漏不出
  路径/链接/凭据。
- **Guardian 按 `pid` **与** `at` 在本次健康窗口内**双重匹配**。对不上、读不动、根本没有 ⇒
  **「这一次没说」**,回落 `core_health_failed`,绝不猜。(与 `Status.Capabilities` 无
  `omitempty` 同一条纪律:陈旧记录看起来与生效中的一模一样,而这个仓库为陈旧文件栽过三次 ——
  `upgrade-intent.json`、`core-process.json`、那份四分之三是假的缺口清单。)
- 读完即删;删不掉只记日志,**不升级成失败**(停止/诊断路径不许因为别的事没做成而失败)。

### 4.4 分类靠哨兵错误,不靠字符串

`supervisor` 导出一组启动失败哨兵,`Run` 在各自产地 wrap:

| 码 | 哨兵 | 产地 |
|---|---|---|
| `tunnel_unreachable` / `tunnel_handshake_failed` | `ErrTunnelUnhealthy` + §4.5 那次判别 | `waitTunnelHealthy` 超时 |
| `tun_open_failed` | `ErrTUNOpen` | `plat.OpenTUN` |
| `hijack_failed` | `ErrHijack` | `plat.Hijack` |
| `provision_failed` | `ErrProvision` | 释放内嵌 brook/sing-box |
| `config_unusable` | `ErrConfig` | 解析/校验配置 |
| `other` | —— | 认不出的一律落这里,**不静默丢弃** |

CLI 侧按 `errors.Is` 分类,一条字符串匹配都不许有。

### 4.5 「隧道没起来」必须一分为二 —— 两种故障的处置完全相反

一个 `tunnel_unreachable` 会把这两件事压成一句话:

- **服务器的 TCP 端口根本连不上** ⇒ 那台机器挂了 / 端口被挡 / 换 IP 了。动作是**换一台或去修 VPS**。
- **TCP 连得上,而隧道就是不健康** ⇒ 服务器活着,是链接/凭据/SNI/被干扰。动作完全不同 ——
  本仓库为此付过一次大代价:reality 全挂的真因是默认 SNI `www.microsoft.com` 证书过大,
  当时先误归因成 sing-box 同机问题与网络 MITM(见 CLAUDE.md「reality 传输收尾」教训坑 ①)。
  **把这两种压成一个码,等于把那次教训重新埋回去。**

判别**不靠读 sing-box 的 stderr 文本**(那正是本设计 §4.1 拒绝的形状),而是**去做一次观测**:
隧道 20 秒没健康之后,对 `serverHostFromLink` 给出的那个 host:port **直连拨一次**(5 秒上限)。

- 拨不通 ⇒ `tunnel_unreachable`
- 拨得通 ⇒ `tunnel_handshake_failed`
- 这次判别本身失败/超时 ⇒ **`tunnel_unhealthy_undetermined`,不许挑一个** —— 这是本仓库的
  `Tristate` 纪律:「问不出来」不是两个答案里的任何一个。

**这次拨号不新增任何暴露面**:目的地是用户自己的服务器,而 bx 刚刚已经连续朝它拨了 20 秒;
此刻还没有 Hijack(§1),普通 socket 走的就是物理网卡。它只在**失败路径上**发生,成功启动
一次都不会跑 —— 与「不后台定时探测」那条边界不矛盾。

---

## 5. 应答体仍然只带码 —— 刻意的,而且它让发布面一寸没扩

「那台服务器是谁」与「你还有哪几台」**两个客户端都已经合法持有**:

- `bx up` 以 root 跑,读得到 `/etc/bx/config.yaml`,`current` 与整张清单都在手里;
- 菜单经 `/v1/servers` 拿到的条目**本来就带 host/port**(Servers 窗口正在显示它们)。

所以 Guardian 只发 `code=core_tunnel_unreachable`,**两个客户端各自在本地把那句可行动的话拼出来**。
这条设计的价值不在省事:它让这次改动**不新增任何一个字节的发布面**,而「发布面扩大靠 review」
在本仓库是已知的弱环。

---

## 6. 不做

- **不自动切服务器。** 所有者定死的边界(servers-window spec §8:不自动容灾、只有用户能切),
  本设计不碰。但「你还配了另一台」这句话必须说出来 —— 否则那条边界的代价白付了。
- **不改「Core 先等隧道健康再开控制 socket」这个顺序。** 反过来能让 `bx status` 第一次答得出
  「正在起、隧道还没通」,但 `core_socket=true` 这个信号被观测层、调谐环准入
  (`decideStartCoreAdmission`)、所有权判定到处在用,改它的语义是全仓爆炸半径。**单独立项。**
- **不改 fail-closed。** 隧道不通就是不上路,一个字不动。

---

## 7. 守卫

- **清理不许再依赖 socket**:注入一个「socket 永远拨不通」的 shutdown,断言失败的 Core 仍被
  收干净、且**不产出 `core_ownership_uncertain`**。变异:把回落 kill 去掉 ⇒ 必须转红。
- **陈旧记录不许被采信**:PID 对不上、`at` 落在窗口外、schema 认不出,三种各一条,
  一律回落 `core_health_failed`。
- **「没说」与「说了 tunnel_unreachable」在渲染出来的话上必须不一样**(钉用户看得见的东西,
  不钉 Facts)。
- **三种隧道结局在渲染出来的话上两两不同**:连不上 / 连得上但没握上 / 没判出来。
  判据是**整句话**,不是某个枚举值 —— 少了这条,把三个分支映射到同一句「隧道没起来」
  照样全绿,而那正是这次要消灭的东西。
- **判别那次拨号只在失败路径上发生**:成功启动的那条路上一次都不许拨(白盒:注入的拨号器
  被调用即红)。这条守的是「不后台定时探测」那条边界不被顺手破坏。
- **判别失败必须落 undetermined,不许挑一个**:注入一个恒超时的拨号器,断言码既不是
  unreachable 也不是 handshake_failed。
- **另一台服务器那句话只在真有另一台时出现**,且**绝不出现链接**
  (照 `TestServerListNeverShipsTheLinkItself` 的形状)。
- 端到端:一个假的、永远连不上的服务器地址 ⇒ `bx up` 的输出里必须出现那个 host:port。

## 8. 真机验收

所有者手上就有现成的复现方式:把 `current` 指向一个不通的地址(或等下次 VPS 出事),
`sudo bx up` 应当**一次**就说出「到 <host:port> 的隧道没建起来」并点名另一台,
**而不是**七次 `core_ownership_uncertain`。
