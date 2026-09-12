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
        // **服务器名优先,传输协议兜底。** 用户认的是「连到哪台」(vps / home),
        // `reality@vps` 是传输@服务器这个内部标识;协议在 bx status / doctor 里。
        let line = [core.server, core.transport]
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

    // 直连域名的查询交给了谁。**这一行只在问出来时出现** —— 一行恒定的
    // "Not checked" 正是这个菜单刚删掉三行的理由。
    //
    // 它是 Core **此刻正在用**的值,不是 config 文件里的值:有人改了文件时 Core
    // 仍跑着旧值直到重启,显示文件里的那个就是撒谎。
    if let upstream = core?.dnsUpstream, !upstream.isEmpty {
        rows.append(MenuRow(label: "Direct lookups", value: upstream, mark: .ok))
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
///
/// **不是 private:规则窗口那条路要用同一份判据。** `failing_rules` 与这里的
/// 每一项同属那批「Reachable=false 时按构造全是零值」的字段
/// (`internal/guardian/types.go`),而规则窗口一度自己写了
/// `core?.failingRules ?? []` —— 那是同一个问题的第二份判据,并且答反了:
/// 空数组被当成「没有规则在失败」,于是 Core 没在应答时每条规则都画成健康的。
/// 判据只留这一份。
func answeringCore(_ status: GuardianStatus?) -> CoreRuntime? {
    guard let core = status?.core, core.reachable == true else { return nil }
    return core
}

/// 菜单里**真正摆出来**的行:`menuRows` 是完整集合(图标裂不裂由它的 anomalyCount
/// 决定),这一层只管显示压缩。
///
/// 「已连接」状态下此前五行数据里四行天天一个样(DNS / Direct lookups / UDP Relay
/// 正常时永远是同一个值),按本仓库「只在真有问题时才占地方」的纪律它们不该常驻
/// —— 常态会变墙纸,把真正要紧的那一行一起淹掉。规则:
/// - Route + Latency 合成一行 `Via`(`reality@vps · 390 ms`);哪一半问不出来就
///   不写那一半,两半都问不出来才是 `Not checked`;任一半 ✗ 则整行 ✗。
/// - DNS / Direct lookups / UDP Relay **只在「不是 ok」时露面** —— 压缩掉的是
///   *正常时的噪声*,而 `.unknown` 不是正常:它是「这一项该有值、这次没拿到」。
///   判据因此是 `== .ok`,**不是** `!= .bad` —— 后者把「查了,没事」与「没问出来」
///   合成同一种沉默,而在这个菜单里沉默恰恰读作前者(与规则窗口「一行没有副标题
///   = 查过了、健康」同一条词汇表)。
///   **它不会变成一行常驻的 "Not checked"**,而防线在**上一层**:一个可能结构性
///   缺席的字段由 `menuRows` **整行不发**(Direct lookups 就是这么做的),所以
///   走到这里还带着 `.unknown` 的行,是真的问过了而没问出来。将来某一行在真机上
///   恒为未知,该修的是它的构造处(照 Direct lookups 整行不发),**不是**回到
///   这里把 unknown 一起藏掉 —— 那会连真的问不出来一起藏。
/// - 维护挂起与任何认不出的行**原样保留**:新加的行默认参与显示(吵的失效好过
///   安静的失效,与 statusdigest 的「默认参与投影」同一条纪律)。
func compactMenuRows(_ set: MenuRowSet) -> [MenuRow] {
    let quietWhenFine: Set<String> = ["DNS", "Direct lookups", "UDP Relay"]
    var out: [MenuRow] = []
    var route: MenuRow?
    var latency: MenuRow?
    var viaInserted = false
    for row in set.rows {
        switch row.label {
        case "Route":
            route = row
        case "Latency":
            latency = row
        default:
            if quietWhenFine.contains(row.label), row.mark == .ok { continue }
            out.append(row)
            continue
        }
        // Via 行占 Route 原来的位置(第一次遇到两者之一时插入占位,最后回填)。
        if !viaInserted {
            viaInserted = true
            out.append(MenuRow(label: "Via", value: "", mark: .unknown))
        }
    }
    guard viaInserted, let slot = out.firstIndex(where: { $0.label == "Via" }) else { return out }
    let halves = [route, latency].compactMap { $0 }.filter { $0.mark != .unknown }
    let marks = [route, latency].compactMap { $0?.mark }
    let mark: MenuRowMark = marks.contains(.bad) ? .bad : (halves.isEmpty ? .unknown : .ok)
    let value = halves.isEmpty ? notObserved : halves.map(\.value).joined(separator: " · ")
    out[slot] = MenuRow(label: "Via", value: value, mark: mark)
    return out
}
