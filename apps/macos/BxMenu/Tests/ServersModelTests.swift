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

    // MARK: - fixtures
    //
    // **形状必须是生产真的会发生的那些。** 本仓库栽过一次:Swift fixture 喂的是
    // 生产不会出现的形状(键缺席 vs null),两者恰好走同一分支,于是守卫守了个寂寞。

    /// 一份正常的两台清单,tokyo 是配置里选的那台、也是 Core 报的那台。
    static func listWithCurrent() -> ServerList {
        ServerList(
            servers: [
                ServerEntry(name: "tokyo", host: "203.0.113.10", port: 443, current: true,
                            peakBPS: 6_400_000, peakAgeSeconds: 7200),
                ServerEntry(name: "osaka", host: "203.0.113.20", port: 443, udpHost: "203.0.113.21"),
            ],
            current: "tokyo", configPath: "/etc/bx/config.yaml", running: "tokyo")
    }

    static func listWithTwo() -> ServerList { listWithCurrent() }

    /// 单服务器配置(`server:` / `transports:`):配置里**根本没有** servers 清单。
    /// 这是每一个正常装好 bx 的用户打开这个窗口时看到的形状。
    static func singleServerConfig() -> ServerList {
        ServerList(servers: [], current: "", configPath: "/etc/bx/config.yaml", singleServer: true)
    }

    /// 有 servers 清单,但确实是空的。
    static func emptyServerList() -> ServerList {
        ServerList(servers: [], current: "", configPath: "/etc/bx/config.yaml", singleServer: false)
    }

    /// Core 在答话。
    static func answeringCoreRuntime() -> CoreRuntime {
        CoreRuntime(reachable: true, tunnelHealthy: true, latencyMS: 1051,
                    server: "tokyo", transport: "reality@203.0.113.10",
                    udpMode: "proxy", udpTransport: "hysteria2@203.0.113.21")
    }

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
        let rows = [
            ServerEntry(name: "tokyo", host: "203.0.113.10", current: true),
            ServerEntry(name: "osaka", host: "203.0.113.20", udpHost: "203.0.113.21"),
            ServerEntry(name: "broken", host: ""),
        ].map { ServerRow(entry: $0) }
        expect(rows[0].detail == "203.0.113.10", "detail = \(rows[0].detail)")
        expect(rows[1].detail.contains("UDP → 203.0.113.21"), "UDP 出口没显示:\(rows[1].detail)")
        expect(rows[2].detail.contains("could not be parsed"), "坏链接没说出来:\(rows[2].detail)")
    }

    // 当前那台点了是空操作(看起来像坏了);主机解析不出来的那台切过去必然失败。
    static func testUnselectableRows() {
        let rows = [
            ServerEntry(name: "tokyo", host: "203.0.113.10", current: true),
            ServerEntry(name: "osaka", host: "203.0.113.20"),
            ServerEntry(name: "broken", host: ""),
        ].map { ServerRow(entry: $0) }
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
        let applied = switchOutcomeMessage(
            ServerSwitchResult(name: "osaka", host: "203.0.113.20", applied: true))
        expect(applied.contains("now leaves from"), "生效那句不对:\(applied)")

        let saved = switchOutcomeMessage(
            ServerSwitchResult(name: "osaka", host: "203.0.113.20", applied: false))
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
        expect(!row.probe.isFailure, "没测过却被标成失败")
        expect(row.detail == "203.0.113.10", "detail 里混进了探测:\(row.detail)")
    }

    // 测通了报毫秒;测不通报**原因**,不是一个光秃秃的红叉 —— 用户要分得清
    // 「服务器关了」和「我这条网络的问题」。
    static func testProbeLineSaysMillisecondsOrWhyNot() {
        let ok = ServerRow(entry: ServerEntry(name: "a", host: "h", probe: ProbeReport(measured: true, reachable: true, rttMS: 42)))
        expect(ok.probeLine == "42 ms", "通了却没报毫秒:\(ok.probeLine ?? "")")
        expect(!ok.probe.isFailure, "通了却被标成失败")

        let bad = ServerRow(entry: ServerEntry(name: "b", host: "h",
                                               probe: ProbeReport(measured: true, reachable: false,
                                                                  errorCode: "timeout")))
        expect(bad.probeLine == "no answer (timed out)", "没说原因:\(bad.probeLine ?? "")")
        expect(bad.probe.isFailure, "没通却没被标成失败")
    }

    // **「没通」不许显示成 0 ms。** 零值读起来像一切正常 —— 这是这个仓库反复
    // 禁止的那种谎,而它在这里的具体形状就是「0 毫秒,真快」。
    static func testUnreachableNeverRendersAsZeroMilliseconds() {
        // 没有码(服务端没归类)时兜底文案是 "unreachable",而不是一个 0 毫秒。
        let row = ServerRow(entry: ServerEntry(name: "b", host: "h", probe: ProbeReport(measured: true, reachable: false)))
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

    // **没观测到吞吐就一个字都不说。**「0 B/s」读起来像这条隧道死了,而真相是
    // 这段时间没人用它传东西 —— 一台没人用的和一台被打满到爬的,在计数上都安静。
    static func testThroughputStaysSilentWhenNotObserved() {
        let row = ServerRow(entry: ServerEntry(name: "a", host: "h"))
        expect(row.throughputLine == nil, "没观测到却说了话:\(row.throughputLine ?? "")")
        expect(!row.detail.contains("B/s"), "detail 里混进了吞吐:\(row.detail)")
    }

    // 措辞是 peak 不是 speed:这是**已经发生过的流量**里最快的那一秒,
    // 不是一次测速,也不是承诺。
    static func testThroughputSaysPeak() {
        let row = ServerRow(entry: ServerEntry(name: "a", host: "h", peakBPS: 3_100_000))
        let line = row.throughputLine ?? ""
        expect(line.hasPrefix("peak "), "没说清楚这是峰值:\(line)")
        expect(line.contains("3.1 MB/s"), "数值不对:\(line)")
    }

    // 单位必须与 Go 侧 stats.HumanBPS 一致(十进制)。两边不一致的话,同一个数
    // 在 `bx status` 与菜单里会显示成两个值。
    static func testUnitsMatchTheGoSide() {
        expect(humanBytesPerSecond(0) == "0 B/s", "0 的写法不对")
        expect(humanBytesPerSecond(999) == "999 B/s", "999 的写法不对")
        expect(humanBytesPerSecond(1_000) == "1 kB/s", "1000 的写法不对")
        expect(humanBytesPerSecond(125_000) == "125 kB/s", "125000 的写法不对")
        expect(humanBytesPerSecond(3_100_000) == "3.1 MB/s", "3100000 的写法不对")
    }

    // **历史数字必须带年龄。** 不带年龄的历史读起来像现状 —— 而存历史的前提
    // 正是界面要标出来这是以前的。
    static func testHistoricalThroughputShowsItsAge() {
        let row = ServerRow(entry: ServerEntry(name: "a", host: "h",
                                               peakBPS: 8_000_000, peakAgeSeconds: 7200))
        let line = row.throughputLine ?? ""
        expect(line.contains("8.0 MB/s"), "数值不对:\(line)")
        expect(line.contains("2h ago"), "没标出这是以前的:\(line)")
    }

    // **「就是现在」不写「0 秒前」** —— 它只会让人怀疑这个数字是不是坏的。
    static func testFreshThroughputHasNoAgeSuffix() {
        let row = ServerRow(entry: ServerEntry(name: "a", host: "h", peakBPS: 3_100_000))
        let line = row.throughputLine ?? ""
        expect(!line.contains("ago"), "刚观测到的却带了年龄:\(line)")
        expect(line == "peak 3.1 MB/s", "文案不对:\(line)")
    }

    static func testRelativeAge() {
        expect(relativeAge(seconds: 0) == nil, "0 秒该是 nil")
        expect(relativeAge(seconds: 119) == nil, "两分钟以内该是 nil")
        expect(relativeAge(seconds: 120) == "2m ago", "2 分钟不对")
        expect(relativeAge(seconds: 7200) == "2h ago", "2 小时不对")
        expect(relativeAge(seconds: 259_200) == "3d ago", "3 天不对")
    }

    // 「Replace Configuration…」从一级菜单搬进了服务器窗口;但旧 Guardian 没有
    // /v1/servers、那个窗口根本开不出来 —— 那时它必须留在菜单里,否则换服务器
    // 又只能开终端(2026-08-14 那次抱怨)。判据只看能力,不试着拨。
    static func testReplaceConfigurationStaysInTheMenuOnlyWithoutServersWindow() {
        expect(replaceConfigurationLivesInMenu(capabilities: nil), "旧 Guardian(没声明能力)要留在菜单里")
        expect(replaceConfigurationLivesInMenu(capabilities: ["rules"]), "没有 servers 能力要留在菜单里")
        expect(!replaceConfigurationLivesInMenu(capabilities: ["servers"]), "有服务器窗口时不再占一级菜单")
    }

    // add 应答里的 added 缺席读作空串(旧 Guardian),不抛。
    static func testServerListDecodesAddedAndToleratesItsAbsence() {
        let with = try! JSONDecoder().decode(ServerList.self, from: Data(#"{"servers":[],"current":"","added":"vps2"}"#.utf8))
        expect(with.added == "vps2", "added 没解出来")
        let without = try! JSONDecoder().decode(ServerList.self, from: Data(#"{"servers":[],"current":""}"#.utf8))
        expect(without.added.isEmpty, "缺席要落成空串")
    }

    // 加完之后那句话:切成功说流量已从新那台出去;切没成说已加进清单但没切;
    // 切换那一步压根没做(add 成功、switch 抛错)说「已加进清单,可以在窗口里 Use」。
    static func testAddServerOutcomeMessageDistinguishesTheThreeEndings() {
        let applied = addServerOutcomeMessage(added: "vps2", switched: ServerSwitchResult(name: "vps2", host: "vps2.example.com", applied: true))
        expect(applied.contains("now leaves from vps2"), "切成功:\(applied)")
        let saved = addServerOutcomeMessage(added: "vps2", switched: ServerSwitchResult(name: "vps2", host: "", applied: false))
        expect(saved.contains("could not tell whether"), "切没成:\(saved)")
        let onlyAdded = addServerOutcomeMessage(added: "vps2", switched: nil)
        expect(onlyAdded.contains("Added vps2") && onlyAdded.contains("Use"), "只加了:\(onlyAdded)")
    }

    // 加服务器那条路上的两种常见失败必须是人话,不是协议码。
    static func testAddServerFailureCodesBecomeSentences() {
        guard let exists = addServerFailureMessage(code: "servers_name_exists", status: 409) else {
            return fail("同名 409 没有对应的句子 —— 用户会读到 code=servers_name_exists")
        }
        expect(exists.contains("already in your list"), "同名那句没说清是名字撞了:\(exists)")
        expect(!exists.contains("servers_name_exists"), "那句话里还带着失败码:\(exists)")
        expect(!exists.contains("409"), "那句话里还带着 HTTP 状态码:\(exists)")

        guard let bad = addServerFailureMessage(code: "servers_add_failed", status: 400) else {
            return fail("加不上那条 400 没有对应的句子")
        }
        // **必须说到名字里能用哪些字符** —— 这条路上最常见的触发就是名字带空格,
        // 而「加不上」本身给不了下一步。
        expect(bad.contains("Check the link"), "没提链接:\(bad)")
        expect(bad.lowercased().contains("hyphens"), "没说名字里能用哪些字符:\(bad)")
        expect(!bad.contains("servers_add_failed"), "那句话里还带着失败码:\(bad)")
    }

    // 认不出的码返回 nil,让调用方退回通用漏斗。**编一句解释比不解释更糟。**
    static func testUnknownAddServerFailureFallsBackToTheGenericFunnel() {
        expect(addServerFailureMessage(code: "servers_switch_busy", status: 409) == nil,
            "别的码不该被这张表认领 —— 它会把一句不相干的解释贴到另一种失败上")
        expect(addServerFailureMessage(code: nil, status: 500) == nil, "没有码就没有句子")
        expect(addServerFailureMessage(code: "", status: 500) == nil, "空码不是一个码")
    }

    // MARK: - 探测三态

    // **三态,不是两态。** 「没测过」/「没测成」/「测了不通」是三个不同的答案,
    // 而线上此前只有两个位置放它们 —— 于是 bx 没在跑的时候点一下 Test,一整排
    // 好服务器全被画成红的。
    //
    // 这条**必须分别喂三种输入**:只喂「没测过」与「测了不通」正是今天那条测试
    // 假绿的原因 —— 中间那一态在输入里根本没出现过,它是死是活测不出来。
    static func testProbeHasThreeStatesNotTwo() {
        expect(probePresentation(nil) == .notChecked, "没测过")
        expect(probePresentation(ProbeReport(measured: false, errorCode: "core_unreachable"))
            == .notMeasured("could not measure (is bx running?)"), "没测成 —— 绝不许画成不可达")
        expect(probePresentation(ProbeReport(measured: true, reachable: false, errorCode: "refused"))
            == .measured(reachable: false, rttMS: 0,
                         reason: "connection refused (nothing is listening)"), "测了不通")
        expect(probePresentation(ProbeReport(measured: true, reachable: true, rttMS: 42))
            == .measured(reachable: true, rttMS: 42, reason: ""), "测通了")
    }

    // **服务端那句失败原因是中文的(它服务 `bx server list`),而这个菜单通篇英文。**
    //
    // 这不是假想:`supervisor.probeServer` 会对一台关着的服务器把 Error 填成
    // 「连接被拒(端口没在听)」,那是**最常见**的失败路径,而它此前一路流到
    // 这一行上、还被画成红的。CJK 守卫只扫菜单自己的源码,看不见从服务端来的
    // 字符串。修法是服务端发码、这一侧出话 —— 下面每一条都必须是英文。
    static func testProbeFailuresSpeakEnglishNotWhateverTheServerSaid() {
        // 与 supervisor.ProbeErrorCodes 一一对应;跨语言对账由 Go 侧那条守卫做,
        // 这里钉的是「每个码都真的有一句英文,而且它是英文」。
        for code in ["timeout", "canceled", "dns", "refused", "network_unreachable",
                     "no_route", "no_host", "bad_port", "unknown",
                     "core_unreachable", "link_unparsed"] {
            let text = probeFailureText(code: code, fallback: "unreachable")
            expect(!text.isEmpty, "\(code) 没有句子")
            expect(text.allSatisfy { $0.isASCII }, "\(code) 那句话不是英文:\(text)")
        }
        // **认不出的码退回一句笼统的英文,绝不退回服务端那句话** ——
        // 说得不够细好过说错语言。
        // 「服务端归不了类」与「这一版没发码」是两件事,不许共用一句话。
        expect(probeFailureText(code: "unknown", fallback: "unreachable") != "unreachable",
               "unknown 那一档退回了兜底 —— 它与「压根没发码」就分不开了")
        expect(probeFailureText(code: "something_new", fallback: "unreachable") == "unreachable",
               "认不出的码没有退回兜底")
        expect(probeFailureText(code: "", fallback: "could not measure") == "could not measure",
               "旧 Guardian(不发码)没有退回兜底")

        // 端到端:一份「服务器关着」的应答,渲染出来必须全是 ASCII。
        let row = ServerRow(entry: ServerEntry(
            name: "osaka", host: "203.0.113.20",
            probe: ProbeReport(measured: true, reachable: false, errorCode: "refused")))
        let line = row.probeLine ?? ""
        expect(line.allSatisfy { $0.isASCII }, "界面上出现了非英文的失败原因:\(line)")
        expect(line.contains("refused"), "没说出原因:\(line)")
        expect(row.probe.isFailure, "服务器真的关着,这一行该画红")
    }

    // **`error` 那个键刻意不解。** 解出来就迟早有人显示它,而它是中文的 ——
    // 这条钉的是「就算服务端把中文放在眼前,它也进不了界面」。
    static func testTheServersHumanStringNeverReachesTheUI() {
        let json = #"""
        {"servers":[{"name":"a","host":"h","probe":
          {"measured":true,"reachable":false,"error":"连接被拒(端口没在听)","error_code":"refused"}}]}
        """#
        guard let list = try? JSONDecoder().decode(ServerList.self, from: Data(json.utf8)) else {
            fail("解不出带中文 error 的应答"); return
        }
        let row = ServerRow(entry: list.servers[0])
        expect(row.detail.allSatisfy { $0.isASCII }, "服务端那句中文出现在了界面上:\(row.detail)")
        expect(row.probeLine == "connection refused (nothing is listening)",
               "码没有被翻成英文:\(row.probeLine ?? "nil")")
    }

    // **只有「测过而且没通」才画红。** 另外两态画红等于把一台好服务器说成坏的,
    // 而用户会据此去换服务器 —— 与 ipify 那次同一类、方向相反的错。
    static func testOnlyAMeasuredFailureIsPaintedRed() {
        expect(!ProbePresentation.notChecked.isFailure, "没测过被画成了红的")
        expect(!ProbePresentation.notMeasured("core not running").isFailure, "没测成被画成了红的")
        expect(!ProbePresentation.measured(reachable: true, rttMS: 7, reason: "").isFailure,
               "通了被画成了红的")
        expect(ProbePresentation.measured(reachable: false, rttMS: 0, reason: "unreachable").isFailure,
               "测了不通却没画红")
    }

    // **`measured` 键缺席 = 这一版 Guardian 没说,不是「测过了」。**
    // 旧 Guardian 的应答里没有这个键,按「测过」渲染就会把它的 reachable=false
    // (那可能只是没测成)当成实测结论。
    static func testAbsentMeasuredKeyIsNotTakenAsMeasured() {
        let json = #"{"servers":[{"name":"a","host":"h","probe":{"reachable":true,"rtt_ms":7}}]}"#
        guard let list = try? JSONDecoder().decode(ServerList.self, from: Data(json.utf8)) else {
            fail("解不出旧 Guardian 的探测应答"); return
        }
        expect(list.servers[0].probe?.measured == false, "measured 缺席却读成了 true")
        if case .notMeasured = probePresentation(list.servers[0].probe) {} else {
            fail("旧 Guardian 的探测被当成了实测结论")
        }
    }

    // 新 Guardian 的 measured 要真的解得出来。
    static func testMeasuredDecodesFromGuardian() {
        let json = #"{"servers":[{"name":"a","host":"h","probe":{"measured":true,"reachable":true,"rtt_ms":7}}]}"#
        guard let list = try? JSONDecoder().decode(ServerList.self, from: Data(json.utf8)) else {
            fail("解不出带 measured 的应答"); return
        }
        expect(probePresentation(list.servers[0].probe) == .measured(reachable: true, rttMS: 7, reason: ""),
               "measured=true 的应答没被当成实测结论")
    }

    // MARK: - 当前那台

    // **Core 没答就不许给一个零值。** 0 毫秒、tunnel unhealthy 都是编出来的答案,
    // 而这个窗口存在的理由正是「我这条隧道现在怎么样」。
    static func testCurrentPanelOmitsCoreFieldsWhenCoreIsSilent() {
        guard let panel = currentServerPanel(list: listWithCurrent(), core: nil) else {
            fail("有当前那台却没给出面板"); return
        }
        expect(panel.latencyMS == nil, "Core 没答就不许给一个 0 毫秒")
        expect(panel.tunnelHealthy == nil, "同上")
        expect(panel.transport == nil, "同上")
        expect(panel.coreSilentNote != nil, "必须明说下面这些没量到")
        // 主机与端口来自配置,不来自 Core —— 它们照常有。
        expect(panel.endpoint == "203.0.113.10:443", "配置里就有的东西也不见了:\(panel.endpoint)")
    }

    // Core 答了话,纵深就该有值 —— 这是这一版窗口存在的全部理由。
    static func testCurrentPanelCarriesTheLiveDepthWhenCoreAnswers() {
        guard let panel = currentServerPanel(list: listWithCurrent(), core: answeringCoreRuntime()) else {
            fail("有当前那台却没给出面板"); return
        }
        expect(panel.latencyMS == 1051, "实时延迟没接上:\(String(describing: panel.latencyMS))")
        expect(panel.tunnelHealthy == true, "隧道健康没接上")
        expect(panel.transport == "reality@203.0.113.10", "传输没接上")
        expect(panel.udpMode == "proxy", "UDP 档没接上")
        expect(panel.udpTransport == "hysteria2@203.0.113.21", "UDP 传输没接上")
        expect(panel.coreSilentNote == nil, "Core 明明答了话却说没量到")
        expect(panel.throughput?.contains("2h ago") == true,
               "峰值没带年龄:\(panel.throughput ?? "nil")")
    }

    // **`reachable == false` 与「没问过」是同一件事:没有数据。**
    // 判据只有 answeringCore 那一份 —— 这里喂一个 reachable=false 的 Core,
    // 它携带的全是零值,当真就会画出一行撒谎的 0 ms。
    static func testCurrentPanelTreatsAnUnreachableCoreAsNoData() {
        let dead = CoreRuntime(reachable: false, tunnelHealthy: false, latencyMS: 0)
        guard let panel = currentServerPanel(list: listWithCurrent(), core: dead) else {
            fail("有当前那台却没给出面板"); return
        }
        expect(panel.latencyMS == nil, "拨不通的 Core 报的 0 毫秒被当成了实测值")
        expect(panel.tunnelHealthy == nil, "拨不通的 Core 报的 false 被当成了「隧道坏了」")
        expect(panel.coreSilentNote != nil, "拨不通却没说下面这些没量到")
    }

    // **配置说 B、实际在跑 A,这两者不同正是最有价值的诊断。** 热切换是先写配置
    // 再切,所以切换失败的那一刻配置已经是新那台了 —— 此时给它加粗打点,就是
    // 断言用户的流量从一台其实没在用的服务器出去。
    static func testCurrentPanelSaysWhenTheRunningServerIsNotTheConfiguredOne() {
        var list = listWithCurrent()
        list.running = "osaka"
        guard let panel = currentServerPanel(list: list, core: answeringCoreRuntime()) else {
            fail("有当前那台却没给出面板"); return
        }
        expect(!panel.runningConfirmed, "实际在跑的是别台,却仍然给当前那台加粗打点")
        expect(panel.runningNote?.contains("osaka") == true,
               "没点名实际在跑的那台:\(panel.runningNote ?? "nil")")
    }

    // 问不出来时既不加粗、也不编一个答案。
    static func testCurrentPanelWillNotConfirmARunningServerItCannotSee() {
        var list = listWithCurrent()
        list.running = ""
        guard let panel = currentServerPanel(list: list, core: answeringCoreRuntime()) else {
            fail("有当前那台却没给出面板"); return
        }
        expect(!panel.runningConfirmed, "问不出来却仍然断言它在跑")
        expect(panel.runningNote?.lowercased().contains("could not confirm") == true,
               "没说这一项没问出来:\(panel.runningNote ?? "nil")")

        // Core 静默时那份 running 可能是上一次取清单时的陈旧值 —— 不许拿它加粗。
        guard let stale = currentServerPanel(list: listWithCurrent(), core: nil) else {
            fail("有当前那台却没给出面板"); return
        }
        expect(!stale.runningConfirmed, "Core 静默时拿一份可能陈旧的 running 加了粗")
    }

    // 一份没有 current 的清单(手改出来的配置就是这样)不该凭空造一个面板。
    static func testCurrentPanelIsAbsentWithoutACurrentServer() {
        expect(currentServerPanel(list: emptyServerList(), core: answeringCoreRuntime()) == nil,
               "空清单却给出了一个当前那台")
    }

    // MARK: - 候选行

    static func testOtherServerRowsLeaveOutTheCurrentOne() {
        let rows = otherServerRows(list: listWithCurrent(), core: answeringCoreRuntime())
        expect(rows.count == 1, "候选行数 = \(rows.count),当前那台应当只出现在上面那一块里")
        expect(rows.first?.name == "osaka", "候选行不对:\(rows.first?.name ?? "nil")")
        expect(rows.first?.endpoint == "203.0.113.20:443", "端口没显示:\(rows.first?.endpoint ?? "nil")")
    }

    // 实际在跑的那台落在候选里(热切换失败之后就是这个样子)时必须点名 ——
    // 那一行才是用户流量真正的出口。
    static func testOtherRowsMarkTheServerCoreIsActuallyUsing() {
        var list = listWithCurrent()
        list.running = "osaka"
        let rows = otherServerRows(list: list, core: answeringCoreRuntime())
        expect(rows.first?.isRunningNow == true, "实际在跑的那台没被点名")
        expect(rows.first?.runningNote != nil, "实际在跑的那台没有说明文字")
    }

    // **Core 静默时不许点名。** 那份 running 来自上一次取清单,可能已经陈旧 ——
    // 而它陈旧的那一刻,恰好就是保护刚被关掉、什么都没在跑的时候。
    static func testOtherRowsDoNotClaimARunningServerWhileCoreIsSilent() {
        var list = listWithCurrent()
        list.running = "osaka"
        let rows = otherServerRows(list: list, core: nil)
        expect(rows.first?.isRunningNow == false, "Core 静默却断言某一台正在跑")
        expect(rows.first?.runningNote == nil, "Core 静默却给了一句「正在用」")
    }

    // MARK: - 空清单

    // **单服务器配置不是「没有服务器」。** 每一个正常装好 bx 的用户打开这个窗口
    // 都是这个形状(`bx setup` 从不写 servers 清单),而 bx 此刻正跑着一台服务器 ——
    // 对他说「No servers yet」是一句当场就能被证伪的假话。
    static func testEmptyReasonTellsSingleServerConfigApartFromNoServers() {
        expect(serverListEmptyReason(list: singleServerConfig())?.contains("single server") == true,
               "单服务器配置没有被说清:\(serverListEmptyReason(list: singleServerConfig()) ?? "nil")")
        expect(serverListEmptyReason(list: emptyServerList())?.contains("No servers yet") == true,
               "空清单没有被说清:\(serverListEmptyReason(list: emptyServerList()) ?? "nil")")
        expect(serverListEmptyReason(list: listWithTwo()) == nil, "有服务器却还在说空清单")
    }

    // `single_server` 缺席(旧 Guardian)读作 false —— 那时退回既有的措辞,
    // 而不是编一句关于配置形状的话。
    static func testSingleServerFlagDecodesAndDefaultsToFalse() {
        let with = try! JSONDecoder().decode(
            ServerList.self, from: Data(#"{"servers":[],"single_server":true}"#.utf8))
        expect(with.singleServer, "single_server 没解出来")
        let without = try! JSONDecoder().decode(
            ServerList.self, from: Data(#"{"servers":[]}"#.utf8))
        expect(!without.singleServer, "缺席要落成 false")
        expect(serverListEmptyReason(list: without)?.contains("No servers yet") == true,
               "旧 Guardian 上要退回既有措辞")
    }

    // MARK: - 切换的四种结局

    // **四种结局四句话,其中两句今天是错的。**
    // 「已生效但确认失败」说成「没切过去」是假的 —— 它切过去了,而死手可能在
    // 超时后把它还原,用户必须立刻动手;「回滚也失败了」被同一句话轻描淡写成
    // 「关了再开就行」,而那是一次正在发生的断网。
    static func testSwitchOutcomeTellsTheFourEndingsApart() {
        func message(_ outcome: String) -> String {
            switchOutcomeMessage(ServerSwitchResult(
                name: "osaka", host: "203.0.113.20", applied: false, outcome: outcome))
        }
        let arm = message("arm_failed")
        let rolled = message("rolled_back")
        let rollbackFailed = message("rollback_failed")
        let commitFailed = message("commit_failed")

        let all = [arm, rolled, rollbackFailed, commitFailed]
        expect(Set(all).count == 4, "四种结局没有四句话")
        // **上面那句话名不副实,必须配这一条。** 删掉某一个分支之后,那种结局
        // 会落到 default 上,而 default 的措辞与另外三句都不同 —— 集合里照样是
        // 四个元素,`Set(all).count == 4` 一声不吭。真正要钉的是「四种里没有一种
        // 走了兜底」。
        let fallback = switchOutcomeMessage(ServerSwitchResult(
            name: "osaka", host: "203.0.113.20", applied: false, outcome: "no_such_code"))
        for (i, line) in all.enumerated() where line == fallback {
            fail("第 \(i + 1) 种结局塌回了兜底那句话 —— 它的分支没了:\(line)")
        }
        for line in all {
            expect(line.contains("osaka"), "没点名目标:\(line)")
            expect(!line.contains("_failed") && !line.contains("rolled_back"),
                   "把协议码原样端给了用户:\(line)")
        }

        // 没切过去的两种:必须明说流量还在原来那台。
        expect(arm.contains("still leaves from the previous server"), "arm_failed 那句不对:\(arm)")
        expect(rolled.contains("still leaves from the previous server"), "rolled_back 那句不对:\(rolled)")

        // 回滚失败:隧道现在可能是断的,而且要给逃生命令。
        expect(rollbackFailed.lowercased().contains("may be down"), "没说隧道可能断了:\(rollbackFailed)")
        expect(rollbackFailed.contains("sudo bx down && sudo bx up"), "没给逃生命令:\(rollbackFailed)")

        // 已生效但确认失败:**它切过去了**,而死手可能还原它。
        expect(commitFailed.contains("already leaves from osaka"),
               "没说它其实已经切过去了:\(commitFailed)")
        expect(!commitFailed.contains("still leaves from the previous server"),
               "对一次已经生效的切换说「还在原来那台」—— 这正是要消灭的那句假话:\(commitFailed)")
        expect(commitFailed.contains("sudo bx down && sudo bx up"), "没给落定的办法:\(commitFailed)")
    }

    // **认不出的码不许被折进四种里的任何一种。** 说错了比不说更糟:一句「已回滚」
    // 会让用户以为流量还好好地走在原来那台上。旧 Guardian(键缺席)与服务端自己
    // 那个「说不出是哪种」的兜底码,走同一句诚实的话。
    static func testUnknownSwitchOutcomeGetsAnHonestFallback() {
        func message(_ outcome: String) -> String {
            switchOutcomeMessage(ServerSwitchResult(
                name: "osaka", host: "", applied: false, outcome: outcome))
        }
        let legacy = message("servers_hot_switch_failed")
        let absent = message("")
        let unknown = message("something_new_from_the_future")
        for line in [legacy, absent, unknown] {
            expect(line.contains("could not tell whether"),
                   "认不出的码没有一句诚实的兜底:\(line)")
            expect(!line.contains("already leaves from"), "把认不出的码说成了已生效:\(line)")
            expect(!line.contains("switched back"), "把认不出的码说成了已回滚:\(line)")
            expect(!line.isEmpty, "认不出的码静默消失了")
        }
        expect(legacy == absent && absent == unknown, "三种「说不出」应当是同一句话")
    }

    // 成功那一句一个字没变。
    static func testAppliedSwitchStillSaysItPlainly() {
        let applied = switchOutcomeMessage(ServerSwitchResult(
            name: "osaka", host: "203.0.113.20", applied: true))
        expect(applied.contains("now leaves from"), "生效那句不对:\(applied)")
    }

    // `outcome` 缺席读作空串(旧 Guardian),不抛。
    static func testSwitchResultDecodesOutcomeAndToleratesItsAbsence() {
        let with = try! JSONDecoder().decode(ServerSwitchResult.self,
            from: Data(#"{"name":"osaka","applied":false,"outcome":"commit_failed"}"#.utf8))
        expect(with.outcome == "commit_failed", "outcome 没解出来")
        let without = try! JSONDecoder().decode(ServerSwitchResult.self,
            from: Data(#"{"name":"osaka","applied":false}"#.utf8))
        expect(without.outcome.isEmpty, "缺席要落成空串")
    }

    // running 与 current 并列解出来,**绝不合并**;缺席(问不出来)读作空串。
    static func testServerListDecodesRunningBesideCurrent() {
        let with = try! JSONDecoder().decode(ServerList.self,
            from: Data(#"{"servers":[],"current":"tokyo","running":"osaka"}"#.utf8))
        expect(with.current == "tokyo" && with.running == "osaka", "两者被合并了")
        let without = try! JSONDecoder().decode(ServerList.self,
            from: Data(#"{"servers":[],"current":"tokyo"}"#.utf8))
        expect(without.running.isEmpty, "问不出来要落成空串,不许退回 current")
    }

    // 同一台主机上两台不同端口的服务器此前渲染得一模一样 —— port 发了但没解。
    static func testPortIsDecodedAndShown() {
        let list = try! JSONDecoder().decode(ServerList.self,
            from: Data(#"{"servers":[{"name":"a","host":"h","port":8443},{"name":"b","host":"h"}]}"#.utf8))
        expect(list.servers[0].port == 8443, "端口没解出来")
        expect(ServerRow(entry: list.servers[0]).endpoint == "h:8443", "端口没显示出来")
        // 端口缺席(旧 Guardian / 链接里看不出端口)就只写主机,不写一个 `:0`。
        expect(ServerRow(entry: list.servers[1]).endpoint == "h", "端口缺席却写出了一个假端口")
    }

    static func main() {
        testServerListDecodesWhatGuardianSends()
        testServerListDecodesAddedAndToleratesItsAbsence()
        testAddServerOutcomeMessageDistinguishesTheThreeEndings()
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
        testThroughputStaysSilentWhenNotObserved()
        testThroughputSaysPeak()
        testUnitsMatchTheGoSide()
        testHistoricalThroughputShowsItsAge()
        testFreshThroughputHasNoAgeSuffix()
        testRelativeAge()
        testReplaceConfigurationStaysInTheMenuOnlyWithoutServersWindow()
        testAddServerFailureCodesBecomeSentences()
        testUnknownAddServerFailureFallsBackToTheGenericFunnel()
        testProbeHasThreeStatesNotTwo()
        testProbeFailuresSpeakEnglishNotWhateverTheServerSaid()
        testTheServersHumanStringNeverReachesTheUI()
        testOnlyAMeasuredFailureIsPaintedRed()
        testAbsentMeasuredKeyIsNotTakenAsMeasured()
        testMeasuredDecodesFromGuardian()
        testCurrentPanelOmitsCoreFieldsWhenCoreIsSilent()
        testCurrentPanelCarriesTheLiveDepthWhenCoreAnswers()
        testCurrentPanelTreatsAnUnreachableCoreAsNoData()
        testCurrentPanelSaysWhenTheRunningServerIsNotTheConfiguredOne()
        testCurrentPanelWillNotConfirmARunningServerItCannotSee()
        testCurrentPanelIsAbsentWithoutACurrentServer()
        testOtherServerRowsLeaveOutTheCurrentOne()
        testOtherRowsMarkTheServerCoreIsActuallyUsing()
        testOtherRowsDoNotClaimARunningServerWhileCoreIsSilent()
        testEmptyReasonTellsSingleServerConfigApartFromNoServers()
        testSingleServerFlagDecodesAndDefaultsToFalse()
        testSwitchOutcomeTellsTheFourEndingsApart()
        testUnknownSwitchOutcomeGetsAnHonestFallback()
        testAppliedSwitchStillSaysItPlainly()
        testSwitchResultDecodesOutcomeAndToleratesItsAbsence()
        testServerListDecodesRunningBesideCurrent()
        testPortIsDecodedAndShown()
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
