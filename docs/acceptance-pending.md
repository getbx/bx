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

### A10. 界面语言(2026-09-26)

**不动网络。** 菜单 ▸ **Language** ▸ 选「简体中文」:

1. 菜单**当场**变中文(不用重启、不用退出菜单);Language 子菜单里「简体中文」打勾;
2. 先把 Servers / Routing Rules / Diagnostics / 按应用看流量 各开一扇,再切一次语言 ——
   开着的窗口应当**原地**换语言,不跳回顶部;「部署新 VPS…」表单里已经打的字不该丢;
3. 鼠标悬停菜单栏图标,那一行也应当是中文(`bx:受保护,… ms`);
4. 选「跟随系统」:系统首选语言是简体中文时仍是中文,改成 English 再切一次应当回到英文;
5. 退出菜单再打开,选择仍在;
6. **哪里还是英文**:Checks 页每一项后面的说明、规则体检那句话、日志正文 —— 这几处是
   服务端发的原话,**预期**仍是英文;别处出现英文就是漏了,截图记下来。

### A12. 「Outside bx」不再点名连 bx 自己服务器的连接(2026-09-28)

真机 2026-09-28:菜单红字 `Outside bx: ssh — quit and reopen`,而那条 ssh 是 Codex 的
ProxyJump 跳板、连的是 bx 自己的 VPS 的 22141 端口 —— 服务器旁路 /32 把它送去 en0 是设计。
判据现在排除 bx 自己绕开的网段(服务器旁路 + 配置 `bypass:`)。升级到带这条修复的版本后:

- [ ] 开着一条到当前 VPS 的 ssh(或 Codex 远程会话),菜单**不**出现 `Outside bx` 那行,
      `bx status` 里没有 `connections_bypassing_bx`。
- [ ] 对照组:`bx down`,开一个到公网站点的长连接(比如浏览器开一个视频页),再 `bx up`
      —— `Outside bx` 那行**要**出现并点名那个应用;关掉它,那行消失。
- [ ] 切换服务器之后,到**旧** VPS 的 ssh 若还开着,应被点名(它现在真的在隧道外面)。

两段式(同日,v0.4.13 起):
- [ ] `bx down`,开几个网页 / 让 Mail、WeChat 重连,再 `bx up`:菜单出现灰字
      `Settling: N connections from before protection was on — …`,**图标不裂**,数字随刷新
      变小,几分钟内那行消失;整个过程**不**出现红字 `Outside bx`。
- [ ] 对照组:`bx down` 期间开一个会活很久的连接(比如浏览器里一个视频流、或一条到公网
      主机的 ssh),`bx up` 五分钟后它还在 → 升级成红字 `Outside bx: <app> — quit and reopen`,
      图标裂开;关掉它,那行消失。
- [ ] `bx status` 里前者是 `Notice`、总状态仍是 Protected;后者把总状态降成 Needs Attention。

### A13. 一次性 pf 重置残留连接(2026-09-28,v0.4.14 起)—— **关保护那一步只能由所有者自己做**

它在 `bx up` 劫持完路由之后装两条 pf 规则(TCP 回 RST、UDP 回 ICMP,白名单 = 私网 +
服务器旁路 + 用户 `bypass:`,`user != root`),每秒看一次,残留清零或满 10 秒就拆。
**没有残留时一个 pfctl 都不调**,所以健康机器上什么都看不见。spec 第三点(anchor
`com.apple/250.bx` 会不会被主规则集求值)只有这一步能答。

**所有者 2026-09-28 定死:造残留必须关保护几秒,那几秒会泄漏真实 IP,这个动作只由他
自己在菜单上做,agent 不请求、不替做**;agent 只事先起观察器、事后读日志。2026-09-28
第一次尝试没跑成:装上的本地测试构建被菜单的「更新」催回了 v0.4.13(判据「运行版本 ≠
最新发布」),所以要验就先发 v0.4.14 再从菜单更新。
**已知不会被重置的**:窗口里没发过包的空闲 socket(`return-rst` 只在它发包时才打得到),
它们留给两段式去点名;日志末行报的是余额不是失败。

前提:`bx status` Protected;所有者自己决定要不要抓包(`sudo tcpdump -ni en0 -w …`)。
1. **所有者**在菜单上关保护,开几个网页、让 Mail/WeChat 重连,等 20 秒,再在菜单上打开。
2. agent 看 Guardian 日志端点里 Core 的 `pf reset:` 那几行:初始几条、几秒清零、还剩几条。
   (`bx run --pf-reset dry-run` 只在 Guardian 没在管的机器上可用;`bx run` 那条路上
   `runPFReset` 的 ≤10 秒里按 Ctrl-C 会把 anchor 留下 —— `sudo bx down` 会冲掉。)
3. 抓包用 python 逐包分类(不用 tcpdump 的复合过滤式):`bx up` 之后 en0 上非 bx 进程到
   公网的 TCP 是否在几秒内只剩 RST;之后没有新的明文 SYN。
4. 菜单:`Settling` 那行要么不出现、要么几秒内消失;`Outside bx` 不出现。
5. 到自己 VPS 的 ssh(Codex 跳板)**不许断**;局域网设备不许断;Tailscale 不许断。
6. `bx down` 之后 `sudo pfctl -a com.apple/250.bx -s rules` 为空、`/var/run/bx/pf.token` 不在;
   `bx doctor` 的 `pf_reset_residue` 是 ok。
7. 对照:`sudo pfctl -s info` 里 pf 的状态与 `bx up` 之前一致(bx 释放了自己的引用)。
8. 若第 2 步日志显示 `loading the reset rules` 失败,先怀疑 anchor 落点(spec 第三点):
   `sudo pfctl -s Anchors` 看 `com.apple/*` 下有没有 `250.bx`。

### A14. 问题上报(2026-09-29)—— 第一份报告真的到了 issue 里吗

Guardian 在五类失败上写 `/var/lib/bx/reports/<时刻>-<签名>.json`,保护开着时经隧道 POST 到
收集端,收集端在私有仓库 `getbx/bx-reports` 建或追加 issue。**agent 能判的**(包的形状、脱敏、
限频、接线)全有守卫;**只有真机能答的**是「经隧道那一跳」与「Cloudflare 上那个 Worker 收到
真实流量之后的行为」。

前提:装上含这一功能的版本,`bx status` Protected。
1. `sudo bx reports` 应说「No problem reports」(健康机器一份都不该有)。
2. 制造一次会触发的失败,最便宜的是 B2 那条(改坏 server 链接 → `bx up` 起不来 → 恢复)。
   之后 `sudo bx reports` 应列出一份 `corestart:…` 或 `attention:…`,状态 `pending`。
3. 保护恢复后等最多 5 分钟(或看 Guardian 日志端点里 `guardian_report_sent`);再 `sudo bx reports`
   应变成 `sent`。**保护没恢复它就一直 `pending`,那是设计**。
4. `sudo bx reports show <name>`:里面**不许**有你的服务器 IP / 域名 / 链接 / bypass 网段
   (应是 `<ip-1>`、`<host>`、`<link>`);私网与 `198.51.100.x` 可以在。
5. 到 `github.com/getbx/bx-reports/issues` 看:标题 `[<签名>] bx <版本> on darwin`,标签 `auto`。
   同一签名第二次应是评论追加、标题计数 +1,而不是第二个 issue。
6. 对照:`reports: off` 写进配置、重启 Guardian 之后重做第 2 步,目录里不许多出文件。

### A15. 部署窗口:填密码、一键装好一台新服务器(2026-09-30)—— 要一台空 VPS

**agent 已经验过的**:密码经 stdin → sshpass → ssh 的整条路(本机 Ubuntu 容器,root 与「sudo 要密码
的普通用户」两种登录都装成功,输出里一次都没出现密码)、错密码 / 指纹变了 / 「我重装过它」三条失败
路径、每一步的参数与判类(见 `apps/macos/BxMenu/CLAUDE.md`「部署窗口」)。**只有人在屏幕前能答的**
是窗口本身,以及真 VPS 上「装完 → 加进清单 → 从这台 Mac 测」那最后两步(会写你的服务器清单)。

1. 菜单 → Servers → Set Up a New Server。填服务商给的地址、登录名(多半 root)、密码,点 Set Up Server。
   **全程不该出现终端**;七个步骤依次打勾,一般一到三分钟。
2. 结束时应说「“名字” is ready」,并说**你现在的出口没变**;Servers 里多了这一台,● 仍在原来那台上。
3. 同一台再装一次:应**沿用**它(结果页说钥匙沿用、分享出去的链接照样能用),清单里**还是一条**,
   出口没变;服务器上 `/var/lib/bx/sbserver.json` 的修改时间不变(没有重装)。
4. 故意填错密码:应说「登录被拒绝」,不重试;密码框是空的。
5. 若服务商有安全组且 443 没开:结果里应点名「安全组」。
6. 装好后点「Add to iPhone…」,用 iPhone **系统相机**扫:应弹出打开 bx 的提示,bx 里先出现
   「Add This Server?」并写着这台的地址;点 Add 才加上。(二维码能解回同一条链接已在本机验过;
   相机 → App 这一跳只有真机能答。)

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

### B10. 升级时 Core 起不来,说出为什么(2026-09-24)

不值得专门制造 —— 下次升级**恰好**撞上服务器不通(比如握手被 RST 的那种风暴)时看一眼:

- [ ] 回滚成功时,`bx update` 在「rolled back」之后多一段「Why: … not the update itself; try updating
      again later」并点名服务器;菜单弹的「Update Rolled Back」也说同样的意思。
- [ ] 回滚也失败(机器被拦住)时,终端的错误前面多一段「Why protection is blocked: …」。

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

### B9. 在屏障下切换 Guardian(D1–D3,2026-09-25)—— 由 agent 做,带抓包

第一次真跑就是所有者 Mac 从 v0.4.3 Guardian 切到带 D1–D3 的那一版。步骤:
1. 先演练屏障本身:装屏障 → `route -n get 1.1.1.1` 应是 reject、`curl https://icanhazip.com` 失败 →
   拆屏障 → 恢复。断网几秒,不泄漏。
2. 真切换:`sudo tcpdump -ni en0 -w <scratch>/switch.pcap` 开着,跑
   `sudo bx app-install --app-source /Applications/Bx.app --yes`。
3. 判据:pcap 里除了发往服务器 IP 与私网/链路本地的包,**一个都没有**;`bx status` 报 Protected,
   `guardian_version == runtime_version`,能力里有 `core_outlives_guardian`。
4. D2 顺带验:之后一次 `bx update` 提交后 Guardian 日志出现 `guardian_restart_for_update`、
   Core PID 不变、全程不断网。

**第一次真跑(2026-09-25,v0.4.6)**:步骤 1 演练过(51 包,0 个发往服务器/私网之外)。步骤 2 停在
「停旧 Core」:launchd 正在收掉 Core,读它的可执行路径报 EINVAL,切换失败 —— **屏障守住了**:
06:38:35–06:45:50 这 7 分钟里 en0 上发往公网(服务器除外)的包 **0 个**。但接下来用户敲 `bx up`,
那条路不知道有个没做完的切换,在一道它不拥有、且服务器 /32 已被删的屏障后面起 Core,报了一句
误导的 local_dial 诊断;之后 `bx down` / `bx up` 恢复。Guardian 因此换到了 v0.4.6,但**用的是旧
plist**(没有 AbandonProcessGroup)。修复:交接请求落盘,`bx up` 认出没做完的切换并经 migrate
做完、`bx down` 拆掉那道屏障;停旧 Core 先等它自己退、读不动隔拍重试;服务器 /32 在 bootout
之后立刻补。另见 known-gaps A11(down 期间开的连接在 up 之后仍走 en0)。

**第二次真跑(2026-09-25,v0.4.6 Guardian 旧 plist → v0.4.7):通过。** 屏障 07:22:04 装上,旧 Core
约 2 秒自己退出,新 Guardian 07:22:17 起、新 Core 07:22:18 起,07:22:30 完成;Guardian=v0.4.7、
plist 带 AbandonProcessGroup、能力里有 `core_outlives_guardian`、observed 全绿。**07:22:04–07:22:17
en0 上发往公网(服务器除外)的包 0 个。** 新 Core 起来之后 en0 上的公网流量逐个对过:直连规则
(Apple push / 腾讯 / Steam / 223.5.5.5,切换前稳态就有)、Tailscale(绑网卡,设计如此)、以及一条
known-gaps A11 的 Chrome 连接。**D2 真机已验(2026-09-25,v0.4.7 → v0.4.8)**:`bx update` 提交后 Guardian 日志
`guardian_restart_for_update running=v0.4.7 installed=v0.4.8` → `guardian_exiting_for_restart
core_left_running=true`;新 Core 1899(08:07:35 由旧 Guardian 起)在旧 Guardian 退出后活着,新 Guardian
(08:07:41 起,v0.4.8)接管它(`core_pid` 仍是 1899),Protected,全程不断网。en0 上的公网流量与切换前
稳态同一批(直连规则、Tailscale、A11 那几条旧连接)。同一次 v0.4.8 的 A11 检测在真机上点名了 Chrome、
Steam —— 以及一个假阳性 trustd(发往 bx 自己的 fake IP 198.18.0.16 的 SYN),已修:198.18/15 不算公网。

## C. 要等机会,不值得专门制造

- [ ] **菜单栏 LaunchAgent 的 EIO(5) 不再出现**:下次升级(或 `bx app-install`)时看输出里有没有
      `Bootstrap failed: 5: Input/output error`。2026-08-14 `966efa16` 已修,之后没再见过;见到了
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
