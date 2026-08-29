# Guardian Linux 适配器(终局第 3 步):让集成台跑真 Guardian

## 目标与边界(一句话各一条)

> **目标**:netns 集成台能跑真 Guardian 内核 + 真 Core,up/down/崩溃重启/调谐
> 收敛第一次有内核状态断言 —— 控制面从「守卫文化硬扛」转到「集成测试背书」。
>
> **边界(刻意决策,继承自终局 spec)**:Linux 适配器**只为集成台服务,不动
> Linux 产品形态** —— 生产 Linux 保持 systemd 直管 supervisor,`bx up` 的用户
> 路径一个字不变。别让测试基建偷偷变成改了所有 Linux 用户部署方式的产品改动。

## 已供货 / 缺口清单(2026-08-29 实测)

| 缝 | 状态 | 备注 |
|---|---|---|
| `scanRunningCores` | ✅ `procscan_linux.go` | /proc 树,纯 I/O 半 fixture 三腿可测 |
| `localPeerCredentials` | ✅ `peercred_linux.go` | SO_PEERCRED |
| `inspectProcess` | ✅ 早已有 | `process_unix.go`(`!darwin && !windows`) |
| Legacy(`install.LegacyCore*`) | ✅ 早已有 | `guardian_other.go` 恒 false/nil,linux 无 legacy launchd |
| Barrier | ❌ | **最大的一块,语义不能照抄,见下** |
| DNSManager | ❌ | install 侧 `GOOS != darwin` ⇒ Enable 报错 ⇒ **Up 必然失败**,见下 |
| NetworkObserver | ❌ | darwin 路由 socket;linux 首期显式不做 |
| `requireDaemonPlatform` | ❌ | **最后放开**(CLAUDE.md 已记档的顺序,不许反) |
| CLI 入口 | ✅ 早已有 | `bx guardian` 隐藏命令无平台门,RunDaemon 的门是唯一闸 |

## Barrier:语义要逐条对齐,机制必须换(本 spec 最重要的一节)

**两个实测事实决定了这一节:**

1. **`PlanBarrier`(无 tag 文件)产出的是 darwin `route` 命令行**
   (`route -n add -net <cidr> 127.0.0.1 -reject` / `route -n add -net <cidr> <gw>`)。
   它看起来平台无关,其实只是「还没有第二个平台来戳穿」。给 linux 供货的第一刀
   是把**语义计划**(哪些网段 reject、哪些 bypass 经网关)与**命令渲染**分开 ——
   语义层继续读 `internal/barriercidr` 那份唯一清单,渲染层按 OS 各一份。
2. **linux 的路由优先级语义与 darwin 相反。** darwin 上屏障赢靠**最长前缀**
   (`/2` reject 压过 Core 的 `/1` split-default,孤儿屏障打死整机连通正是它);
   linux 上 supervisor 的劫持走 `ip rule`(pref 150/200 → table 100),
   **主表里的任何路由都会被这些 rule 抢在前面** —— 把 reject 塞主表,屏障会被
   一个还在跑的 Core 完全绕过,「屏障压过一切」这条 darwin 语义静默失效,
   而它正是 fail-closed 兜底的承重墙。

**linux 机制:专用表 + 更高优先级的 rule。**

```
ip rule add pref 120 table 90            # pref 120:在 fwmark(100) 之后、私网(149/150)与全量(200) 之前
table 90 内容:
  <server bypass /32> via <gw>          # 隧道能建立(与 darwin bypass 同语义)
  throw <route.DefaultPrivateCIDRs 各段># 私网 carve-out:停查本表、落回后续 rule
  unreachable <barriercidr 各 /2 块>    # 公网全量阻断(与 darwin -reject 同语义)
  (v6 启用时)ip -6 同构一份(throw 用 DefaultPrivateV6CIDRs)
```

> **初稿在这里有一个被实测证伪的假设,改之前必读**:初稿写「表 90 未命中即
> 落到后续 rule,linux rule 语义天然给出私网恒直连」——**假的**。barriercidr
> 那四条 `/2` 覆盖**整个** v4 空间(0/64/128/192 各 /2),私网地址一定命中;
> darwin 上救它的是**同一张主表里的连接路由按最长前缀获胜**,而 linux 的 rule
> 命中即终止查找,跨 rule 没有最长前缀可言。**`throw` 路由是那个语义的忠实
> 移植**:表内最长前缀让 throw(私网段,长于 /2)先于 unreachable 命中,
> throw = 停查本表、继续后续 rule ⇒ 私网落回 main/table-100,恒直连保住。
> 私网清单**必须**引用 `route.DefaultPrivateCIDRs`/`DefaultPrivateV6CIDRs`
> (数据面那份唯一清单),不许手抄第二份。

**必须逐条对齐的语义清单**(计划阶段每条一个 netns 断言):
- 屏障在位时,公网 v4/v6 全 unreachable,**Core 跑着也一样**(pref 120 压过 150/200);
- **pref 取 120 而不是压过一切的 50,是 darwin 语义的忠实移植而不是放水**:
  darwin 上 bx 自身出站(IP_BOUND_IF)走 scoped 表、**结构上就逃过**主表的 /2
  reject;linux 的对应物是 supervisor 的 pref-100 fwmark 规则(SO_MARK 0x162,
  只有 bx 自己打标)。屏障排在 100 之后,bx 的打标流量(过渡窗口里 Core 解析
  china DNS、重建隧道)不被自家屏障堵死——压到 100 之前的话,server 域名冷启动
  解析会死锁在自己的屏障上,而 darwin 从来没有这个行为;
- server bypass 经物理网关可达(隧道能重建);
- 私网(`route.DefaultPrivateCIDRs` 那些段)不受影响;
- Remove 逐条对称拆除,不 flush 别人的表;
- `RemoveBlockingBarrierRoutes`(逃生口那份)linux 版一并供货 ——
  哪怕逃生口今天只在 darwin CLI 生命周期里被调,**孤儿 pref-120 rule 在 netns
  里同样能打死连通**,清理原语必须与安装原语同批出现,不许先欠着。

**观测半边不跟着做**:`internal/observe` 问「屏障在不在位」是 darwin 命令,
netns 断言直接用 `ip rule`/`ip route` 打在内核状态上(集成台既有纪律:
断言打内核,不打自己的记账),observe 的 linux 原语留给以后真有 linux 产品
形态那一天。

## DNSManager:linux 的答案是「数据面已经管了,没有东西要接管」

实测:`install.EnableDNSContext` 在 `GOOS != darwin` 返回错误 ⇒
`Manager.Up` 的 `m.dns.EnsureManaged` 必然失败 ⇒ `dns_takeover_failed`,
**linux 上 Up 按今天的接线一定起不来** —— 这不是缺一个实现,是缺一个**决定**:

linux 数据面不需要外部 DNS 接管(整机路由劫持 + engine 拦 UDP:53 到任意目的地,
Mudi 真机 e2e 早已背书;系统 resolv.conf 一个字不改)。故 linux 的 DNSManager
是**诚实的「无需接管」**:`EnsureManaged`/`Inspect`/`Restore` 返回
「Supported:false / 数据面处理」且 **nil error**,`dns_managed` 如实为 false。
**不许**为了让状态好看而伪造 `managed=true` —— 那是用假信念换绿灯,正是这个
项目花三期拆掉的东西。`Manager` 侧若有「非 managed 即降级」的判断,按
「字段缺席是诚实的没问」的既有纪律调整为区分「接管失败」与「本平台无此事」。

## NetworkObserver:首期显式不做

路径恢复是 darwin 真机事故(underlay 变化)养出来的;netns 里 underlay 不变。
首期沿用「没有 observer 就不装 observer 生命周期」的既有 nil 分支
(`startRecoveredDaemon` 对 `options.networkObserver == nil` 的处理已存在,
lifecycle 清单的 `NewNetworkObserver` linux 版返回「显式无观测」),
netlink 版留给将来。**不许**用一个假装在观测的桩 —— 「观测不到 ≠ 观测到没有」。

## 门最后开,且带绞索

`requireDaemonPlatform` 的 linux 放开是**最后一个 commit**,前置是上面每一块
都有 netns 断言背书。放开即 `bx guardian`(隐藏命令)在 linux 可跑 —— 生产
linux 没有任何东西会去装/拉起它(systemd unit 由 `bx up` 写,指向 `bx run`,
不经 Guardian),故产品形态不变的承诺由「没有调用方」保证;spec 明写这一条,
将来谁想给 linux 产品接 Guardian,从这里开始读。

## 集成台接法

沿用 supervisor 集成台的全部纪律(re-exec 进整进程 netns+mount ns、断言打内核、
`scripts/run-netns-tests.sh` 在 Colima 特权容器跑):

- 新 `internal/guardian/harness*_netns_linux_test.go`(tag `integration`),
  在 netns 里直接调 `RunDaemon`(容器内是 root)。**Core 用替身,不用真 bx run
  ——初稿「倾向真数据面」被两个事实推翻(2026-08-29)**:supervisor 台子的假
  隧道靠 `Options.BuildTunnel` 进程内注入,而 RunDaemon 经真 ExecCoreRunner
  **spawn 子进程**,注入缝跨不过 exec;netns 里没有外网,真 bx run 永远到不了
  tunnel healthy。替身(`fakecore_test.go`)满足 Guardian 对 Core 的全部观测面
  (控制 socket /v0/runtime+/v0/shutdown、loopback SOCKS5 探针应答、拷成 `bx`
  以 `bx run` spawn 让 scanRunningCores 认得出),契约由
  `TestFakeCoreSatisfiesTheRealHealthChecker` 直接钉在**生产的**
  HealthChecker.Wait 上 —— 协议/字段/探针三层漂移在 darwin 单测当场红;
- 首批五条断言(每条都要变异验证):① Up 之后屏障不在、Core 在跑、
  `/v1/status` 报 protected;② Down 之后 pref-120 rule + table 90 全量在位、
  公网 unreachable、bypass 可达;③ 杀 Core 后 `handleUnexpectedExit` 自动重启
  且期间扫描普查日志可见;④ 孤儿 launch marker + 真 Core 在跑 ⇒ Up 拒绝
  (fail-closed 三连的 linux 首验);⑤ 调谐环一轮之后 `Reconcile.At` 有值且
  actions=none(健康机器静默)。
- CI `integration` job 逐名 grep 的既有纪律照抄(锚子进程 `--- PASS` 前缀)。

## 不做

- 不动生产 Linux 的 `bx up`/systemd 形态;不写 linux 的 Guardian 安装器。
- 不做 netlink 网络观测、不做 linux 的 observe 原语。
- 不碰 darwin 一行为(lifecycle 清单字段的 darwin 值不变)。
- Windows 不在本期(SCM 适配器等 linux 台子攒够证据再立项)。

## 风险

- **屏障优先级语义是全案最险的一处**:pref 选错或 rule/table 泄漏,在 netns 里
  是断言红,在将来的生产 linux 上是「屏障被 Core 绕过」——所以对齐清单每条
  单独断言、Remove 对称性单独断言,且逃生口清理与安装同批。
- darwin 行为保真的实现选择(实施时定,记档):**darwin `PlanBarrier` 一行不动**,
  linux 计划器(`PlanBarrierLinux` 一族)与它共读同一份语义源
  (`validateBarrierContext` + `barriercidr` + `route.DefaultPrivateCIDRs`),
  防漂移由**跨计划器语义对齐测试**钉住(两边提取出的 bypass/reject 网段集合
  必须相等)——比抽一层中间表示少动 darwin 一行,保真代价为零。
- netns 里跑 RunDaemon 要 root + 会写 `/var/lib/bx`/`/run/bx` —— mount ns 里
  tmpfs 盖住,沿用 supervisor 台子的隔离参照(问 `/proc/1/ns/*`,不信环境变量)。
