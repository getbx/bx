# 按应用看分流 —— 让「谁在走隧道」这个问题有答案

## 起因:一次排查花了半小时,而机器一直知道答案

2026-08-19,项目所有者报「腾讯会议开着 bx 会绕一圈」。查下来是配置缺了
`*.tencent.com`(白名单里只有 `*.qq.com`,那是微信在用),global 模式下 china
列表整个不生效,于是整场会议的信令**和媒体流**都绕到境外 VPS 再回国。

**根因很浅,但发现它的路径很长。** 我最后是靠 `strings /Applications/TencentMeeting.app`
把域名捞出来、再跟配置逐条比对才确认的。而这半小时里,bx 自己在数据面上
**每一条连接都看见了** —— 它知道那条连接来自哪个进程(源端口就在
`stack.TransportEndpointID` 里),也知道自己把它判成了什么(`route.Reason` 里有
判定来源和命中的那一行原文)。两件事都在,只是从来没有被放在一起。

`bx status` 今天能答的是「按**规则**多少次」:

```
user_direct  *.qq.com   attempts=290
default                 attempts=1136
udp_proxy               attempts=1112
```

它答不了「那 1136 次是谁发起的」。而用户脑子里的单位从来不是规则,是**应用**。

这份设计要让「哪些应用在走隧道、哪些直连」变成一眼能看见的事。

## 它不是什么

- **不是流量监控器。** 不做历史、不落盘、不记域名清单。它回答的是**此刻**的分流构成。
- **不是防火墙。** 只观测,不拦截,不改变任何一条流量的去向。
- **不参与分流判定。** 应用身份是旁观者,永远不进 `route.Explain`。见「不变量」一节。
- **不是常驻指示。** 它住在一个用户自己开、自己关的悬浮窗里,不占菜单栏。理由见下。

## 为什么是悬浮窗,不是菜单栏常驻

项目所有者此前否掉过菜单栏「Direct rules: N unreachable」常驻红字,理由是
**「它是常态不是事件(会变墙纸),而且没有附带动作」**。一个按应用的分流看板天然更
啰嗦:正常机器上它永远非空,每次刷新都在变。放进一级菜单就是那条被否掉的红字的
放大版。

悬浮窗(`NSWindow.level = .floating`)同时解掉三件事:

1. **不变墙纸** —— 不看就不开,开着就说明有人正盯着。
2. **采集有了天然的开关** —— 窗口是订阅的载体(见「生命周期」)。
3. **隐私边界跟着窗口走** —— bx 不在后台常年记录你开过什么应用。

---

## 可行性:已由 spike 坐实(2026-08-19)

整件事压在一个前提上:macOS 能不能在 **`CGO_ENABLED=0`** 下把「本地源端口 → PID →
进程名」查出来。标准做法 libproc 要 cgo,而 bx 是静态单文件,这条不能破。

一次性 spike 的实测结论(代码已丢弃):

| 问题 | 答案 |
|---|---|
| 纯 Go 拿得到吗 | **拿得到**。`unix.SysctlRaw("net.inet.{tcp,udp}.pcblist_n")`,`CGO_ENABLED=0` 编过 |
| 覆盖率 | TCP **205/205**、UDP **106/106** 条 pcb 带 PID |
| 准确度(对 `lsof`) | 经 bx TUN 的连接 **一致 29 / 不一致 1 / 缺失 3**,连跑 5 次数字完全稳定 |
| 全表读+解析 | **451µs ~ 1.5ms**,产出 243 条 端口→PID |
| 45 个 PID 解进程名 | **~330µs** |
| 要 root 吗 | socket 表**不要**;但 `kern.procargs2` 读 root 进程要 |

两个解析陷阱,**必须写进代码注释并各配一条回归测试**:

1. **块是 8 字节对齐的,而 `xso_len` 不含尾部填充。** TCP 的 `XSO_TCPCB` 块 len=204,
   下一块其实在 +208。不补齐则整张 TCP 表只解出 1 条 —— 而 UDP 没有那个块,看起来
   一切正常。**一半好一半坏,最容易被误判成「UDP 能做、TCP 不能」。**
2. **`so_last_pid` 在偏移 68、`so_e_pid` 在 72,不在块尾。** 其后还有
   `so_gencnt`/`so_flags`/`so_flags1`/`so_usecount`/`so_retaincnt`/`xso_filter_flags`。
   按「结构体最后两个字段」从块尾往回取,TCP 全读成 0。

那条唯一的不一致是**设计输入,不是误差**:`:59718` 上 `lsof` 说 `trustd`(485),
sysctl 的 `so_e_pid` 说 7961 —— 而 **7961 已经退出了**。macOS 的 socket 可以被委托:
`so_last_pid` 是「谁拿着这个 fd」,`so_e_pid` 是「替谁干活」,而委托方死了之后
`so_e_pid` 是个陈旧值。归因规则必须查活性,见下。

**归因必须住在 Core(root)。** spike 以 uid 501 跑时,bx Core 自己的进程名是空的
—— 非 root 读不到 root 进程的 `procargs2`。放进菜单 App(uid 501)会让所有系统
守护进程显示为空。这不是偏好,是实测。

**一处残留未验**:join 键(gVisor 的 `id.RemotePort` == pcblist 的 `lport`)是推出来
的 —— `metaFromID` 用 `id.Local*` 当目的地,说明 `Remote*` 是应用侧;`lsof` 里应用
socket 确实长成 `198.51.100.1:57076 -> 198.18.0.25:443`(bx 的 TUN 地址 → fake IP),
对得上。但没有真的改 bx 打印一次 `id.RemotePort` 核对过。**实施第一步就该当场证实它**,
而不是等到界面做完才发现两边对不上。

---

## 架构

### 一、判定只有一份,纯的那半单独成包

沿用 `leakcheck`(纯判据)/ `leakserve`(I/O)与 `rulereview` 的形状:

- **`internal/appattr`** —— 纯判据,无 I/O:
  - `ParsePcbList([]byte) ([]PCB, error)` —— 两个解析陷阱锁死在这里
  - `ChooseOwner(lastPID, ePID int32, alive func(int32) bool) (int32, bool)` —— 委托规则
  - `DisplayName(execPath string) string` —— `/Applications/Foo.app/…` → `Foo`
  - `Aggregate(...) Report` —— 按 (路径, 应用) 分组求和
  - `purity_test.go` 按 AST 前缀禁 `net`/`os`/`os/exec`/`syscall`/`golang.org/x/sys`,
    例外逐条明写
- **`internal/supervisor/appsource_{darwin,other}.go`** —— 平台原语,只做两个 sysctl,
  把原始字节交给 `appattr`。非 darwin 是恒返回「不支持」的桩,与 `procscan_other.go`
  同构。

### 二、数据面:只多带一个 `uint16`

`metaFromID`(`internal/tun/engine.go`)今天把 `id.RemotePort` 丢掉了,捡回来:
`route.Meta` 加 `SrcPort uint16`。

**同时加一条守卫**:`Explain`/`ExplainIP` 对 `SrcPort` 必须完全不敏感 —— 同一份输入
只改 `SrcPort`,`Decision` 与 `Reason` 必须逐字节相同。

这条守卫防的是一种非常自然的将来错误:有人看到 `Meta` 里有源端口,顺手按它做分流。
**`Meta` 是连接元数据,不是判据全集。** 应用归因是旁观者,一旦它能影响判定,
「用户看到的分流」和「bx 实际执行的分流」就有了第二个变量。

### 三、生命周期:采集只在有人看的时候发生

订阅式。窗口打开 → 订阅;窗口关闭、菜单退出、连接断开 → 立即停,缓冲清零。

- 未订阅时,热路径的全部代价是**一次 atomic bool 读**。
- 全内存。**不落盘、不进任何日志。**(与 leakcheck「检测结果不留存」同源。)
- Guardian 或 Core 重启 → 订阅消失,从零开始。**这是特性**:它保证没有任何东西
  跨重启记住你开过什么应用。

订阅带 **30 秒 TTL**,菜单每次拉取顺带续期。**理由是失效模式**:菜单被强杀、
窗口进程崩溃时不会有人来退订,没有 TTL 就会永久采集下去 —— 而「没人看的时候
精确为零」是这个设计的隐私前提,不能靠对方守规矩来保证。

### 四、归因规则:三态,不猜

```
so_e_pid > 0 且该进程仍存活   → 用 so_e_pid
                                (把 nsurlsessiond / trustd 这类代劳者归回真正的应用)
否则                          → so_last_pid
两者都拿不到 / 端口查不到      → unknown
```

**`so_e_pid` 必须查活性** —— spike 实测撞到过一个指向已退出进程的 `so_e_pid`。
不查活性就会把流量记在一个不存在的应用上,而那种错误在界面上完全看不出来。

`unknown` **在它所属的那一组里单独成行**(判定是已知的 —— 不知道的只是「谁发起的」,
所以它仍然落在 TUNNEL / DIRECT / BLOCKED 之一里),**不并进任何应用,也不丢弃**。与 `Tristate`、
`WhoOwnsTheRoute` 四态、`BuiltinListChecked` 同一条纪律:**「问不出来」不许被压成
一个具体答案。** 一个 unknown 占比很高的界面是在告诉用户「这份数据现在不可信」,
那是有用的信息;把它悄悄摊进已知应用里则是编造。

### 五、字节数按源端口记

不按连接对象记,按**源端口**记:两张 `[65536]atomic.Int64`(上行/下行)。
`copyOneWay` 的 `onWrite` 闭包捕获 srcPort,一次 `Add` 即可 —— 无锁、无分配、O(1)。
只在订阅时分配(2 × 512KB)。读取时按 srcPort join 成应用再求和。

**已知不精确,必须在界面和文档里都说明**:端口会被复用,旧连接的残留字节可能算到
新连接的应用头上。缓解是 worker 见到某端口的新连接就清零该槽。**这是近似值,
界面不该把它显示成精确账。**

为什么仍然要做:没有量级的话,「偶尔连一次」和「一直在灌」在界面上长得一模一样,
而后者恰恰是用户要找的东西(腾讯会议那次,媒体流的量级就是唯一的信号)。

### 六、三组,不是两组

```
TUNNEL    走隧道的应用
DIRECT    直连的应用
BLOCKED   被 kill-switch 挡掉的应用
```

**同一个应用可以同时出现在多组** —— Chrome 一部分域名直连、一部分走隧道是常态。
把它压成一行「Chrome:混合」是把最有用的那一半信息扔掉。

`BLOCKED` 这一组几乎零成本(`ErrBlocked` 那条路已经在计数),而它直接回答一个
今天完全没有答案的问题:**「为什么这个 App 一开 bx 就废了」**。

### 七、发布面

```
Core      /v0/apps            新增,只读
Guardian  /v1/apps            新增,authorizeOwnerPeer(与 /v1/rules、/v1/up 同一道门)
          CapabilityApps = "apps"
菜单      AppTrafficWindow.swift      NSWindow(level: .floating)
          AppTrafficModel.swift       纯函数,可进 Swift 测试套件
```

**「没订阅」与「订阅了但还没数据」分开报**,不共用空列表 —— 与
`BuiltinListChecked` 刻意无 `omitempty` 同源。空列表读作「一条都没有」是句自洽的
假话。

**能力门控,绝不「试着拨一下看看」** —— 照 `requireStatusWatchCapability` /
`watchIsAvailable` 的先例:旧 Guardian 不认识 `/v1/apps` 会回 404,而客户端无从区分
「这版不支持」与「这版支持但此刻没数据」。判据是 `Capabilities` 里有没有 `apps`。

窗口刷新复用已有形状:`isVisible` + `refreshIfVisible`,以及
`serversFetchInFlight` 那个 in-flight 守卫(watch 时代刷新是事件驱动、可能连着来)。

**授权门取 `authorizeOwnerPeer` 而不是 root-only**,判据与 `/v1/rules` 那次一致:
能关掉保护的人已经能做更坏的事,取一致是要点。

---

## 不变量

1. **应用身份永不进入分流判定。** 由 §二 的守卫测试钉住。
2. **未订阅时不做任何归因工作。** 由一条测试钉住:未订阅时跑一批连接,注入的
   `appsource` 必须**一次都没被调用**、且没有分配那两张 65536 的数组。
   (**不用基准测试** —— 基准不会让 CI 转红,而一条「悄悄开始采集」的回归在
   性能数字上也未必看得出来。)
3. **不落盘。** 由一条测试断言整条路径不写文件系统。
4. **停止路径不受影响。** 订阅状态不参与 `Down`/`teardown` 的任何判断 —— 与
   2026-08-04 那次 71 分钟事故换来的纪律一致:停止不许因为别的事没做完而变慢或失败。
5. **非 darwin 上整条链是「不支持」而不是空数据。**

## 测试策略

- **fixture 从真机采**:把一份真实的 `pcblist_n` 字节快照(已脱敏:只留结构与端口,
  抹掉地址)存进 testdata,`ParsePcbList` 对它断言。**合成 fixture 挡不住这两个坑** ——
  8 字节对齐那个坑只在有 `XSO_TCPCB` 块时才出现。
- **两个坑各一条回归**:去掉对齐补齐 → TCP 解析条数必须塌;把 pid 偏移改回块尾 →
  必须读出 0。两条都要**变异验证**确认会红。
- **委托规则**:`ChooseOwner` 对「`so_e_pid` 指向已死进程」必须回落 `so_last_pid`,
  对「两者皆无」必须回 unknown。
- **purity**:`internal/appattr` 的 AST 前缀禁令,例外逐条明写。
- **Swift 侧**:`AppTrafficModel` 是纯函数,进 `scripts/test-macos-menu.sh` 的文件清单
  (**漏登记会一次都不跑而 CI 全绿** —— 2026-08-10 的教训,已有守卫测试钉着那份清单)。
- **接线**:`/v0/apps → /v1/apps → 菜单` 这几跳各要一条测试。**这个仓库全部的事故都在
  组装根上**,而「判据层测试绿、那一跳没人读」是反复出现的形状。

## 明确不做

- 历史与持久化(隐私面完全不同,要单独决定)
- 按域名的完整连接列表(那是另一个功能)
- Linux / Windows(菜单只有 macOS;`bx status` 也不加 —— 那两个平台一个原语都没有,
  加了只会恒显示「不支持」,而满屏「不支持」正是 `internal/observe` 当初拒绝在
  非 darwin 附观测的理由)
- 任何分流行为的改变

## 已知缺口(不影响交付,但改这块前要知道)

- **字节数是近似值**(端口复用,见 §五)。
- **短连接仍可能漏**:worker 异步解析时若 socket 已关闭,该连接落进 unknown。这是
  「不猜」的代价,可接受 —— 但如果真机上 unknown 占比很高,说明 worker 太慢,
  那是要修的信号,不是要藏的数字。
- **`so_last_pid` 的语义是「最后一个用过这个 socket 的进程」**,不是「创建者」。
  绝大多数情况两者相同,但 fd 被传递(`SCM_RIGHTS`)时会不同。今天不处理。
- **join 键未实测**(见「可行性」末段)。
