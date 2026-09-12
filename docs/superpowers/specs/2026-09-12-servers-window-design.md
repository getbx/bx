# Servers 窗口:从「一份清单」变成「这条隧道现在怎么样,以及我能换到哪儿」

**日期**:2026-09-12
**状态**:设计,待实施
**前身**:`docs/superpowers/specs/2026-08-09-multi-server-design.md`(多服务器清单的产品边界,本文不改动它定下的三条禁令)、`docs/superpowers/specs/2026-09-11-routing-rules-window-design.md`(同形状的上一次重做)

---

## 1. 起点

所有者原话:「servers 的页面也是,可以升级下」。**也是** —— 指的是 Routing Rules 那次的形状:
Guardian 已经在发的数据,窗口没在用。

这个形状在 Servers 页确实存在,而且更彻底:**`/v1/status` 每 2 秒就落进菜单进程**,带着当前
服务器的实时隧道延迟、传输类型、UDP 传输、UDP 档、隧道健康、Core 是否可达 —— 而窗口的管道
`show(rows:probe:)` / `refreshIfVisible(rows:probe:)` 一样都不带。窗口里唯一的延迟是点 Test
才有的**直连握手**,它测的是另一条路(不经隧道、走 Core 的直连拨号器)。

勘察当天实测:`/v1/status` 报隧道延迟 1051 毫秒,而窗口没有任何位置显示得出这个数。

但勘察还发现了比「没用上数据」更重的东西 —— **这个窗口在说三句假话**。它们不是这次升级
顺带修的,它们是这次升级的**主要理由**。

---

## 2. 今天是什么

420×300 不可缩放面板;一行一台服务器:`● 名字` + 一句灰色 detail + 一个 `Use` 按钮;下面
一条按钮带:`Test` / `Exit IP` / `New Server…` / `Add Server…`;再下面一行条件提示(出口 IP
结果)。detail 由 `ServersModel.serverDetail` 拼:host(与名字相同时省略,而这是常态)、
`UDP → host`(仅当 UDP 链接指向别处)、探测行、吞吐行。

判据全在 `ServersModel.swift`(纯函数,24 条 Swift 测试);`ServersWindow.swift` 与
`main.swift` 的接线是 AppKit,只由 Go 侧读源码的守卫盖着。

### 2.1 三句假话

**假话一:空列表是死路,而它给的出路不存在。**
`ServersWindow.render(rows:)` 在 `rows.isEmpty` 时摆一句 `No servers yet` 加一行提示
`bx setup --name <name> '<link>'`,然后 **`return`** —— 而按钮带是在那个 `return` 之后才画的。
于是零行时:没有 Add Server、没有 New Server、没有 Test、没有 Exit IP。

而 `bx setup` **没有 `--name` 这个 flag**(对着装好的二进制查过:只有 config/probe/force/
strict/udp)。urfave/cli 对未知 flag 直接报错,所以窗口给的唯一一条指令必定失败。同一句
死提示在 `internal/cli/servercmd.go:153` 还有一份。

**而这不是边角情况。** `bx setup` 从不写 `servers:` 清单(`setup.WriteConfig` 写的是
`server:` 或 `transports:`),`servers:` 的生产者只有 Guardian 的 add 与 deploy 的
`addDeployedServer`。也就是说**每一个正常装好 bx 的用户**打开这个窗口都看到「No servers
yet」—— 而 bx 此刻正跑着一台服务器。`bx server list` 这句话说对了:「配置里没有服务器清单
(**还是单服务器配置**)」,窗口没有这个区分。

**假话二:「没能测」被画成「这台服务器坏了」。**
`ServersModel` 里那段注释写着:「**只有「测过而且没通」才标** —— 没测过不标,「没能测」
(bx 没在跑)也不标:把那两种画成红的,等于把一台好服务器说成坏的。」

而线上格式**没有第三态**。Guardian 在 Core 不可达时发 `ProbeReport{Error: "没能测(bx 没在
跑?)"}`、链接解析不出主机时发 `ProbeReport{Error: "链接解析不出主机"}`,两者的 `Reachable`
都是零值 false;Swift 侧 `reachable` 缺席也读作 false。于是保护关着时点一下 Test,**每一行
都变红**,各配一句中文错误 —— 在一个对自己的用户可见字符串有 CJK 守卫的全英文菜单里。

那段注释承诺了一个线上表达不出来的区分。Go 侧 `TestProbeFailureIsNotReportedAsUnreachable`
的名字承诺了一个它自己的断言禁止的区分。

**假话三:`●` 跟着配置走,不跟着实际在跑的走。**
`serverEntries` 的 `Current` 完全来自 `config.current`。而热切换是**先写配置再切**(刻意的)。
于是切换失败时:配置已是 B,窗口给 B 加粗打点、断言你的流量从 B 出去,**而同一秒弹出的
对话框说隧道没切过去**。关掉对话框,只剩那个点。

Guardian 手里就有真相 —— `liveThroughput` 已经在问 Core 的 `/v0/status`,那里面有实际在跑
的服务器名。它只是没比对。

同一个根因还有第二个后果:Core 的 `PeakBPS` 来自一个进程级的速率计,峰值保留 30 分钟且
**不因热切换而重置**。于是切换 A→B 之后最长 30 分钟,B 那一行显示的是 A 的峰值,而且
`PeakAgeSeconds` 被强制成 0 ⇒ 不带年龄后缀 ⇒ 读起来像「刚在 B 上量到的」。调谐环还会把
这个数按 B 的名字持久化。

### 2.2 另外四条(中低)

- **切换的四种结局压成一句话。** `supervisor.SwitchServer` 区分:武装失败(什么都没变)、
  不健康已回滚(什么都没变)、不健康**且回滚失败**(你的隧道现在可能是断的)、**已生效但
  确认失败**(死手可能在超时后把它还原)。Guardian 记日志、应答一个常量 `servers_hot_switch_failed`;
  菜单对四种都打同一句「Saved X …, but the running tunnel did not switch. Turn bx off and on
  again to use it.」—— 对「已生效但确认失败」那种,这句话是**错的**;对「回滚失败」那种,
  它轻描淡写了一次断网。`bx server use` 在终端里说的是实话。
- **切换全程无反馈,最长 45 秒。** `probing` 会渲染成 `Testing…` 并禁用按钮,而 `switchInFlight`
  是 `main.swift` 的私有量、从不进窗口。确认对话框之后屏幕上什么都不发生,再点一次
  `Use` 撞上 `guard !switchInFlight` 直接返回 —— 连对话框都不弹。这正是 `shouldSuppressFetch`
  那次已经付过一次代价的「点了没反应」。
- **`port` 发了但没用。** `/v1/servers` 带 `Port`,Swift 的 `CodingKeys` 里根本没有它。
  同一台主机上两台不同端口的服务器渲染得一模一样。
- **`config_path` 解码了但从不读。** Rules 窗口显示它自己的配置路径,这个窗口不显示。

---

## 3. 这一版要答的问题

一个人打开 Servers 页,脑子里是两个问题之一:

1. **我现在这条隧道怎么样?** —— 今天这个窗口一个字都答不出来(它只有一个测另一条路的
   按钮)。而答案已经在菜单进程里躺着。
2. **不好的话我能换到哪儿?** —— 今天答得出「有哪几台」,答不出「它们分别是什么」。

**布局因此从「一份平列的清单」变成「当前那台给纵深,其余是候选」。** 这与 Rules 窗口
「有问题的排最前、健康的一个字不说」是同一条判断的另一面:**把版面给正在起作用的那一个。**

---

## 4. 布局

```
Servers                                            /etc/bx/config.yaml

┌─ Currently using ────────────────────────────────────────────────┐
│  ● vps-tokyo            203.0.113.20:443                          │
│    reality · 1051 ms · tunnel healthy                             │
│    UDP  hysteria2 → 203.0.113.20   (proxy)                        │
│    peak 6.4 MB/s (2 hours ago)                                    │
└───────────────────────────────────────────────────────────────────┘

Other servers
    vps-osaka            198.51.100.7:443        [ Use ]  [ ⋯ ]
    vps-sg               198.51.100.9:8443       [ Use ]  [ ⋯ ]
      not checked

  [ Test All ]  [ Exit IP ]        [ New Server… ]  [ Add Server… ]
  Exit IP: not checked
```

- **当前那台整块**:名字、`host:port`、传输类型、**实时隧道延迟**、隧道健康、UDP 传输与档位、
  带年龄的吞吐峰值。这些除了 host/port 全部来自 `/v1/status`,**零新增发布面**。
- **其余是候选**:名字、`host:port`、探测结果(点过 Test 才有)、`Use`、以及一个放三个动词的
  `⋯` 菜单。
- **配置路径摆在右上角**,与 Rules 窗口一致。
- **窗口可缩放**(`.resizable`)。当前那块会因为传输名字长而截断,而这个窗口不横向滚动 ——
  与 Traffic by App 那条「凡是会截断的格子必须同时给出看全的办法」同一条。

### 4.1 空列表不再是死路

零行时的文案由**服务器清单存不存在**决定,不由行数决定:

- 配置里根本没有 `servers:`(单服务器配置 / `transports:` 配置)⇒
  `This config has a single server, not a server list.` 外加一句说明:加第二台会把它转成清单。
- 有 `servers:` 但确实是空的 ⇒ `No servers yet.`

**两种情况下按钮带都照画。** 空列表恰恰是最需要 `Add Server…` 的时刻。那行
`bx setup --name …` 的死提示整个删掉 —— 窗口里有按钮,不该教用户去终端敲一条不存在的命令。

---

## 5. 判据住在哪里

**全部新判据进 `ServersModel.swift` 的纯函数**,窗口只摆放。这一条不是风格:`ServersWindow.swift`
与 `main.swift` 在本仓库一行 Swift 测试都盖不到,判据留在那里等于没有测试。

新增的纯函数(名字是契约,实施时不许改):

- `currentServerPanel(list:core:) -> CurrentServerPanel?` —— 把 `/v1/servers` 的那一行与
  `/v1/status` 的 `CoreRuntime` 合成当前那块。`core` 缺席或 `reachable == false` 时,
  凡是来自 Core 的字段一律**缺席而不是零值**(见 §5.1)。
- `otherServerRows(list:core:) -> [ServerRow]` —— 候选行。
- `probePresentation(_:) -> ProbePresentation` —— 探测三态(见 §6.1)。
- `switchOutcomeMessage(_:) -> String` —— 四种结局四句话(见 §6.2)。
- `serverListEmptyReason(list:) -> String?` —— §4.1 的两种措辞。

### 5.1 三态贯穿始终

这个窗口此后**不许**出现「拿不到 ⇒ 用零值/好消息」。三处:

| 拿不到什么 | 今天 | 此后 |
|---|---|---|
| Core 不可达时的延迟/传输/健康 | 窗口没这些字段 | 当前那块显示 `Core not answering — nothing below was measured`,下面那几行**不画**,不画成 0 ms |
| 探测没能做成 | 画红 = 服务器坏了 | 第三态,灰色 `not measured`,附原因 |
| 实际在跑的是哪一台问不出来 | 默认信配置 | `●` 不加粗、另说一句「could not confirm which server is running」 |

判据复用既有的 `answeringCore()`(`MenuRows.swift`),**不许写第二份** —— 规则窗口刚因为
「有一份判据没被用上」出过同一个 bug。

---

## 6. 线上契约的改动

三处,每一处都是**因为窗口表达不了一个已经存在的区分**才改,不是为了多发数据。

### 6.1 探测三态

`ProbeReport` 加一个显式的「测了没有」维度。**不许**用 `Error != ""` 反推 —— 那是把两个
独立的问题(测没测成 / 测出来通不通)压在一个字段上,正是今天的 bug。

字段**刻意不带 `omitempty`**:键缺席读作「这一版 Guardian 没说」,与 `Status.Capabilities`
同一条纪律。旧 Guardian ⇒ 键缺席 ⇒ 菜单按「没说」渲染,不按「测过」。

Guardian 侧那两处 `ProbeReport{Error: …}` 改成显式的「没测成」;**错误文案改英文** ——
它会原样出现在全英文菜单里,而 CJK 守卫扫不到从服务端来的字符串(这正是规则窗口刚踩过的
那一条,修法是客户端按机器可读的原因码映射英文,服务端的中文只进日志)。

### 6.2 切换结局

应答带一个**机器可读的结局码**,四种各一个,取代今天那个常量 `servers_hot_switch_failed`。
菜单按码给四句话,其中两句必须说清楚要害:

- 已生效但确认失败 ⇒ 它**切过去了**,死手可能在超时后还原,现在就去让配置落定。
- 回滚失败 ⇒ 你的隧道现在可能是断的,给出逃生命令。

**原始错误串不外传**,只发码 —— 与 `/v1/rules` 的 409 同一条门规。完整原因进 Guardian 日志。

### 6.3 实际在跑的是哪一台

`/v1/servers` 的应答加一个「Core 报的当前服务器名」。Guardian 已经在 `liveThroughput` 里问
Core 拿到它,只是没发。**它与配置里的 `current` 并列发布,绝不合并** —— 两者不同正是最有
价值的诊断信号(与 `desired`/`observed`/`divergence` 并列同一条纪律)。

问不出来(Core 不可达)时**缺席**,不是空串。

顺带修 §2.1 假话三的第二半:吞吐峰值在切换之后不许挂到新服务器名下。判据是「这个峰值是
在哪一台上量的」,而不是「配置现在指着谁」。

---

## 7. 三个动词

底层原语**全都已经在了**,缺的是中间那道门:

| 动词 | 底层 | 今天的缺口 |
|---|---|---|
| 删除 | `setup.RemoveServer` | 只接到 `bx server rm`;`/v1/servers` 的 action 只认 `""`/`add`/`probe` |
| 换链接 | `setup.ReplaceServerLink` | 实施时新加(见 §7.2):`UpsertServer` / `AddServer` 都会挪动 current。**`UpsertServer` 至今仍是零生产调用方** |
| 挂 UDP 链接 | `setup.AddServer(path, name, link, udp)` | Guardian 已经收 `udp`,而 Swift 客户端从不发,Add 表单只有一个框 |

### 7.1 删除要确认,而且没有 Undo

**这是与 Rules 窗口刻意不同的一处,理由必须写下来。** 规则窗口删除不弹确认、只留 Undo,
理由是「为十一条冗余点十一次确认是在惩罚正确的行为」。这里两条都不成立:

1. **菜单在构造上做不到 Undo。** 链接是凭据,`TestServerListNeverShipsTheLinkItself` 钉住它
   永不离开 Guardian。菜单手里从来没有那条链接,所以删掉之后它**无法**把服务器加回去。
   一个撤不回的 Undo 比没有 Undo 更糟。
2. **服务器很少,删除很罕见。** 不存在「要点十一次」那种惩罚。

所以:删除弹确认,文案说明这会丢掉那条链接、而 bx 手里没有副本。

**删除当前那台一律拒绝**,并说明先切走再删。

### 7.2 换链接

就地更新同名那一台。用途是凭据轮换与 VPS 换 IP —— 今天这件事只能手改
`/etc/bx/config.yaml` 或 `bx setup --force`。

> **本节原文写的是「用 `UpsertServer`」,实施时没有照做,而那个偏离是对的**(Task 4,
> 已 review 通过)。这份文档是下一个人照着做的依据,所以把真实的选择记在这里:
>
> - `setup.UpsertServer` **会把 current 设成被改的那一台**(`TestUpsertStillSwitchesBecauseThatIsItsJob`
>   钉着这个行为:它服务的是 `bx setup`「用这一台」)。于是换一条**没在用**那台的链接
>   会顺手把出口换过去,而界面只说了「已替换」—— 正是本设计要守的那条「只有用户可以切」。
>   变异实测:接成 `UpsertServer`,`current` 当场从 tokyo 变成 osaka。
> - `setup.AddServer` 只差半步,而那半步同样会挪出口:**current 空着时它会填上**。
>   一份没有 `current:` 的清单**照样在跑**(`config.resolveServers` 回落 `servers[0]`),
>   而手改出来的配置正是这个样子 —— 恰好就是本节的受众。
> - 实际走的是**新加的** `setup.ReplaceServerLink`:名字必须已在清单里(不存在就报错,
>   绝不顺手加一台),链接就地换,**任何情况下都不动 current**。
>
> **`UpsertServer` 因此仍然是零生产调用方** —— 那一行「待接线的缺口」不再成立,
> 别再照着它去接。

**换的是当前那台时**,配置改了而跑着的隧道还连着旧地址:如实说「已写入,重连后生效」,
并给「现在就重连」,**但绝不替他重连** —— 与规则热生效那条收尾同一条。

### 7.3 挂 UDP 链接

Add 表单加第二个可选框。今天从菜单加一台 reality+hysteria2 的 VPS 会**静默丢掉 QUIC 那半**,
而 `bx server install` 默认就给两条链接。

---

## 8. 不做的事

前四条是所有者已经定死的边界(`2026-08-09-multi-server-design.md`),本文一条都不碰:

- **不做自动容灾。只有用户能切。** 换服务器 = 换出口 IP 与所在国家,自动切会让用户在毫不
  知情的情况下换了身份。
- **不做延迟排序、不自动选最快、不做地区分组、不做订阅/机场格式导入。**
- **不做后台定时探测。** 探测走在隧道外面,会让网络上看得见这台机器联系过那几个地址。
  探测**只在用户点的时候发**,且**串行** —— 同时向几台发握手会留下一个很整齐的模式,
  而这几台恰好是同一个人的资产。
- **不做每台服务器独立的 `rules`/`dns`/`udp.mode`。** 清单只管「走哪条隧道」。

本次另外不做:

- **出口 IP 检查不搬进 Guardian。** `/v1/update-check` 是本地 socket 上唯一一个能让 root
  守护进程发起出网请求的端点,有守卫钉住这个「唯一」。检查留在菜单进程。
- **不显示、不编辑链接原文。** 见 §7.1。
- **不加每台服务器的失败计数与历史。** 今天不存在这个数据(计数是按规则与全局的),
  造它要新开一整套记账。
- **不加「上次使用/上次切换」时间戳。** 同上,今天不存在。
- **不动 Deploy 表单。** 它自己的缺口未勘察。

---

## 9. 守卫

- **纯模型**:每个新纯函数各自的 Swift 测试。三态那三条必须**分别**喂「没测过 / 测了不通 /
  没测成」三种输入 —— 只喂前两种正是今天那条测试假绿的原因。
- **接线**:Go 侧读源码的守卫钉住「`/v1/status` 的 CoreRuntime 真的传进了窗口」与「空列表
  时按钮带仍然被摆进视图树」。后者的判据打在**视图树**上,不是「文件里出现过 Add Server」。
- **线上三处**:各一条 Go 测试;探测那个新字段的「缺席 ≠ 测过」由一条**不带该字段的**
  应答固定住。
- **不许出现第二份判据**:一条守卫禁止窗口自己判 `reachable`,必须走 `answeringCore()`。
- **每条守卫都要变异验证**,并记录哪一条咬中。凡变异「全绿」先查落没落上 —— 而
  `git diff --stat` 在「变异恰好把代码改回 HEAD 的样子」时会给假阴性,用对副本的 `diff -q`。

---

## 10. 真机验收

这个窗口**整套从未有人在屏幕前点过**,包括搬进来的那两个按钮、Add Server 的三种结局、
以及 commit-confirmed 的切换路径(要真切一次、会改出口 IP)。本次之后要看:

1. 当前那块的实时延迟是否真的每 2 秒跟着 `/v1/status` 动。
2. 保护关着时点 `Test All`:**每一行应显示灰色的 not measured 而不是红色**,且文案是英文。
3. 真切一次服务器:切换过程中有可见反馈,四种结局的文案对得上实际发生的事。
4. 删除一台非当前服务器(确认框要说清链接会丢);删除当前那台应被拒绝。
5. 空配置(单服务器配置)下打开窗口:文案说的是「这是单服务器配置」而不是「没有服务器」,
   且四个按钮都在。
6. 加一台带 UDP 链接的服务器,`bx status` 应显示 `UDP→hysteria2@…`。
