# CLAUDE.md — 泄漏检测(internal/leakcheck 及其两个伙伴包)

本文件只在读到 `internal/leakcheck/` 下的文件时加载。产品定位与两条命令的分工在根目录
`CLAUDE.md`;这里是判据。2026-09-23 从根目录下沉,原文逐字存档在
`docs/lessons/leakcheck-archive.md`,页面 JS 闸门的经过在 `docs/lessons/leakcheck-page-js-gate.md`。

**三个包**:`leakcheck`(**纯判据**,无 I/O,`purity_test.go` 按前缀禁 net/os/exec 并明写
例外)· `leakserve`(一次性 loopback 服务 + 页面 + 本机事实采集)· `loopbackgate`(token +
逐字节比 `Host` + 写操作才要 `Origin`;**`Host` 逐字节比对是唯一可靠的 DNS-rebinding 判据**)。
动 `leakserve` 或 `internal/cli` 里 leakcheck 那几条路径时,这份不会自动加载 —— 先读它。

## 四段,各自计数,绝不合成一个总数

`Section`:`path`(流量去哪儿,进 `AnomalyCount`)· `identity`(会不会被单独认出来,进
`IdentityCount`)· `surface`(网站看得到什么,`Verdict.Info`,**无极性、不进任何计数**)·
`reach`(AI 站边缘可达性,进 `Report.Reach` 那五个并排计数)。合成一个数时它永远不为零
(普通 Chrome 就不防指纹),被训练成噪声、把真泄漏一起淹掉。**`Section` 零值是
`SectionPath`**:漏填是多报,反过来是漏报,代价不对称。

**十四条结论**(path 5 · identity 4 · surface 1 · reach 4):`traffic_carrier` ·
`webrtc_srflx` · `ipv6_leak` · `dns_path` · `route_escape` ‖ `local_addresses` ·
`timezone_vs_exit` · `language_vs_exit` · `fingerprint_defence` ‖ `browser_surface` ‖
`reach_*`(前缀拼端点 ID)。
- 条数由 `TestOutlineHasTheDocumentedNumberOfConclusions` 钉住 —— 加减一条时它红一次,那正是
  回来把**这个数和上面这句定义**一起改对的时刻(改分段那天只改了一处,是本仓库罚过的形状)。
- `Outline()` 与 `Judge()` 的 ID/顺序/分段逐项对上;**结论集合不随输入变化**(页面对认不出的
  ID 是 `if (!row) return;` 静默丢弃,少一条就是一行永远等不到结论的空壳 —— 守卫在非零输入上
  也要查,只喂全零输入会让「实现恰好是无条件的」掩盖这个性质)。
- 「哪条需要浏览器」由 `Outline().Inputs` 是否为空推导,**不许手抄 ID 列表**。

## 判断上的取舍(改之前先读)

- **`WhoOwnsTheRoute` 四态**(bx / 别人的隧道 / 没有隧道 / 没问出来)是一切结论的挂靠点。
  判据取 `route -n get 1.1.1.1` 那一跳,**不是 `default`**(split-default 下 `default` 仍指
  物理网关)。接口名分类**白名单式**:认得出隧道→有,认得出物理→没有,**认不出→不知道**
  (把物理网卡判成隧道 = 把裸奔的机器说成受保护)。隧道前缀表**全仓只有 `describe.go` 一份**。
- **规则单边**:不知道有没有隧道就不许判 ok,但仍可判 bad。没有隧道时 WebRTC 两半一致**不是
  好消息**。
- **TunnelVision 判据**:单主机公网 `/32` 不报(bx 自己的 server bypass 就是这形状,真机有
  106 条),**更宽的公网前缀才报**;reject/blackhole 不算逃逸(bx 的屏障就是一组 `/2` reject)。
  CGNAT 豁免必须是「**落在** CGNAT 里」,不是 `Overlaps`(后者会放行 `64.0.0.0/2`)。
- **bx 自己的 UDP 分流长得和泄漏一样**:`udp.transport` 指向另一台服务器时判 `NotChecked`
  **不是 ok**(判 ok 是把假阳性换成假阴性);`udp.mode=direct-realtime` 仍判 bad(只是点名是
  配置选的)。豁免**只对 OwnerBX 生效**。
- **国旗只在能证明时给**:只能靠内嵌 china CIDR 证明「在不在中国大陆」;anycast 让「国家」
  没有答案,判不出返回 nil 判据而非恒 false。
- **指纹问「有没有在防」,不问「指纹是什么」**(没有语料库就没有分母,编百分比比不报更糟);
  判据是同一会话画两遍 canvas 是否相同。
- **检测结果不留存**,页面与 CLI 都明说。

## 跨语言契约与端点

- **探测名**:`leakcheck.Probe*` 常量与页面里 `probeLanded("srflx", …)` 那几个手抄字面量必须
  一致,漂了那一格**永远停在「还在等」**而两侧全绿。`leakserve.TestPageProbeNamesMatchTheGoConstants`
  **双向**钉住。
- **页面 JS 的纯解析区**(`==== BX-PURE-BEGIN/END ====`)由 `scripts/test-page-js.sh` 抽出来
  交给 node 断言(挂进 `verify.sh`,没有 node 时显式 SKIPPED)。四个探针的极性**全部**收进纯
  区段,守卫是「每一处都是」不是「至少一处」;`TestPageJSNeverAssertsThatAProbeLanded` **刻意
  不对称**:第二个实参字面量 `true` 一律禁(没看答案就宣布落地),`false` 允许(`.catch` 里)。
- **端点是用户可见契约**,换之前过三关:常量钉死(`TestEndpointsArePinned`)/ v4、v6、trace
  必须 https / **不在 china 直连列表**(`TestEchoEndpointsAreNotOnTheChinaDirectList`,拿真实
  内嵌列表 + 生产 `DomainSet`,带「列表里确实命中得到东西」的自检)。这个坑踩过三次
  (`ifconfig.me`、`api.ipify.org`、候选里的 `ipapi.co`/`ifconfig.co`)。现用
  `ipv4/ipv6.icanhazip.com` + `stun.cloudflare.com` + `www.cloudflare.com/cdn-cgi/trace`。

## 第四段:AI 站可达性(`reach`,2026-09-14,整段真机未验)

- **接线**:`internal/cli` 的 `collectLeakCheckFacts` → `leakserve.CollectReach`,**单独一跳、
  单独预算**(`reachBudget` 由探测数派生;塞进 `CollectFacts` 的 5 秒会被掐断,掐断与「真的
  不通」在屏幕上一样)。**它给 `bx leakcheck` 新增了出站**,所以 `announceReachTargets` 在第一个
  请求之前把地址原样列出来(`--json` 走 stderr)。CLI 与页面各有自己的标题,摘要另起一行报五态、
  **为零也打印**(`default`/`|| titles.path` 兜底吞掉新分段出现过三次)。
- **`ReachState` 五态,零值 `ReachUndetermined`**:`Reachable`/`Refused`/`Unreachable`/
  **`Challenged`**。`Challenged` 与 `Undetermined` 必须分开:CF 挑战有特征串能否掉「是你的出口
  有问题」,认不出的没有凭据、不许借同一句话(真实的地区封禁页会落进 undetermined)。
  **`JudgeReach` 同时看状态码与 body**:google API 的 403 是正常应答,`chatgpt.com` 的 403 是
  挑战页。
- **端点四个**(anthropic API、claude.ai **favicon**、openai API、google AI API);`chatgpt.com`
  刻意不在(恒为挑战页);claude.ai 用 favicon 是因为首页挂防护。常量带 `ExpectedSignal` 记档。
- **措辞**:可达只说「bx can reach X」,**绝不说「你可以用 X」**;不可达只说观测到什么,不断言
  对方服务状态。
- **`DefaultProbeBypass=false`**:绕过隧道会从物理网卡发 4 个 GET、**暴露真实 IP 给
  Anthropic/OpenAI/Google**,决定留给项目所有者;而且**没有绑物理网卡的拨号器**,只翻常量不供
  `BypassDial` 会安静地什么都不多跑。
- **已知边界**:favicon 200 只证明边缘可达;`Refused` 的关键词是构造的,没有真机样本。
  验收清单 `docs/acceptance-pending.md` A8。

## 真机状态

2026-08-31 本机那一半全绿(十条结论、三段分段、三个计数并排、`WhoOwnsTheRoute` 判对、
TunnelVision 不误报)。**仍未验**:浏览器那半(要人点)、非 root 门槛
(`guardLeakCheckPrivileges`)、`--json` 输出、整个 reach 段。
