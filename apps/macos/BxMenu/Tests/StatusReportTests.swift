import Foundation

@main
struct StatusReportTests {
    static func main() {
        // 这份 JSON 是 `sudo bx down` 之后 Guardian `/v1/status` 的形状:Guardian
        // 自己还活着、照常应答,但 Core 已经退出。Go 侧 `Status.Core` 带
        // `omitempty`,取数函数没接上时整个 `core` 键根本不出现。
        //
        // 判定必须从这份**残缺**的报告里读出 protection_state:"off"。此前(数据源
        // 还是 `bx status --json` 时)BxReport 把 Core 那组字段声明成非可选,
        // JSONDecoder 因缺键直接抛错,菜单于是落到 .warning("Status unreadable")
        // —— 黄灯。真机症状:bx down 后指示灯是黄的不是灰的,且 .warning 分支不
        // 提供 Start Protection,用户只能回去敲 sudo bx up。换数据源不改变这条
        // 教训:缺席必须解得动。
        let stoppedJSON = """
        {"schema_version":1,"desired":"off","phase":"idle",\
        "protection_state":"off","network_generation":"",\
        "recovery":{"recovery_id":"","state":"","stage":"","reason":"","attempt":0,\
        "started_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"},\
        "dns_state":"unmanaged","dns_managed":false}
        """

        guard let stopped = try? JSONDecoder().decode(GuardianStatus.self, from: Data(stoppedJSON.utf8)) else {
            fail("bx down 之后 core 缺席的报告必须能解码 —— 缺 Core 字段不该让整份报告作废")
            exit(1)
        }
        expect(stopped.protectionState == "off", "partial 报告带 protection_state=off")
        expect(stopped.core == nil, "core 键缺席时必须是 nil —— 「没问过」不是「答案是坏的」")
        expect(menuProtectionVerdict(stopped) == .off,
               "保护被用户主动关掉时判定为 off,而不是某种告警")

        // 保护正常时不得被误判
        let healthyJSON = """
        {"schema_version":1,"desired":"on","phase":"idle",\
        "protection_state":"protected",\
        "core":{"reachable":true,"tunnel_healthy":true,"latency_ms":403,\
        "server":"vps","transport":"reality@vps","udp_mode":"proxy"},\
        "dns_state":"managed","dns_managed":true,"dns_service":"Wi-Fi"}
        """
        guard let healthy = try? JSONDecoder().decode(GuardianStatus.self, from: Data(healthyJSON.utf8)) else {
            fail("完整报告必须能解码")
            exit(1)
        }
        expect(healthy.core?.tunnelHealthy == true, "完整报告的 tunnel_healthy 照常读出")
        expect(healthy.core?.latencyMS == 403, "完整报告的 latency_ms 照常读出")
        expect(menuProtectionVerdict(healthy) == .healthy, "protected + 隧道健康 = healthy")

        // Guardian 明确报告的异常态必须原样透出,不能被 off 分支吞掉
        expect(menuProtectionVerdict(makeStatus(protectionState: "blocked", core: reachable(tunnelHealthy: false)))
               == .attention("Blocked"), "blocked 透出为 Blocked")
        expect(menuProtectionVerdict(makeStatus(protectionState: "needs_attention", core: reachable(tunnelHealthy: true)))
               == .attention("Repair Required"), "needs_attention 透出为 Repair Required")

        // Guardian 说在保护,但 Core 不见了 —— 这是真异常,绝不能报成 off。
        // 把它误判为 off 会让用户以为自己关掉了保护,而实际是 Core 崩了。
        //
        // `core_available` 此前由 CLI 推导(CLI 自己去拨 Core 的控制 socket,拨通
        // 即 true);`core.reachable` 是同一个事实的原产地 —— Guardian 拨的是同一
        // 个 socket。故这是等价替换,不是近似。
        expect(menuProtectionVerdict(makeStatus(protectionState: "protected", core: #"{"reachable":false,"tunnel_healthy":false,"latency_ms":0}"#))
               == .attention("Core unavailable"), "声称保护中但 Core 不在 = 告警,不是 off")

        // **问了没答** 与 **压根没问** 必须分得开。core 整个缺席意味着 Guardian
        // 没接取数函数(比如升级窗口里旧 Guardian 还在跑),那时它对 Core 一无所知
        // —— 报成 "Core unavailable" 是一句它无权说的话,报成 healthy 更是撒谎。
        let neverAsked = menuProtectionVerdict(makeStatus(protectionState: "protected", core: nil))
        expect(neverAsked != .healthy, "没问过 Core 就不能说健康")
        expect(neverAsked != .attention("Core unavailable"),
               "「没问过」不得与「问了没答」塌缩成同一句话")
        expect(neverAsked == .attention("Core status unavailable"),
               "没问过 Core 时应如实说状态未知,实际 \(neverAsked)")

        // 键缺席同样是「不知道」。`tunnel_healthy` 没出现时判 "Tunnel unhealthy"
        // 就是拿一个缺失的键造出一个自信的坏答案 —— 而它会让指示灯裂开、让用户
        // 去排查一个 Guardian 从没说过的故障。
        let partialCore = menuProtectionVerdict(makeStatus(protectionState: "protected", core: #"{"reachable":true}"#))
        expect(partialCore != .healthy, "tunnel_healthy 缺席时不能说健康")
        expect(partialCore != .attention("Tunnel unhealthy"),
               "tunnel_healthy 缺席不是「隧道坏了」,实际 \(partialCore)")
        expect(partialCore == .attention("Core status unavailable"),
               "键缺席应归入「问不出来」,实际 \(partialCore)")

        // 隧道不健康仍是告警
        expect(menuProtectionVerdict(makeStatus(protectionState: "protected", core: reachable(tunnelHealthy: false)))
               == .attention("Tunnel unhealthy"), "隧道不健康 = 告警")

        // ── 能力由 Guardian 声明,不再靠解析 `bx logs --help` ────────────────
        //
        // 此前菜单 spawn `bx logs --help`,在帮助文本里找 `--archive`/`--dir` 来判断
        // 装着的 CLI 支不支持诊断归档 —— 一个 UI 靠解析另一个程序的帮助文本做特性
        // 探测,是整份控制面架构诊断里最直白的症状。而且它问错了对象:被问的是
        // /usr/local/bin/bx,可能是旧版;真正知道答案的是正在应答你的那个 Guardian。
        expect(declaresDiagnosticsArchive(["diagnostics_archive"]),
               "声明了就是支持")
        expect(declaresDiagnosticsArchive(["something_else", "diagnostics_archive"]),
               "混在别的能力里也要认出来")
        expect(!declaresDiagnosticsArchive(["something_else"]),
               "声明了但不含 = 这一版确实没有这个能力")
        expect(!declaresDiagnosticsArchive([]),
               "声明了一个都没有 = 不支持")
        // **nil 与 [] 不同源,但结论必须同为「不支持」。** nil 只可能来自本次契约
        // 之前的旧 Guardian(Go 侧刻意没给 capabilities 加 omitempty,健康的新版
        // 永远给得出这个键),那本身就是「该升级了」。反过来把「没说」当成「有」,
        // 会在真的缺能力时让 Run Doctor 收集不到诊断包 —— 恰恰是用户最需要它的时刻。
        expect(!declaresDiagnosticsArchive(nil),
               "键缺席(旧 Guardian)必须判不支持 —— 「没说」不是「有」")
        // 端到端:能力经真实解码抵达判定,不是靠测试自己造的数组。
        let declared = makeStatus(protectionState: "protected", core: nil, capabilities: #"["diagnostics_archive"]"#)
        expect(declaresDiagnosticsArchive(declared.capabilities), "capabilities 必须真的解得出来")
        expect(makeStatus(protectionState: "protected", core: nil, capabilities: nil).capabilities == nil,
               "键缺席时解成 nil,不得被默认成空数组 —— 那会抹掉「旧 Guardian」这个唯一信号")

        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
        print("StatusReportTests passed")
    }

    private static func reachable(tunnelHealthy: Bool) -> String {
        #"{"reachable":true,"tunnel_healthy":\#(tunnelHealthy),"latency_ms":390}"#
    }

    /// `core` 传 nil 表示 Guardian 的响应里根本没有这个键(没问过 Core);
    /// `capabilities` 传 nil 同理(这一版 Guardian 从没声明过能力)。
    private static func makeStatus(
        protectionState: String,
        core: String?,
        capabilities: String? = nil
    ) -> GuardianStatus {
        let coreField = core.map { ",\"core\":\($0)" } ?? ""
        let capabilitiesField = capabilities.map { ",\"capabilities\":\($0)" } ?? ""
        let json = """
        {"schema_version":1,"desired":"on","phase":"idle",\
        "protection_state":"\(protectionState)"\(coreField)\(capabilitiesField)}
        """
        // swiftlint:disable:next force_try
        return try! JSONDecoder().decode(GuardianStatus.self, from: Data(json.utf8))
    }

    private static var failures = 0

    private static func expect(_ condition: Bool, _ label: String) {
        if !condition { fail(label) }
    }

    private static func fail(_ label: String) {
        failures += 1
        FileHandle.standardError.write(Data("FAIL: \(label)\n".utf8))
    }
}
