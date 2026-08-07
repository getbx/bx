import Foundation

@main
struct StatusReportTests {
    static func main() {
        // 这份 JSON 是 `sudo bx down` 之后 `bx status --json` 的**真实**输出,
        // 由 internal/cli 的一次性探针抓取(2026-08-06)。Core 已退出,于是
        // clientStatusReport 内嵌的 *stats.Report 是 nil,Go 省略了整组 Core 字段:
        // 没有 tunnel_healthy、latency_ms、restarts、active、proxy、direct、blocked。
        //
        // 此前 BxReport 把这些声明成非可选,JSONDecoder 因缺键直接抛错,菜单于是
        // 落到 .warning("Status unreadable") —— 黄灯。而报告里明明白白写着
        // protection_state:"off"。真机症状:bx down 后指示灯是黄的不是灰的,
        // 且 .warning 分支不提供 Start Protection,用户只能回去敲 sudo bx up。
        let stoppedJSON = """
        {"core_available":false,"core_evidence":"unavailable","desired":"off",\
        "protection_state":"off","network_generation":"",\
        "recovery":{"recovery_id":"","state":"","stage":"","reason":"","attempt":0,\
        "started_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"},\
        "dns_state":"unmanaged","dns_managed":false}
        """

        guard let stopped = try? JSONDecoder().decode(BxReport.self, from: Data(stoppedJSON.utf8)) else {
            fail("bx down 之后的 partial 报告必须能解码 —— 缺 Core 字段不该让整份报告作废")
            exit(1)
        }
        expect(stopped.protectionState == "off", "partial 报告带 protection_state=off")
        expect(!stopped.tunnelHealthy, "缺 tunnel_healthy 时按 false 处理")
        expect(stopped.latencyMS == 0, "缺 latency_ms 时按 0 处理")
        expect(!stopped.coreAvailable, "core_available=false 必须被读到")
        expect(menuProtectionVerdict(stopped) == .off,
               "保护被用户主动关掉时判定为 off,而不是某种告警")

        // 保护正常时不得被误判
        let healthyJSON = """
        {"core_available":true,"core_evidence":"local_status_socket","desired":"on",\
        "protection_state":"protected","tunnel_healthy":true,"latency_ms":403,\
        "restarts":0,"active":4,"proxy":4,"direct":14,"blocked":0,\
        "dns_state":"managed","dns_managed":true,"dns_service":"Wi-Fi"}
        """
        guard let healthy = try? JSONDecoder().decode(BxReport.self, from: Data(healthyJSON.utf8)) else {
            fail("完整报告必须能解码")
            exit(1)
        }
        expect(healthy.tunnelHealthy, "完整报告的 tunnel_healthy 照常读出")
        expect(healthy.latencyMS == 403, "完整报告的 latency_ms 照常读出")
        expect(menuProtectionVerdict(healthy) == .healthy, "protected + 隧道健康 = healthy")

        // Guardian 明确报告的异常态必须原样透出,不能被 off 分支吞掉
        expect(menuProtectionVerdict(makeReport(protectionState: "blocked", coreAvailable: true, tunnelHealthy: false))
               == .attention("Blocked"), "blocked 透出为 Blocked")
        expect(menuProtectionVerdict(makeReport(protectionState: "needs_attention", coreAvailable: true, tunnelHealthy: true))
               == .attention("Repair Required"), "needs_attention 透出为 Repair Required")

        // Guardian 说在保护,但 Core 不见了 —— 这是真异常,绝不能报成 off。
        // 把它误判为 off 会让用户以为自己关掉了保护,而实际是 Core 崩了。
        expect(menuProtectionVerdict(makeReport(protectionState: "protected", coreAvailable: false, tunnelHealthy: false))
               == .attention("Core unavailable"), "声称保护中但 Core 不在 = 告警,不是 off")

        // 隧道不健康仍是告警
        expect(menuProtectionVerdict(makeReport(protectionState: "protected", coreAvailable: true, tunnelHealthy: false))
               == .attention("Tunnel unhealthy"), "隧道不健康 = 告警")

        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
        print("StatusReportTests passed")
    }

    private static func makeReport(protectionState: String, coreAvailable: Bool, tunnelHealthy: Bool) -> BxReport {
        let json = """
        {"core_available":\(coreAvailable),"protection_state":"\(protectionState)",\
        "tunnel_healthy":\(tunnelHealthy)}
        """
        // swiftlint:disable:next force_try
        return try! JSONDecoder().decode(BxReport.self, from: Data(json.utf8))
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
