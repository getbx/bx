# 泄漏检测 —— 2026-09-23 从根目录 CLAUDE.md 下沉时的原文存档

**这是存档,不是现行判据。** 2026-09-23 根目录 CLAUDE.md 开始按代码目录下沉,这些段落的
**判据**浓缩进了 `internal/leakcheck/CLAUDE.md`(那一份才是改代码之前要读的);这里逐字保留当时的原文,给想知道
「当初怎么换来的」的人看 —— 事故经过、真机数字、变异实测、被否掉的方案的完整理由。
读到与 `internal/leakcheck/CLAUDE.md` 冲突的地方,以后者为准。

---
**包**:`leakcheck`(**纯判据**,无 I/O,`purity_test.go` 按前缀禁 net/os/exec 并明写例外)·
`leakserve`(一次性 loopback 服务 + 页面 + 本机事实采集)·`loopbackgate`(token + 逐字节比
`Host` + 写操作才要 `Origin`;`Host` 逐字节比对是唯一可靠的 DNS-rebinding 判据)。

**判据分四段,各自计数,绝不合成一个总数**(`Section`)(**2026-09-14 从三段改成
四段** —— 加了 `reach` 那天这句定义句漏了改,是本文件罚过的「只清点名的那一句、
不清同一句话的其它副本」当场又犯了一遍):`path`(流量去哪儿,bx 或当前隧道
负责,进 `AnomalyCount`)· `identity`(会不会被单独认出来,进 `IdentityCount`)· `surface`
(网站看得到什么,**`Verdict.Info`,没有极性、不进任何计数**)· `reach`(AI 站边缘
可达性,进 `Report.Reach` 那五个并排的计数,详见下文十四条结论那段)。合成一个数
时它永远不为零(普通 Chrome 就是不防指纹),于是被训练成噪声、把真正的泄漏一起
淹掉。`Section` 零值是 `SectionPath`:漏填是多报,反过来是漏报,代价不对称。

**十四条结论**(此前这里写的是「八条」,**漏了头尾两条**,2026-08-24 按 `Outline()` 实测
更正;**2026-09-14 从十条改成十四条** —— 第四段 `reach`(AI 站可达性)接进
`Outline()`/`Judge()`,四个端点各一条结论):
`traffic_carrier` 谁在承载你的流量 · `webrtc_srflx` WebRTC vs 出口 · `ipv6_leak` IPv6 暴露 ·
`dns_path` DNS 路径 · `route_escape` **路由被动过手脚(TunnelVision CVE-2024-3661 /
TunnelCrack ServerIP)** ‖ `local_addresses` 内网地址是否被 mDNS 遮掉 · `timezone_vs_exit`
时钟 vs 出口国 · `language_vs_exit` 语言 vs 出口国 · `fingerprint_defence` 指纹防护 ‖
`browser_surface` 网站看得到什么 ‖ `reach_*`(前缀拼端点 ID)四个 AI 站的边缘可达性 ——
**不是安全问题**,坏消息是「你用不了」不是「你泄漏了」,与另外三段各自计数、绝不合成。
(`‖` 是分段边界:path 5 条、identity 4 条、surface 1 条、**reach 4 条**。)
骨架(`Outline()`)与 `Judge()` 的 ID/顺序/分段**逐项对上**,由守卫钉住;「哪条需要浏览器」由
`Outline().Inputs` 是否为空推导,**不许手抄一份 ID 列表**(`reach_*` 的 Inputs 为 nil ——
探测是普通 HTTP 拨号,不依赖页面 JS 采集)。
**条数本身也由守卫钉住**(`TestOutlineHasTheDocumentedNumberOfConclusions`)——
加减一条结论时它会红一次,那正是回来把这个数字改对的时刻;而一份说少了的清单会让下一个人
以为某条结论不存在。**另有一条钉住「结论集合不随输入变化」**:页面按骨架先摆行、再按 ID
塞结论,而页面对认不出的 ID 是 `if (!row) return;`(静默丢弃)—— 少一条就是一行永远等不到
结论的空壳,两头都不报错。原来那条守卫只喂全零输入,**而「够是因为实现恰好是无条件的」正是
「测试输入让待守属性不可见」的形状**;变异实测:让 Judge 只在浏览器**到了**时少发一条,
旧守卫全绿、新守卫在两个非零输入上转红。

**几条判断上的取舍,改之前先读**:
- **`WhoOwnsTheRoute` 四态**(bx / 别人的隧道 / 没有隧道 / 没问出来)是一切结论的挂靠点。判据取
  `route -n get 1.1.1.1` 那一跳(**不是 `default`** —— split-default 下 `default` 仍指物理网关)。
  接口名分类是**白名单式**:认得出是隧道→有,认得出是物理→没有,**认不出→不知道**;把物理网卡
  误判成隧道 = 把裸奔的机器说成受保护,是最坏的一种错。隧道前缀表**全仓只有 describe.go 一份**。
- **规则单边**:不知道有没有隧道就**不许判 ok**,但仍可判 bad。没有隧道时 WebRTC 两半一致
  **不是好消息**(那是「你的真实 IP 和你的真实 IP 一致」)。
- **TunnelVision 判据的形状由真机决定**:项目所有者机器上有 106 条公网 `/32` 经物理网关(bx 自己的
  server bypass)。**单主机不报,更宽的公网前缀才报** —— 后者才是攻击要的(目的是截流量不是一台
  主机)。reject/blackhole(标志位 R/B)不算逃逸,bx 自己的屏障就是一组 `/2` reject。CGNAT 豁免
  必须是「**落在** CGNAT 里」不是 `Overlaps` —— 后者会把 `64.0.0.0/2` 这种攻击形状静默放行。
- **bx 自己的 UDP 分流会长得和泄漏一样**:`udp.transport` 指向另一台服务器时 srflx ≠ HTTP 出口
  而零泄漏。判 `NotChecked` 而**不是 ok** —— bx 不知道那台 UDP 服务器的出口,判 ok 是把假阳性换成
  **假阴性**。`udp.mode=direct-realtime` 仍判 bad(真的以真实 IP 直连),只是点名是配置选的。
  豁免**只对 OwnerBX 生效**,拿 bx 的配置解释别人的 VPN 是张冠李戴。
- **国旗只在能证明时给**:不带 geoip,只能靠内嵌 china CIDR 证明「在不在中国大陆」;anycast
  (1.1.1.1/8.8.8.8)让「国家」这个问题本身没有答案。判不出时返回 nil 判据而非恒 false。
- **指纹那条问「有没有在防」,不问「指纹是什么」**:唯一性没有语料库就没有分母,编一个百分比
  比不报更糟。判据是同一次会话画两遍 canvas 是否相同。

**探测名是一条跨语言契约,而它一度只由一句假话「守着」(2026-08-24 补上)**:
`leakcheck.Probe*` 常量与页面里 `probeLanded("srflx", …)` / `fetchEcho(…, "exit_v4")`
那几个**手抄字面量**必须一致。`outline.go` 头上原本写着「两边用同一组常量,免得页面
自己抄一份」—— 而 `pageData` 里根本没有这几个常量,页面确实自己抄了一份。漂移的后果
是静默的:`skeleton()` 按 Go 那份建 `cells`,`probeLanded` 按页面那份查表,对不上时
`(cells[name] || []).forEach` 什么也不做,那一格**永远停在「还在等」**,而两侧测试都绿。
现由 `leakserve.TestPageProbeNamesMatchTheGoConstants` **双向**钉住(页面用的每个名字
都是真常量 + 每个常量都在页面里被用到),三条变异各咬中一个方向。

**端点是用户可见契约**(页面联网前原样显示),换之前过三关守卫:常量钉死 / https / **不在 china
直连列表**。**这个坑踩过三次**:`ifconfig.me`、`api.ipify.org`(文档自己推荐错的)、以及本轮候选里
的 `ipapi.co` 与 `ifconfig.co`(选之前用生产那份 `route.DomainSet` 逐个比出来的)。现用
`ipv4/ipv6.icanhazip.com` + `stun.cloudflare.com` + `www.cloudflare.com/cdn-cgi/trace`(一次请求
同时给出口 IP 与国家)。
**三关 2026-08-24 逐条实测确认都在,而且都不靠记忆**:`TestEndpointsArePinned`(常量
逐个钉死)、同文件里那段 scheme 断言(v4/v6/trace 必须 https —— 明文回声在路上可被
改写,判据就整个失效)、`TestEchoEndpointsAreNotOnTheChinaDirectList`(拿**真实内嵌
列表 + 生产 `DomainSet`**,并带一条「列表里确实有东西能命中」的自检防假绿)。
`internal/cli` 那半由 `TestPublicIPProbeDomainsAreNotChinaDirect` 同法守住。
**同一次复核修掉一条更坏的注释**:`endpoints_test.go` 里写着「同一个坑**今天还活在**
`internal/cli` 的 `collectNetworkProbe` 里」—— 那个说法已经过期(现用 `icanhazip.com` /
`ipinfo.io`)。**一条声称某个 bug 仍然活着的注释,比一条普通的陈旧注释更坏**:它会派
下一个人去修一个不存在的东西,或者让他连带不再相信旁边那些还成立的话。

**检测结果不留存**,页面与 CLI 都明说。

**第四段从 2026-09-14 起真的在跑**(此前 `ProbeReach` 零生产调用方,四条结论在真机上
恒为「这一轮没有检查」而两个包全绿)。接线是 `internal/cli` 的 `collectLeakCheckFacts`
→ `leakserve.CollectReach`,**单独一跳、单独预算**:探测是四次跨洋 TLS 握手,塞进
`CollectFacts` 那 5 秒预算里会被掐断,而掐断的结果与「这条路真的不通」在屏幕上一模一样;
预算由**探测数**派生(`reachBudget`),不写死秒数。**它给 `bx leakcheck` 新增了出站**:
跑一次会从本机 GET 四个 AI 端点(走当前路径,不绕隧道),故 `announceReachTargets` 在
**第一个请求之前**把地址原样列出来(`--json` 走 stderr)—— 页面那份披露只管浏览器那半,
这一半页面一个字节都不经手。bypass 那条路仍关着(`DefaultProbeBypass=false`,spec §5.1),
**而且没有绑物理网卡的拨号器**:只翻常量不供 `BypassDial`,这一轮会安静地什么都不多跑;
spec §5 那三句比较结论(「直连不行、走当前隧道行」…)也等那一天,不是忘了。
第四段在 CLI 与页面**各有自己的标题**,摘要**另起一行**报五态、**为零也打印** ——
`default`/`|| titles.path` 兜底吞掉新分段在这一支里出现过三次,每一次的后果都是把
「连不上」画在一个写着「你的流量去哪儿」的标题底下,读起来就是一次泄漏。

**`ReachState` 是五态,零值 `ReachUndetermined`(问过没问出来)**:`Reachable`/
`Refused`/`Unreachable`/**`Challenged`**。**`Challenged` 与 `Undetermined` 必须分开
(review 抓到的核心判据)**:CF 人机挑战有真凭据(特征串)能主动否掉坏消息「不是你的
出口有问题」,而「认不出」没有凭据、不许替它猜同一句话 —— 合成一态就必然有一半在
撒谎:一个真实的地区封禁页(HTML、不含那几个构造的关键词)会落进 undetermined,若
借用 Challenged 那句话,用户读到的是「不是你的出口有问题」,而这个功能存在的唯一
理由就是回答这个问题。**`JudgeReach` 同时看状态码与 body**:
`generativelanguage.googleapis.com` 的 403 是 API 在正常应答(JSON body),
`chatgpt.com` 的 403 是人机挑战(CF 特征串)—— 同一个码,相反的两件事,只看状态码
会判反。

**端点四个**(anthropic API `/v1/messages`、claude.ai **favicon 路径**、openai API
`/v1/models`、google AI API `/v1beta/models`),**`chatgpt.com` 刻意不在清单里**:
它实测恒为 CF 挑战页,会变成一行永远给不出答案的噪声。**claude.ai 用 favicon 而非
首页**:首页是 403 CF 挑战,favicon 路径不挂防护。

**措辞纪律**:可达只说「bx can reach X」,**绝不说「你可以用 X」**—— 地区限制可能
在登录/调用层,本期只观测到了边缘,bx 无权替对方的产品说话;不可达只说 bx 观测到
什么,**不断言对方服务的状态**(与 `core_tunnel_unreachable` 同一条纪律)—— 本机
自己没网时同样拨不通。

**`DefaultProbeBypass=false`,取舍写在这**:绕过隧道那条路径会从物理网卡直接发
4 个 GET、**暴露真实 IP 给 Anthropic/OpenAI/Google**——是 leakcheck 今天没有的
新行为,决定留给项目所有者;守卫把「翻常量」与「供 `BypassDial`」绑在一起,只翻
常量会安静地什么都不多跑。

**已知边界**:favicon 200 只证明**边缘可达**,不证明能登录能用;`Refused` 的关键词
**没有真机样本、是构造的**;CF 的防护会变(今天 favicon 不挂,明天可能挂),端点
常量带 `ExpectedSignal` 记档,行为变了守卫会红一次。

**以上五段(五态/端点/措辞/bypass 取舍/已知边界)整段真机未验**——下面那条
2026-08-31 的「真机首验」在 reach 接线之前跑的,它的「仍未验」清单里没有 reach。
逐条验收清单见 `docs/acceptance-pending.md` 的 A8。

**真机首验(2026-08-31,项目所有者的 Mac):本机那一半全绿** —— 十条结论一条不少、
三段分段正确、**三个计数并排且绝不合成**(`0 leak(s) / 0 identifying trait(s) /
6 not checked`,没有出现「没有发现泄漏」那句最坏的假话)、`WhoOwnsTheRoute` 判对
(「carried by bx (utun12)」)、TunnelVision 判据形状对(7 条单主机路由走隧道外,
如实说明而**不报逃逸**)。**逐条结果见 `docs/lessons/leakcheck-page-js-gate.md`。**

**仍未验(三项,别读成已验)**:① **浏览器那半**——要人点一下按钮才产生数据,
点下去会向 icanhazip/cloudflare/STUN 发真实探测;② **非 root 门槛**
(`guardLeakCheckPrivileges`)当时没有 sudo 口令,没实跑;③ `--json` 输出。

**页面那半的 JS 有闸门,判据仍全在 Go 里。** `page.html` 用
`==== BX-PURE-BEGIN/END ====` 划出一段**纯解析**(只做「字符串 → 结构」),
`scripts/test-page-js.sh` 把它原样抽出来交给 node 跑断言,挂进 `verify.sh`(没有 node
时显式报 SKIPPED,不安静通过)。覆盖整页最承重的两处:ICE candidate → srflx/host 地址、
`/cdn-cgi/trace` 的 body —— **它们解析错了不会报错,只会让结论悄悄变成「没检查」或者
一个错的 IP,而对一个泄漏检测工具,静默的假阴性是最坏的一种失效。**
Go 侧三条守卫钉住 node 自己证明不了的事:区段是纯的、**区段里定义的每个函数页面都真的
在调**(一个没人调用而测试盖着的纯函数,与没有测试在输出上完全一样)、闸门真的接进了
`verify.sh` 且连收尾横幅一起查。**四个探针的极性全部收进纯区段**,守卫是「**每一处都是**」
而不是「至少有一处」—— 前者在三处内联表达式旁边照样绿,而那三处恰恰没有任何测试盯着。
接线守卫 `TestPageJSNeverAssertsThatAProbeLanded` 的判据**刻意不对称**:第二个实参写
字面量 `true` 一律禁(那是没看答案就宣布探针落地),字面量 `false` **允许**(只出现在
`.catch` 里,什么都没到达)。**逐条经过(闸门装上第二天抓到的那个真 bug、五条变异、
我自己那两条守卫各有一个 bug)见 `docs/lessons/leakcheck-page-js-gate.md`。**

