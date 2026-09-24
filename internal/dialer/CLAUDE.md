# CLAUDE.md — internal/dialer(分流决策)与 `bx explain`

本文件只在读到 `internal/dialer/` 下的文件时加载。2026-09-23 从根目录下沉,原文逐字存档在
`docs/lessons/explain-archive.md`。`bx explain` 横跨 `internal/cli/explain.go`、本包的
`explain.go`、`internal/pathview`(本机视角)、`internal/dialfail`(失败分类)——**改后三处时
这份不会自动加载,先读它。** 按应用看分流的记账(`AppRecorder`/`appTrackedConn`)判据在
`internal/supervisor/CLAUDE.md`。

## 嗅出的 SNI 不许压过真 IP 的规则(2026-09-05,修复真机已验)

`dialInner` 对 fake-IP 反查不中的连接从首包嗅 SNI/Host,按域名判。**嗅出的域名一条规则都没中时,
由那个真 IP 说了算**(`ExplainIP`),且直连拨的就是这个 IP、不把 SNI 再解析一遍(会解析到别处);
域名规则**命中**时仍由域名说了算(更具体);fake-IP 那条路不受影响(那个 IP 是假的)。真机上
`bx direct add <IP>` 之后 explain 答 DIRECT 而 tailscaled 的 TLS 照样经隧道出去,就是 IP 规则
从头到尾没被问过。HTTP Host 里的 IP 字面量不当域名。**测试要带 fake 池**:嗅探只在
`d.Fake != nil` 时发生,`newTestDialer(nil, …)` 走不到那一支(`sniff_realip_test.go`)。

## `bx explain <目标>`:判定的外部出口

动机是三次病历(Steam 图片全裂、腾讯会议绕一圈、`*.qq.com` 57% 失败),bx 都握着答案而没人问得到。
- **问活着的 Core,不在 CLI 里重建 Router**:bx 不热重载,盘上配置可能已和跑着的不同;用户问的是
  「bx 会做什么」不是「应该做什么」。Core 不在就如实说,**不退回配置推断**。
- **合成不重写一遍**:`(*Dialer).Explain` 是同一个 Dialer 上的只读兄弟方法,读同一个 Router、
  Transport、`Killswitch`/`UDPMode`,调**同一个** `killswitchBlocks`。分支结构仍是分别写的两份,
  由 `explain_drift_test.go` 挡住(9 目标 × 健康/不健康 × kill-switch × 三种 udp.mode = 108 组,
  断言 Explain 预言的结局与 `dialInner` 真做的一致)。它用 `Dial`(无首包),**盖不到嗅探那一类**。
- **Explain 绝不许有副作用**:`udpRuleOverride` 不命中时会计反事实计数,故剥出纯判据
  `udpRuleOverrideDecision`(`TestExplainRecordsNothing`)。
- **`route.Explain` 从不解析**(不拿可能被污染的国内 DNS 做 geoip),所以没有 `--resolve`;
  `dialInner` 里 `dec == route.NeedResolve` 那一支今天不可达。
- **失败分类**(`internal/dialfail`,叶子包,dialer 与 stats 之间唯一按字面对齐的东西):一个百分比
  答不出该不该管 —— 全是 `unreachable` 要立刻查路由,全是 `timeout` 一个字都不用改。顺序是判据的
  一部分(DNS 排在超时之前);nil 返回空串不是 Other;`canceled` 只给名字、不改计入与否。
- **数字要说清它在说什么**:`Rule == ""` 那一档是**一个桶的合计**,不是这个目标的(真机上两个
  无关目标拿到逐字相同的数),要加一句话归位;命中具体规则时不加。「累计」必须说清覆盖多长、
  跨几个版本、溢出过没有。具名规则**本次 0 次**要明说「bx 没见过走这条规则的连接」。
- **`--json` 只追加键**(`machine`、`tcp_rule_findings`/`udp_rule_findings`),顶层既有字段一个不动
  (MCP 的 `bx_explain` 直接转发)。

**两句判决(2026-09-14,真机未验)**:
- **`Blame` 那一行**:`dialfail.Blame` 四态,**零值 `BlameUndetermined`**(两态时 `Other` 与
  `Timeout` 都返回 false,「判不出」被渲染成「不是 bx 的问题」);新类别忘了分类由 AST 穷举守卫红。
  **`dialfail.Dominant` 要严格多数**(40/30/30 挑一个是编答案,5/5 是两句相反的话)。选样本的判据
  是「哪份答得出这个问题」不是「哪份有失败」,读的是哪份要印出来。指向对端的那一档**绝不断言
  对方状态**;渲染的话里**没有 markdown、没有反引号**。
- **`Review` 那一行**(这条规则该不该留):explain 是预言不是观测,五类都够得着。**按 kind 分开查**、
  **全部结论都说**、没有用户规则可点名时一个都不给、`CoveredBy` 只在真有时拼。体检对着
  `RuntimeState.ConfigPath` 算;**只有权限不足**才退到 Guardian 的 `/v1/rules`(配置不存在是真问题,
  不许盖住),且要比对 `config_path`。**任何一步问不出来 ⇒ 一个字都不说**,绝不渲染成「这条规则
  没问题」。

## 本机视角(`internal/pathview`,2026-09-05,真机已跑)

explain **先**答「这个目标在这台机器上会怎么走」,再答 Core 那半;Core 连不上不再是错误,只留一句
「bx 没在跑,以上是没有 bx 时的样子」。`pathview` 是纯判据(`purity_test.go`),事实采集在 cli
(`collectPathFacts`,全部只读:一次系统解析、`LookupRoute`、`LookupBoundRoute`(darwin `-ifscope` /
linux `oif`,windows 没问)、`PhysicalDefaultRoute`、一次 Core 运行时读取)。目标分九类,接口归属
**白名单式**(bx 的 TUN / 别的隧道 `leakcheck.IsTunnelInterface` / 物理 / 认不出就说认不出)。
**与 observe 的分工**:observe 是仪表盘(无参、固定几项、只报异常),explain 是听诊器(你指哪听哪)。
两条措辞是真机逼出来的:假 IP 目标要告诉**绑网卡的程序**「拿到的是假 IP、从物理网卡发出去石沉大海,
把域名进 `dns.fakeip_filter`/hosts 或直接写 IP」;CGNAT 目标不带绑网卡那一句。**普通进程走 bx 的
TUN、绑了网卡的进程走 en0** —— 看到 `8.8.8.8 → utun9` 就断定 bx 吞了 Tailscale 是误判。

**真机状态**:`bx explain` 本身已验;失败分类与累计口径、两句判决、`bx_explain` MCP 工具未验。
升级后第一件事 `bx explain qq.com`:`route unreachable` 是 2026-08-13 那个故障的签名,要立刻查;
`peer did not answer` 一个字都不用改。
