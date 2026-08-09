import Foundation

/// 菜单要求运行时具备的能力名,与 Go 侧 `guardian.CapabilityDiagnosticsArchive`
/// 逐字对应(`bx logs --archive/--dir`,Run Doctor 的诊断包靠它)。
let diagnosticsArchiveCapability = "diagnostics_archive"

/// 运行时是否具备菜单要求的能力 —— 由 Guardian **声明**,不再靠解析
/// `bx logs --help` 的帮助文本猜。
///
/// 三种输入必须分得开,而且都不能塌成同一个结果:
///   · 声明了且包含  → 支持
///   · 声明了但不含  → 不支持(这一版运行时确实没有这个能力)
///   · **没声明过(nil)** → 也判不支持,但理由不同:能声明而没声明的只有本次
///     契约之前的旧 Guardian,那本身就是「该升级了」。**刻意选 fail-safe 的那一
///     边**:反过来(把「没说」当成「有」)会在真的缺能力时让 Run Doctor 收集
///     不到诊断包,而那正是用户最需要它的时刻。
func declaresDiagnosticsArchive(_ capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains(diagnosticsArchiveCapability)
}

/// 「把 Guardian 切到已装好的新版」在终端里**真的能跑通**的那条命令。
///
/// 必须与 Go 侧 `upgradeplan.go` 的 `upgradeSwitchCommand` 逐字相同(跨语言由
/// TestMacMenuOldGuardianKeepsProtectionStateVisible 钉住)。不能写成
/// `sudo bx app-install`:/usr/local/bin/bx 是 bridge,它 exec 到 runtime 目录下的
/// bx,那里不在任何 Bx.app 里,`--app-source` 反推直接报错 —— 一条抄下来必然失败
/// 的命令,比不给命令更糟。
let outdatedRuntimeRepairCommand = "sudo /Applications/Bx.app/Contents/Resources/bx-cli app-install"

/// Guardian 缺这个能力时,菜单要**并排**告诉用户的那件事。
///
/// **它是一条附注,不是一个状态。** 这个判据一度门控整个状态机:
/// `guard declaresDiagnosticsArchive(…) else { return .updateNeeded(…) }` 排在所有
/// 保护判定之前。而「Guardian 还在跑旧版」是本产品**明确建模并会主动打印**的一种
/// 处境(见 `internal/cli/upgradeplan.go` 的 `upVersionMismatchMessage`),旧版
/// Guardian 不声明能力 —— 于是那个窗口里菜单只剩表头 "Update Required" 与一句
/// "Update bx":没有 Protected/Off、没有 Turn Off、没有 Reconnect,而那一刻保护
/// 很可能正开着。**能力契约本身发布的那一次,每个既有用户都会撞上一回,不多不少。**
///
/// 高度错了:`diagnostics_archive` 影响的只有 Run Doctor 的诊断包收集这一项。所以
/// 现在只报这一项的降级 + 真正能解决它的那条命令,保护状态照常从 Guardian 确实
/// 回答了的字段推导(`protection_state`/`dns_*`/`core_version` 都早于本轮改动)。
///
/// 两种成因分开说:**键缺席**只可能来自本次契约之前的旧 Guardian;**声明了却不含**
/// 是这一版运行时确实没有这个能力 —— 那时断言人家「是旧版」是一句多出来的假话。
struct OutdatedRuntimeNotice: Equatable {
    /// 数据行的值:降级了什么。说清楚、不夸大 —— 它会与保护状态并排显示,
    /// 含糊其辞会被读成「保护出问题了」。
    let summary: String
    /// 怎么办。
    let remedy: String
}

func outdatedRuntimeNotice(capabilities: [String]?) -> OutdatedRuntimeNotice? {
    if declaresDiagnosticsArchive(capabilities) {
        return nil
    }
    let summary = capabilities == nil
        ? "Older build; diagnostics archive unavailable"
        : "Diagnostics archive unavailable"
    return OutdatedRuntimeNotice(
        summary: summary,
        remedy: "To finish updating, run: \(outdatedRuntimeRepairCommand)"
    )
}

/// MenuProtectionVerdict 是「这份状态该让菜单显示什么」的判定。
enum MenuProtectionVerdict: Equatable {
    /// 保护被用户主动关掉。菜单应显示 Off 并提供 Start Protection。
    case off
    /// 出了需要用户知道的问题;附带的字符串就是指示灯 tooltip 里那句原因。
    case attention(String)
    /// 一切正常,可以继续走 DNS 判定与 connected。
    case healthy
}

/// menuProtectionVerdict 把一份 Guardian 状态归到三类之一。
///
/// 输入是 Guardian `/v1/status` 的响应,不再是 `bx status --json`:菜单不该为了
/// 拿同一件事再 spawn 一个可能是旧版的 CLI 去重新推导一遍(见控制面架构设计)。
///
/// 顺序是有讲究的:`off` 必须先判,因为 Core 已经退出、隧道当然不健康——
/// 若先看 tunnelHealthy 就会把「用户自己关的」报成「隧道坏了」,而那个分支
/// 不提供 Start Protection,用户就被困住了。
///
/// 反过来,「Guardian 说在保护但 Core 不见了」绝不能报成 off:那是 Core 崩了,
/// 报成 off 会让用户以为是自己关的,从而不去排查。
func menuProtectionVerdict(_ status: GuardianStatus) -> MenuProtectionVerdict {
    switch status.protectionState {
    case "off":
        return .off
    case "needs_attention":
        return .attention("Repair Required")
    case "blocked":
        return .attention("Blocked")
    default:
        break
    }
    // 此前这里看的是 `bx status --json` 的 `core_available` —— 那是 **CLI 推导**
    // 出来的:CLI 自己去拨 Core 的控制 socket,拨通即 true。`core.reachable` 是
    // 同一个事实的**原产地**:Guardian 拨的是同一个控制 socket,只是这次由它
    // 代问、并把答案随状态一起发布。所以这是等价替换,不是近似。
    //
    // 但两种「没有健康数据」必须分开说:
    //   `core == nil`      Guardian 压根没问过 Core(没接取数函数,比如升级窗口
    //                      里旧 Guardian 还在跑)。它对 Core 一无所知 —— 断言
    //                      「Core 不在」是一句它无权说的话。
    //   `reachable == false` 问了,Core 没答。这才是「Core 不在」。
    // 二者都不得说成 healthy,也都不得说成「隧道不健康」:那是把「问不出来」压成
    // 「答案是坏的」,正是 internal/observe 的三态 Tristate 存在的理由。
    // 键缺席(`nil`)与 `false` 也是两回事:缺席是「Guardian 没说」,同样归入
    // 「问不出来」那一档,绝不能当成一个自信的坏答案 —— 尤其 `tunnel_healthy`
    // 缺席若被读成 false,就会凭空造出一句 "Tunnel unhealthy"。
    guard let core = status.core, let reachable = core.reachable else {
        return .attention("Core status unavailable")
    }
    if !reachable {
        return .attention("Core unavailable")
    }
    guard let tunnelHealthy = core.tunnelHealthy else {
        return .attention("Core status unavailable")
    }
    if !tunnelHealthy {
        return .attention("Tunnel unhealthy")
    }
    return .healthy
}
