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

        // **一台全部答上话的机器,不该有任何一行是「未观测」。**
        //
        // 这条守卫是翻过来的:它此前断言 Exit Location / IPv6 Leak / WebRTC 三行
        // 必须在场且恒为 "Not checked"。真机上用户看到的是三行空值,问的是
        // 「出口位置应该有值吧?」—— 占位行读起来是坏了,不是路线图,而恒「未检测」
        // 与恒绿是同一种失败:它宣传了一个不存在的能力。
        //
        // 现在钉的是**规则**而不是那三个名字:一行只有在这次真的没问出来时才配说
        // "Not checked"。谁再加一行永远答不上来的占位符,这里就会红。
        for r in set.rows where r.value == "Not checked" {
            expect(false, "「\(r.label)」在一台全部答上话的机器上仍是「未观测」——" +
                          "那不是一项检查,是占位符在冒充检查")
        }
        expect(set.anomalyCount == 0,
               "全部正常时异常数必须为 0,实际 \(set.anomalyCount) —— 未观测不是异常")

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

        // Core 答了,但 Guardian 没给 tunnel_healthy/latency_ms:同样只是未观测。
        // 拿缺席的键画一行 "Tunnel unhealthy ✗" 会让指示灯裂开在一个没人报告过
        // 的故障上。
        let partial = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"server":"vps","transport":"reality@vps"}}
        """)
        let thin = menuRows(status: partial, dns: "127.0.0.1")
        expect(row(thin, "Route")?.mark == .ok, "在场的字段照常点亮")
        expect(row(thin, "Latency")?.mark == .unknown,
               "tunnel_healthy 缺席时延迟必须是 unknown,实际 \(String(describing: row(thin, "Latency")))")
        expect(thin.anomalyCount == 0, "缺席的键不得被计成异常,实际 \(thin.anomalyCount)")

        // DNS 未知不是异常,只是未观测
        let noDNS = menuRows(status: healthy, dns: nil)
        expect(row(noDNS, "DNS")?.mark == .unknown, "DNS 取不到时应为 unknown 而非 bad")
        expect(noDNS.anomalyCount == 0, "DNS 未观测不得计入异常,实际 \(noDNS.anomalyCount)")

        // 完全没有报告(Guardian 都没问到)时不得崩,也不得谎报正常
        let none = menuRows(status: nil, dns: nil)
        expect(none.rows.allSatisfy { $0.mark == .unknown },
               "没有报告时所有行都应是 unknown")
        expect(none.anomalyCount == 0, "没有报告不等于有异常")

        // 没有挂起时这一行一个字都不占:一行常驻的「没有维护」会把它训练成噪声,
        // 而它一年里只该出现几分钟(与 Go 侧 writeClientMaintenanceHold 同一理由)。
        expect(row(set, "Maintenance") == nil, "没有挂起时不许有维护行")

        // 维护挂起在生效:它必须排在最前(它回答的是「为什么保护是这个样子」),
        // 且**不得**计进异常 —— 一次正常的升级不该让菜单栏图标裂开。
        // 供养这几条的生产改动:menuRows 开头那次 append(删掉即红)、
        // maintenanceRow 返回的 `.unknown`(改成 `.bad` 会让 anomalyCount 红)。
        let iso = ISO8601DateFormatter()
        let held = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"off",
         "capabilities":["maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade","expires_at":"2026-08-10T15:36:03.056964Z"}}
        """)
        let paused = menuRows(status: held, dns: nil, now: iso.date(from: "2026-08-10T15:21:33Z")!)
        expect(paused.rows.first?.label == "Maintenance",
               "挂起行必须排在最前,实际第一行是 \(String(describing: paused.rows.first?.label))")
        expect(paused.rows.first?.value.contains("upgrade") == true,
               "挂起行要说清是什么挂起,实际 \(String(describing: paused.rows.first?.value))")
        expect(paused.anomalyCount == 0,
               "维护挂起不是异常,不得让图标裂开,实际 \(paused.anomalyCount)")
        expect(row(paused, "Latency") != nil, "挂起行是加进来的,不是替换掉原有的数据行")

        if failures == 0 { print("MenuRowsTests passed") } else { exit(1) }
    }
}
