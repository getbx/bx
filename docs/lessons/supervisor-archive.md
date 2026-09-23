# supervisor —— 2026-09-23 从根目录 CLAUDE.md 下沉时的原文存档

**这是存档,不是现行判据。** 2026-09-23 根目录 CLAUDE.md 开始按代码目录下沉,这些段落的
**判据**浓缩进了 `internal/supervisor/CLAUDE.md`(那一份才是改代码之前要读的);这里逐字保留当时的原文,给想知道
「当初怎么换来的」的人看 —— 事故经过、真机数字、变异实测、被否掉的方案的完整理由。
读到与 `internal/supervisor/CLAUDE.md` 冲突的地方,以后者为准。

---
## 按应用看分流(2026-08-19,**整套真机未验**)

起因是一次真实排查:项目所有者报「腾讯会议开着 bx 会绕一圈」。根因很浅 ——
白名单里只有 `*.qq.com`(那是微信在用),而腾讯会议整个跑在 `*.tencent.com` /
`*.qcloud.com` 上,global 模式下 china 列表不生效,于是**信令和媒体流全部**绕到
境外 VPS 再回国。**但发现它花了半小时**,最后是靠 `strings /Applications/
TencentMeeting.app` 把域名捞出来比对才确认的 —— 而这半小时里 bx 在数据面上
**每一条连接都看见了**:源端口就在 `stack.TransportEndpointID` 里,判定就在
`route.Reason` 里,两件事都在,只是从来没被放在一起。(**2026-09-01 之后不再如此**:
`bx explain <目标>` 把判定与依据放在了一起,见下文;这个窗口答的仍是「谁在用」,
explain 答的是「这一个为什么」。)

`bx status` 答得出「按**规则**多少次」,答不出「那 1136 次是谁发起的」。而用户
脑子里的单位从来不是规则,是**应用**。

**一个用户自己开关的悬浮窗**(`NSWindow.level = .floating`),三组 —— 界面上的标题是
`Through the tunnel` / `Direct` / `Blocked`(`AppTrafficReport.sectionTitle`,判据在纯
模型里、有 Swift 测试盯着;**这里此前抄的是三个全大写的短名,而屏幕上从来没有出现过
那三个词** —— 下文那份真机验收清单还叫人「确认 BLOCKED 组出现内容」,照着找会找不到),
**同一个应用可以同时出现在多组**(Chrome 一部分
域名直连、一部分走隧道是常态,压成一行「混合」等于把最有用的那一半扔掉)。
**不进菜单栏常驻** —— 与此前否掉「Direct rules: N unreachable」常驻红字同一条
判断:常态不是事件,会变墙纸。

**包**:`internal/appattr`(**纯判据**,`purity_test.go` 按 AST 禁 net/os/exec/
syscall/x/sys)· `internal/supervisor/appsource_{darwin,other}.go`(平台原语)·
`internal/supervisor/apptraffic.go`(订阅、环形缓冲、按端口字节账)· Core
`/v0/apps` → Guardian `/v1/apps`(`authorizeOwnerPeer`,与 `/v1/rules` 同一道门)
→ 菜单 `AppTrafficModel.swift`(纯函数)+ `AppTrafficWindow.swift`。

### 两个字节布局陷阱(共同特征:失败得不明显)

① **块按 8 字节对齐,而 `xso_len` 不含尾部填充。** TCP 的 `XSO_TCPCB` 块 len=204,
下一块其实在 +208。不补齐则**整张 TCP 表只解出 1 条,而 UDP 表完全正常**(它没有
那个块)—— 一半好一半坏,最容易被误判成「UDP 能做、TCP 不能」的平台限制。
② **`so_last_pid` 在偏移 68、`so_e_pid` 在 72,不在块尾**(其后还有 `so_gencnt`/
`so_flags`/`so_flags1`/`so_usecount`/`so_retaincnt`/`xso_filter_flags`)。按
「结构体最后两个字段」从块尾往回取,TCP 的 pid 全读成 0。

**fixture 必须来自真机**(已脱敏提交进 `testdata/`):合成数据不会有 `XSO_TCPCB`
块,挡不住陷阱 ①。另配一条**合成数据的偏移隔离测试**(块内每个 4 字节字填不同
哨兵值)—— 真机 fixture 挡布局陷阱,合成数据挡偏移错位,**两条各守一半**,因为
那 8 条 golden 记录在 offset 44/52/56/60/80/96 上恰好也全是 0。

### 几条判断上的取舍

- **`so_e_pid` 必须查活性。** 它是「替谁干活」(把 `nsurlsessiond`/`trustd` 这类
  代劳者归回真正的应用),但**委托方死了之后它是个陈旧值** —— spike 真机撞到
  `trustd` 的 `so_e_pid` 指向一个早已退出的 7961。不查活性就会把流量记在一个
  不存在的应用上,而**那种错误在界面上完全看不出来**。
- **归因的键必须带协议维度**(`appattr.PortKey{Port, UDP}`)。TCP 与 UDP 的端口
  空间**相互独立**,同一个数字可能同时被两边占用;只用 `uint16` 会让 UDP 那轮
  静默覆盖 TCP 的归因 —— 流量记到错的应用名下,**而错的归因产生的信号就是没有
  信号**(界面只显示一个名字,不显示冲突)。
- **端口复用清账只对 TCP 生效。** TCP 一个源端口同时只有一条活连接,复用即旧连接
  已终结;**UDP 一个 socket 服务多个对端**(gVisor 按 5 元组建流,一个应用打 STUN
  + TURN + 多个 peer 就是 N 条流 ⇒ N 次 `Record`),那些流属于同一个 socket、同一个
  应用,**累加才是对的**。无条件清账会让**腾讯会议的媒体流字节系统性偏低而连接数
  完全正常,且无任何报错** —— 恰好命中这个功能最初的用例。
- **字节数是近似值,界面上必须说出来。** 按源端口记,端口复用时旧账可能算到新
  连接头上。窗口底部固定一行 `Byte counts are approximate (ports get reused).`,
  由守卫钉住。
- **字节账是 map + 一把全局 mutex,不是无锁定长表**(`apptraffic.go` 的
  `addBytes`,挂在 `copyOneWay` 的 `onWrite` 上)。spec 初稿写的两张
  `[65536]atomic.Int64` 在归因键补上协议维度之后就不成立了 —— 定长表要开**四张**
  (TCP/UDP × 上/下),订阅期间常驻 2MB 而真实占用通常只有几百个槽;map 是权衡
  后的选择,不是疏忽。**代价如实记在这里**:窗口开着时整机每一次转发写都串行
  经过同一把锁,临界区很短(一次 map 哈希 + 一次加法,无 I/O、无系统调用、键已
  存在时无分配),但它是**全局**锁,争用的数字只有真机能给。**未订阅时字节记账
  连锁都不碰**(第一句 `t.active.Load()` 就返回)——但**这条只对字节记账成立**:
  `Record`/`ConnClosed` 为了维护活连接表现在无条件抢锁,见下面「订阅之前建立的
  连接」一条。
- **`unknown` 在它所属的组里单独成行**,不摊进已知应用、也不丢弃。
  **「unknown 占比高」是可用的故障信号**(2026-08-20 起,见下一条 —— 在那之前
  不是,而当时那句忠告本身把因果说反了)。
- **报告是「最近 60 秒」,不是「自打开窗口以来的累计」(2026-08-20 改)。**
  见下面「unknown 是最大的一行」一节:因果曾经被写反,真机证据把它翻了过来。
- **陈旧提示只陈述观测到的事实,原因只给可能性**(`Protection may be off.`)——
  这条路上 bx 分不清「保护关了 / Guardian 忙 / Core 刚重启」,**断言其中一个就是
  编答案**。与「国旗只在能证明时给」同一条。

### 采集只在有人看的时候发生

窗口开着才订阅,**30 秒 TTL**、靠拉取续期(菜单被强杀时没人退订,而「没人看时
不问内核、不记字节、不攒历史」是这个设计的隐私前提,不能靠对方守规矩)。
**全内存、不落盘、不进日志。**未订阅时**不问内核、不记字节、不攒历史**,由一条
测试逐条钉住(appSource 一次没被调用 + `records`/`bytesUp`/`bytesDn` 保持 nil)
——**不用基准测试,基准不会让 CI 转红**。
**「未订阅时热路径的全部代价是一次 atomic 读」/「没人看的时候开销精确为零」
这两句话在 2026-08-20 之后不再成立,别再照抄**:未订阅时仍要维护一张活连接表,
代价是**每条连接两次全局锁获取 + 四次 map 操作**(建连时 `Record` 读改写一次、
关闭时 `ConnClosed` 读删一次),是每条连接一次、**不是每个包一次**;那把锁与
字节记账是**同一把全局锁**。
隐私上仍然成立 —— 表里只有端口与判定,**没有应用名**(应用名是 `Snapshot` 时
才去问内核的)。

### 订阅之前建立的连接必须看得见(2026-08-20,真机 bug)

`recordApp` **只在建连的那一刻**被调用(`dialer.DialWithInitial`),于是**订阅
之前就已经建好的连接从来没有被 `Record` 过,永远不会出现在窗口里** —— 窗口只
看得见「打开它之后新拨的连接」。真机现象:项目所有者打开窗口只看到 **2 个应用**,
而 `bx status` 同时报 **66 条**活跃连接。**后果最重的是长连接**:会议媒体流、
WebSocket、SSH、Colima 隧道,建连一次跑几小时,**全在盲区**;而这个功能最初的
用例正是「腾讯会议为什么绕一圈」,开会开到一半才打开窗口,它会告诉你腾讯会议
**不在**走隧道,与事实相反。**十二轮审查没抓到它,因为所有测试都是「先订阅、
再造连接」** —— 测试输入让缺陷不可见(与这一支上「守卫钉住的是缺陷旁边的东西」
出现六次同一个形状)。

修法是**活连接表 + 订阅时播种**:`AppTraffic.live` 是 `PortKey →
(path, source, rule, refs)`,`Record` **无条件**写入(未订阅时也写),
`tun.ByteAttributor` 新增的 `ConnClosed(srcPort, udp)` 在引擎 `handleConn` 的
defer 里删除,`Subscribe()` 把当时全部活连接作为记录播进新建的环形缓冲。

四条不许动的细节,每条都有测试钉住:
- **种子只在一次订阅开始时播,续期不播。** 菜单每 5 秒调一次 `Subscribe`,
  每次都播会让同一条连接每 5 秒多算一次 —— 连接数随时间线性膨胀而无一处报错。
- **`ConnClosed` 必须 defer 在拨号之前。** 判定(`recordApp`)发生在 `Dial`
  内部,被 kill-switch Block 的连接**已经进了表**而拨号返回错误;defer 在拨号
  成功之后,这类条目永远没人删 —— 而隧道挂掉时被 Block 的连接恰恰最多。
- **`refs` 记数,不是裸 delete。** UDP 一个源端口上有多条并存的流(gVisor 按
  5 元组建流,一个会议 socket 打 STUN + TURN + 多个 peer),裸 delete 会在第一
  条流结束时把整条 socket 从表里抹掉,种子于是看不见一个正在灌媒体流的会议。
  它也顺手兜住「旧连接的 `ConnClosed` 晚于新连接的 `Record` 到达」这个真实竞态。
- **活连接表的唯一边界就是 `ConnClosed`。** 少了它,泄漏出来的陈旧条目**不会
  涨到 OOM** —— 键是 `PortKey{uint16, bool}`,硬上限 131072 条、约 10–15MB。
  **真正的后果发作得更早也更糟**:陈旧条目攒到几千条之后,一次 `Subscribe` 的
  种子就能把 4096 格的环形缓冲填满并绕圈,**新记录被自己的陈旧种子挤掉** ——
  报告从「正确但残缺」退化成「错的」,而仍然没有任何一处会报错。由 `liveSize()`
  这个白盒窗口 + 「开关 N 轮后表大小回落到 0」钉住,变异验证过。
  (**「单调增长到 OOM」是第一版写下的过度声明,已改。** 这个仓库为「文档说的
  比实际大」纠过三轮,这条差点成为第四轮。)
- **~~已知缺口:种子把一个 socket 上并存的 N 条流压成一条~~(2026-08-24 已补)。**
  此前 `live` 按 `PortKey` 记、同键最后写入者胜,种子每键只发一条。于是一个会议
  socket 同时打 STUN(可能直连)+ TURN(可能走隧道)时:**订阅前**建立的只出现
  在**一个**组里、连接数恒为 1;**订阅后**建立的正确地出现在**两个**组里 ——
  **同一个事实,按窗口打开时机给出不同答案**,而这正是「腾讯会议为什么绕一圈」
  要回答的问题。
  **补法**:活连接表改成**一条流一个条目**(键是不复用的单调 flowID),另有一张
  `map[PortKey]int` 索引供「这个端口还开着吗」那三处 O(1) 查询。当初判定「真修
  不便宜」的理由是 `ConnClosed(port, udp)` 无从知道该减哪一档 —— 而把释放挂到
  拨号返回的 conn 自己身上之后,它拿得到自己那条流的 ID,**那个障碍是被上一条
  改动顺手搬开的**。同一块补齐的还有裁剪那半:活性豁免从「按端口封顶一条」改成
  「按流封顶一条」,否则并存的流在**过期之后**仍会被压成一条(同一个缺陷的另一
  半,只是发作得晚一点)。
  那两条「明确钉住已知错误行为」的测试因此被**翻过来**,其中「目的地」那条还拆
  成了两半 —— 并存(两个目的地都报)与端口复用(只报接手的那一个)在此之前塌成
  同一句话,现在各有各的正确答案;少了「复用」那一半,一个「永远报全部历史目的
  地」的实现照样全绿,而那是把一张「此刻在连什么」的表悄悄变成一份历史记录,
  隐私性质完全不同。五条变异各咬中一组不同的测试。
- **已做(2026-08-24):配平按构造成立。** `ConnClosed` 从 `tun.ByteAttributor`
  挪到 `dialer.AppRecorder` —— **谁记账谁释放**。`DialWithInitial` 变成薄壳,
  判定全在 `dialInner` 里,壳只做一件事:拿到 conn 就包一层(`appTrackedConn`,
  `Close` 时经 `sync.Once` 释放),返回错误就地释放。**单一漏斗是必需的**:
  `dialInner` 有十几个 return,逐个包会漏,而漏掉的那一个正是「记了账没人释放」。
  引擎那条 defer 一并删掉 —— 留着就是双重释放,refs 提前归零会把一条**还开着**
  的连接从表里抹掉。**接口直接扩而不是可选类型断言**(与 `stats.DecisionCounter`
  同一条:「实现里没有就静默不计」的释放者与没有这个功能在输出上完全一样)。
  **搬家换来的代价要记住**:释放现在骑在 `Close` 上,于是「relay 真的会关掉
  upstream」从一个显然的实现细节变成了活连接表正确性的**前提** —— 由
  `TestEngineClosesTheUpstreamConnSoTheDialerCanRelease` 单独钉住(变异验证:
  删掉 `upstream.Close()` 即转红)。包装层**嵌入** `net.Conn` 而不逐个转发方法:
  吞掉一个 `SetReadDeadline` 不会报错,只是连接再也不会因空闲而结束,goroutine
  与 fd 一起泄漏而界面上什么都看不出;热路径 `copyOneWay` 是手写 Read/Write
  循环、没有 `io.Copy`,所以包一层不丢 `ReaderFrom` 快路径 —— 这一点动手前核过。
  五条变异各咬中一条测试:错误路径不释放 / 重复 Close 释放两次 / Close 不释放 /
  包装层吞掉 deadline / relay 不关 upstream。**不要**改成「把 `len(live)` 发布进 `/v0/apps`
  或 `bx status --json`」:那恰好在错的地方可见(`/v0/apps` 只在**有人订阅时**
  可读,而泄漏正是在**没人看的时候**累积),且一个没有阈值的常驻数字会变墙纸
  (与项目所有者否掉「Direct rules: N unreachable」同一条判断)。

窗口开着时另有一个 **5 秒**心跳,因为菜单的兜底轮询是 60 秒、**比 TTL 还长**,
光靠环境刷新会让窗口反复跳回「Not collecting」并清零。心跳**必须活不过它的窗口**:
首拉失败时窗口从未创建 ⇒ `windowWillClose` 永不触发 ⇒ 心跳永不停止,每 5 秒一次
失败拨号 + 一条 Guardian 日志,**永久,直到菜单进程被杀** —— 而它恰好发生在最常见
的探索场景(保护关着时点一下那个菜单项)。

### 观感与可行性:过程搬走了

可行性 spike 的真实数字(`unix.SysctlRaw("net.inet.{tcp,udp}.pcblist_n")` 纯 Go 拿得到
PID、对 `lsof` 一致 29/不一致 1/缺失 3、全表 451µs~1.5ms)、观感那一轮的每一处取舍
(七列的由来、搜索框为什么住在重建的视图树之外、目的地小字的 `+N`、速率从客户端做差
改成服务端按端口做差)、以及守卫在这一支上被攻破的两次,全在
`docs/lessons/2026-08-app-traffic.md`。下面留判据。

**改这块最容易静默出错的三处**:① `appTrafficNumericColumns` 的下标 ——
`53a4f0a` 把图标搬进应用名格、列从八降到七之后每个下标都要往前挪一位,挪错**不会有
任何编译错误**,现象只是「右对齐落在错的列上」;② **速率的 `nil` 与 0 是两件事** ——
前者是「不知道」(第一次采样只立基线),后者是「量出来就是 0」,压成同一个 0 会把
「不知道」显示成「闲着」;③ **搜索框必须在 `ensureWindow()` 里创建一次**,长在每 5 秒
被拆掉重填的那棵树里的话,用户打两个字就会连同焦点一起消失,而窗口看起来完全正常。

### 真机未验(全部)

悬浮窗、心跳、陈旧横幅、三组渲染、能力门控 —— **没有人在屏幕前点过**。
**但「列宽会不会把规则挤没」这一条已经被真机回答了,而且答案是「会」
(2026-09-13 更正:此处此前还挂着它,而它三周前就有答案了)** —— 2026-08-20
那一版八列表格上过屏幕,截图里图标列自己涨到 ~350pt 把后面的列全挤扁,
`53a4f0a`(2026-08-22)因此把图标搬进应用名格、列降到七,并加了搜索框与目的地
小字。**改动本身随后又没上过屏幕**,所以仍未验的是**那一版之后**的观感:七列
在默认窗口宽度下的分配、图标取不到时应用名那一格长什么样、搜索框过滤时三个分组
标题都还在不在、目的地小字的 `+N` 与 toolTip。速率还有一条只有真机能验:
**第一拍必然是破折号**,第二拍起才有数 —— 若真机上它**一直**是破折号,说明
`refreshIfVisible` 那条路没走到(而窗口看起来完全正常)。窗口那半
AppKit 代码本仓库一行测试都盖不到(只有 `swift build` 证明能编译、一条文本守卫
证明那句小字被摆进了视图树)。真机验收清单:① 三组都在、腾讯会议出现在预期的组里;
② 关掉窗口后 CPU 与拨号回落,30 秒后再开数据从零开始;③ 制造 kill-switch 阻断
确认 `Blocked` 组出现内容;④ `unknown` 占比不高 —— **2026-08-20 起这条在任何时刻
都成立**,不再只限「开窗后 10 秒内」(报告改成 60 秒滚动窗口 + 归因提前到连接还
开着时做;此前那条「之后单调增长是预期行为」的限定,连同它背后写反的因果,已改);
⑤ 每 5 秒重建视图树会不会把滚动位置拉回顶部(已知,未修);
⑥ 窗口开着时采样 Guardian 的 CPU。

设计 `docs/superpowers/specs/2026-08-19-app-traffic-attribution-design.md`、
计划 `docs/superpowers/plans/2026-08-19-app-traffic-attribution.md`。

### unknown 是最大的一行 —— 因果曾被写反(2026-08-20,真机证据)

功能装上生产 Mac 之后,窗口里 **`Unknown app` 是最大的一行:16 条连接,而且是
唯一有真实速率的一行**(959 B/s 上 / 1.8 KB/s 下)。

**速率是决定性证据**:如果 unknown 主要是「累积下来的陈旧记录」,累计值就不会
涨、速率应该是 0。速率不为零 ⇒ **新的 unknown 记录在源源不断进来。**

**主因是「归因发生在读取时」**:`OwnersByPort()` 全仓**只在 `Snapshot()` 里被调
一次**(菜单 5 秒一拍),于是**任何活不过一次刷新的连接,在被归因之前 socket 就
没了** —— 内核里查不到那个端口,结构性地落进 unknown。
**spec 与本文件此前都把因果写反了**(称短连接「只是同一现象的一个瞬时特例,不是
它的主因」),已改;那句话的三处复读也一并清了 —— **这个仓库为「只清点名的那一句、
不清同一句话的其它副本」栽过两次。**

修法两件,缺一不可:
- **滚动窗口 60 秒**(`appattr.ReportWindow`,纯判据 `InReportWindow`,`now` 由
  调用方传)。窗口回答的是「**此刻**谁在连谁」,这是产品决定不是随手取的数。
  **字节账必须跟着裁** —— 只裁记录不裁字节账,那张 map 仍然只涨不落,而 UDP
  刻意不在端口复用时清账,于是一笔早该消失的账会整个算给下一个拿到这个端口的
  应用。**「滚动窗口」滚一半等于没滚。**
- **归因提前**:订阅期间一条 **250ms** 的后台 resolver 把还没解析的记录解出来、
  **存进 `ConnRecord.Owner`**;`Snapshot` 改为优先用记录里存着的归因,只对仍未
  解析的做最后一次现查(并把结果也写回记录,让下一次受益)。连接关掉之后归因
  **仍然在**,这正是这次要修的那件事。

**四条不许动的细节**(数的是下面的条目 —— 此前这里写「三条」而列了四条):
- **resolver 绝不许持着 `t.mu` 调 `OwnersByPort`。** 那是两次 sysctl
  (451µs~1.5ms),而 `t.mu` 是整机每条连接、每次转发写都要过的那把**全局**锁 ——
  持锁去问就是每 250 毫秒把整机的记账阻塞一次。形状固定:**持锁挑出待解析的键
  → 放锁 → 问内核 → 重新持锁按序号写回**。白盒守卫让注入的 `appSource` 在被调用
  时自己去抢 `t.mu`(与 `TestAppTrafficByteAccountingTakesNoLockWhileUnsubscribed`
  同一手法),持锁就死锁、超时即红。
- **写回按序号(`bufferedRecord.seq`,单调递增、永不复用),不按下标。** 放锁
  期间缓冲可能已被裁剪重排,按下标写回会把归因写到别人头上;按序号则「这条记录
  已经不在了」自动退化成「找不到,跳过」。序号住在 supervisor 这一层,不进
  `appattr.ConnRecord` —— 那是纯判据的输入,不该带缓冲的记账。
- **还开着的连接不按时间裁。** 只按时间裁会让一条开了两小时还在灌流的会议媒体流
  在开窗 60 秒后消失 —— 那正是「订阅之前建立的连接看不见」那个真机 bug 换一种
  方式复发。**裁剪的早退判据只看时间**(记录按时间序,最旧的还在窗口内 ⇒ 全在):
  写成「最旧的那条留得住吗」,一条因为还开着而留住的长连接会让它后面所有该裁的
  记录一起逃过裁剪。
- **未订阅时 resolver 一次都不跑,TTL 过期就停。** 「没人看时不问内核、不记字节、
  不攒历史」是这个设计的隐私前提,一个自己滴答的 goroutine 会把它悄悄破掉而界面
  上完全看不出来。测试里默认**关掉**后台 resolver(`newAppTrafficNoResolver`)——
  一个每 250ms 自己去问内核的 goroutine 会让「问了几次」这类断言变成掷骰子,而
  **一个偶发红的闸门比没有闸门更糟**。

**已知代价**:窗口开着时 Core 每秒最多 4 次 `OwnersByPort`(约 0.6% 一个核),
没有待解析记录时那一拍连内核都不问;报告不再答得出「开窗以来一共多少」。
**真机未验** —— 以上全部由单元测试 + 六处变异验证覆盖。


## macOS 的 DirectDialer 一直到不了公网(2026-08-13,真机已验)

`DirectDialer` 用 `IP_BOUND_IF` 绑物理网卡防环,而**它只查该接口的 scoped 路由表**。
macOS 只在有**多个活跃网络服务**时才装 per-interface scoped default;单服务(只有
Wi-Fi)的机器上 `default` 的标志是 `GLOBAL`,scoped 表里根本没有它。

真机实证:`route -n get -ifscope en0 8.8.8.8` → **`not in table`**;
`bound-en0 udp 223.5.5.5:53` → **`network is unreachable`**;而
`bound-en0 tcp <VPS>:443` → **通**(bx 自己装了那条 /32 的 en0 路由)。

**后果:所有用户 direct 规则全部 ENETUNREACH,而隧道毫发无伤** —— 因为隧道的
server bypass 恰好是一条显式 en0 路由。**这一直是坏的,只是 bx 从来数不出失败**,
所以没有人知道;结果计数上线的第一分钟就把它显形:`*.qq.com 1291 条,失败 1289`、
`*.icloud.com 6/6`、`*.push.apple.com 6/6`。(排查 Steam 时我曾归因到「用户的规则把
它逼上了一条不通的路」—— 方向对,**根因判浅了一层**:不通的不是那条网络,
是 bx 自己的直连器。)

**修法**:`Hijack` 顺手给物理接口装一条 scoped 默认路由(`route add -ifscope <dev>
default <gw>`)。它**只进 scoped 表、不碰全局表**,隧道的 `0/1`+`128/1` 照旧压过一切 ——
只有显式用了 `IP_BOUND_IF` 的 socket(恰好就是 bx 自己的直连/解析/socks)会用到它。
不削弱 kill-switch:那是 Dialer 里的判定,不是路由属性。

**这条路由必须「可选」,而且装失败时不许进待删列表** —— 后半句是要害:多服务的
Mac 上系统自己就有一条同款,`route add` 以 "File exists" 失败,若记进待删列表,
拆除时就会 `route delete -ifscope en0 default` **删掉系统自己的那条**,打断用户的网络
而 bx 无从恢复。为此 `darwinRouteSpec` 加了 `optional`;非可选的路由(split-default
那些是保护本身)失败仍然中止并回滚。

**真机验收**:`direct/direct_failed` 从 `1322/1308` 变成 `45/0`,十条用户规则全部
零失败(`*.qq.com` 17/0、`*.icloud.com` 11/0、`*.icloud-content.com` 15/0)。
**注意区分两种错误**:`network is unreachable` 是路由问题,`i/o timeout` 是目标不应答 ——
验收时探针里有一个百度 IP 是后者,与本修复无关。


## 拆除台账:Run 的还原顺序变成数据(2026-08-30,真机未验)

`Run` 里那一串有序 `defer` 改走 `internal/supervisor/teardown.go` 的
`teardownLedger`。**动它的理由不是好看**:defer 是同步的,一步拆除挂住,它
后面的每一步都不会跑,只有关机 watchdog 强制退出兜底 —— 而强制退出会把
**剩下的还原全部跳过**(run.go 那条 watchdog 的注释里早就点名了嫌疑:
`eng.Close`/`tun0.Stop`)。台账给出 defer 给不了的三件:**逐步限时**
(一步挂住只损失那一步)、**命名与记录**(关机时查得出卡在哪,此前只有一份
goroutine dump)、**可断言**(顺序是数据)。

**四条不许动的细节**(数的是下面的条目 —— 此前写「三条」而列了四条,只有把没加粗的
那条第四项不算进去才对得上,而它一样不许动):
- **LIFO 一字不改**:后获取的先释放 —— 路由还原必须排在关 TUN 之前;
- **`defer teardowns.unwind()` 的位置承重**:落在第一个系统资源之前。混着
  来(一半 defer 一半台账)会把两者的相对顺序悄悄颠倒;迁移前后逐条比对过
  `signal.Stop → 台账 → cancel` 与原状一致。**步数这里不写了**:原文写着
  「九处 defer」而同一次迁移落进台账的是 11 处(迁移提交 `60fdfe2` 自己的
  commit message 就同时印着这两个数,十二行之内自相矛盾),今天又长到了 13 处 ——
  **这个数不承重,承重的是 LIFO**;真要数就 `grep -c 'teardowns.push' internal/supervisor/run.go`;
- **超时的 goroutine 是放生不是杀掉**(Go 杀不掉),所以「超时」只等于
  「它没在预算内做完」,不等于「那件事没做成」—— 日志措辞按这个来。
- 单步预算与 `shutdownGrace` 留三倍余量,由
  `TestTeardownStepBudgetLeavesRoomBeforeTheShutdownWatchdog` 钉住:一步挂住
  绝不该把 watchdog 逼出来。watchdog **不许删**,它兜的是台账之外的挂点。

**一条要记住的判据边界**:FIFO 变异下**单测转红而集成台照样绿** ——
顺序性质由单测背书,不是台子。linux 的 `ip rule del` 不依赖 TUN 还在,台子
照不到;真正吃这条顺序的是 darwin 的 split-default。别把台子的绿读成
「顺序有背书」。

## 后台工人登记册:一个 goroutine 的 panic 不再打死整机(2026-08-30,真机未验)

`Run` 里那一批长命 goroutine 此前是裸 `go f(ctx)`、**零个 `recover()`**。
裸 goroutine 的 panic 不会被 `Run` 的 defer 接住 —— Go 当场终止进程,而
**别的 goroutine 的 defer 一个都不会跑**:进程没了而内核里的 ip rule /
策略路由**还在**,整机流量指向一个已经不存在的 TUN。现全部经
`internal/supervisor/workers.go` 的 `workerRegistry.start` 启动(具名 +
panic 收在自己那一层)。

**一律 recover-and-continue,是逐个想过的结论不是图省事。这份枚举的全部意义
就是回答「这个工人死了会静默降级成什么」,所以它必须是全的** —— 判据不是记忆,
是 `grep -n 'workers.start' internal/supervisor/run.go`,那里出现几次就该有几条:
mutation-engine 死 ⇒ 切服务器不工作 · tailscale-bypass 死 ⇒ 旁路停在兜底表 ·
transport-failover 死 ⇒ 不再自动切备(**kill-switch 仍在**) ·
direct-egress-repair 死 ⇒ 直连出口不再自愈(2026-08-13 那个 bug 会回来) ·
rule-history 死 ⇒ 历史停止累计 · **server-bypass-refollow 死 ⇒ 服务器换了 IP
之后旁路再也不跟过去**(NAS 上那次静默断网一个月,见下文「服务器旁路重新跟随
DNS」)· **server-bypass-route-repair 死(仅 darwin)⇒ 休眠唤醒后被冲掉的
`/32` 旁路不再自愈,隧道成环**(2026-09-04 那次 13 分钟断网,见下文「休眠唤醒
后隧道成环」)· china-list-refresh 死 ⇒ 列表不再更新。
**后两个是 2026-09-04 加的,而这份枚举当时没跟上,直到 2026-09-13 的审计才补** ——
它们各自有一整节讲「没有它会断成什么样」,却恰恰从这份「少了谁会怎样」的清单里
缺席,是本文件反复出现的那个形状:**新写一节描述自己,顺手让旧的一处计数变成假的。**
这几件里没有一件值得用「一台受保护的机器断网」来换。**但代价写明了**:炸掉的
工人就此不再跑,那是一次**静默降级** —— 它打一行带栈的日志,并把名字记进
`panicked`。**但别把那份记账读成「少了哪个后台循环答得出来」**(这里此前就是
这么写的):`names()`/`panickedNames()` 至今**零生产调用方**,既不进 `bx status`
也不进控制 socket,今天唯一的办法仍然是翻日志。

**守卫判据取 AST 不取文本**(`TestRunLaunchesNoBareGoroutines`:`Run` 函数体
内真实的 `GoStmt`)—— 注释里、字符串里、别的函数里的 `go ` 都不算。变异实测:
塞回一个裸 `go mutEng.Run(ctx)` **能编译**(说明它是真实可能的改动)且守卫
转红。读不出 `func Run` 时它**响亮失败**而不是静默放行。


## Run() 拆相位:按判据切,不按行数切(2026-08-31)

`Run()` 764 → 734 行,但**行数是副产品,不是目标**。切的过程中确认了一件事:
**它的 700 多行不是等价的**。相位 3(fake-IP + DNS)里真正的判据(hosts 合并、
split 路由)**已经在独立函数里**,剩下的是纯粘合 —— 把粘合搬进另一个函数只是
把同样的耦合换个地方放(一个 3 进 6 出的函数不比原地代码更清晰)。

**判断「哪里还藏着判据」有个客观信号:读源码的守卫指向哪里。** 守卫是前人留下的
路标 —— 他们想断言某件事,而那件事长在接线里够不着,只好去比字符串。

已抽出两块:`buildSplitBrain`(「global 一个字节的 china 列表都不读」「CLI flag
压过 config.lists」,两条各自对应过真实事故)与 `buildSplitRoutes`(顺序即优先级)。
china 列表与它的两个路径**留在相位内** —— 此前是四个只在二十行内被用到的局部
变量,而组装根的每个局部变量都是一次「它后面还会被谁改」的阅读负担。


## Linux:Tailscale 的 WireGuard 底层 UDP 绕开劫持(2026-09-04,真机诊断)

**Mac ↔ 公司工作站 300ms 的真因**:工作站(bx global)上 tailscaled 发往对端**公网**
地址的 WireGuard UDP 落进 pref 200 → table 100 → 进 TUN → 经隧道从美国 VPS 出去,
对端看到的源地址对不上,直连永远建不起来,只能走 DERP。真机 `ip route get
192.0.2.185 mark 0x80000 ipproto udp` → `dev bx0 table 100`;同机 `ip route get
203.0.113.92` → `via 10.84.14.1 dev eno1`(server bypass)。**`tailscale netcheck` 的
`UDP: true` 是假安心**:它探的 STUN 就是自建 DERP,而那个 IP 恰好在 bypass 里 —— 于是
STUN 通、WG 不通,此前「公司封 UDP」那条记录就是这个机制造成的误判。macOS 上没有
这个问题(tailscaled 把 socket 绑在物理网卡)。bx 已照顾 Tailscale 三处(DERP 旁路、
100.64/10 → table 52、tailscale.com 不给 fake-IP),这是漏掉的第四处。

修法是一条规则:`ip rule add pref 90 fwmark 0x80000/0xff0000 ipproto udp table main`
(`platform_linux.go` 的 `optionalRouteUpSteps`;blockV6 时 `-6` 同款)。**只认 Tailscale
打的标 + 只认 UDP**:TCP 控制面/DERP 照旧经 bx。**它是可选步骤**:`ipproto` 选择器要
iproute2 ≥ 4.17,busybox 与老 NAS 没有,混进必装步骤会让一台本来能起的机器起不来
(netns 台子跑在 busybox 上,实测 `exit status 1` 后 `up()` 照常、还原干净 ——
**台子只能证明退路,证明不了规则真装上了**;后者用 alpine + 真 iproute2 验过 argv,
再由工作站真机验)。拆除对称 del;rehijack 的 `routeUp` 同样带上。

## 休眠唤醒后隧道成环:服务器旁路路由自愈(2026-09-04,真机诊断,修复真机未验)

**它看起来像「重启后 bx 断开」,其实不是重启**:电源日志 13:20 电量耗尽进入
Low Power Sleep 并休眠到磁盘,17:50:58 按电源键从休眠唤醒;开机时间是 9 月 1 日,
Guardian 进程从 9 月 3 日一直活着。唤醒 4 秒后 Core 日志开始刷
`singbox: open connection … using outbound/vless[reality-out]: EOF`,**1–2 毫秒**
一条 —— VPS 在 300 毫秒之外,真正的 EOF 不可能 2 毫秒回来,那是本机给的。
机制:en0 重新关联时 macOS 把挂在它网关上的路由冲掉了 —— **包括 bx 装的服务器
/32 旁路** —— 而 utun 上的两条 /1 劫持路由不挂在 en0,照旧活着。sing-box 到 VPS
的连接于是进了 TUN,被 bx 当成一条普通公网连接:隧道不健康 → kill-switch 拦下 →
EOF。**bx 的服务器防环靠的是内核路由,不是 socket mark**(子进程),那条路由丢了
就是成环,而成环是静默的、自锁的(隧道起不来是因为它自己的包被拦,而拦它是因为
隧道没起来)。同一个 Wi-Fi、generation 没变,路径恢复的边沿触发不响;
`direct_egress` 那条循环 17:51:22 修好了 scoped 默认路由(日志坐实),对 /32
一无所知;手动重连造出的候选传输走同一条坏路,20 秒也起不来;13 分钟后用户
down/up 才救回来。**bx 里没有一行处理睡眠/唤醒的代码**(grep 过)。

修法照抄 `direct_egress`,**去问内核**:`internal/supervisor/bypass_route_repair.go`
每 30 秒对每台服务器 `route -n get <ip>`(不带 `-ifscope`:问的是普通 socket 走
哪条路,子进程正是普通 socket),接口是我们自己的 TUN 就是成环,经控制面那把锁里
的 `controlServer.reassertRoutes`(Rehijack 的 apply)重新落实全部路由;有待确认的
改动时让路。判据三态(`decideServerBypassIntact`):**没有服务器可查 / 问不出来
都是「不知道」,不是「完好」**。循环体与 `direct_egress` 共用一份
(`watchKernelRoute`,措辞做成数据),退避、冷静期、change-only 日志一字不差。
只在 darwin 有探测原语(与 `direct_egress` 同一门槛);非 darwin 恒「问不出来」,
循环不动。**真机验收**:合盖休眠 ≥ 数分钟再唤醒(或 `sudo route delete
<服务器IP>` 模拟),看 `bx.log` 在 30 秒内出现 `server_bypass is broken` →
`server_bypass reinstalled the routes`,且 sing-box 的 EOF 刷屏停止、隧道回绿,
**不需要 down/up**。

## 服务器旁路重新跟随 DNS(2026-09-04,真机未验)

**起因是一次静默了一个月的断网**:VPS 2026-08-06 换 IP,NAS 上的 bx 重连 65638 次、
一直到 09-03 才被人发现。根因是「什么必须绕开隧道」**只在启动时算一次**
(`resolveServerBypass` 全仓唯一调用在 `run.go` 启动处):staticA 把服务器域名
钉在启动那一刻的 IP,旁路路由 pin 的也是它,子进程经系统 DNS(=bx)永远拿旧答案。

**修法不另造判定**:切换服务器那条路早就有「重读配置 → 防环解析 → 发布两半 →
变了就 rehijack」(`newBypassRefresher` + `handleSetServer`),
`internal/supervisor/bypass_refollow.go` 只是在**主传输连续不健康 ≥ 2 分钟**时
替用户按一次那个按钮(`controlServer.refollowServerBypass`),变了就 rehijack 再
`Reconnect` 让子进程重新解析;两次之间 ≥ 5 分钟(每次都问一轮 DNS,不限频就是
一个 DNS 探针)。**必须在 `cs.mu` 里、且有待确认的改动时让路**:刷新是替换语义,
与 `/v0/server` 交错会把还没落盘的新服务器从旁路里剔掉,而它的路由已经装上了
(`TestSetServerSerializesConcurrentBypassRefresh` 守着的那个洞)。`Reconnect` 在
锁外:它要等新隧道健康,最长一个 healthTimeout。**只对域名链接有意义**:IP 字面量
在 `resolveAll` 里短路,换 IP 只能改链接(这台 Mac 就是这种)。接线经
`controlServeOptions.OnControlReady` 回调交出 `controlHooks`(serve 的返回形状被
`requireControlSocket` 钉着),循环从 `run.go` 经 `workers.start` 起;
`TestRunWiresTheServerBypassRefollowLoop` 读源码钉住接线(serve 非 root 起不来,
netns 台子造不出「VPS 换 IP」)。同一天顺手做掉的两条:`bx explain` 对**具名规则
本次 0 次记录**明说「bx 没见过走这条规则的连接」(那次 Tailscale 误判就是被省略的
那一行害的;`Rule==""` 那一档仍然不渲染 0,nil 在那里是「没记名」);
`provision.atomicWrite` 写之前先 statfs 问放不放得下(QTS 的 `/` 是几百 MB
内存盘,默认 data_dir 解 29MB sing-box 必 ENOSPC),放不下或写出 ENOSPC 时错误里
直接点名 `data_dir`,探不出空间则放行(保险不是新前置)。
**真机验收**:换一次服务器 IP(或先改 DNS 记录),看 `bx-guard.err.log`/`bx.log`
在 2–7 分钟内出现 `server_bypass_refollow: the server's address changed` 且隧道自己回绿。


## 路由就绪位只有两处会被置真,而一次什么都没改的失败曾把它永久清掉(2026-09-17,真机诊断,修复真机未验)

`RuntimeState.RoutesInstalled` 的写点**全仓只有三处**:启动时 `Hijack` 成功置真、
`liveMutator.Rehijack` 的 apply 成功置真,以及那个 apply/undo 的置假。**没有任何
东西会根据观测把它设回来。** 而 apply 的第一句就是置假。

真机(项目所有者的 Mac)13:04:43 `server_bypass_refollow` 触发一次 Rehijack,
darwin 的 `RehijackRoutes` **第一句**探默认网关就失败(`解析默认路由失败: ""`)——
**一条路由都没碰过**,而就绪位已经被清成 false,并一直假到 Core 重启。三个消费方
各吃一次,全都表现为「机器明明好好的,而某件事就是做不成」:

- **路径恢复自己的 `verify`**(`run.go` 里那个闭包)读这一位 ⇒ 17:34 那次
  `underlay_changed` 连败 20 次 `verification_failed` 才放弃,**而机器全程受保护**
  (observed 五项全绿、10205 条连接经隧道)。2026-09-07 那次记作「为什么那 20 次
  verify 失败仍要看 Guardian 日志」的,大概率就是同一个机制。
- **Guardian 的 health 门**(`validateRuntimeState`)也读它 ⇒ `/v1/update` 轮询 20 秒
  超时,`bx update` 永久停在 `update_runtime_refresh_failed`、菜单只说
  「guardian operation failed」,**在 Core 重启之前升不了级**,而错误里一个字都
  没提到路由。
- `recoverySupersededByCore` 的五项里也有它。

**修法是 `ErrRehijackNoChange`**:三个平台把**前置检查**(探网关 / 判 RouterMode /
拿 wintun LUID)的失败包一层,apply 只在「真的可能动过路由」时才清就绪位,失败时
**恢复成进来时那个值、不是无条件置真** —— 后者会在它进来就是假的时候撒一次谎。
**漏包的后果是退回今天的行为(悲观、卡住),不是放宽**:一个平台忘了标记只少一次
自愈,绝不会让一次拆到一半的 rehijack 谎报完好。

**仍然存在的缺口,别读成已修**:一次**拆到一半**才失败的 rehijack 照样把就绪位永久
清成 false。那一次清它是对的(路由真的可能坏了),但**仍然没有任何东西会把它设回来**。
根治要让这一位变成一次观测而不是一份记账(darwin 上 `underlay.ValidateCapture` 就是
现成的判据,`RoutesInstalled` 也正是本文件点名过的那个「只置位不复查的 `atomic.Bool`」),
而那会朝放宽 fail-closed 的方向动,是一次产品决定,没做。

**守卫**:`TestLiveMutatorRehijackKeepsRoutesReadyWhenNothingWasTouched` 与它旁边那条
`TestLiveMutatorRehijackLeavesRoutesNotReadyAfterFailure` 是**一对** —— 少前者缺陷原样
回来,少后者「apply 干脆不碰这一位」也能全绿,而那是相反方向的、更贵的错。接线由
`TestEveryRehijackPreflightFailureIsTaggedAsNoChange` 钉住(读三个 `platform_*.go`,
判据是「前置区段里**每一个** return 都带标记」而不是「函数体里提到过一次」——
linux 与 windows 各有两处前置检查)。**那条守卫第一版是假绿的,而且是被我自己写的
注释骗的**:它在剥注释之前找锚点,而我刚加的解释性注释里恰好写着锚点本身
(`nc.routeDown()` / `addPlannedRoutes`),于是前置区段被截断在第一个 return 之前,
三个实现里两个漏检。**先剥注释再找锚点**这一步因此是判据的一部分,不是讲究;
它也是「断言被满足,但是因为别的理由」的第 N 次,只有变异实测抓得到。

