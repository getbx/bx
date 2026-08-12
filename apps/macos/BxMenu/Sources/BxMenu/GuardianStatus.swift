import Foundation

/// Guardian `/v1/status`、`/v1/up`、`/v1/down` 响应体里菜单实际用到的字段。
///
/// 刻意只声明用得上的几个:Guardian 的 Status 还有 core_pid 等字段未声明,
/// `Decodable` 默认忽略未声明的键,新增字段不会让菜单解码失败。
///
/// **每个新字段都按「可能缺席」解码**(手写 `init(from:)`、逐个 `decodeIfPresent`)——
/// Guardian 的 JSON tag 里 `omitempty` 贯穿全篇(见 `internal/guardian/types.go` 的
/// `Status`),把缺席解成解码失败会让菜单在完全正常的情况下(比如没接 Core
/// provider、或旧版 Guardian 还没长出某个字段)瞎掉,这比它替换掉的「spawn 子
/// 进程解析」还要脆。`desired`/`phase`/`protectionState` 三个既有字段保持必需
/// (Go 侧无 omitempty、恒出现),行为不变。
struct GuardianStatus: Decodable {
    let desired: String
    let phase: String
    let protectionState: String
    /// 成功时服务端 omitempty 掉这个键,故为可选——不是「有但为空」。
    let lastError: String?
    /// Go 侧 `Recovery RecoverySnapshot` 没有 omitempty(值类型直接内嵌),理论上
    /// 恒出现;仍按可选解码,与本结构体其余字段同一套防御性规则。
    let recovery: RecoverySnapshot?
    let dnsState: String?
    let dnsManaged: Bool?
    let dnsService: String?
    let dnsServers: [String]?
    let coreVersion: String?
    /// nil == Guardian 没接 CoreRuntime provider(压根没问过 Core)。
    /// 非 nil 时再看 `.reachable`——那才是「问了但 Core 没答」与「答了」的分界。
    /// 绝不能让这两种「没有健康数据」的场景在 Swift 里塌缩成同一件事。
    let core: CoreRuntime?
    /// Guardian 声明自己支持哪些能力。**nil 与 `[]` 是两件事**:nil 是「这一版
    /// 压根没声明过能力」(旧 Guardian,键缺席),`[]` 是「声明了,一个都没有」。
    /// Go 侧刻意没给这个键加 omitempty 就是为了让这个区分成立。
    let capabilities: [String]?
    /// 正在生效的那次维护挂起。**nil 有两种来由,必须靠 `capabilities` 分开**:
    /// 声明了 `maintenance_hold` 能力而键缺席 = 「此刻没有挂起」;没声明能力 =
    /// 「这一版 Guardian 压根没有挂起这个概念」。后者下菜单不许断言「没有挂起」
    /// (见 declaresMaintenanceHold)。
    let maintenanceHold: MaintenanceHold?

    enum CodingKeys: String, CodingKey {
        case desired
        case phase
        case protectionState = "protection_state"
        case lastError = "last_error"
        case recovery
        case dnsState = "dns_state"
        case dnsManaged = "dns_managed"
        case dnsService = "dns_service"
        case dnsServers = "dns_servers"
        case coreVersion = "core_version"
        case core
        case capabilities
        case maintenanceHold = "maintenance_hold"
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        desired = try container.decode(String.self, forKey: .desired)
        phase = try container.decode(String.self, forKey: .phase)
        protectionState = try container.decode(String.self, forKey: .protectionState)
        lastError = try container.decodeIfPresent(String.self, forKey: .lastError)
        recovery = try container.decodeIfPresent(RecoverySnapshot.self, forKey: .recovery)
        dnsState = try container.decodeIfPresent(String.self, forKey: .dnsState)
        dnsManaged = try container.decodeIfPresent(Bool.self, forKey: .dnsManaged)
        dnsService = try container.decodeIfPresent(String.self, forKey: .dnsService)
        dnsServers = try container.decodeIfPresent([String].self, forKey: .dnsServers)
        coreVersion = try container.decodeIfPresent(String.self, forKey: .coreVersion)
        core = try container.decodeIfPresent(CoreRuntime.self, forKey: .core)
        capabilities = try container.decodeIfPresent([String].self, forKey: .capabilities)
        maintenanceHold = try container.decodeIfPresent(MaintenanceHold.self, forKey: .maintenanceHold)
    }
}

/// Guardian 正在生效的那次维护挂起(`internal/guardian.MaintenanceHoldStatus` 的镜像)。
///
/// **两个字段都可选。** Go 侧今天没给它们 omitempty,但那是**生产者当下的选择**,
/// 不是本结构体能依赖的保证:哪天有人加上,菜单会因为缺一个键而整份 GuardianStatus
/// 解码失败 → 落到 "Status unreadable" —— 2026-08-06 那个失明 bug 换个层级重演。
///
/// `expiresAt` 保持**字符串**:解析放在 MaintenancePresentation.swift 里做,那里
/// 能对着 Go 真实发出的形状(带小数秒、带数字时区偏移)被测试钉住;在这里解成
/// Date 会把「时间格式解不动」变成「整份状态解不动」,又是同一个失明。
struct MaintenanceHold: Decodable {
    let reason: String?
    let expiresAt: String?

    enum CodingKeys: String, CodingKey {
        case reason
        case expiresAt = "expires_at"
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        reason = try container.decodeIfPresent(String.self, forKey: .reason)
        expiresAt = try container.decodeIfPresent(String.self, forKey: .expiresAt)
    }
}

/// Guardian 代取的 Core 运行时统计——`internal/guardian.CoreRuntime` 的镜像。
///
/// **三个「必有」的键也按可选解码。** Go 侧今天没给 `reachable`/`tunnel_healthy`/
/// `latency_ms` 加 `omitempty`,但那是**生产者当下的选择**,不是本结构体能依赖的
/// 保证:哪天有人加上 `omitempty`,Go 侧的测试全都照样绿(它们 unmarshal 回同一个
/// 结构体),而菜单会因为缺一个键就整份 `GuardianStatus` 解码失败 → 落到
/// `.warning("Status unreadable")` —— 正是 2026-08-06 那个「bx down 后菜单变黄、
/// 连 Start Protection 都没有」的失明 bug 换个层级重演。`Status.Core` 里那三个键
/// 恒在场这件事改由 Go 侧一条对 marshal 结果的键存在性断言钉住(生产者自己的
/// 契约由生产者守),这里则**在任何形状下都解得动**。
///
/// 缺席 ⇒ `nil` ⇒ 「不知道」,消费侧一律不得把它读成一个自信的答案:
/// `tunnelHealthy == nil` 不是「隧道坏了」,`reachable == nil` 不是「Core 不在」。
///
/// `reachable = false` 时 Go 侧承诺其余字段全为零值——这里不额外校验,只如实
/// 解码;菜单侧判断健康与否必须先看 `reachable`,不能拿 `tunnelHealthy` 直接冒充。
struct CoreRuntime: Decodable {
    let reachable: Bool?
    let tunnelHealthy: Bool?
    let latencyMS: Int64?
    let server: String?
    let transport: String?
    let udpMode: String?
    let dnsUpstream: String?

    enum CodingKeys: String, CodingKey {
        case reachable
        case tunnelHealthy = "tunnel_healthy"
        case latencyMS = "latency_ms"
        case server
        case transport
        case udpMode = "udp_mode"
        case dnsUpstream = "dns_upstream"
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        reachable = try container.decodeIfPresent(Bool.self, forKey: .reachable)
        tunnelHealthy = try container.decodeIfPresent(Bool.self, forKey: .tunnelHealthy)
        latencyMS = try container.decodeIfPresent(Int64.self, forKey: .latencyMS)
        server = try container.decodeIfPresent(String.self, forKey: .server)
        transport = try container.decodeIfPresent(String.self, forKey: .transport)
        udpMode = try container.decodeIfPresent(String.self, forKey: .udpMode)
        dnsUpstream = try container.decodeIfPresent(String.self, forKey: .dnsUpstream)
    }
}
