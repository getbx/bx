import Foundation

@main
struct ServersModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func fail(_ message: String) { expect(false, message) }

    // 服务器清单那一半的纯逻辑守卫。

    static func testServerListDecodesWhatGuardianSends() {
        let json = """
        {"servers":[
          {"name":"tokyo","host":"203.0.113.10","current":true},
          {"name":"osaka","host":"203.0.113.20","udp_host":"203.0.113.21"}],
         "current":"tokyo","config_path":"/etc/bx/config.yaml"}
        """
        guard let list = try? JSONDecoder().decode(ServerList.self, from: Data(json.utf8)) else {
            fail("解不出 /v1/servers 的应答"); return
        }
        expect(list.servers.count == 2, "清单长度 = \(list.servers.count)")
        expect(list.current == "tokyo", "current = \(list.current)")
        expect(list.servers[0].current, "第一台没标成当前")
        expect(list.servers[1].udpHost == "203.0.113.21", "UDP 出口没解出来")
        expect(list.configPath == "/etc/bx/config.yaml", "配置路径没解出来")
    }

    // **旧 Guardian 上不许画出入口。** 能力键缺席 = 那一版没有 /v1/servers,
    // 画出来的按钮每次点都会失败,而用户看不出为什么。
    static func testServerSwitchingNeedsTheCapability() {
        expect(!serverSwitchingAvailable(capabilities: nil), "能力未声明(旧版)却判成可用")
        expect(!serverSwitchingAvailable(capabilities: []), "空能力集却判成可用")
        expect(!serverSwitchingAvailable(capabilities: ["rules"]), "只有 rules 却判成可用")
        expect(serverSwitchingAvailable(capabilities: ["rules", "servers"]), "声明了 servers 却判成不可用")
    }

    // 副标题要显示出口主机;UDP 走另一台时必须单独标出来 —— 少了它,UDP 会静默
    // 从另一个 IP 出去,而界面上一个字都不说。
    static func testServerRowShowsWhereTrafficLeaves() {
        let rows = serverRows(from: ServerList(servers: [
            ServerEntry(name: "tokyo", host: "203.0.113.10", current: true),
            ServerEntry(name: "osaka", host: "203.0.113.20", udpHost: "203.0.113.21"),
            ServerEntry(name: "broken", host: ""),
        ]))
        expect(rows[0].detail == "203.0.113.10", "detail = \(rows[0].detail)")
        expect(rows[1].detail.contains("UDP → 203.0.113.21"), "UDP 出口没显示:\(rows[1].detail)")
        expect(rows[2].detail.contains("could not be parsed"), "坏链接没说出来:\(rows[2].detail)")
    }

    // 当前那台点了是空操作(看起来像坏了);主机解析不出来的那台切过去必然失败。
    static func testUnselectableRows() {
        let rows = serverRows(from: ServerList(servers: [
            ServerEntry(name: "tokyo", host: "203.0.113.10", current: true),
            ServerEntry(name: "osaka", host: "203.0.113.20"),
            ServerEntry(name: "broken", host: ""),
        ]))
        expect(!rows[0].isSelectable, "当前那台还能点")
        expect(rows[1].isSelectable, "另一台点不了")
        expect(!rows[2].isSelectable, "主机解析不出来的那台还能点")
    }

    // **确认文案必须点明会换出口 IP。** 这正是项目所有者拒绝自动容灾的理由 ——
    // 换出口是有后果的事,必须是用户明知的一下。
    static func testSwitchConfirmationNamesTheConsequence() {
        let message = serverSwitchConfirmMessage(name: "osaka", host: "203.0.113.20")
        expect(message.contains("osaka"), "没提到目标:\(message)")
        expect(message.contains("203.0.113.20"), "没提到出口主机:\(message)")
        expect(message.lowercased().contains("ip"), "没说会换 IP:\(message)")
    }

    // **热切没成功时不许说「已切换」。** 配置写好了但正在跑的实例还在旧服务器上,
    // 不明说要重启,用户就会以为已经换过去了。
    static func testSwitchOutcomeSeparatesConfigFromRunningTunnel() {
        let applied = serverSwitchOutcomeMessage(
            result: ServerSwitchResult(name: "osaka", host: "203.0.113.20", applied: true))
        expect(applied.contains("now leaves from"), "生效那句不对:\(applied)")

        let saved = serverSwitchOutcomeMessage(
            result: ServerSwitchResult(name: "osaka", host: "203.0.113.20", applied: false))
        expect(!saved.contains("now leaves from"), "没生效却说流量已经从新那台出去了:\(saved)")
        expect(saved.lowercased().contains("off and on"), "没告诉用户要重启:\(saved)")
    }

    // `applied` 缺席读作 false:**说不出「已生效」的时候就不许说**。
    static func testMissingAppliedIsNotSuccess() {
        let json = #"{"name":"osaka","host":"203.0.113.20"}"#
        guard let result = try? JSONDecoder().decode(ServerSwitchResult.self, from: Data(json.utf8)) else {
            fail("解不出切换应答"); return
        }
        expect(!result.applied, "applied 缺席却读成了 true")
    }

    // 失败说「没问出来」,绝不说成某个具体答案 —— 与 Tristate 同一条纪律。
    static func testExitIPLineNeverInventsAnAnswer() {
        expect(exitIPLine(.unknown).contains("not checked"), "未探测那句不对")
        expect(exitIPLine(.checking).contains("checking"), "探测中那句不对")
        let failed = exitIPLine(.failed)
        expect(failed.contains("could not"), "失败那句不对:\(failed)")
        expect(!failed.contains("0.0.0.0"), "失败却报了一个地址:\(failed)")
        expect(exitIPLine(.address("203.0.113.20")).contains("203.0.113.20"), "拿到地址却没显示")
    }

    // **校验过才认。** 一段 HTML 错误页(或一个被劫持的应答)不许被原样当成
    // 「你的出口 IP」显示出来 —— 那是这个功能唯一能造成的伤害。
    static func testExitIPResponseIsValidated() {
        expect(parseExitIPResponse("203.0.113.20\n") == "203.0.113.20", "正常应答没认出来")
        for bad in [
            "", "   ", "not an ip", "<html><body>502 Bad Gateway</body></html>",
            "203.0.113", "203.0.113.20.1", "203.0.113.999", "203.0.113.-1",
            "2001:db8::1", "203.0.113.20 and more", "203.0.113.2o",
        ] {
            if let got = parseExitIPResponse(bad) {
                fail("畸形应答 \(bad.prefix(30).debugDescription) 被当成了地址:\(got)")
            }
        }
    }


    // **没测过 ≠ 测了没通。** probe 键缺席时一个字都不说 —— 一行「未测试」在每台
    // 后面重复是墙纸;而把它画成红的,等于把一台好服务器说成坏的。
    static func testUntestedServersSaySilentlyNothing() {
        let row = ServerRow(entry: ServerEntry(name: "tokyo", host: "203.0.113.10"))
        expect(row.probeLine == nil, "没测过却说了话:\(row.probeLine ?? "")")
        expect(!row.probeFailed, "没测过却被标成失败")
        expect(row.detail == "203.0.113.10", "detail 里混进了探测:\(row.detail)")
    }

    // 测通了报毫秒;测不通报**原因**,不是一个光秃秃的红叉 —— 用户要分得清
    // 「服务器关了」和「我这条网络的问题」。
    static func testProbeLineSaysMillisecondsOrWhyNot() {
        let ok = ServerRow(entry: ServerEntry(name: "a", host: "h", probe: ProbeReport(reachable: true, rttMS: 42)))
        expect(ok.probeLine == "42 ms", "通了却没报毫秒:\(ok.probeLine ?? "")")
        expect(!ok.probeFailed, "通了却被标成失败")

        let bad = ServerRow(entry: ServerEntry(name: "b", host: "h",
                                               probe: ProbeReport(reachable: false, error: "超时(没有应答)")))
        expect(bad.probeLine == "超时(没有应答)", "没说原因:\(bad.probeLine ?? "")")
        expect(bad.probeFailed, "没通却没被标成失败")
    }

    // **「没通」不许显示成 0 ms。** 零值读起来像一切正常 —— 这是这个仓库反复
    // 禁止的那种谎,而它在这里的具体形状就是「0 毫秒,真快」。
    static func testUnreachableNeverRendersAsZeroMilliseconds() {
        let row = ServerRow(entry: ServerEntry(name: "b", host: "h", probe: ProbeReport(reachable: false)))
        let line = row.probeLine ?? ""
        expect(!line.contains("0 ms"), "没通却显示成 0 ms:\(line)")
        expect(line == "unreachable", "没有原因时的兜底文案不对:\(line)")
    }

    // 探测结论能从 Guardian 的应答里解出来,且 rtt 缺席读作 0(不是「很快」)。
    static func testProbeDecodesFromGuardian() {
        let json = """
        {"servers":[{"name":"a","host":"h","probe":{"reachable":true,"rtt_ms":7}},
                    {"name":"b","host":"h2","probe":{"reachable":false,"error":"连不上"}}]}
        """
        guard let list = try? JSONDecoder().decode(ServerList.self, from: Data(json.utf8)) else {
            fail("解不出带探测结论的清单"); return
        }
        expect(list.servers[0].probe?.rttMS == 7, "rtt 没解出来")
        expect(list.servers[1].probe?.reachable == false, "第二台的结论不对")
        expect(list.servers[1].probe?.rttMS == 0, "rtt 缺席却不是 0")
    }

    static func main() {
        testServerListDecodesWhatGuardianSends()
        testServerSwitchingNeedsTheCapability()
        testServerRowShowsWhereTrafficLeaves()
        testUnselectableRows()
        testSwitchConfirmationNamesTheConsequence()
        testSwitchOutcomeSeparatesConfigFromRunningTunnel()
        testMissingAppliedIsNotSuccess()
        testExitIPLineNeverInventsAnAnswer()
        testExitIPResponseIsValidated()
        testUntestedServersSaySilentlyNothing()
        testProbeLineSaysMillisecondsOrWhyNot()
        testUnreachableNeverRendersAsZeroMilliseconds()
        testProbeDecodesFromGuardian()
        // 通过横幅是「这个套件真的跑过」的唯一证据 —— 一个没被脚本登记的套件
        // 退出码也是 0(本仓库实测栽过)。
        if failures == 0 {
            print("ServersModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
