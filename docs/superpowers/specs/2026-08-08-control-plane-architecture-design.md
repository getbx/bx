# bx 控制面架构设计

## 诊断

2026-08-08 一整轮开发(菜单栏重做、升级流程、hosts 覆盖)结束后统计:

| | 本轮 bug |
|---|---|
| 数据面(TUN → engine → dialer → route → dns → tunnel) | **0** |
| 控制面(supervisor 编排 / Guardian / CLI / 菜单 / 安装升级) | **全部** |

数据面的平台抽象是好的——接口按「意图」定义(`DirectDialer()` 而非 `SetSOMark`),
三平台各一文件,core 不动。**本设计不碰数据面。**

问题集中在控制面,且是同一个性质。数出来:

```
持久化状态 6 份:core-process.json · guardian-state.json · dns-original.json
                upgrade-intent.json · .brook-version · .singbox-version
                (未计 launchd 的 job 状态、内核路由、networksetup 的 DNS)

改系统状态的包:  internal/cli        7 个文件   ←
                internal/supervisor 7 个文件
                internal/install    2 个文件
                internal/guardian   1 个文件   ←

菜单取状态:      spawn 子进程 9 处,其中包括用 `bx logs --help` 做能力探测
```

### 一句话

**CLI 不是客户端,是第二个控制面;菜单是第三个。**

`bx up`/`down`/`app-install` 自己跑 networksetup、bootout launchd、写状态文件、做拆除;
Guardian 也做这些。**同一个系统有两个平等的改动者,靠约定协调。** 菜单则每几秒 spawn
两个进程、解析 stringly-typed 输出、维护自己的八态状态机、用 AppleScript 提权做事。

### 这解释了本轮的每一类 bug

| 现象 | 根因 |
|---|---|
| 「升级欠条该谁清」纠缠三轮 | CLI 和 Guardian **都合法地**是「关掉保护」的那个人 |
| 菜单栏那一期 bug 最多 | 一个 UI 在重新实现状态推导 |
| 装了新版但跑的还是旧版 | 文件由 CLI 换、进程由 launchd 管、**没人负责让两者一致** |
| status 显绿而实际不对(反复) | status 是**记账的公布**,不是**观测的推导** |
| 孤儿屏障 / 孤儿启动标记 / DNS 残留 / 升级欠条 | 全是边沿触发:某步半失败就留下没人修的状态 |

最后一行是关键:`forcedMacOSTeardown`、进程扫描、`upgrade-intent.json`、
`RemoveBlockingBarrierRoutes`——**每一个都是手写的补偿逻辑,补的是同一件缺失的东西:
没有调谐循环。**

### `internal/observe` 是对的,但装错了位置

它是本项目自己发现的那条洞见——「不信记账,去问系统」。但它被**并排**装在信念旁边
(`bx status --json` 同时发 believed 与 observed,由用户自己 diff),而不是取代它。

在调谐架构里,**observed 就是 status**,不需要两份。

## 参照:成熟产品的共同性质

| 产品 | 关键性质 |
|---|---|
| **Tailscale** | `tailscaled` 独占全部状态,`ipn.State` 一个状态机;CLI 与 GUI 都是 LocalAPI 瘦客户端。`tailscale down` 是一次 RPC,**自己不碰网络**;GUI 从不 spawn CLI |
| **systemd** | unit 声明式,systemd 独占运行期状态,`systemctl` 是瘦客户端 |
| **Kubernetes** | 声明期望 → controller 调谐 → **status 由观测导出,从不由「我记得做过什么」导出** |
| **wg-quick** | 近乎无状态:配置 → 内核,`down` 从同一份配置反向执行。没有记账,也就没有记账错 |

共同点两条:**状态只有一个主人;status 是推导出来的,不是记住的。** bx 两条都违反。

## 边界:哪些复杂是必要的

不能把话说过头。以下不是设计缺陷,是环境强加的:

- macOS **强制**三个进程(root daemon + GUI app + CLI),launchd 必然拥有一份进程状态。
- **强制拆除必须在 Guardian 已死时可用**——所以 CLI 侧保留一条改系统的能力是**本质需求**。
  这条是 2026-08-04 事故(用户 71 分钟关不掉保护)换来的,不可退让。
- 安装与卸载运行时 daemon 可能根本不存在,同理必须留在 CLI。

## 目标架构

一条能说清的线:

> **生命周期(up / down / status / reconnect)归 daemon,CLI 与菜单是瘦客户端。**
> **安装 / 卸载 / 强制拆除留在 CLI——因为它们必须在 daemon 不存在时也能工作。**

三条不变量必须活着(它们各自都是事故换来的):

1. **fail-closed**:隧道不健康 → Block,绝不降级直连。
2. **停止永不依赖别的先成功**:强制拆除是显式的、被文档化的例外,不是 `down` 失败分支里的隐藏行为。
3. **菜单必须一直存在**,且任何状态下都有退出入口。

## 迁移:三步,每步单独可发布可验证

### 第一步:菜单变瘦客户端

菜单停止 spawn `bx status --json` 等六种调用,改为直连 Guardian socket 取结构化状态;
删掉菜单侧的八态推导,渲染 daemon 发布的东西。

Guardian 需要补的端点(现有 7 个:status/up/down/migrate/update/recoveries×2):

- 版本(菜单现在跑 `bx --version`,两处)
- 诊断摘要(现在跑 `bx doctor --json --skip-probe`)
- 更新检查(现在跑 `bx update --check --json`)
- **删掉 `bx logs --help` 那个能力探测**——UI 靠解析帮助文本判断 CLI 支不支持某 flag,
  是本诊断最直白的症状。能力应当由 daemon 在 status 里声明。

**为什么先做这步**:不动 Guardian 的行为,风险最低;直接消掉菜单那一整类 bug 的土壤;
并且能在投入更大之前验证这条路走得通。

### 第二步:`bx up` / `down` 变成纯 RPC

把 networksetup / launchctl / 路由操作从 `internal/cli/guardian.go` 收回 Guardian,
CLI 只剩发请求 + 渲染。

**逃生口显式保留并改名**——`bx force-teardown`,让「这是例外」有自己的名字。

> **2026-08-09 更正:本节原先写「今天 `down` 会在干净路径失败时**静默**落到强制拆除,
> 用户看到的措辞与实际做的事不完全一致」——**这是错的,已实测**。`macOSDownAction`
> (`internal/cli/guardian.go:611-624`)会打印「已强制停止 bx」、一行 ⚠️ 点名原因、
> 逐条列出实际做过的动作,并明确拒绝断言「网络已还原」。回落是响亮的。
>
> 阶段②真正要修的是另一件事:**`bx down` 的干净路径会先安装并启动 Guardian**
> (`cleanGuardianDown` → `ensureGuardianOwnership`,写 plist + `launchctl bootstrap`,
> **然后**才发 `POST /v1/down`)——「停止永不依赖别的先成功」的正面违反。
> 且 `down` 的自动回落**不该删除**:用户在最需要关掉保护的时候,恰恰最不可能知道该换个命令。
> 详见 `2026-08-09-stage2-pure-rpc-design.md`。

### 第三步:调谐循环

desired 与 observed 持续比对并收敛,status 直接发布 observed。

做到这一步,以下手写补偿可以逐个删除——它们会变成同一个循环的普通输入:

- `upgrade-intent.json`(升级欠条)——desired 始终为 on,调谐器在换完二进制后自然拉起
- 孤儿屏障路由清理
- 孤儿启动标记 / 进程扫描
- DNS 残留检查
- `bx status` 的 believed/observed 并列 + divergence

## 完成后应当消失的东西

这是本设计成功与否的可检验判据,不是感觉:

- `internal/cli` 中改系统状态的文件:7 → 3(仅安装/卸载/强制拆除)
- 持久化状态:6 份 → 2 份(desired + 安装元数据)
- 菜单 spawn 子进程:9 处 → 0
- 手写恢复路径:5 条 → 1 个调谐循环

## 风险

**这是系统里最危险的部分。** 控制面出错的两种后果是「流量明文泄漏」和「整机断网」,
而后者会让用户连重装包都下不了。因此:

- 每一步必须**单独可发布、单独真机验证**,不允许三步一起上。
- 三条不变量(fail-closed / 停止永不依赖 / 菜单常在)在每一步的验收清单里都要复验,
  而不是只在最后验一次。
- 第二步和第三步各自应有独立的 spec 与 plan;本文档只定目标与边界。

**现有改动尚未真机验证。** 菜单栏重做、升级流程、hosts 覆盖三批改动到 2026-08-08 为止
只有单元测试与源码守卫背书。第一步会替换掉其中大部分菜单代码,风险被改动本身吸收;
但升级与 hosts 仍是未验的,真机验收时要记得它们与架构改动是**两个独立的失败来源**。

## 本设计不做

- **不碰数据面。** TUN/engine/dialer/route/dns/fakeip/tunnel 一行不动。
- **不改 fail-closed 语义。**
- **不动 Linux/Windows 的现有形态**——它们没有菜单 App,CLI 直接跑 supervisor,
  本诊断描述的三控制面问题在那里不成立。
