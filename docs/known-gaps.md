# 已知没修的问题与待定的决定

**这份清单管「待修 / 待决定」,`docs/acceptance-pending.md` 管「待验证」,两者分开。**
每一条都指回判据所在的那份 CLAUDE.md(那里有来龙去脉);这里只回答三件事:它是什么、
今天还成不成立、要谁来动。

**维护纪律**(与根目录 CLAUDE.md 对陈旧陈述的纪律同一条):修完就回来**删掉**这一条,不是划掉;
拿不准某条还成不成立时**先去代码里核**再动手。「核过」一列写的是最后一次去代码里确认的日期 ——
一份说谎的缺口清单比没有清单更糟(2026-08-24 那份「四分之三是假的」就是先例)。

起草于 2026-09-23,从 8 份 CLAUDE.md 与记忆里收拢;起草当天就核出一条半已经不成立
(「进度浮层走不到 Quit」已修,只剩恢复浮层那一半)。

## A. 待修:是 bug,修法大致清楚

| # | 问题 | 核过 | 判据在哪 | 估计 |
|---|---|---|---|---|
| A11 | **关掉保护期间建立的连接,重新 `bx up` 之后仍从物理网卡直出**(2026-09-25 真机抓包:`bx down` 那 14 秒里 Chrome 开的一条 TCP 连接(`192.168.50.15:49528 → 203.0.113.11:443`),**36 分钟后、中间又经过一次完整的 Guardian 切换,仍在 en0 上以真实 IP 收发**)。macOS 已连接的 socket 不会因为路由表变了而改走 TUN;所有 VPN 都有同一个性质,但 bx 的承诺是「不泄漏」,而用户从菜单开关一次保护就会碰上。**看得见了(2026-09-25)**:Core 每 15 秒读一次内核 socket 表(`appattr.StrayConnections`:本地地址在物理网卡、远端公网、没绑网卡、不是 bx 自己),有这种连接时 `bx status` 出一条 error 级告警 `connections_bypassing_bx`,点名应用、叫用户重开它们;菜单同时出一行红的 `Outside bx`(图标裂开,经 Guardian 的 `CoreRuntime.bypassing_apps`,2026-09-26)。**仍没做的是主动断掉它们**(macOS 没有按 socket reset 的现成原语;要不要为此动 pf 待定)。**假阳性(2026-09-28,真机):** ssh 跳板连的是 bx 自己的 VPS,服务器旁路 /32 把它送去 en0 是设计,却被点名「Outside bx: ssh — quit and reopen」,而重开也改不了它走哪儿。判据现在排除 bx 自己绕开的网段(`RuntimeState.ServerBypass` + 配置 `bypass:`,`appattr.StrayConnections` 的 `routedAround`),修复真机未验(`docs/acceptance-pending.md` A12)。**两段式(同日,真机未验)**:每次 `bx up` 之后菜单裂开几分钟点名 Chrome/Mail/WeChat,而那些连接几分钟内自己就没了 —— 「退出重开」对它们是白要求,天天裂图标会把它训练成墙纸。现按 Core 看见它多久分组(`supervisor.strayTracker`,门槛 `strayStubbornAfter` 5 分钟):未满的只报数(`connections_settling_outside_bx`,warn,菜单灰字「Settling」、不裂图标),满了的才点名「退出重开」(原 `connections_bypassing_bx`,error)。**这不改变泄漏本身**;让那五分钟消失只有 pf 回 RST 那条路 —— **同日已做**(`internal/pfreset`,一次性、含 UDP,判据在 `internal/supervisor/CLAUDE.md`),**真机未验**(`docs/acceptance-pending.md` A13);验过之前这一条不删。 | 2026-09-25 | `docs/acceptance-pending.md` B9 | 待定 |
| A12 | **菜单一天只查一次更新,发布落在两次检查之间就一整天看不见**(2026-09-28 真机:菜单 10:00 启动、10:05 更新完查过一次,v0.4.15 于 19:00 发布,菜单上没有「Update bx…」;`/v1/update-check` 明明答 `available:true`)。检查只在启动、每 24 小时、一次更新完成后三处。修法候选:打开菜单时若上次检查早于 1 小时就再查一次(仍是被动、每次开菜单最多一次)。当天的处置是 `launchctl kickstart -k gui/$UID/com.getbx.bx.menu` 只重启菜单。 | 2026-09-28 | `apps/macos/BxMenu/Sources/BxMenu/main.swift` `refreshUpdateCheck` 三个调用点 | 待修 |
| A13 | **升级之后菜单没有自己换成新版**(A13 那条修复真机验了一次,没生效:菜单 PID 69447 于 10:00 启动,10:05 一次完整升级把 Bx.app 换成 v0.4.13 之后它仍活到 19:15,盘上 `release.json` 与它启动时记的 `v0.4.14-pf` 明明不同)。没查原因;候选:`XPC_SERVICE_NAME` 判 launchd 托管那一支在 install.sh 手动 bootstrap 的进程上不成立、或「没人在用」那几个条件之一一直为真。要查就在下次升级后看 `menu.err.log` 与 PID。 | 2026-09-28 | `menuShouldRelaunchForNewBundle`、`TestMacMenuRelaunchesItselfWhenTheBundleIsReplaced` | 待查 |

## B. 要你拍板:产品或安全上的取舍

| # | 决定 | 为什么现在是这样 | 判据在哪 |
|---|---|---|---|
| B1 | **Developer ID 有了(2026-09-28),菜单授权面那半还没接** | 账号与签名公证已落地(根目录 CLAUDE.md 首装面),Gatekeeper 那半解了。剩下的是菜单免密开关的授权面:有了 Developer ID 就能用 SMJobBless 把权利绑到 Bx.app,收掉「**同一用户下任何进程都能静默开关 bx**」这条已接受的安全后果(今天靠日志记 uid 缓解)。 | 控制面那段 |

## C. 等数据:判据写好了,门槛要真机跑一段才敢定

| # | 事 | 要攒的数据 |
|---|---|---|
| C1 | **死规则的门槛 14 天 / 20,000 次**够不够、有没有假阳性 | `bx status --json \| jq '.rule_history'` 跨重启是否持续累计 |
| C2 | **「一直直连失败的域名」要不要提醒**(explain 第四件) | 同上,再加 `failure_kinds`:全是 `dns_nxdomain` 不该开口,全是 `unreachable` 该说的是直连出口坏了 |
| C3 | **让 china 列表也对 UDP 生效**(搁置的选项 B) | UDP 反事实计数;第一批约 150 条样本里 `udp_would_flip_*` 为 0 |
| C4 | **Core 起不来的宽限余量 3 秒**够不够 | 日志 `guardian_core_start_failure_record_absent` 的 `waited=` |

## D. 知道、接受、暂不动

- **调谐环不再加执行权**(原 B4,2026-09-23 所有者定):`stop_core` 最常见的触发是用户自己在跑的
  `sudo bx run` 调试进程,重启卡住的 Core 与自动解所有权锁存都靠近「双 Core」这个最坏结局;没有
  一次真实事故需要它们。**等真出现「Core 活着但卡死、没人管」的事故再议。**
- **Linux 不改成走 Guardian**(原 B5,同日定):Guardian 的主要价值是给菜单栏 App 供状态与开关,
  而 linux 上没有菜单;换架构要动 NAS、路由器这些无人值守设备的启动方式。linux 真正缺的那一样
  (Core 起不来时说不出原因)已作为 A9 单独修掉(2026-09-23)。

- **Guardian 自己的日志没有轮转**:实测约 26.6 KB/天(约 10 MB/年),要修得在 daemon 启动路径上做 fd 手术,且无法不升级真机就验证。
- **后台工人炸了只能翻日志**:`panickedNames()` 零生产调用方,不进 `bx status`。
- **规则窗口把「配置解析失败」说成「这一版没检查」**:它绝不宣称健康,要紧的那一半对。
- **Guardian 声明了 `status_watch` 却不带 `status_generation` 时,菜单会按轮询节拍反复进出 watch**:那说明某一端在能力声明上撒谎,不是菜单的 bug。
- **规则窗口开着时每次刷新都让 Guardian 重建一张约 12k 条的 `DomainSet`**:没量过。
- **规则体检的第 4 类「缺失规则」**没做。

## E. 没有机器,长期挂着

- **Windows**:托盘 App 与 Inno 安装包的 GUI 没有真机验过;server 写主机名时断线重连要重解析,
  而 WFP 只放行 `bx.exe`、子进程 app-id 不同,它 off-TUN 的 :53 可能被封(理论上被哨兵 DNS +
  `staticA` 兜住,没实测)。见根目录 CLAUDE.md「Windows」。
- **非 darwin 的关闭路径没排查过**(不变量 5:拆除永不拒绝,今天由 `internal/cli` 的 darwin 强制入口测试保住)。

## F. 结构性的,等基线干净再做

- **菜单状态机搬出 `main.swift`**(3,675 行,`resolve()` 嵌在函数体里、编不进测试)。搬进一个
  可测的 `MenuState.swift` 能让一批读源码的守卫变成行为测试,也是「真机未验」最集中的地方。
  **等 `acceptance-pending.md` 的 A 组验完、菜单处在已知良好的状态,再单独开分支做。**
- `internal/cli/cli.go`(5,688 行)同理,优先级低于菜单。
