# 待人工验收清单

**这份清单是给项目所有者的,不是给 agent 的。** 上面每一条都**只有人在机器前才能做**
—— 要么要在屏幕上点一下,要么要制造一次真实故障。agent 能从日志和 `bx status` 判的
那些已经判完了(见各节的「真机已验」),剩下的就是这些。

**它刻意不放进 CLAUDE.md**:验收步骤是操作手册,会随每次验收增删,而 CLAUDE.md 是
每次会话都加载的那一份。**但每一节的「真机未验」标签仍留在 CLAUDE.md** —— 那是待办
状态,不是历史;这里只是把「怎么验」搬出来。

**验完一条就回来划掉,并把结论写进 CLAUDE.md 对应那一节**(「真机已验(日期)」+
看到了什么)。**一份说自己还没验而其实验过了的清单,与一份说已经验过而其实没验的,
都会让下一个人不再相信它。**

预期文案逐条从 `apps/macos/BxMenu/Sources/BxMenu/` 的源码里核对过(2026-09-13),
**看到的与这里写的不一样就是发现了一个 bug,不是清单写错了** —— 但请先核一遍源码再下结论。

---

## A. 现在就能做 —— 打开菜单点一圈(约 15 分钟)

这一批在 2026-09-13 的升级里**第一次装上机器**,此前全部只有单元测试与读源码的守卫。

### A1. Servers 窗口:三句假话是否真的修好了

菜单 → `Servers…`

- [ ] **空列表不再是死路**。你这台是 `bx setup` 写的单服务器配置,应看到
      `This config has a single server, not a server list. Adding a second one turns it
      into a list you can switch between.` —— **不是** `No servers yet`。
      **四个按钮(Test All / Exit IP / Set Up a New VPS… / Add Existing Server…)都该在**,
      此前它们画在 `rows.isEmpty` 的 return 之后,整个不出现。
- [ ] **`⋯` 菜单里有 Remove… 与 Replace Link…**(需要 `servers_edit` 能力,
      升级后已声明)。**删当前那台应被拒绝**(置灰或 409)。
- [ ] **实时延迟每 2 秒跟着 `/v1/status` 动**(不是打开窗口那一刻的快照冻住)。
- [ ] 点 `Test All`:保护开着应给出实测延迟;**保护关掉再点,每行应是灰色英文
      `not measured`,不是红色** —— 红只从**实测失败**来,「没能测」不该被画成
      「这台服务器坏了」。
- [ ] **单服务器配置下顶上有「Currently using」那一块**(2026-09-23 补上,此前整块缺席):
      标题是 `Your server` 加主机:端口,保护开着时点是**实心**的并带实时延迟,**没有 `⋯`**
      (这种配置没有清单条目可改)。关掉保护再看:点变空心,并说 Core 没在答话。
      离屏快照 `servers-single` 是它应有的样子。**需要先升级 Guardian**(新字段
      `current_server_running`),旧 Guardian 下这一块照旧不出现。

### A2. Routing Rules 窗口

菜单 → `Routing Rules…`

- [ ] 一行一条(direct + proxy 都在),**有问题的排最前、健康的一个字不写**。
- [ ] **健康时窗口顶上一个字都没有**。常驻横幅本身就是缺陷。
- [ ] `Add Rule…` 三种结局:接受 / 危险规则弹 409 后可 `Add Anyway` / 非法输入被挡。
- [ ] `Remove` 之后有 `Undo`(删除**不弹确认框**,这是刻意的 —— 为 11 条冗余规则点
      11 次确认是在惩罚正确的行为)。
- [ ] **关掉保护再打开这个窗口**:顶上应出现
      `A rule with nothing written under it here has not been checked: bx's core is not
      answering, so it could not say which rules are failing.`
      —— **不该**是一排没有副标题、按最健康档排序的行(那是 2026-09-12 修的那个 bug:
      空数组被读成「一条都没在失败」)。
- [ ] 窗口开着时失败计数跟着环境刷新走(不冻在打开那一刻)。

### A3. 按应用看分流

菜单 → `Traffic by App…`

- [ ] 三组标题是 **`Through the tunnel` / `Direct` / `Blocked`**(不是三个全大写短名 ——
      CLAUDE.md 曾抄错过)。
- [ ] **七列**:App / Conns / Up/s / Down/s / Up / Down / Rule,图标在**应用名那一格里**
      (不单独占一列 —— 单独占列时它自己涨到 ~350pt 把后面全挤扁)。
- [ ] **速率第一拍是破折号,第二拍起有数。** 若**一直**是破折号,说明
      `refreshIfVisible` 那条路没走到,而窗口看起来完全正常。
- [ ] 搜索框过滤时**三个分组标题都还在**(没有匹配的组补一条 `No matches`)——
      标题自己消失会让人分不清「这组没匹配」与「这组本来就空」。
- [ ] 搜索框打两个字**焦点不丢**(它住在每 5 秒重建的视图树之外)。
- [ ] `unknown` 占比不高(报告是 60 秒滚动窗口 + 归因提前到连接还开着时做)。
- [ ] 底部固定一行 `Byte counts are approximate: ports get reused, and an app listed in
      two sections may show all its bytes on one side.`
- [ ] 关掉窗口后 CPU 与拨号回落;30 秒后再开,数据从零开始。
- [ ] **滚动位置不再被拉回顶部**(2026-09-24 所有者确认会拉回、当天修掉):往下翻到一半,
      等过几次 5 秒刷新,应停在原处;改搜索词时回到顶部是刻意的。

### A4. 菜单本体:精简 + 第一行开关

- [ ] **9 行 2 条分隔线,没有抬头**(「bx / Connected」那两行已删 —— 菜单栏图标是身份、
      开关是状态)。
- [ ] 第一行是 `[盾] Protection …… ◉━━` 开关;下面一行暗色小字是
      **`<服务器名> · <延迟>ms`(服务器名优先)**,不是 `reality@vps`。
- [ ] 拨动开关:**位置停在目标位置并禁用**(不是弹回再跳过去),进度写在它下面,
      **菜单不关**。失败要弹回。
- [ ] `Troubleshoot ▸` 子菜单展开时**不会每 2 秒被拆掉**(commitMenu 按签名比对,
      稳态下菜单开着也不再闪)。
- [ ] `Quit` **不带图标、带 ⌘Q**(电源符号紧挨保护开关会被读成「关掉保护」)。
- [ ] 装的版本号在 `Troubleshoot ▸` 里一行,**不再常驻一级菜单**。

### A5. 右键加规则 + 规则热生效

- [ ] 在 `Traffic by App` 窗口里**右键一个应用** → 选一条 `Always direct`。
      (右键是发现不了的,窗口底部应有一行提示。)
- [ ] 预期弹**「已生效」而不是「Reconnect Now」** —— Guardian 写盘后叫 Core
      `/v0/reload`,成功即 `requires_restart:false`。
- [ ] 立刻 `bx explain <那个域名>` 应当场答 `DIRECT`。
- [ ] 候选过滤**只作用于 direct**:三段以上域名给「精确 + `*.父域`」,两段给 `*.host`,
      IP 原样;开放平台域名(如 `*.s3.amazonaws.com`)在 direct 一侧应被挡。

### A6. 泄漏检测的浏览器那半

```
bx leakcheck          # 非 root,不读 config,不需要 Guardian
```

- [ ] 页面**在它自己联系任何人之前**先列出四个第三方(icanhazip v4/v6、
      stun.cloudflare.com、www.cloudflare.com/cdn-cgi/trace),措辞是
      「Before **this page** sends anything」。**范围只到页面那一半** ——
      打开页面的时刻 bx 已经从本机探过四个 AI 端点(A8),那一半的披露在终端里;
      页面上应另有一句说明这件事。**别把这一条读成「bx 这一轮还没联系过任何人」
      就打勾** —— 那正是它 2026-09-14 被收窄的原因。
- [ ] 点 `Run the check` —— **这会向那四个发真实探测**。
- [ ] 十四条结论一条不少,四段(path 5 / identity 4 / surface 1 / reach 4)分段正确 —— 第四段见 A8。
- [ ] **三个计数并排,绝不合成一个总数** —— 今天那一行长这样:
      `N leak(s) in the traffic path, M identifying trait(s), K not checked.`
      绝不出现任何一句笼统的好话(「no leaks found」「you are not leaking」那一类):
      一份全是 not checked 的报告渲染出那句话,正是这个功能最坏的失效。
- [ ] 另外两项:`sudo bx leakcheck` 应被**拒绝**(`guardLeakCheckPrivileges`,从没实跑过);
      `bx leakcheck --json` 输出。

### A7. Diagnostics Checks 页与 CLI 逐条对比

- [ ] 菜单 → `Troubleshoot ▸` → Diagnostics → `Checks` 页
- [ ] 对照 `sudo bx doctor --json --skip-probe`
- [ ] **Checks 页比 CLI 那份多一行 `probe` 是预期的**(Guardian 那份永远探测,
      它没有 `--skip-probe` 这个概念),不是漂移。
- [ ] 两页渲染完都应**滚回顶部**(第一眼看到的该是合计句与 WARN,不是末尾的 OK)。
- [ ] Checks 页有带秒的时间戳(只到分钟的话连点两次看不出有没有刷新)。

### A8. AI 站可达性(第四段)

```
bx leakcheck          # 探测在前、页面在后:打印页面 URL 之前最长静默约 42 秒
                      #   = 本机事实采集 ≤5 秒 + 可达性探测 ≤37 秒
                      #     (4 个目标 × 8 秒 + 5 秒余量)
```

- [ ] **先盯这条**:敲下命令之后,终端应**立刻**打出要联系的四个 AI 地址与
      `This step takes about 37 seconds at most`,然后才是那段静默。静默期间什么都不打印是预期的,
      不是命令挂了 —— 若它让人难忍,**下一步不是缩短 probeTimeout**(会把慢链路上
      的可达站判成不可达),台账里记着一个「Listen 先、Serve 后」的零并发方案。

- [ ] 页面上出现第四段,四个目标各一行(Anthropic API / claude.ai / OpenAI API /
      Google AI API)。**`chatgpt.com` 不在清单里,这是刻意的**,不是漏了。
- [ ] 五态计数(reachable/refused/undetermined/unreachable/challenged)**与
      泄漏那两个计数并排**,不是合成一个数。
- [ ] `claude.ai` 那行应是**可达**(favicon 路径);若显示「没查出来」
      (undetermined 或 challenged),说明 CF 把防护加到 favicon 上了 ——
      回来更新 `internal/leakcheck/endpoints.go` 里那条 target 的 `ExpectedSignal`。
- [ ] 措辞是「bx can reach claude.ai」,**不是**「你可以用 Claude」。
- [ ] `bx leakcheck --no-reach` 应当**不发**那四个请求(用抓包或防火墙日志确认),
      而第四段的四条结论仍在、如实报「没查」,不是从页面上消失。
- [ ] 把服务器切到一台不通的 VPS 再跑一次:四个目标应变成 **unreachable**,
      而**不是**多了几条「泄漏」——不可达属于 reach 这一段,不进 path/identity 的
      异常计数。

---

### A9. explain 的两句新话 + 组的副标题(2026-09-14)

**不动网络,全是只读。** 先挑一条**真有失败**的规则:

```
bx status --json | jq -r '.rules[] | select(.failures > 0) | .rule' | head
bx explain <上面挑出来的那个域名>
```

要看三件事:

1. **`Blame` 那一行在不在,以及它说的处置对不对。** 主导那一类是 `route unreachable` 时
   它应当点名 direct_egress(那是 2026-08-13 与 08-16 两次故障的签名),是
   `peer did not answer` 时应当说「改 bx 的规则不会有帮助」。**两句读起来必须相反。**
   失败混杂(没有哪一类过半)时**这一行不该出现** —— 那不是 bug。
2. **`Review` 那一行。** 拿一条你知道有问题的规则试(例如一条被内建 china 列表
   覆盖的,或 `sudo bx doctor --skip-probe` 报过的任意一条),explain 应当在那条
   规则旁边说出同一句结论。**没有结论时这一行不该出现,更不该出现「这条规则
   没问题」那种话。**
3. `bx explain <域名> --json | jq '.tcp_rule_findings'` —— 文本有而 JSON 没有
   就是回到了「诊断只长在文本路径上」那个老形状。

另外打开 **Routing Rules 窗口**,看 Presets 那四行:

- 每行标题右边应当有**一句英文小字**说「开了会怎样」(例如 Tencent 那行是
  「WeChat, Tencent Meeting and QQ sign-in, chat and media」),**一行都不该是空的**;
- 窗口窄的时候该被截断的是**那句小字**,不是右边的数字与 Show 按钮;
- 四行仍然是**一组一行**(副标题没有把它变成两行)。

## B. 要制造一次故障(每条都会真的动网络,自己挑时间)

### B1. 段重置 —— 2026-09-13 刚修的那个,**唯一没被任何真机证据覆盖的新代码**

复现条件:**VPS 不通 + 你按一次开关**。

1. 把 `/etc/bx/config.yaml` 的 `server` 临时指向一个不通的地址(或等 VPS 再挂一次)
2. `sudo bx up` —— 它会失败
3. 盯 `sudo tail -f /var/log/bx-guard.err.log`

- [ ] 修复前的行为:此后**一次都不再试**,只有 `outcome=skipped code=start_core_exhausted`
- [ ] **修复后应看到**:调谐环重新试满 5 次(`action=start_core outcome=failed`),
      然后才 `start_core_exhausted`
- [ ] 改回地址、`sudo bx up`,计数归零

### B2. Core 起不来时那五句话

把 `current` 指向一个不通的地址,`sudo bx up`:

- [ ] **一次**就说出「bx 连不上 `<host:port>`」并点名另一台服务器(如果配了),
      **而不是**七次 `core_ownership_uncertain` 三百字不沾边的排查指引
- [ ] `/var/lib/bx/core-start-failure.json` 在**成功**启动之后不该存在
- [ ] 若那句话报的是 `local_dial`(不该,但那是 2026-08-13 那种机器状态的样子),
      去 `bx.log` 找 `core_start_diagnosis_unbound_retry`:**有**这一行说明不绑网卡
      那次重试发生过而也在本机失败;**没有**说明绑网卡那次拿到的是服务器的答复。

**2026-09-24 真机上自己触发了一次(不是造出来的),给你打勾参考**:`sudo bx up` 一次就给出
`core_tunnel_handshake_failed` 那一档 ——「server <地址>:443 answers on its TCP port, but the tunnel
did not come up inside the start window」,并点名了另一台服务器;没有出现 `core_ownership_uncertain`。
Core 日志里对应的是 20 秒内几十次 REALITY 握手约 150ms 后被 `connection reset by peer`,判别拨号
TCP 连得上 —— 与这句话说的一致。50 秒后重试成功。**这只覆盖五档里的一档**(handshake_failed);
unreachable / udp_transport / local_dial / 笼统那档仍然没在真机上出现过。同一次暴露的三处措辞毛病
(名字就是地址时写两遍、命令留着 `<name>` 占位符、英文里混中文标点)已修,需要升级后再看一眼。

### B9. Replace Link 去掉 UDP 链接(2026-09-24)

Servers 窗口 → 某台带 UDP 链接的服务器的 `⋯` → `Replace Link…`:

- [ ] 表单里有「Remove this server's UDP link」勾选框,UDP 框下的提示说「留空 = 保持,
      勾选 = 去掉」。**需要先升级 Guardian**;旧 Guardian 上不该出现这个勾选框。
- [ ] 勾上、主链接照旧粘贴 → 确认后 `/etc/bx/config.yaml` 里那一台的 `udp:` 行没了。
- [ ] 勾上又填了一条 UDP → 应弹「Two Answers for UDP」,配置一个字节不变。

### B3. 状态转换通知

- [ ] `sudo bx down && sudo bx up` **不该弹通知**(那是你自己做的)
- [ ] 拔掉 VPS 或 `sudo route delete <服务器IP>` 让隧道断 **≥ 30 秒** →
      应弹一条 `traffic blocked`
- [ ] 恢复后弹 `protected again` 并**顶掉**前一条(同一个 identifier)
- [ ] 30 秒内的抖动(睡醒 Wi-Fi 起落)**不该**响

### B4. ③c 的合成路径(它自己已被 VPS 不通那次验过,这条是补另一半)

```
sudo mv /var/lib/bx/sing-box /var/lib/bx/sing-box.bak
sudo kill -9 <Core PID>
```
- [ ] 日志 `core_unexpected_exit` → `core_restart_failed` → 循环
      `start_core … execute_failed` 五次 → `start_core_exhausted`
- [ ] 改回名字、`sudo bx up` 归零回绿

### B5. 休眠唤醒后的旁路自愈

- [ ] 合盖休眠 ≥ 数分钟再唤醒(或 `sudo route delete <服务器IP>` 模拟)
- [ ] 30 秒内 `bx.log` 出现 `server_bypass is broken` → `server_bypass reinstalled the routes`
- [ ] sing-box 的 EOF 刷屏停止、隧道回绿,**不需要 down/up**

### B6. 换服务器 / 换 IP

- [ ] **切换一次服务器**(会改出口 IP):四种结局的措辞对得上实际发生的事
- [ ] **换服务器 IP 或先改 DNS 记录**:2–7 分钟内应出现
      `server_bypass_refollow: the server's address changed`,隧道自己回绿

### B7. 规则写入路径(会改配置,但热生效、不断隧道)

- [ ] `bx direct add <某域名>` —— **从来没有人在真机上敲过这条**
- [ ] 畸形写法应被拒(空白、引号、URL、`a..b.com`)
- [ ] 被更宽 proxy 规则盖住的 direct 规则应被拒,**且错误里点名挡路的那一行**

### B8. AI 站可达性的直连对照(`--compare-direct`,2026-09-23)

**会从物理网卡直接访问四个 AI 端点,那四家会看到你的真实 IP** —— 自己决定要不要跑。

- [ ] `bx leakcheck --compare-direct`(不加 sudo):第一行应说「probe … twice … directly from your
      physical network interface … show these sites your real IP address」,**在任何请求之前**。
- [ ] 每条 AI 站结论末尾多一句对照(例如「It is reachable directly too.」或「… through your current
      path but not directly …」)。**有一边是挑战页或没测成时不该有这句。**
- [ ] 与 `--no-reach` 同时给应直接报错,一个请求都不发。

---

## C. 要等机会,不值得专门制造

- [ ] **菜单栏 LaunchAgent 的 EIO(5) 不再出现**:下次升级(或 `bx app-install`)时看输出里有没有
      `Bootstrap failed: 5: Input/output error`。2026-08-14 `580c0363` 已修,之后没再见过;见到了
      就说明还有另一条路径,回来重新立项。

- [ ] **路由就绪位自愈**(2026-09-23):要一次**拆到一半才失败**的换路由才会触发,不值得专门制造。
      真发生时 `bx.log` 应先出现 `routes_ready is false while bx is running`,最多 30 秒 ~ 5 分钟后
      出现 `routes_ready: reinstalled the capture routes`,之后路径恢复与 `bx update` 不再因
      「capture routes are not installed」失败。

- [ ] **强制门户**:下次住酒店/在咖啡店连 Wi-Fi 时,看 bx 有没有给出那句
      「常常要 down/up」的提示,以及菜单里的 `Open Wi-Fi Sign-In Page` 按钮能不能
      打开网关页。
- [ ] **死规则判定**:要 Core 累计跑 **14 天** + **20,000 次判定**才会开始判。
      今天是 9.2 天 / 68 万次 —— **时长还差**。到期后 `sudo bx doctor --skip-probe`
      看有没有假阳性(判错的后果是用户删掉一条天天在工作的规则)。
- [ ] **日志折叠**:要 sing-box 那句 `missing default interface` **成串**刷屏才有素材。
      13 天里只出现 5 次、分散在 3 天,一次都没落在同一个折叠窗口内。发作时
      `bx.log` 会出现带 `[同一行重复 N 次已折叠]` 的行。
- [ ] **`*.qq.com` 那 40%**:跑几小时后 `bx explain qq.com` 看**本次**计数;
      失败非零时 `bx status --json` 的 `rules` 表带 `failure_kinds` ——
      `unreachable` 是路由问题(2026-08-13 那个 DirectDialer 故障的签名)要立刻查,
      `timeout` 是对端不应答,一个字都不用改。

---

## D. 非 macOS(没有机器,长期挂着)

- [ ] **Linux 上 Core 起不来时 `bx status` 说出原因**(2026-09-23):升级一台 Linux 客户端后,
      `bx update` 应打印「The service definition now records why the core fails to start…」,
      `/etc/systemd/system/bx.service` 的 ExecStart 末尾多出 `--start-failure-file …`。
      之后把服务器链接指到一个不通的地址、`sudo bx up`,几秒后 `sudo bx status` 应给出
      「bx could not start: …」那段话(不是「bx is not running」),「Full reason」指向
      `journalctl -u bx.service`;不加 sudo 应说「要 sudo bx status 才看得到原因」。
      `sudo bx down` 之后 `bx status` 应回到普通的「not running」,**不许**再报那段失败。

- [ ] **Windows 托盘 App 与 Inno 安装包**:代码完成、GUI 从未真机点过
      (`030-SJWJ-GSR-B` 那次验的是 CLI 与服务,不含托盘)。
- [ ] **IPv6 在 darwin 上的 `-reject` 语法**、本地 errno 是否 EHOSTUNREACH。
- [ ] **Linux 的 Tailscale UDP 旁路规则**(`pref 90 fwmark ipproto udp`)——
      netns 台子只能证明「iproute2 不支持时退路是对的」,证明不了规则真装上了。
