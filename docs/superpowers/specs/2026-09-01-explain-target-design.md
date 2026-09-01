# `bx explain <目标>`:让判定本身可查询

**日期**:2026-09-01
**状态**:设计
**动机**:三次真实排查,三次 bx 都握着答案,三次都没人问得到。

---

## 问题

bx 隐形地坐在每一条连接的中间。当一个请求失败,人和 agent 看到的都是**症状**
(`curl: (28)`、`connection refused`、TLS 挂住),而不是**判定**。

三次病历:

- **Steam 图片全裂**:用户去怀疑 bx 和 CDN。真因是一条 direct 规则指向的路完全
  不通,而 bx 在数据面上**每一次都看见了**(`dial direct failed`),然后扔掉。
- **腾讯会议绕一圈**:排查半小时,最后靠 `strings` 从 app 里捞域名比对。而源端口
  就在 `TransportEndpointID` 里、判定就在 `route.Reason` 里,两件事都在,只是从
  没被放在一起。
- **`*.qq.com` 57% 失败**:2026-09-01 这一轮,来回查了好几次。

共同形状:**请求级的「为什么」**。而 bx 今天的全部对外面(11 个只读 MCP 工具、
`bx status`、`bx doctor`)答的都是**系统级的「状态如何」**。

`route.Explain(Meta) → (Decision, Reason)` 返回「走哪条路 + 由配置里哪一行决定」,
它每秒执行上万次,**只被 `dialer.go` 调用,没有任何外部出口。**

## 这不是「再加一个只读工具」

已经有 11 个只读工具了。问题不是覆盖率,是**没有一个回答请求级的问题**。判据的
稀缺性在于:**在这台机器上,只有 bx 知道每一条连接为什么走了它走的那条路。**
那个知识现在只用来转发数据包,用完就扔。

它还必须是**反事实的** —— 可以在动手之前问,而不是失败之后猜。agent 的代价不
对称:猜错了它会去改一堆无关的东西(重装、改 DNS、怀疑对端),每一步都可能把
事情弄糟。

---

## 关键设计决定

### D1:问活着的 Core,不在 CLI 里重建 Router

CLI 完全可以读 `/etc/bx/config.yaml` 自己建一个 Router 来回答。**不这么做。**

bx 不热重载:盘上的配置可能已经和跑着的那个不一样。CLI 自建的答案会是
「bx **应该**做什么」,而用户问的是「bx **会**做什么」。两者不同的那一刻,正是
最需要这个命令的那一刻。

这也是本仓库那条纪律的直接应用:**CLI 不许是第二个控制面。**

**Core 没在跑 ⇒ 如实说没在跑,不退回配置推断。** 「bx 没在做决定」是一个诚实
的答案;拿一个猜出来的判定冒充它,正是这个功能要消灭的东西。

### D2:合成住在 dialer 里,与 dialInner 共读同一批字段

`route.Explain` 只给**路由**判定。「实际会发生什么」还要叠上:

```
路由判定(Explain) × kill-switch(隧道健康) × UDP 档(udp.mode / udp.transport)
```

三种做法,选第三种:

1. ~~在 CLI 里重新合成~~ —— 第二个判据源,直接否掉。
2. ~~把 dialInner 的合成整块抽出来~~ —— 它和 stats、recordApp、真拨号绞在一起,
   而那是全产品最热的路径。风险大于收益。
3. **在 `internal/dialer` 里加一个只读的兄弟方法 `(*Dialer).Explain`**,它是
   **同一个 Dialer 实例**上的方法,读的是同一个 Router 指针、同一个 Transport、
   同一个 `Killswitch`/`UDPMode` 字段,并且调用**同一个** `killswitchBlocks`。

第 3 种不是「另一份判据」,是同一个对象的另一个问法。

**漂移由测试挡住**,手法照抄本仓库既有先例(`Decide` 是 `Explain` 的薄壳,另有
一条测试逐输入比对):给一张输入表,断言 `Explain` 预言的结局与 `dialInner`
**真的做出来的**结局一致。两者独立成文,分歧会被当场抓到。

### D3:根本不需要解析(写这条时我判断错了,已按代码更正)

初稿写的是「默认不解析,`--resolve` 才解析」,前提是「域名没命中域名规则时,
数据面会解析出真实 IP 再按 IP 判」。**那个前提不成立。**

`route.Explain` 对域名的最后一支是:

```go
// 未命中任何列表:默认走代理。不再用(可能被污染的)国内 DNS 做 geoip,
// 避免境外域名被误判直连而泄漏真实 IP。
return Proxy, Reason{Source: SourceDefault}
```

**判定从不解析** —— 解析只发生在**拨号**那一步(Direct 分支需要一个 IP 才能连)。
所以 explain 天然不需要出站 DNS,`--resolve` 这个开关整个不必存在。

(顺带发现:`dialInner` 里 `dec == route.NeedResolve` 那一支今天不可达 ——
Explain 与 ExplainIP 都不返回它。不在本设计范围内,记下来。)

这也顺带让「被动观测优于主动探测」在这里不需要被援引:没有探测可言。

### D4:TCP 与 UDP 都要答,而且分开答

`udp.transport` / `udp.mode` 意味着同一个目的地的 UDP 可能去往和 TCP 完全不同
的地方。压成一个答案就会把这个事实抹掉。

先例是 `appattr.PortKey{Port, UDP}`:**协议维度不能省** —— 省掉的后果是把一件
事记在另一件事名下,而错的归因产生的信号就是没有信号。

### D5:fake-IP 反查是输入的一部分

`bx explain 198.18.0.7` 应当先反查回域名再判 —— 那正是数据面做的事,也正是
用户读日志时手上拿着的东西。

### D6:带上这条规则最近的成败

只说「命中了 `*.steamstatic.com`」不够。Steam 那次的关键事实是**那条规则
1291 次里失败 1289 次**。Core 手上就有(`stats` 的按规则 {attempts, failures}
与跨重启的 `rule_history`),顺手发出去。

---

## 输出形状(示意)

```
$ bx explain steamstatic.com
  目标      steamstatic.com
  TCP       DIRECT
    依据    用户规则 direct: '*.steamstatic.com'
    本次    1291 次判定 / 1289 次拨号失败 (99.8%)
    累计    跨 14 天 8113 次判定
  UDP       DIRECT(同上;udp.mode=proxy 但用户规则对 UDP 同样生效)
  隧道      健康 (reality@…, 293ms)
```

`--json` 是同一份结构,字段名与 `bx status --json` 同风格。

---

## 刻意不做

- **不加只读状态转储。** 这个命令要么回答请求级的「为什么」,要么不做。
- **不扩大 agent 的改动权。** explain 全程只读。
- **不给 Core 加任何写路径。** 新端点 `/v0/explain` 是纯读。
- **不做 `bx explain --watch`。** 事件流是另一件事(见「后续」)。

## 分期

- **① 判据 + 漂移守卫**:`dialer.Outcome` + `(*Dialer).Explain` + 与 `dialInner`
  逐输入比对的测试。**本期。**
- **② 出口**:Core `/v0/explain` + `bx explain` CLI。
- **③ 富化**:规则计数、`rule_history`、`rulereview` 结论并入输出。
- **④ agent 面**:`bx_explain` MCP 工具。

## 后续(不在本设计内)

`bx_since(generation)` —— 「我上次看到 N,之后发生了什么」。它排在 explain
之后,理由是:**「我不在的时候发生了什么」只有在能接着问「那这个为什么失败」
时才有用。** 没有 explain,事件流只是第 12 个状态转储。
