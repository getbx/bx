import Foundation

struct RecoverySnapshot: Decodable, Equatable {
    let recoveryID: String
    let state: String
    let stage: String
    let reason: String
    let generation: String?
    let lastErrorCode: String?
    let detail: String?
    let attempt: Int
    let startedAt: String
    let updatedAt: String

    enum CodingKeys: String, CodingKey {
        case recoveryID = "recovery_id"
        case state, stage, reason, generation, detail, attempt
        case lastErrorCode = "last_error_code"
        case startedAt = "started_at"
        case updatedAt = "updated_at"
    }
}

struct RecoveryPresentation: Equatable {
    let title: String
    let indicator: StatusIndicator
    let shortReason: String?
    let showsSuccessAlert: Bool

    var isRunning: Bool {
        title == "Reconnecting"
    }
}

struct RecoveryFailureTransition: Equatable {
    let snapshot: RecoverySnapshot
    let reconnectInFlight: Bool
}

func visibleStatusRecovery(_ snapshot: RecoverySnapshot?) -> RecoverySnapshot? {
    guard let snapshot else { return nil }
    return recoverySnapshotForDisplay(snapshot, allowsTerminalSuccess: false)
}

/// Filters a recovery snapshot for a state that resolved to a warning.
///
/// `updateIcon` lets a live recovery snapshot override the state-derived
/// indicator, so a snapshot that paints the shield green (a `succeeded`
/// reconnect, still published while `reconnectInFlight` is true) would
/// contradict the warning and show green while, say, DNS is unmanaged. Those
/// are dropped. Yellow `Reconnecting` and red `Reconnect Failed` snapshots are
/// kept: they carry more information than the generic warning, and dropping the
/// red one would quietly downgrade a failure to a mere warning.
func recoverySnapshotSurvivingWarning(_ snapshot: RecoverySnapshot?) -> RecoverySnapshot? {
    guard let snapshot else { return nil }
    return recoveryPresentation(for: snapshot).indicator == .green ? nil : snapshot
}

func passiveStatusRecovery(protectionState: String?, recovery: RecoverySnapshot?) -> RecoverySnapshot? {
    if protectionState == "blocked" || protectionState == "needs_attention" {
        return nil
    }
    return visibleStatusRecovery(recovery)
}

func recoverySnapshotForDisplay(
    _ snapshot: RecoverySnapshot,
    allowsTerminalSuccess: Bool
) -> RecoverySnapshot? {
    // `ignored` 是 Guardian 说「我**故意**什么都没做」——请求进来时 desired=off,
    // 于是那次恢复被丢掉(internal/guardian/path_recovery.go 三处
    // `State = "ignored"` / `Stage = "off"`)。它与 `idle` 同类:一条不该occupy
    // 界面的终态。
    //
    // **不过滤它会产生一句彻头彻尾的假话,而且是在真机上被抓到的**:
    // `bx down` 会拆掉路由 ⇒ 底层网络变化 ⇒ NetworkObserver 请求一次路径恢复
    // ⇒ desired 此刻是 off ⇒ 记下一条 ignored 快照。它一直留到有别的东西覆盖
    // 为止,于是**紧接着的 `bx up` 之后它还在**;而 `recoveryPresentation` 的
    // switch 只认 accepted/running/succeeded、其余一律当失败,`reason` 又恰好是
    // `underlay_changed` ⇒ 菜单在一台 `bx status` 报 Protected、隧道健康的机器上
    // 显示 **Blocked / Recovery failed**。
    //
    // 后果不止于难看:恢复浮层建完 header/Status/Recovery 与一个动作项就
    // `return`,于是 Turn Off、Traffic by App、退出**全部消失** —— 保护开着,
    // 而用户从菜单里找不到任何出口。那正是这个项目为 2026-08-04 那次 71 分钟
    // 事故立下的规矩要防的形状。
    if snapshot.state == "idle" || snapshot.state == "ignored" {
        return nil
    }
    if snapshot.state == "succeeded" && !allowsTerminalSuccess {
        return nil
    }
    return snapshot
}

func recoveryFailureTransition(from snapshot: RecoverySnapshot, errorCode: String) -> RecoveryFailureTransition {
    RecoveryFailureTransition(
        snapshot: RecoverySnapshot(
            recoveryID: snapshot.recoveryID,
            state: "failed",
            stage: "failed",
            reason: snapshot.reason,
            generation: snapshot.generation,
            lastErrorCode: errorCode,
            detail: nil,
            attempt: snapshot.attempt,
            startedAt: snapshot.startedAt,
            updatedAt: snapshot.updatedAt
        ),
        reconnectInFlight: false
    )
}

func recoveryObservationFailure(
    submitted: RecoverySnapshot,
    observed: RecoverySnapshot
) -> RecoveryFailureTransition? {
    guard observed.recoveryID != submitted.recoveryID else {
        return nil
    }
    return recoveryFailureTransition(from: submitted, errorCode: "recovery_replaced")
}

func recoveryPresentation(for snapshot: RecoverySnapshot) -> RecoveryPresentation {
    switch snapshot.state {
    case "accepted":
        return RecoveryPresentation(
            title: "Reconnecting",
            indicator: .yellow,
            shortReason: recoveryStageReason(snapshot.stage),
            showsSuccessAlert: false
        )
    case "running":
        return RecoveryPresentation(
            title: "Reconnecting",
            indicator: .yellow,
            shortReason: recoveryStageReason(snapshot.stage),
            showsSuccessAlert: false
        )
    case "succeeded":
        return RecoveryPresentation(
            title: "Reconnected",
            indicator: .green,
            shortReason: nil,
            showsSuccessAlert: false
        )
    case "failed", "blocked":
        let title = snapshot.reason == "underlay_changed" ? "Blocked" : "Reconnect Failed"
        return RecoveryPresentation(
            title: title,
            indicator: .red,
            shortReason: recoveryFailureReason(snapshot),
            showsSuccessAlert: false
        )
    default:
        // **认不出来的 state 不许被说成「失败」。**
        //
        // 这个 switch 原本是 `default:` 直接落红 —— 于是任何一个 Swift 这边没
        // 列举到的 Go 状态都被当成失败宣告出去。`ignored` 就是这么在一台完全
        // 健康的机器上被渲染成 **Blocked / Recovery failed** 的(它现在已在
        // `recoverySnapshotForDisplay` 里被过滤掉,但那只治了一个实例;把
        // `default` 留在「失败」上,下一个新状态会原样重演)。
        //
        // 与本仓库 `observe.Tristate`、`leakcheck.NotChecked` 同一条:**「没认出来」
        // 是第三种答案,不是那两种里更坏的那一个。** 用黄色而不是红色,因为
        // 红色会连带一个它给不出的断言(失败原因);而**把实际的 state 名字打出来**
        // 是刻意的 —— 一条正常时永不出现的路径一旦出现,它本身就是信号,而它得
        // 让看到的人有东西可查。
        return RecoveryPresentation(
            title: "Recovery",
            indicator: .yellow,
            shortReason: "Unrecognized recovery state (\(snapshot.state))",
            showsSuccessAlert: false
        )
    }
}

private func recoveryStageReason(_ stage: String) -> String {
    switch stage {
    case "queued":
        return "Waiting for Guardian"
    case "observe":
        return "Checking network path"
    case "validate_capture":
        return "Validating protected path"
    case "rebind_underlay":
        return "Rebinding network path"
    case "transport_health":
        return "Checking protected transport"
    case "commit":
        return "Applying protected path"
    case "verify":
        return "Verifying protection"
    default:
        return "Reconnecting protected path"
    }
}

private func recoveryFailureReason(_ snapshot: RecoverySnapshot) -> String {
    // **刚换过网络、而隧道够不着服务器 —— 这一种单独说话。**
    //
    // 起因是一次真实排查:酒店/咖啡店 Wi-Fi 下 `bx down && bx up` 成了必修课,而
    // 家里的 Wi-Fi 重连从来不用。Guardian 日志(2026-08-18)坐实了机制:换网络触发
    // 的恢复连续 20 次全部停在 transport_health,重试 11 分钟后放弃。真正卡住人的
    // 是**强制门户够不着** —— bx 接管了 DNS、境外流量走隧道被 kill-switch 拦下,
    // 于是酒店网关根本没看见你的请求,那个「点击同意」的页面永远不会弹出来。
    //
    // 判据窄是刻意的(reason 与 error code 两个都要对):一条到处都出现的提示会被
    // 训练成墙纸,而它要提醒的事一年遇不到几次。措辞只给可能性(`often`),不断言
    // 这就是门户 —— bx 分不清「门户」与「服务器真挂了」,与 `Protection may be off.`
    // 同一条纪律。完整的做法(先试网关、再退到 down/up)在 `bx status` 里,菜单这
    // 一行放不下。
    if recoveryLooksLikeCaptiveNetwork(snapshot) {
        return "Can't reach the server — cafés and hotels often need sign-in first"
    }
    switch snapshot.lastErrorCode {
    case "capture_invalid":
        return "Protected path changed"
    case "capture_missing":
        return "Protected path unavailable"
    case "network_unavailable":
        return "Network unavailable"
    case "recovery_canceled":
        return "Recovery canceled"
    case "recovery_unavailable":
        return "Recovery unavailable"
    case "recovery_replaced":
        return "Recovery was replaced"
    case "transport_unavailable":
        return "Protected transport unavailable"
    case "underlay_rebind_failed":
        return "Could not rebind network path"
    case "underlay_unavailable":
        return "Network path unavailable"
    case "verification_failed":
        return "Protection verification failed"
    default:
        return "Recovery failed"
    }
}

/// 这次失败**看起来**像「这个网络要先登录」(咖啡馆/酒店 Wi-Fi 的强制门户)。
///
/// 判据窄到只有一种组合:`underlay_changed`(换网络触发的恢复)且
/// `transport_unavailable`(路由那半已经重绑成功、卡在传输健康那一步 —— Rebind 排在
/// transport_health 之前,走到这个码就说明路由没问题)。别的失败不给这句话:一条到处
/// 出现的提示会被训练成墙纸,而它要提醒的事一年遇不到几次。
///
/// **只是「看起来像」。** bx 分不清「强制门户」与「服务器真挂了 / 运营商在拦」,
/// 措辞与这里的函数名都按可能性写,不断言。
func recoveryLooksLikeCaptiveNetwork(_ snapshot: RecoverySnapshot) -> Bool {
    snapshot.reason == "underlay_changed" && snapshot.lastErrorCode == "transport_unavailable"
}

/// 从 `route -n get default` 的输出里取出网关,拼成登录页的地址。
///
/// **为什么这条路不用关掉保护**:私网恒直连(`route.DefaultPrivateCIDRs`,kill-switch
/// 只拦 Proxy 判定),而网关通常还在本网段、按最长前缀根本不会进 TUN。门户之所以弹
/// 不出来,是**发现机制**(DNS 劫持 + HTTP 重定向)被 bx 接管了,不是那条路被堵死。
/// 由 `TestPrivateStaysDirectWhileTheTunnelIsDownButPublicIsBlocked`(Go 侧,带对照组)
/// 钉住这个前提。
///
/// **只接受私网地址,这是一条安全判断不是洁癖。** 网关若是公网 IP(某些网络确实如此),
/// 打开它就是在隧道外发一个明文 HTTP 请求 —— 那正是 bx 存在的理由要防的事。宁可
/// 不给这个按钮,也不给一个会漏的按钮。
func wifiSignInURL(fromRouteOutput output: String) -> URL? {
    var gateway: String?
    for line in output.split(separator: "\n") {
        let parts = line.split(separator: ":", maxSplits: 1)
        guard parts.count == 2, parts[0].trimmingCharacters(in: .whitespaces) == "gateway" else { continue }
        gateway = parts[1].trimmingCharacters(in: .whitespaces)
    }
    guard let gateway, isPrivateIPv4(gateway) else { return nil }
    return URL(string: "http://\(gateway)/")
}

/// 是不是 RFC1918 / CGNAT / link-local 的 IPv4 字面量。
///
/// 手写解析而不是借 Foundation:这一段是安全判断,判据必须看得见、测得到。
/// IPv6 一律不接受 —— bx 的 v6 是 fail-closed 阻断的,给一个 v6 地址等于给一个
/// 打不开的按钮。
func isPrivateIPv4(_ text: String) -> Bool {
    let parts = text.split(separator: ".", omittingEmptySubsequences: false)
    guard parts.count == 4 else { return false }
    var octets: [Int] = []
    for part in parts {
        guard !part.isEmpty, part.count <= 3, part.allSatisfy(\.isNumber),
              let value = Int(part), value >= 0, value <= 255 else { return false }
        octets.append(value)
    }
    switch (octets[0], octets[1]) {
    case (10, _): return true
    case (192, 168): return true
    case (172, 16...31): return true
    case (100, 64...127): return true // CGNAT:酒店网络常见,仍然不是公网
    case (169, 254): return true      // link-local
    default: return false
    }
}
