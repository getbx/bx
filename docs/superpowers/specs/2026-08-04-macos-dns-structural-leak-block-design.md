# macOS DNS 结构性防泄漏设计

Status: DRAFT (2026-08-04)

## 背景

`2026-08-04-macos-guardian-dns-lifecycle-design.md` 把 DNS 接管纳入了 Guardian 的
状态机:激活、路径恢复和更新都会校验 DNS,失败则 fail-closed。那一轮解决的是
**切换点**的正确性。

本设计解决它没有覆盖的两件事:**稳态漂移**与**恢复能力**。

### 问题 A:稳态漂移无人发现,且状态会撒谎

`ensureDNSManaged` 全仓库只有两个调用点——激活(`manager.go:670`)和路径恢复
(`path_recovery.go:382`)。`m.dnsStatus` 只在这两条路径上被刷新
(`manager.go:708/714/726`),而 `setStatus`(`manager.go:1176`)报给 CLI 与菜单栏的
是这份**缓存**。`health.go` 的巡检循环探的是 SOCKS 隧道,不碰 DNS。

因此:若 DNS 在运行中漂移且未触发路径恢复(MDM/DHCP 改写、用户手动改),Guardian
会持续用陈旧的 `managed` 回答,菜单栏保持绿色,流量照跑,**DNS 明文外泄且无人察觉**。

漂移的泄漏面比直觉窄,但真实存在:

- 漂移到**公网** DNS(如 `8.8.8.8`):目的地落在 `0/1`+`128/1` 劫持范围 → 进 TUN →
  引擎拦下 UDP:53 → **不泄漏**。
- 漂移到**私网/同链路** DNS(DHCP 派发的路由器,最常见):命中私网 carve-out →
  直连物理网卡 → **泄漏**。

macOS 的 DNS 模型与 Linux/Windows 不同:后两者是 DNS-into-TUN,macOS 是
`networksetup -setdnsservers <服务> 127.0.0.1`(`install.go:837`)+ bx 自身监听
`127.0.0.1:53`。系统 DNS 一旦被改走,就没有任何结构性机制拦住查询。

Windows 上 bx 已有结构性防护(`winfw.BlockDNSLeak` 用 WFP 封掉一切 off-TUN :53),
**macOS 上没有等价物**——全仓库无 `pfctl` 调用。

### 问题 B:关不掉,且原因被隐瞒

`bx down` 在停掉 core 后还原 DNS,**若还原失败会主动把 core 重新起来**并返回错误
(`manager.go:483-497`)。这个行为本身站得住:bx 接管时把系统 DNS 指向了自己,还原
失败又放任退出,机器会剩下一个指向已死进程的 DNS,整机域名解析全断。

缺陷在于菜单栏(`main.swift:792`)只弹一句 `Turn Off Failed / bx did not stop.`,
既含糊又误导——bx 确实停了,是因为 DNS 还原失败才被**主动**重新拉起。用户看到它
还在跑,自然得出"退出不掉、Guardian 在跟我作对"的错误结论。

已排除的其它重启路径(不是它们导致的):

- 进程监控:`Down` 全程持有 mutation 锁,放锁前把 `m.current` 清零,
  `sameProcessGeneration` 首条件 `a.PID > 0` 必为 false,直接返回不重启。
- launchd 拉起 Guardian:`RunStartupRecovery`(`manager.go:579`)读到 `DesiredOff`
  只还原 DNS 后停在 idle,不起 core。

## 产品不变量

1. 系统 DNS 被改成什么,DNS 查询都**不得**明文离开物理网卡。这是结构性保证,不依赖
   任何轮询的及时性。
2. 安全机制**绝不能**把用户永久锁在断网状态。任何失败路径都必须有恢复途径,且至少
   有一条是用户无需专业知识即可执行的。
3. 期望状态为 off 时,不得存在任何 bx 装载的阻断规则。
4. anchor 的存在严格跟随保护的终态:凡终态为"保护已关闭",**必须**不残留 anchor;
   拆除动作不得因无关步骤的失败而被跳过或永久阻塞。反之,若某条失败路径的终态是
   "保护已恢复"(如 DNS 还原失败导致 core 被重新拉起),anchor 应当保留——此时机器
   处于受保护状态,拆除反而制造泄漏。
5. 上报给 CLI 与菜单栏的 DNS 状态必须反映**当前**系统实际状态,不得是陈旧缓存。
6. 失败时必须把**真实原因**和**可执行的下一步**告诉用户。

不变量 2 与 1 同等重要。一个把用户锁在门外的安全机制,比没有这个机制更糟——
用户会卸载它。

## 已验证的事实

以下结论均在本机验证,不是推测:

| 事实 | 验证方式 | 意义 |
|---|---|---|
| 开机只加载 `com.apple` anchor | `/System/Library/LaunchDaemons/com.apple.pfctl.plist` 执行 `pfctl -f /etc/pf.conf`;`/etc/pf.conf` 仅 `load anchor "com.apple"` | **重启必然清空**第三方 anchor,前提是 bx 绝不写 `/etc/pf.conf` |
| 可按 anchor 精确清除 | `man pfctl`:`-a anchor` 使 `-f/-F/-s` 只作用于该 anchor | 救命命令可做到外科手术式,不碰用户配置 |
| `scutil -w` 不能做变更通知 | `man scutil`:key 存在时立即返回 0;实测 `scutil -w State:/Network/Global/DNS -t 1` 秒回 0 | 事件驱动检测此路不通,只能轮询 |
| 当前无 cgo 依赖 | `CGO_ENABLED=0 GOOS=darwin go build ./...` 通过 | SCDynamicStore 回调方案会破坏静态构建,排除 |
| 单次 DNS 探测约 58ms | 实测 `route -n get default` + `networksetup -listnetworkserviceorder` + `networksetup -getdnsservers` | 10s 巡检的开销可忽略 |

## 业内实现对照

**Mullvad**(开源,macOS 用 pf,形态最接近 bx):用 named anchor 装规则并原子更新,
只放行到指定 DNS 服务器、其余 :53 全封,转场时 flush pf 连接状态防止旧状态绕过隧道。

但其恢复故事很差:lockdown 模式下 app 崩溃或升级失败会导致整机断网,官方文档给出的
恢复命令是 `sudo pfctl -d` + `sudo pfctl -f /etc/pf.conf`——**把全机 pf 连锅端掉**,
用户自有的 pf 配置一并抹除。社区中"装到一半崩了、现在上不了网"的求助真实存在。

**NetworkExtension 路线**(WireGuard 官方 app、Tailscale)由 OS 托管拆除,结构上不
易卡死,但需要 System Extension、entitlements 与公证,对 bx 是整体重写,不在本轮范围。

**bx 的结构性优势**:Guardian 本身是 KeepAlive 的 LaunchDaemon。Mullvad 的守护进程
崩了就没人收拾规则;bx 的会在秒级被拉起来收拾。本设计把这一点作为恢复保证的核心。

## 方案选择

### 采用:pf 结构性封堵 + 巡检修复 + 如实上报

pf anchor 提供不变量 1 的硬保证——系统 DNS 指向哪里都无所谓,查询出不去。巡检不再
是安全的最后防线,降级为**修复与可观测性**职责:把 DNS 改回 `127.0.0.1` 让解析恢复
可用,并让状态停止撒谎。两者职责正交,不重复。

### 未采用:只做巡检 + 屏障

轮询天然存在窗口,再快也只是缩小而非消灭。与不变量 1 冲突。

### 未采用:迁移到 NetworkExtension

能从根上获得 OS 托管的拆除语义,但要求 System Extension 与公证,是整体重写。
留作长期方向,不在本轮。

## 架构

新增三个单元,遵循既有的 `_darwin` / `_other` 分文件惯例:

| 单元 | 职责 | 依赖 |
|---|---|---|
| `internal/guardian/dnsfirewall.go` | 纯逻辑:生成 pf 规则文本、anchor 名、救命命令字符串 | 无 |
| `internal/guardian/dnsfirewall_darwin.go` | 执行 `pfctl` 装载/清空/查询 | 既有 `CommandRunner` |
| `internal/guardian/dnsfirewall_other.go` | no-op 实现 | 无 |
| `internal/guardian/dnswatch.go` | 纯逻辑:漂移判据、巡检决策 | 无 |

Manager 只做接线。规则文本生成与漂移判据都是纯函数,免 root 可测;系统调用集中在
`_darwin` 文件并走 runner 抽象,可注入假 runner 验证时序。

## anchor 挂载路径(决定冲突面大小)

pf 的规则装进一个**未被主规则集引用**的 anchor 里完全不生效。macOS 的
`/etc/pf.conf` 只引用 `com.apple/*`,因此 bx 必须解决"如何让自己的 anchor 被求值"。
两条路径的代价差异巨大:

**路径 B — 挂进 `com.apple` 子 anchor(优先)。** `man 5 pf.conf` 明确定义:

> Anchors may end with the asterisk ('*') character, which signifies that **all
> anchors attached at that point should be evaluated** in the alphabetical
> ordering of their anchor name.

即 `anchor "com.apple/*"` 会求值所有挂在 `com.apple` 下的子 anchor,**包括运行时
装入的**。Apple 自己就是这么组织的(`/etc/pf.anchors/com.apple` 内含
`anchor "200.AirDrop/*"` 与 `anchor "250.ApplicationFirewall/*"`)。若可行:

- **完全不碰主规则集** → 不会覆盖 Little Snitch / MDM 等第三方的 pf 规则,冲突面归零
- 不改 `/etc/pf.conf`,重启仍必然清空
- `pfctl -a com.apple/<名字> -F all` 仍可精确清除

代价:占用 Apple 的命名空间,不够体面,且 Apple 理论上可能变更该结构。另需注意求值
按**字母序**,anchor 命名决定它排在 AirDrop/ApplicationFirewall 之前还是之后,而
`quick` 是先匹配先生效——命名即优先级,须谨慎选定。

**路径 A — 替换主规则集(兜底)。** 用 `pfctl -f` 装载一份包含 Apple 原有 anchor
加 bx anchor 的主规则集。`pfctl -f` 是**整体替换**语义(且是原子的:装载失败则原
规则集保持不变),因此会**覆盖任何其它工具在内存中装载的主规则集**。

覆盖不可逆:`pfctl -s rules` 的输出**不是**可原样回灌的格式,抓取只能用于事后排查,
无法保真还原。因此该路径必须先检测第三方内容、默认中止、由用户显式放行。

**决策依赖真机验证**:路径 B 是否可行(SIP 是否允许第三方写入 `com.apple` 命名空间、
规则是否真被求值、是否影响 Apple 其它子 anchor)只能实测。探测脚本
`scripts/darwin-dns-firewall-probe.sh` 同时验证两条路径,优先 B,A 需显式开关。

## pf 规则

anchor 名视上节验证结果确定。**绝不写入 `/etc/pf.conf`**——这是"重启必然恢复"这条
逃生口的前提,由源码守卫测试钉死。

规则形状(确切文本与 `quick` 顺序**必须真机验证**,见"待验证项"):

```
# 放行 loopback:bx 自身的解析器监听在 127.0.0.1:53
pass out quick on lo0 proto { udp, tcp } to port 53

# 放行 TUN:漂移到公网 DNS 时查询会路由进 TUN 并由引擎正常处理,
# 这条合法路径不能被误封
pass out quick on <tun> proto { udp, tcp } to port 53

# 放行 bx 自身的上游查询(解析器要查国内 DNS 做分流判断)
pass out quick proto { udp, tcp } to port 53 user root

# 其余出站 :53 一律封死
block out quick proto { udp, tcp } to port 53
```

**这里是本设计最容易翻车的地方。** Windows 移植时踩过完全同类的坑:WFP 权重若照抄
上游(`deny 14 > permitTun 12`)会打死进 TUN 的合法 :53,必须让 `permitTun(14)` 压过
`blockDNS(12)`。pf 的 `quick` 语义等价于"先匹配先生效",放行规则必须排在 block 之前
且带 `quick`。真机验证必须覆盖:漂移到公网 DNS 时解析仍然正常(走 TUN),漂移到私网
DNS 时解析被阻断(不泄漏)。

## 生命周期与数据流

**开启**(屏障期内完成,顺序在屏障保护下不敏感):

```
起 core → 接管 DNS(setdnsservers 127.0.0.1) → 装 anchor → 校验 → 撤屏障放行
```

装 anchor 失败并入既有 `failDNSActivation` 语义(装屏障 + needs_attention),不发明
新的失败路径。

**巡检**(每 10s,独立循环,与 NetworkObserver 职责分离):

```
探测(稳态下仅 1 个子进程,服务名取自 dnsStatus.Service 缓存)
  → 未漂移:刷新缓存,结束
  → 漂移:装屏障 → 重新接管 DNS → 校验 → 撤屏障
```

anchor **全程保持**,所以修复期间也不泄漏。巡检失败不降级安全性,只影响可用性:
反复失败则停在 needs_attention,由用户介入。

选择独立循环而非复用 `NetworkObserver` 的 1 分钟 tick:后者的职责是底层代际变化,
把 DNS 塞进去会混杂职责,且 60s 的修复延迟对可用性不利。

**关闭**(anchor 的拆除在屏障保护下进行,按终态决定去留):

```
装屏障 → 停 core → 还原 DNS
  ├─ 还原成功:落 desired=off → 拆 anchor → 撤屏障        [终态:关闭,anchor 必须消失]
  └─ 还原失败:重启 core → 保留 anchor → 撤屏障 → 报错     [终态:受保护,anchor 应当保留]
```

两条分支的共同要求:**拆除动作本身绝不能被无关步骤的失败跳过或永久阻塞**。断网的
元凶是 anchor,若它的拆除被别的失败拖住,就会出现连环卡死——这正是不变量 2 要防的
"把用户锁在门外"。

注意"无条件拆除"是错的:还原失败分支的终态是**受保护**(core 已被重新拉起),此时
拆掉 anchor 反而制造泄漏。正确的表述是不变量 4——anchor 跟随保护终态,而非跟随
某个动作是否被调用。

## 恢复保证

四层,从全自动到手动:

| 层 | 机制 | 覆盖的失败 |
|---|---|---|
| L1 | desired=off 时绝不装 anchor;`bx down` 成功落到关闭终态时必定拆除 | 正常关闭 |
| L2 | Guardian 崩溃 → launchd KeepAlive 秒级拉起 → **启动时无条件把 anchor 对齐到期望状态**,off 则拆除 | 崩溃、被杀、升级中断 |
| L3 | **重启必然清空**(已验证);开机若 desired=off 不重装 | 所有自动机制都失效 |
| L4 | `sudo pfctl -a <anchor> -F all`(路径 B 为 `com.apple/<名字>`,路径 A 为 `com.getbx.bx`,取决于验证结果),**由菜单栏在检测到阻断态时直接展示**,并写入 README | 用户手动自救 |

L2 是相对 Mullvad 的关键改进:它的守护进程崩溃后规则会一直留着,bx 的会被拉起来
对齐。用户此前担心的"bx 又被拉起、反复"在这里是**优点**,前提是拉起后必须尊重
desired=off——现有 `RunStartupRecovery` 已具备该语义,anchor 对齐接入同一判断。

L4 的命令是 anchor 作用域的,只清 bx 自己的规则,**不碰用户其它 pf 配置**,优于
Mullvad 文档给出的全局 `pfctl -d`。

## 错误处理与文案

| 情况 | 行为 | 用户看到 |
|---|---|---|
| anchor 装不上 | 并入 `failDNSActivation`:装屏障 + needs_attention | 黄色 + 具体错误码 |
| anchor 拆不掉(终态应为关闭) | 显式报错,不静默;状态明确标记为"仍在阻断" | 直接展示 `sudo pfctl -a <anchor> -F all`(实际 anchor 名),并说明重启同样可恢复 |
| DNS 还原失败致 down 回滚 | 保留既有重启 core 行为(避免留下指向死进程的 DNS) | "已停止但 DNS 还原失败,已重新开启保护以免断网" + 修复指引,取代现有误导性的 `bx did not stop` |
| 巡检发现漂移 | 屏障内自动修复 | 短暂黄色,恢复后转绿 |
| 巡检修复反复失败 | 停在 needs_attention,保持 anchor | 黄色 + 原因 |

## 测试策略

**纯逻辑(免 root)**:pf 规则文本生成、漂移判据、巡检决策、救命命令字符串。

**假 runner 注入(免 root)**:Manager 的装/拆/启动对齐时序;重点覆盖"拆 anchor 不被
DNS 还原失败阻挡"与"desired=off 时启动不装 anchor"。

**源码守卫**:断言 bx 代码中不存在对 `/etc/pf.conf` 的写入。这条守卫保护的是 L3
逃生口——它一旦被破坏,用户就真的可能被锁死。按仓库既有惯例(如
`TestMacMenuDerivesDNSFromStatusJSONOnly`)实现,并必须用注入违规的方式验证守卫本身
会失败。

**真机验证(留给用户)**:配 dry-run 脚本,梯度为——装 anchor 后漂移到公网 DNS 仍可
解析(走 TUN)→ 漂移到私网 DNS 被阻断 → 杀掉 Guardian 验证被拉起后对齐 → 重启验证
清空 → 执行救命命令验证只清 bx 规则。

## 分段验收

实施计划分三段,每段边界上系统均自洽:

1. **pf 结构性封堵 + 恢复四层**——单独完成即可用且安全,兑现不变量 1/2/3/4。
2. **巡检与自动修复**——兑现不变量 5(状态不再陈旧),并让漂移后的解析自动恢复可用。
3. **退出语义与文案**——兑现不变量 6,修掉问题 B 的误导。

中途若需停止,停在任何一段边界都不会留下半成品状态。

## 已知残留风险

1. **`user root` 放行粒度粗于 Windows 的 app-id**:任何以 root 运行的进程都能查
   DNS。这是本轮明确接受的取舍(替代方案"按目的地白名单"会在上游 DNS 配置变更时
   产生新的漂移面)。
2. **与其它 pf 使用者共存**:Little Snitch、公司 MDM 都可能操作 pf(已核实 Docker
   Desktop 走 VM+vmnet,不碰宿主 pf)。冲突面的大小**完全取决于采用哪条 anchor
   挂载路径**,见下节"anchor 挂载路径"。原先"独立 anchor 已把冲突面降到最小"的
   说法不准确,已更正。
3. **Apple 自有流量绕过**:Mullvad 曾公开记录 macOS 上系统组件与 Private Relay
   绕过防火墙规则的情况。若 bx 遇到同类问题,属 OS 层面限制,应如实记入文档而非
   假装已覆盖。
4. **巡检期间的可用性抖动**:修复时会短暂装屏障,期间网络暂停。漂移是低频事件,
   但若遇到反复重置 DNS 的 MDM,可能表现为周期性短暂断网——此时停在
   needs_attention 让用户介入,优于无限重试。

## 待验证项(真机)

0. **anchor 挂载路径(最高优先级,决定整个方案形态)**:路径 B(挂进 `com.apple`
   子 anchor)是否可行——SIP 是否允许第三方写入该命名空间、规则是否**真的被求值**
   (仅"装载成功"不足为证,必须通过行为验证)、以及是否影响 Apple 自己的
   `200.AirDrop` / `250.ApplicationFirewall`。B 可行则冲突面归零,A 作废;B 不可行
   才退回 A 并接受其覆盖风险。另需记录 `com.apple` 下的实际求值顺序,以确定 bx
   anchor 的命名(命名即优先级)。
1. pf 规则的确切文本与 `quick` 顺序:必须验证进 TUN 的 :53 放行、off-TUN 的 :53
   阻断,两者同时成立。
2. TUN 接口名的动态获取:规则中 `<tun>` 需在装载时替换为实际 utun 名。
3. `pfctl -e` 的状态管理:若 pf 原本未启用,bx 启用后关闭时是否应恢复原状态。
4. anchor 装载与既有 `route` 屏障的相互作用:两者机制不同(pf 过滤 vs 路由 reject),
   预期正交,需实测确认。
