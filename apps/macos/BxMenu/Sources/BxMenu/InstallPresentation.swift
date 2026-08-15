import Foundation

struct RuntimeRelease: Decodable, Equatable {
    let version: String
    private enum CodingKeys: String, CodingKey { case version }
}

func decodeRuntimeVersion(_ data: Data) -> String? {
    (try? JSONDecoder().decode(RuntimeRelease.self, from: data))?.version
}

func unifiedRuntimeVersion(root: String = "/Library/Application Support/bx/runtime") -> String? {
    let path = root + "/current/release.json"
    guard let data = FileManager.default.contents(atPath: path) else { return nil }
    return decodeRuntimeVersion(data)
}

func installActionTitle(runtimeInstalled: Bool, cliUsable: Bool) -> String? {
    if runtimeInstalled && cliUsable { return nil }
    if !runtimeInstalled && cliUsable { return nil } // legacy 布局:沿用既有指引,不递归安装
    return "Install bx…"
}

func menuUpdateActionTitle(check: UpdateCheck?) -> String? {
    updateActionTitle(for: check)
}

func updatingBanner(phase: String?) -> String? {
    guard let phase else { return nil }
    switch phase {
    case "prepared", "barrier_active", "activating", "rolling_back":
        return "Updating bx…"
    default:
        return nil
    }
}

let turnOffActionTitle = "Turn Off bx"

let repairActionTitle = "Repair bx…"

func repairActionNeeded(bundleVersion: String?, runtimeVersion: String?, coreVersion: String?, phase: String?) -> Bool {
    // Mid-transaction: brief version drift across bundle/runtime/core is expected and not a fault.
    if updatingBanner(phase: phase) != nil { return false }
    // Not installed yet: that's the Install flow's job, not Repair's.
    guard let runtimeVersion else { return false }
    let versions = [bundleVersion, runtimeVersion, coreVersion].compactMap { $0 }
    guard let first = versions.first else { return false }
    return versions.contains { $0 != first }
}

/// 「卸载 bx」的确认文案与判定。
///
/// ## 为什么必须有这个入口
///
/// 在此之前菜单里**一个卸载入口都没有**。于是一个想删掉 bx 的普通用户,唯一
/// 显而易见的动作是把 Bx.app 拖进废纸篓 —— 而那**只删掉界面**:Guardian 仍以
/// root 在跑、保护仍然开着、DNS 与路由仍然被接管,而他刚刚亲手删掉了唯一能
/// 关掉它的那个东西。此后只剩终端一条路,而没有终端的人就卡在那里。
///
/// 这不是加一个功能,是**堵掉一条会把人锁住的路**:给出正确的删除方式,他就
/// 不会去用错误的那个。
enum UninstallPresentation {
    static let actionTitle = "Uninstall bx…"
    static let confirmTitle = "Uninstall bx?"

    /// **必须说清三件事**:会停掉保护(网络回到没有 bx 的状态)、会删掉什么、
    /// 以及**什么被保留**。
    ///
    /// 最后一件最容易漏而最要紧:用户在决定「删了以后还装得回来吗」。连接配置
    /// 留着,意味着重装之后不用重新贴链接 —— 这句话直接改变他会不会点确认。
    static let confirmMessage =
        "bx will stop protection and remove its background service, the menu bar app, "
        + "and the bx command. Your network returns to how it was before bx.\n\n"
        + "Your connection settings are kept, so reinstalling does not need the link again. "
        + "macOS will ask for administrator authorization."

    static let confirmButton = "Uninstall"

    /// 卸载完成之后菜单要退出 —— 它自己的程序体正在被删掉。
    ///
    /// **但只在真的成功之后。** 失败了还退出,就把唯一的指示灯藏起来了,而保护
    /// 可能还开着 —— 与「关不掉就不退出」同一条规矩(删 Quit Menu 时拒绝的
    /// 正是那个隐形状态)。
    static func shouldQuitAfter(uninstallSucceeded: Bool) -> Bool { uninstallSucceeded }

    /// 失败时说人话,并给出**不依赖菜单**的那条出路 —— 菜单此刻可能已经半残。
    static let failureTitle = "Uninstall Failed"
    static let failureMessage =
        "bx could not finish uninstalling. Protection may still be running.\n\n"
        + "You can finish it from Terminal:\n    sudo bx uninstall"
}
