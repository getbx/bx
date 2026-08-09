# 阶段②:`bx up`/`down` 变纯 RPC + 逃生口显式命名

## 先更正架构文档里的一个错误前提

`2026-08-08-control-plane-architecture-design.md` 说:

> 今天 `down` 会在干净路径失败时**静默**落到强制拆除,用户看到的措辞与实际做的事不完全一致

**这是错的,已实测。** `macOSDownAction`(`internal/cli/guardian.go:611-624`)在强制路径上会打印
`已强制停止 bx`、一行 `⚠️` 点名原因(或「Guardian 未响应」)、逐条列出实际做过的六个动作,
并**明确拒绝断言「网络已还原」**。回落是响亮的,不是静默的。

那么阶段②真正要解决的是什么?测绘给出了答案,而且比我原先写的更具体。

## 真正的问题:停止路径会去安装和启动

`cleanGuardianDown`(`internal/cli/guardian.go:446-457`)的第一件事是
`ensureGuardianOwnership`(`:447`),而后者会:

- 写 `/Library/LaunchDaemons/com.getbx.bx.guard.plist`(`guardian.go:665`)
- `launchctl bootstrap` + `kickstart` 拉起 Guardian(`guardian.go:690`)

**然后**才发 `POST /v1/down`。

也就是说:**`bx down` 想停下保护,得先成功把 Guardian 装好并启动起来。** 这正面违反
「停止永不依赖别的先成功」——那条不变量是 2026-08-04 那次用户 71 分钟关不掉保护换来的。

今天唯一挡着它的是 `:430` 那个 10 秒 socket 预检:socket 通了才走干净路径。但
**「Guardian 可达」不等于「干净停止可行」**——`Manager.Down` 的第一句是
`if m.recoveryBlocked { return errRecoveryIncomplete }`(`internal/guardian/manager.go:474-476`),
而 `recoveryBlocked` 在启动恢复失败后会**永久为真**。于是:socket 应答、屏障被这次事务装上、
事务永远失败。这条路径今天靠 `:435` 的回落兜住,而回落之前 `ensureGuardianOwnership` 已经跑过了。

## 第二个问题:逃生口没有自己的名字

强制拆除只作为 `down` 的失败分支存在。后果有两个:

1. **用户明知 Guardian 卡死时,没有办法直接要求它。** 只能跑 `bx down`、等 10 秒预检、
   等干净路径失败,再落到强制。
2. **菜单栏被迫经 `bx down` 提权 shell-out** 来拿到逃生能力
   (`cli_test.go:637` 的守卫原话:「逃生路径必须跑提权的 `bx down`,因为它拥有
   `forcedMacOSTeardown`」)。

## 设计

### 一、把安装/启动从停止路径上摘掉

`cleanGuardianDown` 不再调 `ensureGuardianOwnership`。停止只做停止:发 `POST /v1/down`。

Guardian 没装、没起、不应答 —— 那是**回落到强制拆除**的理由,不是「先装好再说」的理由。

**这条改动很小,但它是本阶段的全部要点。** 一条守卫测试钉死:
`cleanGuardianDown` 的调用图里不得出现任何安装/启动动作。今天
`TestMacOSDownDoesNotRequireGuardianBootstrap`(`macos_lifecycle_test.go:414`)
只覆盖了 socket 不可达那一支 —— 它证明不了「可达时也不会去装」。

### 二、`bx force-teardown`:给逃生口一个名字

新增命令,直接执行 `forcedMacOSTeardown` 的六步,**跳过 socket 预检、跳过干净路径**。

**`bx down` 的自动回落保留不动。** 这是与架构文档的一处有意偏离:
文档说要把逃生口从「`down` 的失败分支」里搬出来,但**搬走它会削弱不变量而不是加强**——
用户在最需要关掉保护的时候,恰恰是最不可能知道该换个命令的时候。
所以:`down` 照旧「干净路径失败就强制拆除」,`force-teardown` 只是让同一件事**可以被直接要求**。

命名与文案:
- `bx force-teardown` 打印的内容与 `down` 走强制路径时**逐字一致**(同一个报告函数),
  只是不带「干净路径失败了」那句原因。
- 帮助文本必须说明它做什么、以及**它不保证网络已还原**——照 `down` 现有措辞。

### 三、把 CLI 里的系统探测搬进 Guardian

三处,都属于「CLI 在替 Guardian 做它自己能做的事」:

| 搬什么 | 今天在哪 | 为什么能搬 |
|---|---|---|
| **迁移请求的构造** | `guardian.go:291-352`、`:735-750`(网关发现 + 读 Core `/v0/runtime` + 解析 config + DNS 查询) | Guardian 两个输入都已接好:`GatewayProviderFunc(DiscoverDefaultGateway)`(`daemon.go:369`)与 Core 控制 socket 客户端(`daemon.go:340`)。`/v1/migrate` 自己推导即可,CLI 只发一个空请求 |
| **孤儿 legacy plist 清除** | `guardian.go:682-688` | `Manager` 已持有 `m.legacy` 并在 `/v1/migrate` 里调 `legacy.Remove()`(`manager.go:303`),只是今天要求有网关和活着的 Core |
| **菜单 LaunchAgent bootstrap** | `guardian.go:370-378` + `menu_darwin.go` | Guardian 是 root,`launchctl asuser` 从 root 可用(`menu_darwin.go:198-201`) |

### 四、明确留在 CLI 的东西

**不可迁移**(必须在 Guardian 不存在/已死/正在被替换时可用):

1. `install.WriteGuardianUnit` — 装的就是它自己
2. `install.EnableGuardian` — `launchctl bootstrap`/`kickstart`
3. `install.BootoutGuardian` — 守护进程无法 RPC 停掉自己还继续应答
4. `supervisor.ShutdownControl` — Guardian 不在时停 Core 的唯一确定手段
   (代码里没有 `Setpgid`/`Setsid`,`bootout` 的 SIGTERM 到不了 Core)
5. `guardian.RemoveBlockingBarrierRoutes` — `bootout` 抹掉内存里的所有权记录,
   而内核里的 `/2` reject 会留下;不清它,整机断网
6. `install.DisableDNSContext` — 还原系统 DNS
7. `Store.SaveDesired(Off)` — 否则 `RunAtLoad`+`KeepAlive` 下次开机把坏状态带回来
8. `clearUpgradeIntent`(强制路径上)
9. `bx uninstall`、`bx setup`、`install.SelfInstall`/`UnifiedInstall`

## 本期不做

- **不动 `up` 的 `ensureGuardianOwnership`。** 在 `up` 上「先把 daemon 装好起来」是**正当的**——
  那正是 `up` 的工作。不变量约束的是停止,不是启动。
- **不删 `down` 的自动回落**(理由见上)。
- **不改 `bx dns on/off`。** 它是 Guardian 所拥有状态的第二个无协调改动者
  (`cli.go:4223`、`:4233`),确实该收,但它不在 `up`/`down` 的路径上,单独一期。
- **不改 sudo 语义。** 测绘指出:`up`/`down` 全路径**没有任何 `os.Geteuid()` 检查**,
  而 `/v1/up`、`/v1/down` 在 0666 socket 上接受 owner uid。阶段②之后 `bx down` 变纯 RPC,
  理论上 `sudo` 就不再必需 —— 但那是个产品决定,不是重构的机械后果,本期不碰。
- **不引入调谐循环**(阶段③)。

## 风险

**最大的一条:菜单栏的逃生口指向 `bx down`。**
`cli_test.go:637` 的守卫是对 `privilegedTurnOffScript(bxPath:` 的字符串匹配。本期
`down` 的回落行为不变,故菜单**不需要改**——但任何后续把回落删掉的改动,
必须同一个提交里把菜单指到 `force-teardown`,否则 Guardian 死时菜单会死在 `connect()` 上,
而那正是它需要逃生口的时刻。**这一条要写进 `force-teardown` 的注释里。**

**第二条:强制路径的用户可见报告今天零测试**(`guardian.go:611-621`)。
本期要重排这段代码,而唯一盯着 `macOSDownAction` 的是一条查它有没有调
`forgetUpgradeDebtOnExplicitOff` 的源码文本守卫(`upgraderun_test.go:352-361`)。
**动它之前先补上对报告内容的断言**,否则重构会静默改掉用户在最坏时刻看到的唯一指引。

**第三条:两个写者抢 `guardian-state.json`,而且是故意的。**
CLI 在强制路径上写两次(`guardian.go:475`、`:516`),`Manager` 也写
(`manager.go:316`、`:391`、`:563`)。`guardian.go:471-475` 的注释明说第一次写是为了
**赢一场与 `Manager.handleUnexpectedExit` 的竞速**,第六步再写一次是因为第一次可能输给并发的 `Up`。
没有锁、没有代际号、没有 CAS。本期不修它(那是阶段③的事),但**重排强制路径时不得改变这两次写的相对位置**。

## 测试策略

- **守卫(新)**:`cleanGuardianDown` 的调用图里不得出现 `WriteGuardianUnit`/`EnableGuardian`/
  `ensureGuardianOwnership`。用注入的 fake deps 断言这些 hook 在干净路径上**调用次数为 0**——
  行为断言,不是源码文本匹配。
- **回归**:`macos_lifecycle_test.go` 里那组钉住强制路径**六步精确顺序**的测试
  (`:489` 断言 `desired.off|core.shutdown|guardian.forceTeardown|barrier.clear|dns.restore|desired.off`)
  必须**一个断言都不改**就通过 —— 那是「重排没有改变行为」的证据。
- **补测(先于重构)**:`macOSDownAction` 在强制路径上打印的内容 —— 它今天零覆盖,
  而它是用户在最坏时刻看到的唯一指引。
- **`bx force-teardown`**:与 `down` 的强制路径走同一个实现,断言两者产生**相同的动作序列**。
- **集成台**:阶段②不新增 netns 断言 —— 台子在 Linux 上,而本期改的全是 macOS 生命周期代码。
  这一点要说明白,避免「台子绿了所以安全」的错觉。
