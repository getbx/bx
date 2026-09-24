# bx explain 与 SNI —— 2026-09-23 从根目录 CLAUDE.md 下沉时的原文存档

**这是存档,不是现行判据。** 这些段落的**判据**浓缩进了 `internal/dialer/CLAUDE.md`(那一份才是改代码之前要读的);
这里逐字保留当时的原文,给想知道「当初怎么换来的」的人看。读到与 `internal/dialer/CLAUDE.md` 冲突的地方,以后者为准。

---
## 嗅出的 SNI 不许压过真 IP 的规则(2026-09-05,真机诊断,修复真机已验)

真机(公司工作站,bx global):`bx direct add 192.0.2.185` 之后 `bx explain 192.0.2.185`
答 DIRECT、计数也记在那条规则下,而 tailscaled 到它的 TLS 照样经隧道从 VPS 出去 ——
家里的 derper 记到的源 IP 是 VPS,tcpdump 里 eno1 上一个发往该 IP 的 TCP 包都没有。
机制:`dialInner` 对 fake-IP 反查不中的连接从首包嗅 SNI/Host,按域名判;域名规则全不中
就 `Proxy/SourceDefault`,**IP 规则从头到尾没被问过**;explain 没有首包,按 IP 判,自然说
DIRECT。修法:嗅出的域名一条规则都没中时,由那个**真 IP** 说了算(`ExplainIP`),且直连
拨的就是这个 IP、不把 SNI 再解析一遍(那会解析到 VPS);域名规则**命中**时仍由域名说了算
(更具体);fake-IP 那条路不受影响(那个 IP 是假的,按它判什么都判不出)。顺手:HTTP Host
里的 IP 字面量不再被当成域名嗅出来。**测试要带 fake 池**:嗅探只在 `d.Fake != nil` 时发生,
`newTestDialer(nil, …)` 走不到那一支 —— 第一版测试正因此假绿。既有的 explain 漂移守卫用
`Dial`(无首包),盖不到这一类,新加的四条在 `sniff_realip_test.go`。

## `bx explain` 的本机视角:这个目标在这台机器上会怎么走(2026-09-05,真机已跑)

`bx explain <目标>` 此前只答「进了 bx 的连接会怎样」,Core 没在跑就报错。现在**先**答
本机视角(`internal/pathview`,纯判据,`purity_test.go` 钉住不做 I/O),再答 Core 那半;
Core 连不上不再是错误,只留一句「bx 没在跑,以上是没有 bx 时的样子」。动机是同一天两次
误判:另一会话看到 `8.8.8.8 → utun9` 就断定 bx 吞了 Tailscale,真相是**普通进程走 bx 的
TUN、绑了网卡的进程走 en0**;休眠成环那次,哨兵地址进了 TUN 而发往服务器的 /32 早已不在,
只有指着那个具体地址问才看得见。**与 observe 的分工**:observe 是仪表盘(无参、固定几项、
只报异常),explain 是听诊器(你指哪它听哪),两者共用同一批原语。
事实采集在 cli(`collectPathFacts`,全部只读:一次系统解析、`LookupRoute`、新导出的
`LookupBoundRoute`(darwin `-ifscope` / linux `oif`,windows 没问)与 `PhysicalDefaultRoute`、
一次 Core 运行时读取);判据在 pathview(`Judge`):目标分九类(假 IP / 回环 / 私网 / CGNAT
/ 链路本地 / 服务器旁路 / 国内 / 公网 / 解析不出),接口归属白名单式(bx 的 TUN / 别的
隧道(`leakcheck.IsTunnelInterface`)/ 物理网卡 / 认不出就说认不出),结论一句 + 证据几行。
**两条措辞是真机逼出来的**:假 IP 目标要告诉绑网卡的程序「拿到的是假 IP、从物理网卡
发出去石沉大海,域名进 `dns.fakeip_filter`/hosts 或直接写 IP」(就是 DERP 域名那次);
CGNAT 目标不带绑网卡那一句(overlay 走自己的隧道,底层那句是噪声)。`--json` 在 Core 应答
上**追加** `machine` 键,顶层字段一个不动(MCP 的 `bx_explain` 直接转发);Core 连不上时
只有 `machine` + `core_unavailable`。这台 Mac 上五类目标(公网 / 服务器旁路 / 假 IP 域名 /
CGNAT / 私网)实跑过,输出与内核一致。

## `bx explain <目标>`:判定第一次有了外部出口(2026-09-01,部分真机已验)

**动机是三次病历,三次 bx 都握着答案、三次都没人问得到**:Steam 图片全裂(用户去
怀疑 bx 和 CDN,真因是一条 direct 规则指向的路完全不通,而 bx 每一次都看见了
`dial direct failed` 然后扔掉)· 腾讯会议绕一圈(查半小时,最后靠 `strings` 捞
域名)· `*.qq.com` 57% 失败。**共同形状是请求级的「为什么」**,而 bx 全部 11 个
只读 MCP 工具、`bx status`、`bx doctor` 答的都是系统级的「状态如何」。

`route.Explain(Meta) → (Decision, Reason)` 每秒执行上万次,**此前只被 dialer.go
调用**。这个功能就是给它开一个出口。

**几条改之前要读的判断**:

- **问活着的 Core,不在 CLI 里重建 Router。** bx 不热重载,盘上的配置可能已经和
  跑着的那个不一样;CLI 自建的答案是「bx **应该**做什么」,而用户问的是「bx
  **会**做什么」—— 两者不同的那一刻恰恰最需要这个命令。Core 没在跑就如实说,
  **不退回配置推断**。
- **合成不重写一遍。** `route.Explain` 只给路由判定,实际结局还要叠 kill-switch
  与 UDP 档。三条路选了第三条:`(*Dialer).Explain` 是**同一个 Dialer 实例**上的
  只读兄弟方法,读同一个 Router 指针、同一批 Transport、同一个 `Killswitch`/
  `UDPMode`,并调用**同一个** `killswitchBlocks` —— 不是第二份判据,是同一个对象
  的另一个问法。(把 `dialInner` 的合成整块抽出来风险大于收益:它和 stats、
  recordApp、真拨号绞在一起,是全产品最热的路径。)
- **分支结构仍是分别写的两份**,由 `explain_drift_test.go` 挡住:9 个目标 ×
  健康/不健康 × kill-switch 开关 × 三种 udp.mode = 108 组,断言 Explain **预言的**
  结局与 `dialInner` **真的做出来的**一致。
- **Explain 绝不许有副作用。** `udpRuleOverride` 在不命中时会调
  `countUDPCounterfactual`,走那条路等于拿一条根本没发生的连接污染反事实计数 ——
  而那份计数正是「让 china 列表也对 UDP 生效」那个搁置选项的依据。故剥出纯判据
  `udpRuleOverrideDecision`,并由 `TestExplainRecordsNothing` 钉住。
- **`route.Explain` 从不解析。** 写 spec 时我判断错了(以为未命中域名规则时会用
  国内 DNS 解析再按 IP 判),动手时被代码证伪:它直接 `return Proxy, SourceDefault`,
  注释写明理由是不拿可能被污染的国内 DNS 做 geoip。**所以 `--resolve` 整个不必
  存在。**(顺带发现 `dialInner` 里 `dec == route.NeedResolve` 那一支**今天不可达**。)

**失败分类(`internal/dialfail`,叶子包)**:一个百分比**答不出该不该管** ——
同样是 15%,全是 `unreachable` 就要立刻去查路由(2026-08-13 那个 DirectDialer 故障
的签名),全是 `timeout` 就一个字都不用改。而 err 一直在手边
(`conn, err := d.Direct.DialContext(...)`),此前进一行 debug 日志然后被扔掉。
类别名下沉叶子包的理由同 `internal/udpsource`:它是 dialer 与 stats 之间唯一按
字面对齐的东西。**顺序是判据的一部分**:DNS 排在超时之前(DNS 超时的可行动信息
是解析器不是对端);**nil 返回空串不是 Other**;`canceled` **只给名字、不改它是否
计入失败** —— 先量再决定,反过来做会让改动前后的累计不可比。

**两处「数字看起来在说 A、实际在说 B」,都是真机首用当场发现的**:
- `Rule == ""` 那一档(默认/内建列表)的计数是**一个桶的合计**,不是这个目标的。
  真机实测 `steamstatic.com` 与 `1.1.1.1` 拿到逐字相同的 49/15228/73。不删那两行
  (整体失败率是有用背景),加一句话把它归位;命中具体规则时**不加** —— 多余的
  免责声明会让一个准确的数字显得可疑。
- **「累计」必须说清覆盖多长时间、跨几个版本、表溢出过没有**。三个字段本来就在
  `RuleHistorySnapshot` 里,第一版全丢了。一个跨半年几个版本的 15% 与一天之内的
  15% 是完全不同的两件事,而读的人会默认它是后者。

### explain 的两句判决(2026-09-14,真机未验)

**分类到处置之间那一跳此前一直留给读的人自己走。** 失败分类从 2026-09-01 就印着
(`[route unreachable×410 peer did not answer×15]`),而「所以该怎么办」写在 `internal/dialfail`
每个常量的注释里、抽成过一个判据 `LooksLikeOurFault`,**零生产调用方** —— 判据
写下来了、测试盖着,从没有一个字到过屏幕上。现在是 `Blame` 那一行(2026-09-14 落地时它叫「判决」,2026-09-17 随整条 explain 改英文)。

- **`dialfail.Blame` 四态取代了那个 bool,零值是 `BlameUndetermined`。** 两态之下
  `Other`(认不出)与 `Timeout`(确知是对端的问题)返回同一个 false,于是
  「我判不出来」被渲染成「不是 bx 的问题」。新加类别忘了分类时由一条 AST 穷举
  守卫红一次 —— 否则它静默落进零值,**而零值读起来像「想过了判不出来」**。
- **`dialfail.Dominant` 的门槛是严格多数,不是「最多的那一类」。** 40/30/30 里挑
  一个说成主因就是编答案;正好一半更不行 —— 5 个路由不可达 + 5 个对端不应答是
  两句处置完全相反的话。没有主因就不说话,那一行的 `[×N ×N ×N]` 拆分本身已经
  说明「它不是一个原因造成的」。
- **选样本的判据是「哪份答得出这个问题」,不是「哪份有失败」。** 累计那份可能有
  8000 次失败却一个分类都没有(旧版本记的、或这一轮还没落盘),按后者会把唯一
  答得出问题的样本整个扔掉,**而输出与「这一版不下判决」逐字节相同**;选中的那份
  说不出主因时也不回落到更小的那份。读的是哪一份必须印出来。
- **指向对端的那一档绝不断言对方的状态**(本机没网时同样表现为「没收到回应」),
  渲染出来的话里**没有 markdown、没有反引号** —— 用户读到的是字面符号,而这个
  仓库刚被反引号坑过一次(文档里反引号包着的命令被 zsh 当成命令替换执行)。

**第二句是 `Review` 行:这条规则该不该留。** `internal/rulereview` 那四类加死规则
整份早就在(`bx doctor`/`bx status` 都在用),而 explain 此前不调它。
**explain 是预言不是观测,所以五类都够得着** —— 它报「会命中哪一条」,于是一条
从来没命中过的死规则照样会被点名。四条判据:**按 kind 分开查**(同一条原文可同时
在两张表里、语义相反)· **全部结论都说不是第一条**(一条规则可以既危险又被盖住,
取第一条会静默丢掉其余安全结论)· **没有用户规则可点名时一个都不给**(内建/默认
那一档没有哪一行是用户写的)· **`CoveredBy` 只在真有时才拼**(死规则没有「被谁
盖住」这回事)。体检对着 **`RuntimeState.ConfigPath`** 算 —— 拿错输入而判据没错
正是 wrong-reference-object 那类事故;两条取数路与 `bx doctor` 逐字同源,
**只有权限不足**才退到 Guardian 的 `/v1/rules`(配置**不存在**是「还没 setup 过」
这个真问题,拿 Guardian 的答案盖住它是掩盖故障),且要比对 config_path 同不同。
**任何一步问不出来 ⇒ 一个字都不说,绝不渲染成「这条规则没问题」。**
两句都同时进 `--json`(`tcp_rule_findings`/`udp_rule_findings`,顶层既有字段
一个没动)—— 只长在文本路径上的诊断正是 `bx doctor` 那次「被一句 0 failed 顶掉」
的形状。

**真机状态**:`bx explain` 本身**已验**(当场答出 `*.qq.com`:命中 `*.qq.com`、
本次 78 次/15 次失败、累计 2726/425)。**失败分类与累计口径未验** —— 升级后第一件
事就是 `bx explain qq.com`,看那些失败是 `route unreachable` 还是 `peer did not answer`:前者是
8-13 那个故障的签名要立刻查,后者一个字都不用改。`bx_explain` MCP 工具也未验
(没有 agent 调过)。设计 `docs/superpowers/specs/2026-09-01-explain-target-design.md`。
