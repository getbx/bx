# CLAUDE.md — 规则体检(internal/rulereview)

本文件只在读到 `internal/rulereview/` 下的文件时加载。2026-09-23 从根目录下沉,原文逐字存档在
`docs/lessons/rules-archive.md`。**消费方在别的包里**:`internal/cli/rulereview.go`
(`bx doctor`)、`internal/supervisor/riskyrules.go`(`bx status` 常驻告警)、
`internal/rulereviewsrc`(Guardian `/v1/rules` 用的组装)、`internal/cli` 的 explain、
菜单规则窗口。**改这些消费方时这份不会自动加载 —— 先读它。**

**判据只有一份**:`rulereview.Review` 是纯函数(`purity_test.go` 按 AST 禁 net/os/exec),
给定 `direct`/`proxy` 两张表 + `Global` + china `DomainSet` 产出分类结论。**但 `Input` 的组装
不止一份**:cli 那份摊平两张表并接内建 china 列表;supervisor 那份只摊平 `Direct`(`bx status`
只要危险那一类,够用);Guardian 那份经 `rulereviewsrc` 用 **Core 实际在用的** china 列表,
所以它能给完整四类、客户端自己算只能给三类。

## 分类(各自计数,永远不合成总数 —— `Report` 没有 `TotalCount`)

- `ClassRisky`(公有云/开放子域直连,去匿名化风险)—— **零值**(漏分类是多报)。判据来自
  `policy.DirectRisk`,它是 `policy.DirectRuleHazard` 的薄壳(见根目录「加规则的风险门」)。
- `ClassShadowedByUserRule`:被自己同表更宽的一条盖住,模式无关。
- `ClassOverriddenByOppositeKind`:被**另一张表**更宽的一条压住,**从来没生效过** ——
  `route.Router` 先查 proxy 再查 direct,没有「更具体优先」。由
  `internal/supervisor/ruleprecedence_test.go` 用**真 Router** 背书,判定分歧会被当场抓到。
- `ClassShadowedByBuiltinList`:被内建 china 列表覆盖,**依赖 mode**。
- 死规则(见下),`DeadCount` 与以上并列。

**两个 mode 陷阱,都是真机撞出来的**:
- **门读 `cfg.Global`,不是 `cfg.Mode`**(后者只有 `host|router`)。global 下 china 列表整个
  不生效,曾把 22 条正在工作的规则报成「被覆盖」,照着删会让 22 个域名改走隧道。**只压制
  `ClassShadowedByBuiltinList` 这一类**,另三类模式无关。「没查」与「查了没有」分开
  (`BuiltinListChecked` 刻意无 `omitempty` + `BuiltinSkipReason`)—— global 下报「0 条冗余」
  是一句自洽的假话。
- **proxy 规则命中 china 列表不是冗余,是生效中的例外**(把流量拉回隧道),两支措辞刻意相反;
  说反了就是叫用户删掉一条正在工作的规则。

**「这条规则危不危险」与「这条规则生不生效」是两个独立的问题**:`ClassRisky` 独立判,填不填
`Proxy` 这条告警都一样(`TestRiskyClassIsJudgedIndependentlyOfWhetherTheRuleEverFires`)。
所以一条本身永不生效的危险规则仍会发常驻告警 —— 方向是过度告警,**刻意接受**(漏报的代价是
真实 IP 暴露)。

**文案是英文、一处产地**(2026-09-17 起;菜单直接渲染 `summary`)。「每一类说一句属于自己的话」
由 `TestEveryClassSaysSomethingOfItsOwn` 穷举 Class、走生产 `Review` 钉住。`Class` 有
`UnmarshalJSON`:认不出的词**不报错**且落到 `ClassRisky`。

## 渲染(静默丢弃在这一支出现过四次)

- **按 Class 字面枚举的渲染层,新加一类只会静默消失**:
  `TestEveryRuleReviewClassHasARenderingPath` 穷举 Class,并带一条前置断言确认那张表覆盖到
  **最后一个** Class。菜单那五个字面量由 `TestMacMenuRuleClassLiteralsMatchTheGoClassNames`
  双向钉住。
- **多条同类结论合并成一条 check**,不许打多条同名 JSON check(名字是稳定查找键,按名字取的
  消费方会丢掉其余);hint 必须是真敲得动的命令(`bx direct rm`,不是 `remove`)。
- **`CoveredBy` 只在真有时才拼**(死规则没有「被谁盖住」,曾渲染出 `*.a ← 、*.b ← `)——
  **断言「说了什么」与「说得像句人话」是两件事。**
- **`bx status` 只发危险那一类,severity=warn**(冗余与失效对成熟配置恒不为零,放进常驻面板
  会变墙纸;`warn` 不把总状态降成 Needs Attention)。它在 `Run()` 里算一次传进去,不在读状态
  的路上重算(配置运行期不变);接线的缝是 `newStatusReporter`(守卫不许在测试里自己重造一遍
  生产的合并表达式)。

## 死规则:「这条从来没命中过」(2026-08-24,真机未验)

**唯一一条要跨重启才敢说的话**;判错的后果是用户删掉一条天天在工作的规则。三道门槛,
**任何一道问不出来就整类不判**。门槛值 **14 天 / 20,000 次判定**,没有真机依据支撑「够不够」。

- **门槛一是「Core 累计在跑的时长」,不是「距首次见到过了多久」**(后者把 Core 没跑的时间算进
  分母,等于悄悄降低门槛)。
- **门槛二(全局判定数)含内建列表命中**,否则流量几乎全走内建列表的机器永远达不到门槛。
  内建那条(`Rule == ""`)**存、参与门槛、永不产出 finding**,也**不是孤儿**(剪掉它门槛永远
  达不到)。跟踪表满了也继续计数。
- **`Overflowed` 是粘性的**:表满过之后,「没被记过」与「记了、从没命中」长得一样 ⇒ **溢出即
  整类标「没查」**,不是提高上限(没有真机数据说明有人接近过 256)。
- **写盘用 delta 不是总量**(否则累计随写盘频率虚高);**写失败不推进基线**;**总量回退按重置
  处理取当前值,绝不产出负数**(负值一旦落盘就纠正不回来,门槛永远达不到)。
- **配置读不出来时退回启动快照,不喂空表**(`pruneRuleHistory` 对空表会剪光所有条目 —— 瞬时
  故障变永久数据丢失)。
- **周期写 5 分钟 + 关闭时再写一次**(launchd 会 SIGKILL Core);收尾那次必须有人等,但**等待
  有 2 秒上限**(停止路径不许变慢)。
- **比对按归一化形式,并按 Source 把 direct/proxy 分开查**(同一条原文可同时在两张表里、语义
  相反)。
- **累计与本次运行的计数并列发布,绝不合并**(`Report.RuleHistory` 与 `Snapshot.Rules`)。
- **doctor 经控制 socket 拿历史,不读文件**(`/var/lib/bx` 是 `drwx------`),与 `bx status`
  用同一个 `FetchStatusReport`。历史跨了几个版本要报出来,让用户对累计打折。
- **头一次装上后这一类必然显示「没查」,那是对的。** 真机要盯的只有一件事:
  `rule_history.uptime_seconds` 与 `decisions` 是否**跨重启继续涨**。

## 仍未做

「缺失规则」(第 4 条)没做。
