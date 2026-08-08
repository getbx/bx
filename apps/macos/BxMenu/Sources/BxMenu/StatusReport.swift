import Foundation

/// BxReport 是 `bx status --json` 的菜单栏视图。
///
/// **Core 相关的字段全部按「缺失即默认」解码,不能声明成非可选。** `bx down` 之后
/// Guardian 仍然活着,`bx status --json` 照常成功,但 Core 已退出——Go 侧
/// clientStatusReport 内嵌的 `*stats.Report` 是 nil,于是整组 Core 字段
/// (tunnel_healthy / latency_ms / restarts / active / proxy / direct / blocked)
/// 在 JSON 里根本不出现。把它们声明成非可选会让 JSONDecoder 因缺键抛错,**整份
/// 报告作废**——而报告里明明写着 `protection_state: "off"`。真机症状(2026-08-06):
/// bx down 后菜单指示灯是黄的而非灰的,且落进 .warning 分支后连 Start Protection
/// 都没有,用户只能回去敲 sudo bx up。
struct BxReport: Decodable {
    let tunnelHealthy: Bool
    let latencyMS: Int64
    let restarts: Int
    let udpMode: String?
    let udpNote: String?
    let active: Int64
    let proxy: Int64
    let direct: Int64
    let blocked: Int64
    let coreAvailable: Bool
    let desired: String?
    let protectionState: String?
    let networkGeneration: String?
    let recovery: RecoverySnapshot?
    let phase: String?
    let coreVersion: String?
    let guardianVersion: String?
    let runtimeVersion: String?
    let dnsState: String?
    let dnsManaged: Bool?
    let dnsService: String?
    let server: String?
    let transport: String?

    enum CodingKeys: String, CodingKey {
        case tunnelHealthy = "tunnel_healthy"
        case latencyMS = "latency_ms"
        case udpMode = "udp_mode"
        case udpNote = "udp_note"
        case coreAvailable = "core_available"
        case protectionState = "protection_state"
        case networkGeneration = "network_generation"
        case coreVersion = "core_version"
        case guardianVersion = "guardian_version"
        case runtimeVersion = "runtime_version"
        case dnsState = "dns_state"
        case dnsManaged = "dns_managed"
        case dnsService = "dns_service"
        case server, transport
        case restarts, active, proxy, direct, blocked, recovery, phase, desired
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        tunnelHealthy = try container.decodeIfPresent(Bool.self, forKey: .tunnelHealthy) ?? false
        latencyMS = try container.decodeIfPresent(Int64.self, forKey: .latencyMS) ?? 0
        restarts = try container.decodeIfPresent(Int.self, forKey: .restarts) ?? 0
        udpMode = try container.decodeIfPresent(String.self, forKey: .udpMode)
        udpNote = try container.decodeIfPresent(String.self, forKey: .udpNote)
        active = try container.decodeIfPresent(Int64.self, forKey: .active) ?? 0
        proxy = try container.decodeIfPresent(Int64.self, forKey: .proxy) ?? 0
        direct = try container.decodeIfPresent(Int64.self, forKey: .direct) ?? 0
        blocked = try container.decodeIfPresent(Int64.self, forKey: .blocked) ?? 0
        coreAvailable = try container.decodeIfPresent(Bool.self, forKey: .coreAvailable) ?? false
        desired = try container.decodeIfPresent(String.self, forKey: .desired)
        protectionState = try container.decodeIfPresent(String.self, forKey: .protectionState)
        networkGeneration = try container.decodeIfPresent(String.self, forKey: .networkGeneration)
        recovery = try container.decodeIfPresent(RecoverySnapshot.self, forKey: .recovery)
        phase = try container.decodeIfPresent(String.self, forKey: .phase)
        coreVersion = try container.decodeIfPresent(String.self, forKey: .coreVersion)
        guardianVersion = try container.decodeIfPresent(String.self, forKey: .guardianVersion)
        runtimeVersion = try container.decodeIfPresent(String.self, forKey: .runtimeVersion)
        dnsState = try container.decodeIfPresent(String.self, forKey: .dnsState)
        dnsManaged = try container.decodeIfPresent(Bool.self, forKey: .dnsManaged)
        dnsService = try container.decodeIfPresent(String.self, forKey: .dnsService)
        server = try container.decodeIfPresent(String.self, forKey: .server)
        transport = try container.decodeIfPresent(String.self, forKey: .transport)
    }
}

/// MenuProtectionVerdict 是「这份报告该让菜单显示什么」的判定。
enum MenuProtectionVerdict: Equatable {
    /// 保护被用户主动关掉。菜单应显示 Off 并提供 Start Protection。
    case off
    /// 出了需要用户知道的问题;附带的字符串就是指示灯 tooltip 里那句原因。
    case attention(String)
    /// 一切正常,可以继续走 DNS 判定与 connected。
    case healthy
}

/// menuProtectionVerdict 把一份状态报告归到三类之一。
///
/// 顺序是有讲究的:`off` 必须先判,因为 Core 已经退出、隧道当然不健康——
/// 若先看 tunnelHealthy 就会把「用户自己关的」报成「隧道坏了」,而那个分支
/// 不提供 Start Protection,用户就被困住了。
///
/// 反过来,「Guardian 说在保护但 Core 不见了」绝不能报成 off:那是 Core 崩了,
/// 报成 off 会让用户以为是自己关的,从而不去排查。
func menuProtectionVerdict(_ report: BxReport) -> MenuProtectionVerdict {
    switch report.protectionState {
    case "off":
        return .off
    case "needs_attention":
        return .attention("Repair Required")
    case "blocked":
        return .attention("Blocked")
    default:
        break
    }
    if !report.coreAvailable {
        return .attention("Core unavailable")
    }
    if !report.tunnelHealthy {
        return .attention("Tunnel unhealthy")
    }
    return .healthy
}
