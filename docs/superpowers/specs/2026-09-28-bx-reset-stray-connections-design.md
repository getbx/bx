# 让保护关着时开的连接当场重连:一次性 pf 回 RST(design)

**状态:所有者 2026-09-28 拍板(一次性 + 含 UDP),同日实施(`internal/pfreset`,
计划 `docs/superpowers/plans/2026-09-28-pf-reset-stray-connections.md`),**真机未验**
(`docs/acceptance-pending.md` A13)。下文是当初的设计,判据的现状以
`internal/supervisor/CLAUDE.md`「一次性 pf 重置」一节为准。**

## 问题与证据

known-gaps A11:`bx down` 到 `bx up` 之间应用开的连接,`bx up` 之后仍从物理网卡以真实
IP 收发,直到它们自己关掉。macOS 不会把一条已建立的 socket 挪进 TUN,bx 的全部保护建立
在路由上,这一类连接它此前看不见。2026-09-25 起看得见了(`appattr.StrayConnections`),
2026-09-28 又分成两段(退场中只报数、五分钟还在的才点名「退出重开」)。

**看得见与分两段都不改变泄漏本身。** 真机 2026-09-28:一次 `bx up` 之后 Chrome、Mail、
WeChat、IMTransferAgent 的连接带着真实 IP 活了几分钟;Chrome 的三条活得更久。所有者的
判断是「不太可能要求用户所有 app 都重开」—— 对,而这正是两段式解决不了的那一半。

## 目标与非目标

目标只有一个:**`bx up` 之后,保护关着时开的连接在几秒内被重置,应用自己重连,新连接
按现在的路由进 TUN。** 用户不做任何事,菜单上那行灰字几乎看不见。

非目标:
- 不做常驻的第二道 kill-switch。常驻规则把风险面从几秒扩到永远,而它多防的那种场景
  (bx 自己的路由被谁冲掉)今天由路由自愈在管。要做也是另一份 spec。
- 不改 Linux / Windows。Linux 有 `ss -K`,Windows 有别的原语,各自另议。
- 不碰 Network Extension。那需要数据面整个搬进 system extension,是一次重写。

## 为什么是 pf、为什么是 RST

macOS 没有按 socket 重置的原语:没有 `tcpdrop`,没有对应的 sysctl(2026-09-28 在
macOS 26.6 上核过)。能做到的是让**本机 TCP 栈自己**把 socket 判死:pf 的
`block return-rst out` 拦下一个出站包并向本机回一个 RST,本机 socket 立刻收到
ECONNRESET,应用按自己的重连逻辑重连。UDP 同法用 `block return-icmp`,连接态的 UDP
socket 会收到 ECONNREFUSED(Chrome 的 QUIC 会退回或重建)。

规则只有两条,住在 bx 自己的 anchor 里,**只在 bx 需要的那几秒存在**:

```
table <bx_routed_around> persist { <私网 route.DefaultPrivateCIDRs> <服务器旁路> <用户 bypass> }
block return-rst  out quick on <物理网卡> inet proto tcp from (<物理网卡>) to !<bx_routed_around> user != root
block return-icmp out quick on <物理网卡> inet proto udp from (<物理网卡>) to !<bx_routed_around> user != root
```

判据与 `appattr.StrayConnections` 是同一份:本地地址在物理网卡、远端是公网、目的地不在
bx 自己绕开的网段。`user != root` 替代了「不是 bx 自己的进程」那条:Core、隧道子进程、
tailscaled 都是 root,应用都不是。用户 `bypass:` 与服务器旁路进表,所以到自己 VPS 的
ssh 不会被重置(与 2026-09-28 那次假阳性同一条边界)。

## 时序

1. `Run()` 里 Hijack 完成、路由就绪位置真之后(不等隧道健康:新连接进 TUN 之后由
   kill-switch 按每次拨号判)。
2. **先读一次 socket 表**:没有绕过连接就一个字不做,pf 完全不碰。这是常态。
3. 有,则 `pfctl -E`(带引用计数地启用 pf,拿到 token)、把两条规则装进 anchor。
4. 每秒读 socket 表,绕过连接清零或满 10 秒即止。
5. 冲掉 anchor、`pfctl -X <token>` 释放引用。整个过程写进 Core 日志:重置了几条、
   花了几秒、有没有到 10 秒还没清零的(那些就交给两段式去点名)。

## 所有权与清理:与屏障路由同款的三条路

pf 规则活在内核里,Core 崩溃不会带走它。残留的后果是「en0 上非 root 进程的公网
TCP/UDP 全被拒」—— fail-closed,但用户读到的是「网坏了」。所以:

- **token 落盘** `/var/run/bx/pf.token`(与 `core.sock` 同目录、同权限纪律)。
- **正常路径**:步骤 5 的 defer;`bx down` 经 Core 的 `/v0/shutdown` 时同样走到。
- **强制拆除**(`sudo bx down` 的兜底,CLAUDE.md「逃生路径不变量」):多一步 —— anchor
  里有规则就冲掉,token 文件在就 `pfctl -X`。任一步失败都继续做完剩下的。
- **Guardian 启动**:发现 anchor 里有规则(上一个 Core 没来得及清)先冲掉再起 Core;
  `bx doctor` 加一项「bx 的 pf anchor 有残留规则」。

`pfctl -E`/`-X` 的引用计数是 Apple 给第三方临时启用 pf 的正规接口:bx 释放引用时,
别人(比如另一个 VPN)开着的 pf 不受影响;bx 之前 pf 本就关着的话,释放后它回到关着。

## 已知会受影响的东西,逐条

| 谁 | 影响 | 为什么 |
|---|---|---|
| 浏览器里正在上传的 POST | 那一次失败,应用未必重试 | 与切 Wi-Fi 同一种代价;`bx up` 本就是一次网络切换 |
| 用户自己 IP_BOUND_IF 绑在 en0 的非 root 进程 | 那几秒内到公网的连接被重置 | pf 看不见 IP_BOUND_IF;实际只有 `bx leakcheck --compare-direct` 这种刻意直连,发生在 `bx up` 那几秒内的概率可忽略 |
| Tailscale / 其他 root 的 VPN 守护进程 | 无 | `user != root` |
| Colima / Docker | 无 | 到 VM 的 ssh 走 127.0.0.1;VM 出网由 vmnet(内核 / root)代发 |
| 到自己 VPS 的 ssh、用户 `bypass:` 网段 | 无 | 在表里 |
| 私网、局域网 | 无 | 在表里 |

## 待所有者定的三个点

1. **一次性 vs 常驻。** 本文按一次性写。常驻是另一份 spec。
2. **UDP 那条要不要。** 不要的话 QUIC 连接仍会漏几分钟(Chrome 对 Google 系站点几乎全走
   QUIC);要的话多一种被重置的形状(ICMP unreachable),风险面略大。建议要。
3. **anchor 的落点。** 放在 `com.apple/` 通配 anchor 之下(如 `com.apple/250.bx`)不用
   改 `/etc/pf.conf` 就会被求值,这是常见做法;但确切写法要在真机上核,本文不拍胸口。

## 验证,按梯度,不在所有者的 Mac 上首跑

- 规则文本是纯函数(输入:网卡名、三组网段;输出:那两条规则与表),单测钉住每一段
  都在表里、`user != root` 在、`quick` 在。
- 「读 socket 表 → 装 → 等 → 拆」那条循环用假的 pf 与假的 socket 表跑通,断言:没有绕过
  连接时 pf 一次都没被叫;清零即止;10 秒封顶;拆的动作在每条失败路径上都发生。
- **首次真机**:一台不是所有者日常用的 Mac,或所有者的 Mac 上用 `bx run --no-hijack`
  之外的新旗标 `--pf-reset-dry-run`(只打印将装的规则与将被重置的连接,不装);再用带
  死手的 `bx run --test-timeout 2m` 全量跑一次,同时 `tcpdump -ni en0` 抓包,看 RST 与
  重连是否如预期、几秒内清零。
- 抓包分类用 python 逐包判,不用 tcpdump 的复合过滤式(memory:那种会假绿)。
