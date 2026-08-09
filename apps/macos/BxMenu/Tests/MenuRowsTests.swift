import Foundation

@main
struct MenuRowsTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func row(_ set: MenuRowSet, _ label: String) -> MenuRow? {
        set.rows.first { $0.label == label }
    }

    /// GuardianStatus 有自定义 init(from:),Swift 因此不合成 memberwise init ——
    /// 只能从 JSON 解码。这反而是好事:测试吃的是真实的 `/v1/status` 形状。
    static func decode(_ json: String) -> GuardianStatus {
        try! JSONDecoder().decode(GuardianStatus.self, from: Data(json.utf8))
    }

    static func main() {
        let healthy = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"tunnel_healthy":true,"latency_ms":390,
                 "server":"vps","transport":"reality@vps","udp_mode":"hysteria2"}}
        """)
        let set = menuRows(status: healthy, dns: "127.0.0.1")

        // 阶段②能上的行
        expect(row(set, "Route")?.value.contains("reality") == true,
               "线路行应显示传输,实际 \(String(describing: row(set, "Route")))")
        expect(row(set, "Latency")?.value == "390 ms",
               "延迟行,实际 \(String(describing: row(set, "Latency")?.value))")
        expect(row(set, "UDP Relay")?.value == "hysteria2",
               "UDP 中继行,实际 \(String(describing: row(set, "UDP Relay")?.value))")
        expect(row(set, "DNS")?.mark == .ok, "DNS 已知时应为 ok")

        // 阶段③的行:必须在场、必须是 unknown、必须不算异常
        for pending in ["Exit Location", "IPv6 Leak", "WebRTC"] {
            guard let r = row(set, pending) else {
                expect(false, "\(pending) 行必须在场(阶段③点亮),现在缺席"); continue
            }
            expect(r.mark == .unknown, "\(pending) 在阶段②必须是 unknown,实际 \(r.mark)")
            expect(r.value == "Not checked", "\(pending) 的占位文案应为「未观测」,实际 \(r.value)")
        }
        expect(set.anomalyCount == 0,
               "全部正常 + 三行未观测时异常数必须为 0,实际 \(set.anomalyCount) —— 未观测不是异常")

        // 隧道不健康是真异常
        let unhealthy = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"tunnel_healthy":false,"latency_ms":0,
                 "server":"vps","transport":"reality@vps","udp_mode":"hysteria2"}}
        """)
        let bad = menuRows(status: unhealthy, dns: "127.0.0.1")
        expect(bad.anomalyCount >= 1, "隧道不健康必须计入异常,实际 \(bad.anomalyCount)")
        expect(bad.rows.contains { $0.mark == .bad }, "必须有一行标记为 bad")

        // Guardian 问了、Core 没答:这**不是**「隧道坏了」。零值 tunnel_healthy
        // 被当成 bad 就是把「问不出来」压成「答案是坏的」—— 三态存在的全部理由。
        let unreachable = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":false,"tunnel_healthy":false,"latency_ms":0}}
        """)
        let silent = menuRows(status: unreachable, dns: "127.0.0.1")
        expect(row(silent, "Latency")?.mark == .unknown,
               "Core 没答时延迟是未观测,不是 bad,实际 \(String(describing: row(silent, "Latency")))")
        expect(silent.anomalyCount == 0, "Core 没答不得被计成隧道异常,实际 \(silent.anomalyCount)")

        // Guardian 压根没问过 Core(core 键缺席):同样只是未观测
        let neverAsked = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected"}
        """)
        let unasked = menuRows(status: neverAsked, dns: "127.0.0.1")
        expect(row(unasked, "Latency")?.mark == .unknown, "没问过 Core 时延迟必须是 unknown")
        expect(row(unasked, "Route")?.mark == .unknown, "没问过 Core 时线路必须是 unknown")
        expect(unasked.anomalyCount == 0, "没问过不等于有异常,实际 \(unasked.anomalyCount)")

        // DNS 未知不是异常,只是未观测
        let noDNS = menuRows(status: healthy, dns: nil)
        expect(row(noDNS, "DNS")?.mark == .unknown, "DNS 取不到时应为 unknown 而非 bad")
        expect(noDNS.anomalyCount == 0, "DNS 未观测不得计入异常,实际 \(noDNS.anomalyCount)")

        // 完全没有报告(Guardian 都没问到)时不得崩,也不得谎报正常
        let none = menuRows(status: nil, dns: nil)
        expect(none.rows.allSatisfy { $0.mark == .unknown },
               "没有报告时所有行都应是 unknown")
        expect(none.anomalyCount == 0, "没有报告不等于有异常")

        if failures == 0 { print("MenuRowsTests passed") } else { exit(1) }
    }
}
