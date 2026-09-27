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
    return L("Install bx…")
}

func menuUpdateActionTitle(check: UpdateCheck?) -> String? {
    updateActionTitle(for: check)
}

/// 菜单顶部那一行更新相关的东西:一个可点的入口,或一句不可点的说明。
enum MenuUpdateRow: Equatable {
    case action(String)
    case note(String)
}

/// **更新检查拿的是 Guardian 自己的版本去比**,而保护开着时 `bx update` 换不掉 Guardian
/// 自己(2026-09-25 真机:两次升级之后 Guardian 仍是 v0.4.3)。于是已装的就是最新时,
/// 「Update bx…」会一直挂着,点下去什么都不改变。判据:
/// - 真有比**已装版本**更新的发布 ⇒ 照旧给入口;
/// - 已装的就是最新、只是 Guardian 还在旧版 ⇒ 一句说明,不给入口(今天的切换会让流量
///   在那几秒里无保护地外出,见 fail-closed-guardian-switch 设计);
/// - 两个版本问不到 ⇒ 维持原判,不猜。
func menuUpdateRow(check: UpdateCheck?, guardianVersion: String?, runtimeVersion: String?) -> MenuUpdateRow? {
    let installed = (runtimeVersion ?? "").trimmingCharacters(in: .whitespaces)
    let running = (guardianVersion ?? "").trimmingCharacters(in: .whitespaces)
    if let check, check.available, check.verified, !installed.isEmpty, check.latest != installed {
        return .action(L("Update bx…"))
    }
    if !installed.isEmpty, !running.isEmpty, installed != running {
        return .note(L("{0} installed · Guardian switch pending", installed))
    }
    if installed.isEmpty, let title = updateActionTitle(for: check) {
        return .action(title)
    }
    return nil
}

func updatingBanner(phase: String?) -> String? {
    guard let phase else { return nil }
    switch phase {
    case "prepared", "barrier_active", "activating", "rolling_back":
        return L("Updating bx…")
    default:
        return nil
    }
}

var turnOffActionTitle: String { L("Turn Off bx") }

var repairActionTitle: String { L("Repair bx…") }

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
    static var actionTitle: String { L("Uninstall bx…") }
    static var confirmTitle: String { L("Uninstall bx?") }

    /// **必须说清三件事**:会停掉保护(网络回到没有 bx 的状态)、会删掉什么、
    /// 以及**什么被保留**。
    ///
    /// 最后一件最容易漏而最要紧:用户在决定「删了以后还装得回来吗」。连接配置
    /// 留着,意味着重装之后不用重新贴链接 —— 这句话直接改变他会不会点确认。
    static var confirmMessage: String {
        L("bx will stop protection and remove its background service, the menu bar app, and the bx command. Your network returns to how it was before bx.\n\nYour connection settings are kept, so reinstalling does not need the link again. macOS will ask for administrator authorization.")
    }

    static var confirmButton: String { L("Uninstall") }

    /// 卸载完成之后菜单要退出 —— 它自己的程序体正在被删掉。
    ///
    /// **但只在真的成功之后。** 失败了还退出,就把唯一的指示灯藏起来了,而保护
    /// 可能还开着 —— 与「关不掉就不退出」同一条规矩(删 Quit Menu 时拒绝的
    /// 正是那个隐形状态)。
    static func shouldQuitAfter(uninstallSucceeded: Bool) -> Bool { uninstallSucceeded }

    /// 失败时说人话,并给出**不依赖菜单**的那条出路 —— 菜单此刻可能已经半残。
    static var failureTitle: String { L("Uninstall failed") }
    static var failureMessage: String {
        L("bx could not finish uninstalling. Protection may still be running.\n\nYou can finish it from Terminal:\n    sudo bx uninstall")
    }
}

/// 菜单 LaunchAgent 的 label。launchd 拉起的进程环境里 `XPC_SERVICE_NAME` 就是它 ——
/// 那是「退出之后有人会把我拉起来」的唯一可靠证据。
let menuLaunchdLabel = "com.getbx.bx.menu"

/// 为换成新版本而退出时用的退出码。**必须非零**:LaunchAgent 是
/// `KeepAlive{SuccessfulExit:false}`,只有非零退出才会被拉起;退 0 等于把菜单关掉。
let menuRelaunchExitCode: Int32 = 75

/// 盘上的 Bx.app 已经换成新版本、而跑着的还是旧进程时,该不该现在退出,
/// 让 launchd 拉起新版。
///
/// **缺口是真机撞到的(known-gaps A13,2026-09-26)**:`bx update` 与菜单里的
/// Update bx… 都会换掉 `/Applications/Bx.app`,却都不重启菜单 —— Guardian、Core、
/// runtime 全是新版,菜单还是早上那个进程,新菜单项一个都不出现,直到下次登录。
///
/// 只在四件事同时成立时退出:
/// - 两个版本**都读得出来且不同**(读不出来 = 不知道,绝不据此退出:一个装到一半
///   的包、一次读盘失败都会长成这个样子);
/// - 由 launchd 托管(否则退出之后没人拉起,菜单栏指示灯就这么没了 —— 那正是
///   这个菜单最不许出现的「保护在跑、指示灯不在」);
/// - 此刻**没有人在用它**(更新 / 开关在飞、排队中的 Quit、弹窗、打开的窗口、
///   展开的菜单):退出会把用户正在看或正在填的东西一起抹掉。等他忙完,
///   下一次刷新再判一次就是了。
func menuShouldRelaunchForNewBundle(
    launchedVersion: String?, onDiskVersion: String?, launchdManaged: Bool, busy: Bool
) -> Bool {
    let launched = (launchedVersion ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    let onDisk = (onDiskVersion ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
    guard !launched.isEmpty, !onDisk.isEmpty, launched != onDisk else { return false }
    return launchdManaged && !busy
}
