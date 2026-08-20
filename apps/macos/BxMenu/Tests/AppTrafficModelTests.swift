import Foundation

@main
struct AppTrafficModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    // Swift 合成的 Decodable 不用属性默认值,而服务端对 `rules`/`error` 用了
    // omitempty —— 那两个键会整个缺席。「一条规则都没有」是完全正常的状态,
    // 用合成版会让它直接解码失败,RulesModel 那次就是这么被抓到的。
    static func testDecodesReportWithMissingRowsArray() {
        let json = """
        {
          "subscribed": true,
          "report": {
            "groups": [
              { "path": "tunnel", "rows": [
                { "app": "Safari", "conns": 3, "bytes_up": 100, "bytes_down": 200 }
              ] }
            ]
          }
        }
        """
        guard let report = decode(json) else { return }
        expect(report.subscribed, "subscribed 没解出来")
        expect(report.error.isEmpty, "不该有 error")
        let tunnel = report.report.groups.first { $0.path == .tunnel }
        expect(tunnel?.rows.count == 1, "tunnel 组的行数不对")
        // rules 键缺席 —— 必须解成空数组,不能整个解码失败。
        expect(tunnel?.rows.first?.rules == [], "缺席的 rules 没有落成空数组:\(String(describing: tunnel?.rows.first?.rules))")
    }

    // 真实线上形状:Go 端 `Rows []AppRow json:"rows"`(**没有** omitempty),
    // 值为 nil 时序列化成字面 `null`,这个键不会整个消失。这条测的是
    // 「rows 为 null 时按空处理」,不是「rows 键缺席」——那是两种不同的输入,
    // 只是 Swift 的 decodeIfPresent 恰好对两者走同一条返回路径,不能因为
    // 恰好都通过就拿一个代替另一个来验证契约。
    static func testDecodesGroupWithNullRows() {
        let json = """
        { "subscribed": true, "report": { "groups": [ { "path": "direct", "rows": null } ] } }
        """
        guard let report = decode(json) else { return }
        let direct = report.report.groups.first { $0.path == .direct }
        expect(direct?.rows == [], "null 的 rows 没有落成空数组")
    }

    // 防御性用例,**生产不会出现这个形状**(Go 端 `rows` 没有 omitempty,键
    // 恒在)。留着是为了兜住万一契约将来改动、或者别的调用方喂进一份手写的
    // 不完整 JSON——不能与上面那条真实形状的测试混为一谈。
    static func testDecodesGroupToleratesEntirelyMissingRowsKeyDefensively() {
        let json = """
        { "subscribed": true, "report": { "groups": [ { "path": "direct" } ] } }
        """
        guard let report = decode(json) else { return }
        let direct = report.report.groups.first { $0.path == .direct }
        expect(direct?.rows == [], "缺席的 rows 键没有落成空数组(防御性兜底)")
    }

    // report.error 缺席(正常情况)必须解成空串,不能整个解码失败。
    static func testDecodesReportWithMissingErrorKey() {
        let json = """
        { "subscribed": false, "report": { "groups": null } }
        """
        guard let report = decode(json) else { return }
        expect(report.error == "", "缺席的 error 没有落成空串:\(report.error)")
        expect(report.report.groups == [], "null 的 groups 没有落成空数组")
    }

    // 三组顺序固定(tunnel / direct / blocked),渲染层按顺序摆 —— 即便服务端
    // 发下来的 JSON 顺序被打乱(不该发生,但客户端不该假设它不会发生)。
    static func testGroupsKeepTunnelDirectBlockedOrder() {
        let json = """
        {
          "subscribed": true,
          "report": {
            "groups": [
              { "path": "blocked", "rows": [ { "app": "Malware", "conns": 1, "bytes_up": 0, "bytes_down": 0 } ] },
              { "path": "tunnel", "rows": [ { "app": "Chrome", "conns": 2, "bytes_up": 10, "bytes_down": 10 } ] },
              { "path": "direct", "rows": [ { "app": "qBittorrent", "conns": 5, "bytes_up": 500, "bytes_down": 900 } ] }
            ]
          }
        }
        """
        guard let report = decode(json) else { return }
        let headers = report.rows().compactMap { row -> String? in
            if case let .sectionHeader(title) = row { return title }
            return nil
        }
        expect(headers.count == 3, "组数不对:\(headers)")
        expect(headers == [
            AppTrafficReport.sectionTitle(for: .tunnel),
            AppTrafficReport.sectionTitle(for: .direct),
            AppTrafficReport.sectionTitle(for: .blocked),
        ], "组的渲染顺序没有固定成 tunnel/direct/blocked:\(headers)")
    }

    // 空串 app 是 unknown,必须显示成一句人话,不能是空白行。
    static func testUnknownAppRendersAsExplicitLabel() {
        let json = """
        {
          "subscribed": true,
          "report": {
            "groups": [
              { "path": "tunnel", "rows": [ { "app": "", "conns": 1, "bytes_up": 0, "bytes_down": 0 } ] }
            ]
          }
        }
        """
        guard let report = decode(json) else { return }
        let entries = report.rows().compactMap { row -> String? in
            if case let .entry(app, _) = row { return app }
            return nil
        }
        expect(entries.count == 1, "没有渲染出这一行")
        expect(entries.first != "", "unknown app 被渲染成了空白")
        expect(entries.first?.isEmpty == false, "unknown app 渲染出的标题是空串")
    }

    // 三种「空」状态的文案必须彼此不同:
    // ① 没在采集(subscribed=false)
    // ② 在采集但问不出来(subscribed=true 且 error 非空)
    // ③ 在采集且确实没连接(subscribed=true、error 缺席、三组皆空)
    static func testThreeEmptyStatesRenderDifferently() {
        let notSubscribed = AppTrafficReport(subscribed: false)
        let cannotTell = AppTrafficReport(subscribed: true, error: "core socket unavailable")
        let genuinelyEmpty = AppTrafficReport(subscribed: true)

        func message(_ report: AppTrafficReport) -> String? {
            guard report.rows().count == 1, case let .notice(text) = report.rows()[0] else { return nil }
            return text
        }

        let a = message(notSubscribed)
        let b = message(cannotTell)
        let c = message(genuinelyEmpty)

        expect(a != nil, "未订阅状态没有产出一句说明")
        expect(b != nil, "问不出来状态没有产出一句说明")
        expect(c != nil, "确实为空状态没有产出一句说明")
        expect(a != b, "「未订阅」与「问不出来」说了同一句话 —— 把没在采集说成了没查到")
        expect(a != c, "「未订阅」与「确实为空」说了同一句话 —— 把没在采集说成了没有流量")
        expect(b != c, "「问不出来」与「确实为空」说了同一句话 —— 把没查到说成了没有流量")
    }

    // groups 为空数组(而非某组的 rows 为空)也要落进「确实没有流量」这一句,
    // 而不是报错或者渲染出一个没有内容的分组标题。
    static func testAllEmptyGroupsRenderAsGenuinelyEmpty() {
        let report = AppTrafficReport(
            subscribed: true,
            report: AppTrafficReportBody(groups: [
                AppTrafficGroup(path: .tunnel, rows: []),
                AppTrafficGroup(path: .direct, rows: []),
                AppTrafficGroup(path: .blocked, rows: []),
            ])
        )
        let rows = report.rows()
        expect(rows.count == 1, "空分组渲染出了多余的行:\(rows)")
        if case .notice = rows.first {
            // ok
        } else {
            expect(false, "空分组没有落成 notice:\(rows)")
        }
    }

    static func decode(_ json: String) -> AppTrafficReport? {
        do {
            return try JSONDecoder().decode(AppTrafficReport.self, from: Data(json.utf8))
        } catch {
            expect(false, "解码失败: \(error)")
            return nil
        }
    }


    // 能力门控的判据。**旧 Guardian 不认识 /v1/apps,会回 404** —— 而客户端
    // 无从区分「这版不支持」与「这版支持但此刻没数据」,所以绝不「试着拨一下
    // 看看」(status watch 那次真机实测过绕过门的代价:CPU 常驻 26%~46%、
    // 吞吐上千次/秒)。
    //
    // **nil 与 [] 是两件事**:nil 是「这版压根没声明过能力」(旧版,键缺席),
    // [] 是「声明了、一个都没有」。两者都不开这个入口,但不是同一件事 ——
    // GuardianStatus.capabilities 刻意保留了这个区分。
    static func testCapabilityGate() {
        expect(!appTrafficAvailable(capabilities: nil), "能力未声明(旧版)却判成可用")
        expect(!appTrafficAvailable(capabilities: []), "空能力集却判成可用")
        expect(!appTrafficAvailable(capabilities: ["rules", "servers"]), "别的能力却开了这个入口")
        expect(appTrafficAvailable(capabilities: ["rules", "apps"]), "声明了 apps 却判成不可用")
    }

    // 刷新间隔必须**明显**短于订阅 TTL。
    //
    // 订阅是靠每一次拉取续期的(Core 侧 appTrafficTTL = 30 秒,惰性结算)。
    // 间隔一旦逼近 TTL,订阅就会在两次刷新之间过期:窗口开着,界面却反复跳回
    // 「Not collecting app traffic right now.」,而且每次续上都从零开始计数。
    //
    // **这一对数字是跨语言手抄的**(Go 侧 appTrafficTTL 未导出),这条只能钉住
    // Swift 这一侧留了余量,证明不了 Go 那边真的是 30 —— 与
    // guardianStatusWatchTimeout 注释里记下的是同一个局限。
    static func testRefreshIntervalStaysWellInsideTheSubscriptionTTL() {
        expect(appTrafficRefreshSeconds > 0, "刷新间隔必须为正,否则定时器不会触发")
        expect(appTrafficRefreshSeconds * 3 < appTrafficSubscriptionTTLSeconds,
               "刷新间隔 \(appTrafficRefreshSeconds)s 对 TTL \(appTrafficSubscriptionTTLSeconds)s 没有余量")
    }

    // 界面上那句「字节数是近似值」不是可选的:端口复用会让残留字节算到新连接
    // 头上,spec 明写「界面不该把它显示成精确账」。
    static func testApproximateNoteSaysWhichNumbersAreApproximate() {
        expect(appTrafficApproximateNote.contains("approximate"),
               "那句小字没说这些数是近似的:\(appTrafficApproximateNote)")
        expect(appTrafficApproximateNote.lowercased().contains("byte"),
               "那句小字没点明说的是字节数:\(appTrafficApproximateNote)")
        // 点明**为什么**近似 —— 只说「近似」会被读成「四舍五入」,而真实原因
        // 是端口复用,那是用户看到数字对不上时唯一能自洽的解释。
        expect(appTrafficApproximateNote.lowercased().contains("port"),
               "那句小字没说清近似的来源(端口复用):\(appTrafficApproximateNote)")
    }

    // `rules` 非空 = 这一行是**用户自己写的规则**决定的(命中内建 china 列表时
    // 服务端给空)。那正是唯一可行动的那一半:用户能去改的只有自己写的规则。
    // 所以有话说时才占地方,没话说时一个字都不加。
    static func testDetailNamesTheUserRuleThatDecidedIt() {
        let report = AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: .direct, rows: [
                AppTrafficRow(app: "Steam", conns: 2, bytesUp: 1, bytesDown: 2, rules: ["*.steamstatic.com"]),
            ]),
        ]))
        let rows = report.rows()
        guard case .entry(_, let detail)? = rows.first(where: {
            if case .entry = $0 { return true }
            return false
        }) else {
            expect(false, "没有渲染出应用行")
            return
        }
        expect(detail.contains("*.steamstatic.com"),
               "没有点名那条用户规则,用户无从知道该去改哪一行:\(detail)")
    }

    static func testDetailSaysNothingAboutRulesWhenTheBuiltinListDecided() {
        let report = AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: .direct, rows: [
                AppTrafficRow(app: "Safari", conns: 2, bytesUp: 1, bytesDown: 2),
            ]),
        ]))
        let rows = report.rows()
        guard case .entry(_, let detail)? = rows.first(where: {
            if case .entry = $0 { return true }
            return false
        }) else {
            expect(false, "没有渲染出应用行")
            return
        }
        expect(!detail.lowercased().contains("rule"),
               "没有用户规则可点名时仍然提了规则 —— 那会让内建判定看起来像用户配的:\(detail)")
    }

    static func main() {
        testDecodesReportWithMissingRowsArray()
        testDecodesGroupWithNullRows()
        testDecodesGroupToleratesEntirelyMissingRowsKeyDefensively()
        testDecodesReportWithMissingErrorKey()
        testGroupsKeepTunnelDirectBlockedOrder()
        testUnknownAppRendersAsExplicitLabel()
        testThreeEmptyStatesRenderDifferently()
        testAllEmptyGroupsRenderAsGenuinelyEmpty()
        testCapabilityGate()
        testRefreshIntervalStaysWellInsideTheSubscriptionTTL()
        testApproximateNoteSaysWhichNumbersAreApproximate()
        testDetailNamesTheUserRuleThatDecidedIt()
        testDetailSaysNothingAboutRulesWhenTheBuiltinListDecided()
        // 通过横幅是「这个套件真的跑过」的唯一证据 —— 退出码只证明「没失败」。
        if failures == 0 {
            print("AppTrafficModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
