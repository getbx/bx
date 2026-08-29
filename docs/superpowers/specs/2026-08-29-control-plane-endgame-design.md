# bx 控制面终局架构:调谐内核 + 窄生命周期接口

## 这份 spec 与既有几份的关系

`2026-08-08-control-plane-architecture-design.md` 定了诊断与三步走,至今:
阶段①(菜单瘦客户端)已完成、阶段②(up/down 纯 RPC + `force-teardown` 得名)已完成、
阶段③a(只观察的调谐环)已完成,③b(逐项授权执行)未开工。

本 spec 回答的是另一个问题:**③b 之后走到哪里才算「解决」而不是小修小补。**
它定终局形态与到达顺序;每一步仍要有自己的 spec 与 plan,本文不替代它们。

## 诊断的剩余部分(2026-08-29 实测数字)

三步走走完前两步之后,还剩什么?

```
数据面(tun/dialer/route/dns/fakeip/socks5)     ~2,800 行,平台接口 3 个方法
控制面(cli/guardian/supervisor/install/setup/update) ~41,000 行
组装根:supervisor/run.go 1050 行 · guardian/manager.go 1955 行
Guardian 平台耦合:9 个 darwin 文件 + 5 处 internal/install 引用
                + daemon_other.go 一道 requireDaemonPlatform() 焊死的门
```

剩余的结构病只有一条,但它是根:**控制面没有数据面那个形状。**
数据面优雅的原因写在 CLAUDE.md 里——接口按意图定义、只有 3 个方法、加平台不动 core。
控制面则是:Guardian 只在 darwin 跑(launchd 假设经 `install` 包渗入 dns/update/client/
process/daemon 五个文件),Linux 是 systemd 直管 supervisor、没有 Guardian 没有调谐,
Windows 是 SCM 服务 + 托盘自己的 3 秒轮询——**三套生命周期模型**。

这个错位的直接后果,是本仓库反复付的两笔租金:

1. **唯一的集成台在 Linux(netns),而 Guardian 只在 darwin** —— 真 Guardian 从来
   没进过任何集成测试。阶段③a 自己写下的话:「凡本期写『由测试保证』的对循环的
   接线而言都是『由替身保证』」。
2. **不可测的面只能靠守卫硬扛** —— 读源码文本/AST 的守卫被攻破了 20+ 次
   (阶段①8 次、③a 7 次、按应用分流那支 6 次……),每次形状相同:守卫钉住的是
   缺陷旁边的东西。守卫不是写得差,是它们在替一个进不了测试的结构还债。

## 终局(一句话)

> **一个平台无关的调谐内核,独占生命周期与状态;三个薄平台适配器
> (launchd / systemd / SCM);CLI、菜单、托盘是渲染快照的瘦客户端;
> 集成台跑真内核。**

与数据面同构:内核之于 `lifecyclePlatform`,如同 `Run()` 之于 `platform`。
「加一个平台 = 一个 `platform_<os>.go`」这句话,扩展到「加一个平台的生命周期 =
一个 `lifecycle_<os>.go`」。

三条不变量原样活着(每条都是事故换来的,任何一步不许削弱):

1. **fail-closed**:隧道不健康 → Block,绝不降级直连。
2. **停止永不依赖别的先成功**:`force-teardown` 是唯一合法的非 daemon 路径,
   永不收编进调谐器——逃生口的定义就是「Guardian 已死时可用」。
3. **菜单必须一直存在**,任何状态下都有退出入口。

## 五步

依赖关系:第 1、2 步可并行;第 3 步依赖第 2 步;第 4、5 步吃第 2、3 步的红利,
其中第 5 步的 cli 拆包随时可做。

### 第 1 步:调谐环拿到执行权(③b→③c),「改状态」统一成「写意图」

**终态**:`bx up` = 写 `desired=on` + watch 收敛;`bx down` 同理;升级 = 武装
maintenance-hold + 等调谐器停核换文件再收敛。desired 文件是唯一输入,调谐环是
唯一执行者。

**为什么现在能做了**:③b 当初列的两个前置都已交付——真机 soak 有了第一批数据
(2026-08-10 夜 + 2026-08-17),maintenance-hold 解掉了「升级欠条会骗过忠实调谐器」
那个数据模型问题(`2026-08-10-maintenance-hold-design.md`)。

**边界照抄 ③a spec,一条不放**:「装屏障」永不进循环(瞬时网关探测失败会降级成
block-only 整机黑洞,循环每拍重掷骰子);ownership-uncertain 是栅栏不是待收敛差异
(它整个存在的意义就是拒绝);执行权按动作逐项开,每项以 soak 数据为门。

**回报**:五条手写补偿逐个删除——upgrade-intent 迁移残留、Guardian 侧孤儿屏障
清理、启动标记锁存的特判、DNS 残留检查、believed/observed 并列(status 只由
observed + desired 推导)。CLI 侧那份孤儿屏障清理**留在逃生口里**,那是③设计
2026-08-09 更正里已定的:逃生口跑的时候没有循环在跑。

### 第 2 步(杠杆最大):Guardian 拆成「调谐内核 + lifecyclePlatform 窄接口」

**实测耦合面比想象浅**:manager.go/reconcile.go/statuswatch.go/localapi.go 已经
无 build tag,manager.go 的 import 里唯一的项目内依赖是 supervisor。要正规化的是
既有的 darwin/other 文件对(barrier / procscan / process / network_observer /
daemon / peercred)加上 dns.go、update.go、client.go、process.go、daemon.go 里
那 5 处 `internal/install` 引用。

**接口按意图,不按机制**(与数据面 `platform` 同一条纪律),草案:

```go
type lifecyclePlatform interface {
    SpawnCore(...) / StopCore(...)      // launchd job vs systemd unit vs SCM 藏在后面
    ScanRunningCores() (...)            // 准入控制;darwin 已有,移植警告早已记档
    ObserveNetwork() (...)              // 路由/DNS 观测原语(internal/observe 那半)
    ManageDNS(...)                      // networksetup vs resolv.conf vs winipcfg
    Barrier(...)                        // 屏障装拆(darwin 的 /2 reject 那套)
}
```

方法数以抽取时实测为准,原则只有一条:**每个方法是一个意图,不是一次
系统调用的转发**。

**行为保真纪律(这一步的全部风险所在)**:抽取时 darwin 行为一行不改,既有
darwin 测试**原样全绿、一个断言不动**——这是「没削弱保护」的判据,与 `e7e413c`
两段式标记那次「三条既有安全测试原样通过」同一个标准。抽取分两拍:先把缝画出来
(接口 + darwin 实现 = 今天代码的重新摆放),再动任何行为。

**已记档的移植警告在这一步兑现**:`procscan_other.go` 的桩恒 fail-closed,
「谁移植 Guardian 必须先实现 `scanRunningCores`,不能只放开
`requireDaemonPlatform`」——第 3 步做 Linux 时按此执行,Linux 的实现反而简单
(`/proc/<pid>/cmdline`,不需要 darwin 那套 `kern.procargs2` 的权限舞蹈)。

### 第 3 步:Linux 适配器,让集成台跑真 Guardian

**这是整个方案的回报兑现点**:netns 集成台已经能跑真 `supervisor.Run()`
(真 TUN、真策略路由、真屏障),补上 Linux 的 `lifecyclePlatform`(systemd 或
直接子进程管理)之后,台子能跑**真 Guardian 内核 + 真 Core** 的完整控制面——
up/down/升级/崩溃重启/调谐收敛,全部打在内核状态上断言。控制面从「守卫文化
硬扛」转到「集成测试背书」,靠的就是这一步。

**刻意决策:Linux 适配器先只为集成台服务,不动 Linux 产品形态。**
生产 Linux 保持 systemd 直管 supervisor 的现状——那里没有菜单 App,原诊断的
三控制面问题不成立,用户没有痛。等内核在台子上攒够证据、且 Linux 用户真需要
调谐/自愈时,再把产品形态切过去,那是独立的一期。**别让「测试基建」偷偷变成
「改了所有 Linux 用户的部署方式」。**

**顺带的红利**(做完记录,不作为本步验收):Windows 托盘那套 3 秒 spawn 轮询
可以退役,接同一个 status watch;但 Windows 适配器不在本步范围。

### 第 4 步:组装根变成数据

`run.go` 1050 行、`manager.go` 1955 行的问题不是长,是**接线不可断言**——
「逻辑对但接线错”是本仓库全部事故的形状,而两处已验证的解法(`localAPIOptionsFor`、
`newStatusReporter`:把内联组装抽成可测的缝)证明了方向。系统化它:

- 组装根输出一份**接线计划**(纯数据:组件、连接、顺序),极薄的执行器照单执行;
- 测试断言计划本身,不再读源码文本;
- **每抽出一块,对应的 AST/文本守卫退役一条,且按「注释里点名的测试必须存在」
  那条纪律登记接手者**(`retiredTestNames` 机制现成)。退役的守卫数量就是这一步
  的进度条。

第 2 步天然完成 manager.go 的一大半(接口注入本身就是接线计划);本步主要
处理 `run.go` 与 daemon 组装。netns 台子在场,重构的每一拍都有真系统兜底。

### 第 5 步:快照驱动 UI + cli 拆包

**菜单渲染 = `/v1/status` 快照的纯函数。** Guardian 发布自足的声明式快照
(行、极性、可用动作),Swift 侧只做「快照 → NSMenu」的机械映射;`BxState`/
`resolve()` 按既有记档搬进 harness 可编译的 `MenuState.swift`,`main.swift`
只剩 spawn/dial/AppKit 摆放。客户端逻辑趋近于零之后,Go 侧读 Swift 文本的守卫
大部分可删(同样走退役登记)。Windows 托盘同构受益。

**`internal/cli`(12.6k 行)按一条判据拆三包**:这个命令在 Guardian 死了的
时候必须能跑吗?——必须的进「安装器」(setup/app-install)或「逃生口」
(force-teardown,独立小包 + 自己的不变量测试);其余全是瘦客户端(纯 RPC +
渲染)。

## 刻意不做

- **不推倒重来。** 全部事故都在组装根,大爆炸重写是制造史上最大的一次组装根
  变更。绞杀者路径,每步独立可发布、真机可验。
- **协议不进树。** 黑盒子进程是这个架构最对的决策,「优雅=自研」是诱惑不是理由。
- **UI 不做跨平台框架。** 原生薄壳是对的;薄到没有逻辑之后,跨平台框架毫无收益。
- **不做规则 DSL / provider 订阅。** 那是 mihomo 的定位,不是「两步开箱」的定位。
- **不碰数据面。** 一行不动。

## 完成判据(可检验,不是感觉)

1. 五条手写补偿全部删除,`git log` 里有五个删除 commit;
2. netns 集成台里跑着真 Guardian 内核,up/down/升级/崩溃重启有内核状态断言;
3. 三平台共用同一个调谐内核,平台差异只在 `lifecycle_<os>.go`(Windows 可延后,
   但接口不许为 darwin 特化);
4. 读 Swift/Go 源码文本的守卫数量降到接近零,每条退役都有登记的接手者;
5. `bx status` 的信念字段退场——「status 显绿而流量明文直连」在类型上表达不出来。

## 风险

- **第 2 步的行为保真是全案最危险的一处**:控制面出错的两种后果是「明文泄漏」
  与「整机断网」。对策已写在第 2 步里:先摆放后改动,既有 darwin 测试一个断言
  不动地全绿。
- **③b 的执行权是第二危险**:对策是逐项授权 + soak 数据为门,且「装屏障」这个
  动作在设计上就不存在,不是被推迟。
- **每一步单独可发布、单独真机验证**,三条不变量在每步验收清单里复验——这条
  从 2026-08-08 原 spec 原样继承。
- 第 3 步的「只为集成台」边界要守住,防止测试基建演变成未经决策的产品改动。
