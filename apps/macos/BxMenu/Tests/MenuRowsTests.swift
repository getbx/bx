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
                 "server":"vps","transport":"reality@vps","udp_mode":"hysteria2",
                 "dns_upstream":"223.5.5.5 \u{1F1E8}\u{1F1F3}"}}
        """)
        let set = menuRows(status: healthy, dns: "127.0.0.1")
        // 直连解析器:Core 此刻正在用的那个,已由 Go 渲染好(含可证明的国旗)。
        expect(row(set, "Direct lookups")?.value.contains("223.5.5.5") == true,
               "直连解析器行,实际 \(String(describing: row(set, "Direct lookups")?.value))")

        // **问不出来时这一行必须整个消失,而不是留一句「未观测」。**
        // 旧 Guardian 不发布这个键,而那正是升级中途的常态 —— 那时留一行恒定的
        // "Not checked" 就是刚刚才删掉的那三行占位符换个名字回来。
        let noUpstream = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"tunnel_healthy":true,"latency_ms":390,
                 "server":"vps","transport":"reality@vps","udp_mode":"proxy"}}
        """)
        expect(row(menuRows(status: noUpstream, dns: "127.0.0.1"), "Direct lookups") == nil,
               "上游问不出来时不许留一行占位符")

        // 阶段②能上的行
        expect(row(set, "Route")?.value == "vps",
               "线路行应显示服务器名(用户认的是连到哪台),实际 \(String(describing: row(set, "Route")))")
        let serverless = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"tunnel_healthy":true,"transport":"reality@203.0.113.20"}}
        """)
        expect(row(menuRows(status: serverless, dns: nil), "Route")?.value == "reality@203.0.113.20",
               "没有服务器名时退回传输标识")
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

        // 有应用绕过 bx 以真实 IP 收发(A11):一行点名、标成异常(图标裂开),健康机器上一个字都没有。
        let leaking = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"tunnel_healthy":true,"latency_ms":390,"server":"vps","udp_mode":"proxy",
                 "bypassing_apps":["Google Chrome","steam_osx"]}}
        """)
        let leakSet = menuRows(status: leaking, dns: "127.0.0.1")
        expect(row(leakSet, "Outside bx")?.value == "Google Chrome, steam_osx — quit and reopen",
               "绕过 bx 的应用没被点名,实际 \(String(describing: row(leakSet, "Outside bx")))")
        expect(row(leakSet, "Outside bx")?.mark == .bad && leakSet.anomalyCount == 1,
               "一次真实泄漏必须是异常(让图标裂开)")
        expect(compactMenuRows(leakSet).contains { $0.label == "Outside bx" },
               "压缩后的菜单把泄漏那一行藏掉了")
        expect(row(set, "Outside bx") == nil, "没有绕过 bx 的连接时不许出现这一行")

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

        // ---- 紧凑显示(compactMenuRows):菜单里只摆一行「Via」,诊断行只在 ✗ 时露面 ----
        //
        // 「已连接」状态下此前五行数据里四行天天一个样(DNS / Direct lookups /
        // UDP Relay),按「只在真有问题时才占地方」它们不该常驻。**判据在这里,
        // 不在 rebuildMenu**:菜单那半在 CI 里编不了。anomalyCount 仍按完整集合算
        // (图标裂不裂不受显示压缩影响),这一条由下面最后一句钉住。
        let compact = compactMenuRows(set)
        expect(compact.count == 1, "健康时只该有一行,实际 \(compact.map(\.label))")
        expect(compact.first?.label == "Via", "那一行叫 Via,实际 \(String(describing: compact.first?.label))")
        expect(compact.first?.value == "vps · 390 ms", "Via 行合并服务器与延迟,实际 \(String(describing: compact.first?.value))")
        expect(compact.first?.mark == .ok, "健康时 Via 是 ok")

        let tunnelDown = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"tunnel_healthy":false,"server":"vps","transport":"reality@vps"}}
        """)
        let sick = compactMenuRows(menuRows(status: tunnelDown, dns: "127.0.0.1"))
        expect(sick.first?.value == "vps · Tunnel unhealthy", "隧道坏了要在 Via 行说出来,实际 \(String(describing: sick.first?.value))")
        expect(sick.first?.mark == .bad, "隧道坏了 Via 行是 bad")

        let blind = compactMenuRows(menuRows(status: nil, dns: nil))
        expect(blind.first?.label == "Via" && blind.first?.value == "Not checked" && blind.first?.mark == .unknown,
               "什么都问不出来时头一行是 Not checked,实际 \(blind.map { "\($0.label)=\($0.value)" })")
        // **这一行此前钉的是 `blind.count == 1`,现在不再成立,而那是刻意的。**
        // 压缩的判据从「不是 ✗ 就藏」改成「是 ok 才藏」之后,这份全 unknown 的
        // 输入会摆出三行 Not checked。它**在生产里到不了**:compactMenuRows 只在
        // `.connected` 那一支被调用,而那一支按构造要求 Core 答过话、隧道健康
        // (menuProtectionVerdict),Via 那半永远是 ok。留着这个入参组合只是因为
        // 纯函数不该对没见过的输入崩;真要它只摆一行,得给压缩层再加一条「headline
        // 自己也未知时别重复」的特例 —— 而一条特例就是一个真 unknown 的藏身处,
        // 换来的只是一个到不了的画面更好看。

        // 诊断行在 ✗ 时必须露面 —— 压缩的是「正常时的噪声」,不是「坏消息」。
        let withBadDNS = MenuRowSet(rows: set.rows.map {
            $0.label == "DNS" ? MenuRow(label: "DNS", value: "Not managed by bx", mark: .bad) : $0
        }, anomalyCount: 1)
        let shown = compactMenuRows(withBadDNS)
        expect(shown.contains { $0.label == "DNS" && $0.mark == .bad }, "坏掉的 DNS 行被压缩没了:\(shown.map(\.label))")
        expect(!shown.contains { $0.label == "UDP Relay" }, "正常的 UDP Relay 不该露面")

        // **「没问出来」与「一切正常」在屏幕上必须长得不一样。**
        //
        // 压缩掉的是**正常时的噪声**(DNS / Direct lookups / UDP Relay 天天一个样);
        // `.unknown` 不是正常,它是「这一项该有值、这次没拿到」。判据因此是
        // `== .ok`,不是 `!= .bad` —— 后者把两种沉默合成同一种,而在这个菜单里
        // 沉默恰恰读作「查过了,没事」。
        //
        // 「那会不会变成一行常驻的 Not checked?」不会,而且防线在**上一层**:
        // 一个可能结构性缺席的字段由 menuRows **整行不发**(Direct lookups 就是
        // 这么做的,上面那条 noUpstream 钉着),压缩层看到的 `.unknown` 因此是
        // 真的「问过了、没问出来」,不是一个永远答不上来的占位符。
        let udpUnknown = decode("""
        {"schema_version":1,"desired":"on","phase":"idle","protection_state":"protected",
         "core":{"reachable":true,"tunnel_healthy":true,"latency_ms":390,
                 "server":"vps","transport":"reality@vps",
                 "dns_upstream":"223.5.5.5 \u{1F1E8}\u{1F1F3}"}}
        """)
        func screen(_ rows: [MenuRow]) -> String {
            rows.map { "\($0.label)=\($0.value)" }.joined(separator: " | ")
        }
        let allKnown = compactMenuRows(menuRows(status: healthy, dns: "127.0.0.1"))
        let udpBlind = compactMenuRows(menuRows(status: udpUnknown, dns: "127.0.0.1"))
        expect(screen(allKnown) != screen(udpBlind),
               "UDP 中继问不出来那次与全部答上话那次在屏幕上一模一样(\(screen(allKnown)))——" +
               "「没问出来」被压成了「一切正常」")
        expect(udpBlind.contains { $0.label == "UDP Relay" && $0.value == "Not checked" },
               "问不出来的那一行必须露面,实际 \(screen(udpBlind))")
        expect(!allKnown.contains { $0.label == "UDP Relay" },
               "正常时那一行仍不该占地方,实际 \(screen(allKnown))")

        // 维护挂起是「为什么保护是这个样子」,压缩不动它,而且仍在最前。
        let compactPaused = compactMenuRows(paused)
        expect(compactPaused.first?.label == "Maintenance", "挂起行在紧凑显示里仍排最前,实际 \(compactPaused.map(\.label))")
        // 认不出的新行默认参与显示(吵的失效好过安静的失效)。
        let novel = MenuRowSet(rows: [MenuRow(label: "Something new", value: "x", mark: .ok)], anomalyCount: 0)
        expect(compactMenuRows(novel).contains { $0.label == "Something new" }, "新加的行不该被静默压掉")
        expect(set.anomalyCount == 0 && withBadDNS.anomalyCount == 1, "anomalyCount 由完整集合决定,与显示压缩无关")

        if failures == 0 { print("MenuRowsTests passed") } else { exit(1) }
    }
}
