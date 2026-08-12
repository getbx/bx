import Foundation

/// 行标记。**三态,不是两态。**
///
/// `unknown` 是「没问出来」,与 `bad`(问了,答案是坏的)必须分开:把前者压成后者,
/// 就是重新制造 internal/observe 专门要消灭的那个谎 —— 也会让图标无缘无故裂开。
enum MenuRowMark: Equatable {
    case ok
    case bad
    case unknown
}

struct MenuRow: Equatable {
    let label: String
    let value: String
    let mark: MenuRowMark
}

struct MenuRowSet: Equatable {
    let rows: [MenuRow]
    /// 仅统计 `.bad`。`.unknown` 不计入 —— 未观测不是异常。
    let anomalyCount: Int
}

/// 阶段③才有数据的行的占位文案。刻意不是空字符串:留白会被读成「没这回事」,
/// 而「未观测」如实说明我们没问过。
private let notObserved = "Not checked"

/// 把一份 Guardian 状态摊成菜单里的数据行。
///
/// Core 的统计由 Guardian 代取(`status.core`),菜单不再自己 spawn 一个 CLI 去问
/// 第二个源 —— 两个源会各自正确却互相矛盾,而指示灯只画其中一个。
///
/// **「没有答案」有两种,都记 `.unknown` 而不是 `.bad`:**
///   `core == nil`        Guardian 压根没问过 Core;
///   `reachable == false` 问了,Core 没答。
/// 两种情况下 `tunnelHealthy` 都是 Go 侧承诺的零值,拿它画一行 "Tunnel unhealthy ✗"
/// 就是把「问不出来」伪装成一个自信的坏答案。二者的区别由 menuProtectionVerdict
/// 用两句不同的告警文案承担(它进菜单正文的 Status 行),这里不重复表达。
func menuRows(status: GuardianStatus?, dns: String?, now: Date = Date()) -> MenuRowSet {
    var rows: [MenuRow] = []

    // 维护挂起排在最前:它回答的是「为什么保护是这个样子」,而不是保护的某一项
    // 指标。没有挂起时它一个字都不占(判定见 maintenanceRow)。
    if let hold = maintenanceRow(status: status, now: now) {
        rows.append(hold)
    }

    // **这里曾经有三行占位符**(Exit Location / IPv6 Leak / WebRTC),恒为
    // "Not checked",等阶段③点亮。它们已经删掉。
    //
    // 当初的理由是「用户能看见 bx 打算回答哪些问题」。真机上用户的反应证伪了它:
    // 他看到的是三行空值,问的是「出口位置应该有值吧?」—— **占位行读起来是坏了,
    // 不是路线图。** 而这正是本项目自己那条原则的另一面:恒绿的检查会把界面训练
    // 成装饰,恒「未检测」是同一种失败,且更糟 —— 它宣传了一个不存在的能力。
    //
    // IPv6 与 WebRTC 现在由 `bx leakcheck` 真的答得了,菜单底部的
    // "Check for leaks ↗" 就是入口:**两行死行换一个活动作**。
    // Exit Location 要向外问一次才知道(没有任何本机观测能得出公网出口),
    // 那需要 Guardian 开第二条对外出网路径 + 缓存,单列一期;在它有值之前不占位。
    let core = answeringCore(status)
    if let core {
        let line = [core.transport, core.server]
            .compactMap { $0 }.first { !$0.isEmpty }
        rows.append(line.map { MenuRow(label: "Route", value: $0, mark: .ok) }
            ?? MenuRow(label: "Route", value: notObserved, mark: .unknown))
        // 三档,不是两档:`tunnel_healthy` 缺席时 Guardian 没说过隧道好不好,
        // 画一行 "Tunnel unhealthy ✗" 就是拿一个缺失的键造出一个坏答案。
        switch core.tunnelHealthy {
        case .some(true):
            rows.append(core.latencyMS.map { MenuRow(label: "Latency", value: "\($0) ms", mark: .ok) }
                ?? MenuRow(label: "Latency", value: notObserved, mark: .unknown))
        case .some(false):
            rows.append(MenuRow(label: "Latency", value: "Tunnel unhealthy", mark: .bad))
        case .none:
            rows.append(MenuRow(label: "Latency", value: notObserved, mark: .unknown))
        }
    } else {
        rows.append(MenuRow(label: "Route", value: notObserved, mark: .unknown))
        rows.append(MenuRow(label: "Latency", value: notObserved, mark: .unknown))
    }

    if let dns, !dns.isEmpty {
        rows.append(MenuRow(label: "DNS", value: dns, mark: .ok))
    } else {
        rows.append(MenuRow(label: "DNS", value: notObserved, mark: .unknown))
    }

    if let mode = core?.udpMode, !mode.isEmpty {
        rows.append(MenuRow(label: "UDP Relay", value: mode, mark: .ok))
    } else {
        rows.append(MenuRow(label: "UDP Relay", value: notObserved, mark: .unknown))
    }

    return MenuRowSet(rows: rows, anomalyCount: rows.filter { $0.mark == .bad }.count)
}

/// 只有**答过话的** Core 的统计才算数据。没问过(nil)与问了没答(reachable=false)
/// 在这里一律归成「没有数据」——它们携带的全是零值,当真会画出一行撒谎的 ✗。
private func answeringCore(_ status: GuardianStatus?) -> CoreRuntime? {
    guard let core = status?.core, core.reachable == true else { return nil }
    return core
}
