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

func menuRows(report: BxReport?, dns: String?) -> MenuRowSet {
    var rows: [MenuRow] = []

    // ── 阶段③:数据尚未接入,占位但在场 ──
    // 在场是有意的:用户能看见 bx 打算回答哪些问题,以及哪些还没答上。
    rows.append(MenuRow(label: "Exit Location", value: notObserved, mark: .unknown))

    if let report {
        let line = [report.transport, report.server]
            .compactMap { $0 }.first { !$0.isEmpty }
        rows.append(line.map { MenuRow(label: "Route", value: $0, mark: .ok) }
            ?? MenuRow(label: "Route", value: notObserved, mark: .unknown))
        rows.append(MenuRow(
            label: "Latency",
            value: report.tunnelHealthy ? "\(report.latencyMS) ms" : "Tunnel unhealthy",
            mark: report.tunnelHealthy ? .ok : .bad))
    } else {
        rows.append(MenuRow(label: "Route", value: notObserved, mark: .unknown))
        rows.append(MenuRow(label: "Latency", value: notObserved, mark: .unknown))
    }

    if let dns, !dns.isEmpty {
        rows.append(MenuRow(label: "DNS", value: dns, mark: .ok))
    } else {
        rows.append(MenuRow(label: "DNS", value: notObserved, mark: .unknown))
    }

    rows.append(MenuRow(label: "IPv6 Leak", value: notObserved, mark: .unknown))
    rows.append(MenuRow(label: "WebRTC", value: notObserved, mark: .unknown))

    if let mode = report?.udpMode, !mode.isEmpty {
        rows.append(MenuRow(label: "UDP Relay", value: mode, mark: .ok))
    } else {
        rows.append(MenuRow(label: "UDP Relay", value: notObserved, mark: .unknown))
    }

    return MenuRowSet(rows: rows, anomalyCount: rows.filter { $0.mark == .bad }.count)
}
