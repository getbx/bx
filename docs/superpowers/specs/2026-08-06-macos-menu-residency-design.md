# macOS 菜单栏常驻与状态入口去重设计

## 背景

真机使用中暴露两个 UI 层问题(2026-08-06):

1. **`Open Status` 面板与一级菜单重复。** `connected` 状态下一级菜单显示 7 项
   (Status / Network changes / Tunnel / UDP Relay / DNS / Active / Version),而
   `openStatus()` → `statusSnapshot()` → `.protected(latency, udpRelay, dns, active, version)`
   只有 5 项,**是一级菜单的严格子集**。多点一次换来的是信息更少,这个入口没有挣到
   自己的位置。

2. **菜单被退出后普通用户找不回来。** 菜单栏是普通用户唯一的非命令行控制面;
   `Quit bx` 之后要重新打开 `/Applications/Bx.app`,而用户未必想得到。

## 产品判断

用户(单人项目所有者)明确的取舍:

> 普通用户关闭 menu,就是连同 bx 一起关掉。不然 bx menu 一直在,反正有指示灯,
> 看得出来是否开着 bx。

这条比"UI 手势不该停保护"更站得住,理由是**保护开着却没有指示灯,是一个不可见的
活动状态**——用户忘了 bx 在跑,之后遇到任何网络异常都无从判断。要么两个都在,
要么两个都不在。

原先"UI 手势导致 fail-open"的顾虑是**说重了**:`Quit bx` 有确认弹窗,原文即
"bx will stop protecting system traffic",用户点确认是**故意**关闭保护。fail-open
指的是它在用户不知情时发生,这里不是。

## 设计

### 〇、更正:`Quit Menu` 一直存在(2026-08-06,实施中发现)

本 spec 起初断言「今天没有『只关菜单』这个选项」。**那是错的。**
`main.swift:424` 在 state switch 之后无条件给**每个状态**加了 `Quit Menu`
(`quitMenuActionTitle` → `quit()` → `NSApp.terminate(nil)`),`:326` 的
recovery/updating 分支也有一份。它只关界面、不动保护。

这与「关菜单 = 关 bx」直接冲突,而且加上 `KeepAlive={SuccessfulExit:false}` 之后更
彻底:`NSApp.terminate` 是 exit 0,launchd 不会拉回来,用户能一键进入「保护跑着但
没有任何指示灯」的状态。

**裁决(用户,2026-08-06):删掉 `Quit Menu`。** 见下面第六节。

### 一、菜单项:删 `Open Status`

`connected` 与 `warning` 状态的动作区:

```
现在                          改后
─────────────────────────    ─────────────────────────
Open Status              →   (删除)
View Logs                     View Logs
Run Doctor                    Run Doctor
─────────────────────────    ─────────────────────────
Troubleshoot: Reconnect       Troubleshoot: Reconnect
Turn Off bx                   Turn Off bx
Quit bx                       Quit bx        (保留,行为不变)
```

`off` / `setupNeeded` / `missing` / `updateNeeded` / `notInstalled` 各状态不变。

`Turn Off bx` 与 `Quit bx` 并存,区分是有意义的:

| 动作 | 保护 | 菜单 | 语义 |
|---|---|---|---|
| `Turn Off bx` | 停 | 留(显示 Off,提供 Start Protection) | 暂停,等下还要开 |
| `Quit bx` | 停 | 关 | 不用 bx 了 |

### 二、Quit 确认文案补一句恢复路径

直接对着"普通用户用 CLI 太难"这条约束。现文案只说会发生什么,不说怎么回来:

```
现在:  bx will stop protecting system traffic, restore managed DNS settings,
       and close this menu.

改后:  bx will stop protecting system traffic, restore managed DNS settings,
       and close this menu. To start bx again, open Bx.app from Applications.
```

### 三、`KeepAlive = {SuccessfulExit: false}`

菜单 LaunchAgent 现只有 `RunAtLoad`,崩溃或被强退后要等下次登录才回来。

**必须用字典形式,不能用 `KeepAlive=true`。** 后者会与 `Quit bx` 直接冲突:
`NSApp.terminate` 是干净退出(exit 0),launchd 会立刻把菜单拉回来,Quit 变成没有
效果。字典形式只在异常退出时重启:

| 情形 | 行为 |
|---|---|
| 用户点 `Quit bx`(exit 0) | 保护停 + 菜单关,**不重启** |
| 崩溃 / 被强退(退出码非 0) | launchd 拉回来 |

**四处** plist 生成器都要改,否则不同安装路径行为不一致:

| 生成器 | 用途 |
|---|---|
| `internal/install/unified_darwin.go` 的 `MenuAgentPlistText` | **统一安装(生产路径)** |
| `scripts/install-macos-menu.sh` 的 `write_launch_agent` | legacy 开发安装 |
| `scripts/package-macos-menu.sh` | 打包产物内的样例 plist |
| `apps/macos/BxMenu/Sources/BxMenu/InstanceGate.swift` 的 `menuLaunchAgentPlist` | **菜单自愈**:`main.swift` 的 `ensureLoginItemIfCanonical()` 每次菜单启动都拿它与盘上 plist 比对,不一致就覆写 |

最后一处最容易漏也最致命:它跑得最勤,漏写任何一个键都等于**每次菜单启动都把装
对的 plist 改回错的**(launchd 本次会话仍用旧配置,故当场看起来正常,下次
`bx up` 或下次登录才发作)。四处必须逐字一致;守卫见
`internal/install/menu_plist_generators_darwin_test.go`(读三份源码逐字比对)与
`TestMenuAgentPlistTextRestartsOnlyOnAbnormalExit`(对 Go 生成器的输出断言)。

`bx uninstall` 与更新流程用 `launchctl bootout`,它把服务移出 domain,压得过
KeepAlive,二者不冲突。

### 四、删除范围

| 文件 | 处置 |
|---|---|
| `main.swift` | 删 `openStatus()`、`statusSnapshot()`、`statusPanel` 成员、`Open Status` 菜单项(**只有 1 处**:`.connected, .warning` 是合并的 case) |
| `StatusPanel.swift`(83 行) | 整个删除 |
| `StatusPresentation.swift`(56 行) | 删 `StatusRow`、`StatusSnapshot`;**保留 `DNSPresentation` 与 `dnsPresentation`**——一级菜单的 DNS 行在用 |
| `Tests/StatusPresentationTests.swift` | 删 `StatusSnapshot`/`StatusRow` 断言;**保留 dnsPresentation 的 6 处断言**,文件本身不删 |

`main.swift` 中 `StatusSnapshot`/`StatusRow`/`statusPanel` 共 14 处引用**全部**落在三个
位置:`statusPanel` 成员声明(:73)、`openStatus()` 里的调用点(:442)、`statusSnapshot()`
函数体(:445-478)。删掉这三处即清零,没有散落引用需要单独处理。

`quitBx()` 与 `quitBxActionTitle` **不删**。

### 五、测试

- **Go**:`MenuAgentPlistText` 补测试,断言输出含 `SuccessfulExit` + `<false/>`,
  且**不含**裸 `<key>KeepAlive</key>\n  <true/>`。后者是真实的回归风险——写成
  `true` 会让 `Quit bx` 静默失效,而那个失效在单测里不看这一条就发现不了。纯字符串
  断言,免 root。
- **Swift**:`scripts/test-macos-menu.sh` 现有用例保持全绿;`status-presentation`
  编译单元在删掉 `StatusSnapshot`/`StatusRow` 后仍须通过(证明 dnsPresentation 未被
  连累)。
- 两个脚本里的 plist 由 review 保证与 Go 侧一致(它们不进 CI 测试)。

### 六、删除 `Quit Menu`

删除以下五处,使「关菜单」不再是一个独立于「关 bx」的动作:

| 位置 | 内容 |
|---|---|
| `main.swift:326` | recovery/updating 分支的 `Quit Menu` 菜单项 |
| `main.swift:424` | state switch 之后、加给所有状态的 `Quit Menu` 菜单项 |
| `main.swift:755` | `@objc private func quit() { NSApp.terminate(nil) }` |
| `UpdatePresentation.swift:16` | `let quitMenuActionTitle = "Quit Menu"` |
| `Tests/UpdatePresentationTests.swift:16` | 对应断言 |

保留 `Turn Off bx`(停保护、菜单留着显示 Off)与 `Quit bx`(停保护 + 关菜单)。

**已知后果(有意接受):** `Quit bx` 只出现在 `.connected`/`.warning`,因此在
`.off`/`.setupNeeded`/`.missing`/`.notInstalled`/`.updateNeeded` 这些状态下将**没有
任何退出菜单的入口**,菜单会一直留到下次登录。这正是「菜单要一直存在」的直接结果;
在这些状态下保护本来就没开,不存在「保护跑着却看不见」的问题。若日后觉得
`.off` 状态该允许收起图标,那是一次独立的产品决定,不在本期。

同一后果还落在**安全恢复覆盖层**上(`main.swift` 的 `rebuildMenu()`,`recoverySnapshot`
非 nil 的那一段):它建完 header/Status/Recovery 与一个动作项就 `return`,压根走不到
下面按 `state` 建菜单的 `switch`,因此 `quitBxActionTitle` 同样不出现——恢复**正在
进行**时那唯一的动作项还是**禁用**的 `Troubleshoot: Reconnect`,即菜单在这段时间里
一个可点的项都没有。与上面几个状态不同的是,此时保护通常是开着的,但恢复本就是个
过渡态(结束后菜单回到 `.connected`/`.warning`,退出入口随即回来),且恢复途中允许
用户关掉菜单也并不可取。记录在案,本期不改菜单代码。

## 本期不做

- **观测面板**:`Open Status` 有第二条出路——改造成显示观测层的「信念 vs 事实」
  对照(`bx status --json` 的 `observed`/`divergence` 已在产出)。本期不做,留待
  真机攒够 divergence 样本后另立一期设计,免得凭空猜要显示什么。
- 不碰 `Turn Off bx` / `Start Protection` / `Troubleshoot: Reconnect` / 更新 的任何逻辑。
- 不碰 Guardian、CLI、保护语义。本期纯 UI 层。

## 已知风险

`KeepAlive` 下若 BxMenu 启动即崩,launchd 会反复重启。launchd 有 10 秒节流,不会烧
CPU,但会持续刷 `menu.err.log`。可接受:菜单是个很小的 Swift 进程,且启动即崩本身
是必须修的 bug,反复重启反而让它更早暴露。
