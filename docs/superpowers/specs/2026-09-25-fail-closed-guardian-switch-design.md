# Guardian 切换与崩溃不许泄漏真实 IP —— 设计(2026-09-25)

**所有者的边界(原话):「断网是允许的,但不允许泄漏 ip」。** 本文每一个切换窗口都要回答
同一个问题:**这一刻,一个 App 发往公网的包是被拦住,还是从物理网卡直出?**

## 1. 现状(2026-09-25 在所有者的 Mac 上逐条核过)

| 事实 | 证据 |
|---|---|
| `bx update`(保护开着)走 Guardian 的 `/v1/update` 事务,换了 Core 与盘上文件,**换不掉 Guardian 自己** | 升级后 `guardian_version v0.4.3`、`core_version v0.4.5`;能力清单里没有 v0.4.5 才有的 `servers_clear_udp` |
| v0.4.4、v0.4.5 两次升级之后 Guardian 都停在 v0.4.3 | 同上;Guardian 进程 9 月 23 日启动 |
| 切换 Guardian 只有一条路:`sudo bx app-install --app-source /Applications/Bx.app`(`bx up` 在版本不一致时提示它),`install.sh` 升级也走同一套 | `internal/cli/upgradeplan.go` 的 `upgradeSwitchCommand` |
| 那条路的「停保护」是 `DownForUpgrade` = 普通的 Down:**停 Core、DNS 还给系统、不装屏障**;CLI 侧调用前也不装 | `Manager.Down`、`cleanGuardianDown` |
| Core 是 Guardian 的**子进程、同一进程组** | `ps`:Core ppid = Guardian pid,pgid 相同 |
| **launchd 默认在任务主进程退出时收掉同一进程组里的其余进程**;设 `AbandonProcessGroup` 才不收 | 本机实测:两个临时 LaunchAgent(`sh` 父 + `sleep` 子),默认下子进程随父死,`AbandonProcessGroup=true` 下存活 |
| Core 收到 SIGTERM 走正常关机:还原它装的路由 | `supervisor.Run` 的拆除台账 |
| 启动恢复在 `desired=on` 时:有在跑且身份核对得上的 Core 就**接管**(Verify → waitHealthy → acceptHealthy),没有才 `startCoreLocked` —— **起 Core 之前不装屏障** | `Manager.upLocked` |
| 屏障 = 四条 IPv4 + 四条 IPv6 的 `/2` reject,比 Core 的 `/1` 更长 | `internal/barriercidr` |

## 2. 今天会泄漏的窗口

**W1 升级 / 切换 Guardian**:`DownForUpgrade` 停 Core(路由还原)+ DNS 还给系统 ⇒ 从那一刻到新 Core
劫持完成,**全部流量直连,DNS 也走系统解析**。几秒到二十秒。**泄漏。**

**W2 Guardian 崩溃或被 kill**:launchd 收掉 Core(SIGTERM ⇒ 路由还原);DNS 仍指着 127.0.0.1(没人
去还原)⇒ 需要解析的新连接失败,但**缓存了地址的、直接用 IP 的、已建连接重连的流量直连**。持续到新
Guardian 起来并把新 Core 带到健康(最长约 20 秒)。**泄漏。**

W1 是这次要修的;W2 是同一个根因(Core 的寿命绑在 Guardian 进程上),一并修掉。

## 3. 设计

### D1 Core 不再随 Guardian 进程一起死:Guardian plist 加 `AbandonProcessGroup=true`

Guardian 退出(崩溃、被 kill、升级后重启)时 Core 继续跑、路由与隧道原封不动。新 Guardian 起来走
启动恢复,**现成的接管路径**(`Existing` → `Verify` → `waitHealthy` → `acceptHealthy`)把它认领回来。
W2 因此消失:Guardian 不在的那几秒里,流量仍然经隧道,kill-switch 仍在(它在 Core 里)。

- **双 Core 的门不因此打开**:启动恢复先认领;认领不了时 `startCoreLocked` → `runner.Start` 的准入
  是现扫 `ScanRunning`,扫到在跑的 Core 就 fail-closed(拒起第二个)。
- **显式停止不受影响**:`bx down` / 强制拆除一律先经 Core 的 `/v0/shutdown` 协作关闭,不依赖 launchd
  收进程组。卸载同理。**需要逐一核对**:凡是依赖「bootout Guardian 顺便杀掉 Core」的路径都要改成显式
  关闭,否则 Core 会变成没人管的孤儿(保护照常、但没有 Guardian)。
- Guardian 的 plist 只有**一个**生成器(`install.GuardianPlistText`;「四处必须字面一致」是菜单 agent
  那份,不是这份),`TestGuardianPlistTextUsesCanonicalLifecycleOwner` 钉住这个键。
- **核对结果(开放问题 2,2026-09-25)**:依赖「bootout 顺带杀 Core」的只有两处 —— `bx uninstall`
  (Guardian 不可达或不在活跃态时直接 bootout)与强制拆除在 Core 不应答时的那一支。两处都改成 bootout
  之后调 `StopOrphanedCore`(只认 core-process.json 记下且身份核对得上的那一个;协作关闭 → SIGTERM →
  SIGKILL,每次发信号前重新核对身份)。`kickstart -k` 与崩溃重启是**想要的**新行为:Core 活下来,
  新 Guardian 接管。**D1 只在 plist 被重写并重新 bootstrap 之后生效**(`EnableGuardian` 对已加载的
  任务什么都不做),所以旧机器第一次拿到它就是 D3 那一次切换。

### D2 升级后让 Guardian 自己换成新版:事务提交后**退出**,由 launchd 以新二进制重启

有了 D1,Guardian 退出不再牵连 Core。`/v1/update` 提交成功、应答写出之后,Guardian 记一行日志并
**退出**;launchd(`KeepAlive`)按 plist 重启 `runtime/current/bx` —— 此时它指向新版 —— 新 Guardian
启动恢复接管正在跑的新 Core。**全程不动路由、不动 DNS、不断网。** Guardian 不在的那几秒只影响菜单
与 CLI 能不能问到它。

- 选「退出」而不是「原地 exec」:前者复用 launchd 已有的重启语义,后者要处理 fd、信号与 Go 运行时的
  状态,换来的只是省掉 launchd 的重启延迟。
- 只在「新 Core 已健康、事务已提交、Guardian 版本与 runtime 不一致」时退出;回滚路径不退出。

### D3 第一次切换(从没有 D1 的旧 Guardian 升上来):**在屏障下**换

旧 Guardian(≤ v0.4.5)的 plist 没有 `AbandonProcessGroup`,换 plist 必须 bootout 旧任务,而那一下会
收掉 Core。这一次只能靠屏障兜住,由新版 CLI(`bx app-install` / `install.sh`)按这个顺序做:

1. **装屏障**(与 Guardian 同一份计划:`/2` reject + 服务器 `/32` 旁路经物理网关 + 私网直连)。从这一刻起
   公网包只有两条去路:进隧道(Core 的 `/1` 被更长的 `/2` 压住,所以实际上也被拦)、或发往服务器本身。
2. **DNS 保持指向 127.0.0.1**(不还给系统)。
3. bootout 旧 Guardian(Core 随之退出,它的路由还原;屏障仍在 ⇒ 公网包被拒)。
4. 写新 plist、bootstrap 新 Guardian;启动恢复在屏障下起新 Core(隧道经服务器旁路建立,健康检查走隧道)。
5. 等新 Guardian 报 Protected、Core 报路由已装。
6. **拆屏障**(`RemoveBlockingBarrierRoutes`,与逃生口同一个原语),确认 `route get 1.1.1.1` 落在我们的 TUN。

**任何一步失败都停在屏障后面**(断网,不泄漏),并打印唯一的出路 `sudo bx down`(用户显式选择不要保护)。

**实现上复用 `/v1/migrate`,不另写一份交接**(2026-09-25 读代码定)。`Manager.Migrate` 本来就是
「一个不归我管的 Core 在跑 → 在屏障下接过来」:装屏障(`file exists` 被容忍,所以 CLI 先装过的那几条
不冲突,且装完 Guardian 自己**持有**这份屏障的所有权)→ 停 legacy(机器上没有 legacy unit 时是空操作)→
`ReassertBypass`(旧 Core 退出时可能删掉了那条服务器 `/32`)→ 带 handoff 起新 Core → 释放屏障。
网关与服务器地址由 `legacyMigrationRequest` 从**正在跑的 Core** 的运行时事实里取,开放问题 3 因此不需要
第二份。所以 CLI 这一侧的顺序是:

1. `legacyMigrationRequest` 取网关 + 服务器 `/32`(问不出来就**不开始**,什么都没动过);
2. 武装维护挂起(旧 Guardian 看到 Core 退出时不重启它;新 Guardian 启动恢复时不自己起 Core ——
   否则新 Core 在没有 handoff 的情况下去装那条已被屏障占着的 `/32`);
3. CLI 自己装屏障(同一份 `migrationBarrierContext` 计划)。**从这一刻起公网包被拒**;
4. bootout 旧 Guardian(launchd 收掉 Core,Core 还原自己的 `/1`;屏障还在,DNS 仍指 127.0.0.1 而没人
   在听 ⇒ 解析失败,不回落到系统解析器);等到 `ScanRunning` 数不到 Core 为止;
5. 写新 plist(带 D1)、bootstrap、等 Guardian socket;
6. `/v1/migrate`(它清挂起、写 desired=on、接过屏障、起 Core、释放屏障);
7. 核对:Guardian 报 Protected,`route get 1.1.1.1` 落在 TUN,`/2` reject 不在了。

第 3 步之后任何一步失败:**不拆屏障**,打印 `sudo bx down`。第 1、2 步失败:什么都没改,照常退出。

### D4 版本漂移如实说出来(先做,不依赖以上)

Guardian 版本与 runtime 不一致时:`bx status`(人读与 `--json`)说「新版已装好,Guardian 还差一步
切换」;菜单不再把它显示成「有更新」;`bx update` 成功后同样说出来。**措辞不许给那条会泄漏的切换
命令**,直到 D3 落地。

**D4 已做(2026-09-25)**:`bx status` 加一行 `Update  <已装> is installed, but Guardian is still
running <旧>`;`bx up` 那句提示不再推荐切换命令;菜单解码 `guardian_version`/`runtime_version`,已装的
就是最新时把「Update bx…」换成一行不可点的说明(`menuUpdateRow`)。**D3 落地时要一并换掉**:菜单的
`OutdatedRuntimeNotice` 补救命令(`outdatedRuntimeRepairCommand`)也是那条会泄漏的切换命令 —— 它只对
2026-08 之前、连能力都不声明的 Guardian 出现,今天够不着,但 D3 之后要指向安全的那一条。

## 4. 验证(由 agent 做,不交给所有者)

- **判据层**:D1 的 plist 生成器守卫;D2 的「只在提交且版本不一致时退出」单测;D3 的步骤顺序
  (屏障先装、最后拆、失败不拆)用注入的原语测。
- **Linux netns 台子**:Guardian 已能在 linux 上跑。在 netns 里真起 Guardian + Core,kill Guardian,
  断言 Core 存活、路由未动、新 Guardian 认领(linux 上等价于 AbandonProcessGroup 的机制要另查:systemd
  的 `KillMode`)。
- **所有者的 Mac 上,最终那一次切换本身就是验收**,同时用 `tcpdump -i <物理网卡>` 抓切换全程的包,
  过滤掉服务器地址与私网,**断言数量为 0**。这是「不泄漏」唯一直接的证据;抓到一个包就说明设计有洞。
  在跑之前,先在同一台机器上单独演练 D3 的第 1、6 步(装屏障 → 确认公网被拒 → 拆屏障 → 确认恢复),
  那只会断网几秒,不会泄漏。

## 5. 开放问题(动手前要查清)

1. launchd `KeepAlive` 的重启节流(默认 10 秒)在 D2 下是否可接受 —— 只影响 Guardian 可达性。
2. 哪些路径今天依赖「bootout Guardian 顺便杀掉 Core」(D1 的核对清单)。
3. D3 第 1 步需要物理网关与服务器地址:Guardian 的屏障计划从哪里取这两样,CLI 能不能复用同一份
   (不许手抄第二份)。
4. linux(systemd 直管 Core,不经 Guardian)有没有同形的问题:`bx update` 在 linux 上刻意不重启服务,
   但 `systemctl restart` 与崩溃重启时 Core 的路由还原是否同样 fail-open。
