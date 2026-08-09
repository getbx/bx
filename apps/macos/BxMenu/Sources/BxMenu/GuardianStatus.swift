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
    let coreVersion: String?
    /// nil == Guardian 没接 CoreRuntime provider(压根没问过 Core)。
    /// 非 nil 时再看 `.reachable`——那才是「问了但 Core 没答」与「答了」的分界。
    /// 绝不能让这两种「没有健康数据」的场景在 Swift 里塌缩成同一件事。
    let core: CoreRuntime?

    enum CodingKeys: String, CodingKey {
        case desired
        case phase
        case protectionState = "protection_state"
        case lastError = "last_error"
        case recovery
        case dnsState = "dns_state"
        case dnsManaged = "dns_managed"
        case dnsService = "dns_service"
        case coreVersion = "core_version"
        case core
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
        coreVersion = try container.decodeIfPresent(String.self, forKey: .coreVersion)
        core = try container.decodeIfPresent(CoreRuntime.self, forKey: .core)
    }
}

/// Guardian 代取的 Core 运行时统计——`internal/guardian.CoreRuntime` 的镜像。
///
/// `reachable`、`tunnelHealthy`、`latencyMS` 在 Go 侧没有 `omitempty`,`core` 对象
/// 一旦存在就恒带这三个键,故按必需解码;`server`/`transport`/`udpMode` 有
/// `omitempty`(空字符串即省略),按可选解码。
///
/// `reachable = false` 时 Go 侧承诺其余字段全为零值——这里不额外校验,只如实
/// 解码;菜单侧判断健康与否必须先看 `reachable`,不能拿 `tunnelHealthy` 直接冒充。
struct CoreRuntime: Decodable {
    let reachable: Bool
    let tunnelHealthy: Bool
    let latencyMS: Int64
    let server: String?
    let transport: String?
    let udpMode: String?

    enum CodingKeys: String, CodingKey {
        case reachable
        case tunnelHealthy = "tunnel_healthy"
        case latencyMS = "latency_ms"
        case server
        case transport
        case udpMode = "udp_mode"
    }
}
