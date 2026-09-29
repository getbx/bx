# 菜单栏 App 可读性一轮:文案层 + 两处可见性(design)

**状态:所有者 2026-09-28 过了(两处修订见表:IP 保留、顽固残留降为 warn),动 Swift 与 Go 中。** 所有者 2026-09-28 原话:「虽然产品要求是
简洁,但可读性,用户感受并不好」,并同意先做文案层 + 菜单告警与规则入口两处可见性,
顺序主菜单 → Servers → Rules → Checks/Logs → Traffic → Deploy;布局层另议。

## 诊断(七个窗口离屏快照 + 三张真机截图)

四种东西漏到了用户面前,每页都在重复:

1. **内部词汇当用户文案**:Core、Guardian、`state=unknown managed=false`、日志路径当标题、
   `sudo bx up; bx logs` 当修复建议、部署页印一行 `bx server deploy root@…`。
2. **该按的地方看起来像不能按**:Rules 的 Show/Hide 是灰字;Servers 的 `⋯` 里藏着换链接与
   删除;菜单第二行的 `✕` 分不清「关掉提示」还是「出错了」;加规则要右键,页面上看不出来。
3. **说坏话的口气,哪怕没坏**:Servers 开头「Core not answering — the live readings below
   are missing」;Checks「bx is not running, so this round could not look」;菜单上 error 级
   的 `Outside bx` 是灰字。
4. **密度与留白**:Rules 580×320 一屏塞 Presets + 等宽域名 + 每行 Remove;Traffic 六列缩写;
   脚注一整句;各页没有标题区。第 4 条是布局层,本轮不动。

## 写法原则(本轮所有改动共用)

- **用户看到的每个名词都是他能指着屏幕认出的东西**:bx、保护、隧道、服务器、规则、这台 Mac。
  Core / Guardian 一律不出现;要区分时说「bx 的后台服务」。
- **失败句 = 发生了什么 + 现在能做什么**,不解释机制、不道歉、不用「could not look」这类
  拟人。判据是「读完知道下一步按哪儿」。
- **健康状态不说话或只说一句正面的话**;告警按严重度分色,error 红、warn 橙、info 灰。
- 命令行只在两种地方出现:用户明确选了「在终端里跑」;或 GUI 已经没有出路时作为最后一行,
  且用 `elevate.Prefix`。
- 中文译文补齐服务端发来的 detail/hint(产地改在 Go 侧,`bx doctor` 与菜单同一份)。
- 不动的:窗口尺寸、分组结构、能力门、任何判据;所有者定死的边界(不排序、不自动容灾、
  不进菜单的模式选择)。

## 改稿表

### 主菜单

| 位置 | 现在 | 改成 | 备注 |
|---|---|---|---|
| 开关下第二行(单服务器配置) | `203.0.113.92 · 555 ms` | **不动**(所有者:IP 有用) | 有名字的照旧 `vps-eu · 555 ms` |
| 顽固残留 | 灰字 `Outside bx: Google Chrome — quit and reopen ✕`,error 级,图标裂开 | 橙字 `Google Chrome still has an older connection outside bx — quit and reopen it to move it in` + 右侧 `Dismiss`;**降为 warn,图标不裂;`bx status` 同步降级,不再拉成 Needs Attention** | 所有者 2026-09-28:「红字有点吓人?这是需要被用户处理的么?」—— 不需要:风险只有那一条老连接、只对它本来就在连的站点;pf 重置已把活跃的重置掉,剩下的是空闲的。红字与裂图标留给真正要人动手的事 |
| 退场中(warn) | 灰字 `Settling: 3 connections from before protection was on — they move into bx as apps reconnect` | 灰字 `3 older connections are still finishing outside bx` | 一句话说清;「reconnect 后自动进」放进 hover 提示 |
| Via 行(展开时) | `Via  reality  UDP→hysteria2` | `Tunnel  reality · UDP via hysteria2` | 「Via」不是用户词 |
| Latency 不健康 | `Latency  Tunnel unhealthy ✗` | `Tunnel  not responding ✗` | |
| 未观测 | `Not checked` | `Couldn't check` | 「没查」与「查不了」在这里都是后者 |
| Recovery 行 | `Waiting for Guardian` | `Waiting for bx to start` | 全部 Guardian 字样同此 |
| 转换通知 | `bx: tunnel is down` / `Protection is on but the tunnel is unavailable. bx is blocking traffic so nothing leaks (kill-switch).` | `bx: tunnel is down` / `Protection stays on: bx is blocking traffic so nothing leaks while it reconnects.` | 去掉括号术语 |
| 更新弹窗 | `Update couldn't be completed: during the switch the new version could not bring up the tunnel. That points at your server or the path to it, not the update itself. Previous version restored — try updating again later.` | `The update was rolled back: the new version could not reach your server. Your previous version is running. Try again later.` | 三句压一句 |

### Servers

| 位置 | 现在 | 改成 |
|---|---|---|
| 顶部提示(Core 不应答) | `Core not answering — the live readings below are missing.` | `bx is off right now, so live readings are unavailable.` |
| 当前那台的副标题 | `Carrying your traffic now (confirmed by bx)` / `Not confirmed as carrying your traffic right now` | `Your traffic goes through this server` / `bx couldn't confirm this is the server in use` |
| 按钮 | `Test All` / `Exit IP` / `Set Up a New VPS…` / `Add Existing Server…` | `Test Latency` / `Check Exit IP` / `Set Up a New Server…` / `Add a Server…` |
| 出口行 | `Exit IP: 203.0.113.92` | `Exit IP 203.0.113.92 — matches vps-eu` 只在测过后出现;未测时不画 |
| `⋯` | 藏着 Replace Link… / Remove… | 保留 `⋯`,但当前那台行尾直接画 `Replace Link…`;非当前行尾画 `Use` 与 `Remove…`(本轮唯一的 Servers 可见性改动) |
| 测量失败短语 | `could not measure (is bx running?)` | `couldn't measure — bx is off` |
| 切换确认正文 | `Your public IP changes immediately. Sites you are signed in to may ask you to verify again, and downloads in flight will break.` | 保留(这句写得对) |
| 删除拒绝 | `That is the server your traffic uses right now. Switch to another one first, then remove this one.` | 保留 |
| 单服务器提示 | `This config has a single server, not a server list. Adding a second one turns it into a list you can switch between.` | `You have one server. Add another to switch between them.` |

### Routing Rules

| 位置 | 现在 | 改成 |
|---|---|---|
| 标题句 | `Your own rules (6) — global mode: these are the only domains that go direct` | 两行:`Your rules (6)` / 小字 `Everything goes through the tunnel except these.`(split 时 `These are added on top of the built-in China list.`) |
| 预设行 | `China CDN  Chinese apps, video and shopping load from nearby servers  1/11  Show` | `China CDN  Chinese apps, video and shopping load from nearby servers` + 右侧真按钮 `Show 11 ▾`;部分勾选时副标题后加 `(1 of 11 on)` |
| 加规则入口 | 只有右键 | 「Your rules」组下方常驻一行:输入框占位 `Add a domain, e.g. *.example.com` + `Direct` / `Tunnel` 两个按钮(复用 `Add Rule…` 的校验与风险门) |
| 体检缺席句 | `A rule with nothing written under it here has not been checked: this version of bx did not check these rules for problems.` | `Rules haven't been checked: this version of bx doesn't check them.`(Core 不应答那半:`Rules haven't been checked: bx is off.`) |
| 500 错误 | `bx answered with an error (HTTP 500, code=…). Use Show Details for the reason.` | `bx couldn't do that. Show Details has the reason.` |
| 风险门正文 | 五句 | `Anyone can create a subdomain there, and a direct rule covers every subdomain — a stranger's page could send your real IP outside the tunnel. Only add this if you control that host.` |

### Troubleshoot → Checks / Logs

产地改在 Go(`internal/doctor`、`internal/platformcheck`、`internal/guardian/doctor.go`),
`bx doctor` 文本与 `--json` 的 `detail`/`hint` 一起变;golden 随之更新(名字与顺序不变)。

| check | 现在 | 改成 |
|---|---|---|
| `Guardian DNS` | 标题 `Guardian DNS`,detail `state=unknown managed=false`,hint `sudo bx up; bx logs` | 标题 `System DNS`,detail `bx isn't managing this Mac's DNS`,hint `Turn protection on` |
| `Tunnel` | `bx is not running, so this round could not look` | `Not checked — bx is off` |
| `Traffic failing rules` | `1 rule(s) failing in bulk — *.qq.com: 1289 of 1291 failed (100%)` | `*.qq.com: 1289 of 1291 connections failed` + hint `Open Routing Rules and remove it` |
| `Config readable` | detail 是路径 | `Settings file found`(路径进 hover) |
| `Status socket` | | `bx running` |
| `Service installed/enabled/active` | | `Background service installed` / `… set to start at login` / `… running` |
| `Guardian installed` | | `Background service installed` |
| `Tunnel claims` | | `Other VPNs` |
| `Direct egress` | | `Direct route` |
| `UDP policy` / `UDP transport` / `UDP traffic` | | `UDP handling` / `UDP tunnel` / `UDP traffic` |
| `Desired off` | | `Protection turned off on purpose` |
| `Permission fallback` | | `Checked without admin rights` |
| `pf_reset_residue` | `bx's pf reset anchor still holds rules …` | `Leftover firewall rules from a previous run` + hint `Turn protection off and on` |
| Logs 页标题 | `Core · /var/log/bx.log` / `Guardian · /var/log/bx-guard.err.log` | `bx` / `Background service`,路径进 hover;`Not available: could not read this log` → `Couldn't read this log (needs admin rights)` |
| 合计句 | `1 failed · 1 warning · 1 not checked` | 保留 |

### Traffic by App

| 位置 | 现在 | 改成 |
|---|---|---|
| 列名 | `Conns  Up/s  Down/s  Up  Down  Rule` | `Connections  Upload/s  Download/s  Uploaded  Downloaded  Rule` |
| 目的地副标题 | `rr1---sn-4g5e6nsz.googlevideo.com +4` | 折到注册域 `googlevideo.com +4`(hover 全名) |
| 脚注 | `Byte counts are approximate: ports get reused, and an app listed in two sections may show all its bytes on one side.` | `Byte counts are approximate.` |
| 空态 | `Not collecting app traffic right now.` | `bx is off, so nothing is being counted.` |
| 陈旧 | `Not updating — this is the last report bx could read. Protection may be off.` | `Not updating — last report shown. bx may be off.` |
| 右键提示 | `Right-click an app to always send one of its destinations direct or through the tunnel.` | `Right-click an app to add a rule for one of its destinations.` |

### Set Up a New Server

| 位置 | 现在 | 改成 |
|---|---|---|
| 占位 | `Server address — 1.2.3.4 or an ssh_config alias` | `Server address (IP or hostname)` |
| 命令预览行 | `bx server deploy root@<server address>` 常驻 | 收进 `Show the command` 折叠;默认只显示 `bx will connect over SSH as root and install its server.` |
| 按钮 | `Open in Terminal` | `Install over SSH in Terminal` |
| 免责句 | `Your current exit does not change. bx never sees your SSH password.` | 保留 |

## 不做、以及为什么

- 布局(窗口尺寸、Rules 的密度、每页标题区):所有者要先看观感再决定。
- 菜单常驻「N unreachable」之类的指示器:所有者否掉过(会变墙纸)。
- 自动重连、自动选服务器:边界不动。
- Checks 的 check 名与顺序:`--json` 契约,只改 detail/hint 与显示标题。

## 守卫

- 每条新句子进 `Localization_zhHans.swift`(既有 `TestMenuEveryLocalizedStringHasAChineseTranslation`)。
- Go 侧 detail/hint 改动由 `TestJudgeGolden` 逼着逐条确认。
- 新加的 Rules 输入框走既有 `Add Rule…` 路径(同一个风险门,`TestMacMenuRulesWindow…` 系列)。
- 快照重出一套,人眼看一遍(所有者要的正是观感)。
- 文案里不许出现 `Core` / `Guardian` / `state=` / 日志路径:一条读 `Localization_zhHans.swift`
  key 表的守卫,白名单只留「Show Details」里的原始错误。
