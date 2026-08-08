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

    /// BxReport 有自定义 init(from:),Swift 因此不合成 memberwise init ——
    /// 只能从 JSON 解码。这反而是好事:测试吃的是真实的 `bx status --json` 形状。
    static func decode(_ json: String) -> BxReport {
        try! JSONDecoder().decode(BxReport.self, from: Data(json.utf8))
    }

    static func main() {
        let healthy = decode("""
        {"tunnel_healthy":true,"latency_ms":390,"active":47,
         "server":"vps","transport":"reality@vps","udp_mode":"hysteria2"}
        """)
        let set = menuRows(report: healthy, dns: "127.0.0.1")

        // 阶段②能上的行
        expect(row(set, "线路")?.value.contains("reality") == true,
               "线路行应显示传输,实际 \(String(describing: row(set, "线路")))")
        expect(row(set, "延迟")?.value == "390 ms",
               "延迟行,实际 \(String(describing: row(set, "延迟")?.value))")
        expect(row(set, "UDP 中继")?.value == "hysteria2",
               "UDP 中继行,实际 \(String(describing: row(set, "UDP 中继")?.value))")
        expect(row(set, "DNS")?.mark == .ok, "DNS 已知时应为 ok")

        // 阶段③的行:必须在场、必须是 unknown、必须不算异常
        for pending in ["出口位置", "IPv6 泄漏", "WebRTC"] {
            guard let r = row(set, pending) else {
                expect(false, "\(pending) 行必须在场(阶段③点亮),现在缺席"); continue
            }
            expect(r.mark == .unknown, "\(pending) 在阶段②必须是 unknown,实际 \(r.mark)")
            expect(r.value == "未观测", "\(pending) 的占位文案应为「未观测」,实际 \(r.value)")
        }
        expect(set.anomalyCount == 0,
               "全部正常 + 三行未观测时异常数必须为 0,实际 \(set.anomalyCount) —— 未观测不是异常")

        // 隧道不健康是真异常
        let unhealthy = decode("""
        {"tunnel_healthy":false,"latency_ms":0,"active":0,
         "server":"vps","transport":"reality@vps","udp_mode":"hysteria2"}
        """)
        let bad = menuRows(report: unhealthy, dns: "127.0.0.1")
        expect(bad.anomalyCount >= 1, "隧道不健康必须计入异常,实际 \(bad.anomalyCount)")
        expect(bad.rows.contains { $0.mark == .bad }, "必须有一行标记为 bad")

        // DNS 未知不是异常,只是未观测
        let noDNS = menuRows(report: healthy, dns: nil)
        expect(row(noDNS, "DNS")?.mark == .unknown, "DNS 取不到时应为 unknown 而非 bad")
        expect(noDNS.anomalyCount == 0, "DNS 未观测不得计入异常,实际 \(noDNS.anomalyCount)")

        // 完全没有报告(bx 没跑)时不得崩,也不得谎报正常
        let none = menuRows(report: nil, dns: nil)
        expect(none.rows.allSatisfy { $0.mark == .unknown },
               "没有报告时所有行都应是 unknown")
        expect(none.anomalyCount == 0, "没有报告不等于有异常")

        if failures == 0 { print("MenuRowsTests passed") } else { exit(1) }
    }
}
