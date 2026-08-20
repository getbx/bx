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
            if case let .entry(entry) = row { return entry.app }
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
        expect(appTrafficRefreshSeconds * 3 <= appTrafficSubscriptionTTLSeconds,
               "刷新间隔 \(appTrafficRefreshSeconds)s 对 TTL \(appTrafficSubscriptionTTLSeconds)s 没有余量")
        // **下界同样是判据。** 每一拍都让 root 侧跑一次 OwnersByPort()(两张
        // pcblist + 逐 PID 解名)。只钉上界,「为了更跟手」调到 0.5 秒这种改动
        // 会一路绿灯而扫描频率翻十倍。
        expect(appTrafficRefreshSeconds >= 2,
               "刷新间隔 \(appTrafficRefreshSeconds)s 太密 —— 每一拍都是一次 root 侧全量端口扫描")
    }

    // 连续失败到一定次数就必须在界面上说「这不是此刻的事实」。
    //
    // 静默冻在上一份快照上是这个界面最坏的失效模式:那份快照读起来是「这些应用
    // 此刻正在走隧道」,而此刻保护可能已经被关掉了。
    static func testStaleNoticeOnlyAppearsAfterRepeatedFailures() {
        expect(appTrafficStaleNotice(consecutiveFailures: 0) == nil, "一次都没失败却报了陈旧")
        expect(appTrafficStaleNotice(consecutiveFailures: appTrafficStaleAfterFailures - 1) == nil,
               "还没到门槛就报陈旧 —— 一次瞬时失败会让界面闪一下")
        guard let notice = appTrafficStaleNotice(consecutiveFailures: appTrafficStaleAfterFailures) else {
            expect(false, "到了门槛却没有陈旧提示 —— 窗口会静默冻在上一份快照上")
            return
        }
        expect(notice.lowercased().contains("not updating"),
               "陈旧提示没说清「不再更新了」:\(notice)")
        // **不许断言原因。** bx 在这条路上分不清「保护被关了」「Guardian 正忙」
        // 「Core 刚重启」,断言其中一个就是编一个自己没查过的答案。
        expect(notice.contains("may be"),
               "陈旧提示把一个没查过的原因说成了结论:\(notice)")
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
        guard let entry = firstEntry(report.rows()) else { return }
        expect(entry.rule.contains("*.steamstatic.com"),
               "没有点名那条用户规则,用户无从知道该去改哪一行:\(entry.rule)")
    }

    static func testDetailSaysNothingAboutRulesWhenTheBuiltinListDecided() {
        let report = AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: .direct, rows: [
                AppTrafficRow(app: "Safari", conns: 2, bytesUp: 1, bytesDown: 2),
            ]),
        ]))
        guard let entry = firstEntry(report.rows()) else { return }
        // 没有用户规则可点名时**那一格是空的** —— 编一句「(built-in list)」
        // 会让内建判定看起来像用户配的,而用户去配置文件里根本找不到它。
        expect(entry.rule.isEmpty,
               "没有用户规则可点名时仍然填了规则格:\(entry.rule)")
    }


    // ===== 2026-08-20:图标 / 速率 / 列对齐 / 缺口提示 =====

    // exec_path 是 omitempty 的:问不出路径时整个键缺席,那是**正常状态**
    // (unknown 行、kern.procargs2 读失败),不能让整份应答解码失败。
    static func testDecodesExecPathAndToleratesItsAbsence() {
        let json = """
        {
          "subscribed": true,
          "report": {
            "groups": [
              { "path": "tunnel", "rows": [
                { "app": "Slack", "conns": 1, "bytes_up": 1, "bytes_down": 2,
                  "exec_path": "/Applications/Slack.app/Contents/MacOS/Slack" },
                { "app": "", "conns": 1, "bytes_up": 0, "bytes_down": 0 }
              ] }
            ]
          }
        }
        """
        guard let report = decode(json) else { return }
        let rows = report.report.groups.first { $0.path == .tunnel }?.rows ?? []
        expect(rows.count == 2, "行数不对:\(rows.count)")
        expect(rows.first?.execPath == "/Applications/Slack.app/Contents/MacOS/Slack",
               "exec_path 没解出来:\(String(describing: rows.first?.execPath))")
        expect(rows.last?.execPath == "", "缺席的 exec_path 没落成空串 —— 那一行会去取一个不存在路径的图标")
    }

    // **第一帧不许编造速率。** 速率是相邻两次快照做差得到的,第一份快照没有前一份,
    // 就是没有速率 —— 显示 0 是在说「此刻没有流量」(一句它没查过的话),
    // 拿累计值除以一个猜来的时长则更糟。
    static func testFirstSnapshotProducesNoRatesAtAll() {
        var tracker = AppTrafficRateTracker()
        let report = oneRow(path: .tunnel, app: "Slack", up: 1_000_000, down: 2_000_000)
        let rates = tracker.ingest(report, at: Date(timeIntervalSince1970: 100))
        expect(rates.isEmpty, "第一帧就给出了速率(凭空造的):\(rates)")
    }

    // 相邻两帧做差 —— 这是速率唯一诚实的来源。
    static func testRateComesFromTheDeltaBetweenTwoSnapshots() {
        var tracker = AppTrafficRateTracker()
        _ = tracker.ingest(oneRow(path: .tunnel, app: "Slack", up: 1000, down: 2000),
                           at: Date(timeIntervalSince1970: 100))
        let rates = tracker.ingest(oneRow(path: .tunnel, app: "Slack", up: 6000, down: 2000),
                                   at: Date(timeIntervalSince1970: 105))
        let key = AppTrafficRateKey(path: .tunnel, app: "Slack")
        guard let rate = rates[key] else {
            expect(false, "第二帧没有算出速率:\(rates)")
            return
        }
        expect(rate.bytesUpPerSecond == 1000, "上行速率 \(rate.bytesUpPerSecond) != (6000-1000)/5")
        // 没有变化就是 0,而这个 0 是**量出来的**,与第一帧那个「没有」不是一回事。
        expect(rate.bytesDownPerSecond == 0, "下行没变化却不是 0:\(rate.bytesDownPerSecond)")
    }

    // 键是 (组, 应用名)。同一个应用同时在两组里是常态(Chrome 一半直连一半走隧道),
    // 只按名字做键会把两组的字节混在一起,算出一个谁也不是的速率。
    static func testRateKeyIsGroupAndApp() {
        var tracker = AppTrafficRateTracker()
        func frame(_ tunnelUp: Int64, _ directUp: Int64) -> AppTrafficReport {
            AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
                AppTrafficGroup(path: .tunnel, rows: [
                    AppTrafficRow(app: "Chrome", conns: 1, bytesUp: tunnelUp, bytesDown: 0),
                ]),
                AppTrafficGroup(path: .direct, rows: [
                    AppTrafficRow(app: "Chrome", conns: 1, bytesUp: directUp, bytesDown: 0),
                ]),
            ]))
        }
        _ = tracker.ingest(frame(0, 0), at: Date(timeIntervalSince1970: 0))
        let rates = tracker.ingest(frame(10, 50), at: Date(timeIntervalSince1970: 10))
        expect(rates[AppTrafficRateKey(path: .tunnel, app: "Chrome")]?.bytesUpPerSecond == 1,
               "tunnel 组的速率不对:\(rates)")
        expect(rates[AppTrafficRateKey(path: .direct, app: "Chrome")]?.bytesUpPerSecond == 5,
               "direct 组的速率不对:\(rates)")
    }

    // 两帧之间**新出现**的应用没有速率:它上一帧根本不存在,累计值里可能含着
    // 订阅之前就建好的连接(种子),拿它当「这几秒传的」就是编。
    static func testAppThatAppearsBetweenFramesGetsNoRate() {
        var tracker = AppTrafficRateTracker()
        _ = tracker.ingest(oneRow(path: .tunnel, app: "Slack", up: 10, down: 10),
                           at: Date(timeIntervalSince1970: 0))
        let rates = tracker.ingest(
            AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
                AppTrafficGroup(path: .tunnel, rows: [
                    AppTrafficRow(app: "Slack", conns: 1, bytesUp: 10, bytesDown: 10),
                    AppTrafficRow(app: "Zoom", conns: 1, bytesUp: 999_999, bytesDown: 0),
                ]),
            ])), at: Date(timeIntervalSince1970: 5))
        expect(rates[AppTrafficRateKey(path: .tunnel, app: "Zoom")] == nil,
               "刚出现的应用被算出了速率:\(rates)")
        expect(rates[AppTrafficRateKey(path: .tunnel, app: "Slack")] != nil,
               "上一帧就在的应用反而没有速率:\(rates)")
    }

    // 累计值**会往回走**:环形缓冲丢最旧的记录,一个应用的累计字节因此可能变小。
    // 那不是「速率为 0」,是「这两帧之间发生了什么我说不好」—— 不给速率。
    static func testCounterGoingBackwardsYieldsNoRateRatherThanZero() {
        var tracker = AppTrafficRateTracker()
        _ = tracker.ingest(oneRow(path: .tunnel, app: "Slack", up: 5000, down: 5000),
                           at: Date(timeIntervalSince1970: 0))
        let rates = tracker.ingest(oneRow(path: .tunnel, app: "Slack", up: 100, down: 5000),
                                   at: Date(timeIntervalSince1970: 5))
        expect(rates[AppTrafficRateKey(path: .tunnel, app: "Slack")] == nil,
               "累计值倒退却给出了速率:\(rates)")
    }

    // 两帧时间戳相同(理论上的时钟回拨/同一瞬间)不许除以零。
    static func testZeroElapsedProducesNoRates() {
        var tracker = AppTrafficRateTracker()
        let at = Date(timeIntervalSince1970: 42)
        _ = tracker.ingest(oneRow(path: .tunnel, app: "Slack", up: 0, down: 0), at: at)
        let rates = tracker.ingest(oneRow(path: .tunnel, app: "Slack", up: 100, down: 0), at: at)
        expect(rates.isEmpty, "零时长也算出了速率(除以零):\(rates)")
    }

    // 界面上:没有速率的那一格必须是一句「没有」,不能是 0 —— 与上面那条同一件事,
    // 只是这一半发生在渲染层。
    static func testEntryShowsNoRatePlaceholderOnTheFirstFrame() {
        let report = oneRow(path: .tunnel, app: "Slack", up: 4096, down: 8192)
        guard let entry = firstEntry(report.rows()) else { return }
        expect(entry.upRate == appTrafficRateUnavailable,
               "第一帧的上行速率被渲染成了 \(entry.upRate)")
        expect(entry.downRate == appTrafficRateUnavailable,
               "第一帧的下行速率被渲染成了 \(entry.downRate)")
        expect(!entry.upRate.contains("0 B"), "没有速率被显示成了 0")
        // **累计值不许因为有了速率就删掉**:速率答「现在多快」,累计答「一共多少」。
        expect(entry.upTotal.contains("4.0 KB"), "累计上行没了:\(entry.upTotal)")
        expect(entry.downTotal.contains("8.0 KB"), "累计下行没了:\(entry.downTotal)")
        expect(entry.conns == "1", "连接数没有单独成列:\(entry.conns)")
    }

    static func testEntryShowsRateWhenItIsKnown() {
        let report = oneRow(path: .tunnel, app: "Slack", up: 4096, down: 8192)
        let rates = [AppTrafficRateKey(path: .tunnel, app: "Slack"):
                        AppTrafficRate(bytesUpPerSecond: 2048, bytesDownPerSecond: 0)]
        guard let entry = firstEntry(report.rows(rates: rates)) else { return }
        expect(entry.upRate.contains("2.0 KB") && entry.upRate.hasSuffix("/s"),
               "速率没有按每秒渲染:\(entry.upRate)")
        expect(entry.downRate == "0 B/s", "量出来的 0 被渲染成了「没有」:\(entry.downRate)")
    }

    // unknown 行没有路径,渲染层据此**不画图标**(而不是画一个空白占位)。
    static func testEntryCarriesTheExecutablePathForTheIcon() {
        let report = AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: .tunnel, rows: [
                AppTrafficRow(app: "Slack", conns: 1, bytesUp: 0, bytesDown: 0,
                              execPath: "/Applications/Slack.app/Contents/MacOS/Slack"),
            ]),
        ]))
        guard let entry = firstEntry(report.rows()) else { return }
        expect(entry.execPath == "/Applications/Slack.app/Contents/MacOS/Slack",
               "可执行路径没有传到渲染层:\(entry.execPath)")
    }

    // 图标要的是 **.app 包**,不是包里那个可执行文件 —— 对后者取图标拿到的是
    // 一个通用的可执行文件图标,一整列都长一个样,等于没有图标。
    static func testIconPathClimbsToTheApplicationBundle() {
        expect(appIconPath(forExecutable: "/Applications/Slack.app/Contents/MacOS/Slack")
                == "/Applications/Slack.app",
               "没有回到 .app 包:\(appIconPath(forExecutable: "/Applications/Slack.app/Contents/MacOS/Slack"))")
        // helper 进程嵌在外层包里:取**最外层**那个 .app,那才是用户认得的图标。
        let helper = "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper"
        expect(appIconPath(forExecutable: helper) == "/Applications/Google Chrome.app",
               "helper 没有回到最外层的包:\(appIconPath(forExecutable: helper))")
        // 不是 app bundle 的普通可执行文件原样返回(ssh、node 之类)。
        expect(appIconPath(forExecutable: "/usr/bin/ssh") == "/usr/bin/ssh",
               "普通可执行文件的路径被改了")
        expect(appIconPath(forExecutable: "") == "", "空路径不该被造出一个路径来")
    }

    // 列的定义住在纯模型里,窗口只照着摆。**右对齐的判据也在这儿** ——
    // 图标列、应用名列、规则列(变长文本)不在数字列里,其余全在。
    static func testNumericColumnsCoverEveryNumberAndNothingElse() {
        expect(appTrafficColumnTitles.count == 8, "列数变了:\(appTrafficColumnTitles)")
        expect(!appTrafficNumericColumns.contains(0), "图标列被当成数字列右对齐了")
        expect(!appTrafficNumericColumns.contains(1), "应用名列被右对齐了")
        expect(!appTrafficNumericColumns.contains(appTrafficColumnTitles.count - 1),
               "规则列(变长文本)被右对齐了")
        expect(appTrafficNumericColumns.count == appTrafficColumnTitles.count - 3,
               "有数字列没被右对齐:\(appTrafficNumericColumns) vs \(appTrafficColumnTitles)")
        expect(appTrafficNumericColumns.allSatisfy { $0 >= 0 && $0 < appTrafficColumnTitles.count },
               "数字列下标越界:\(appTrafficNumericColumns)")
    }

    // 那句缺口提示:**只陈述观测得到的事实,不断言原因** —— 与陈旧提示同一条
    // 纪律(bx 分不清是保护被关了还是 Guardian 正忙,断言其中一个就是编答案)。
    static func testPreexistingConnectionsNoteStatesTheObservationOnly() {
        let note = appTrafficPreexistingNote.lowercased()
        expect(note.contains("already open"), "没说清是哪一批连接:\(appTrafficPreexistingNote)")
        expect(note.contains("one section") || note.contains("one group"),
               "没说清观测到的现象(只出现在一个组里):\(appTrafficPreexistingNote)")
        expect(note.contains("may"), "把一个有条件的现象说成了必然:\(appTrafficPreexistingNote)")
        expect(note != appTrafficApproximateNote, "两句小字重复了")
    }

    static func oneRow(path: AppTrafficPath, app: String, up: Int64, down: Int64) -> AppTrafficReport {
        AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: path, rows: [
                AppTrafficRow(app: app, conns: 1, bytesUp: up, bytesDown: down),
            ]),
        ]))
    }

    static func firstEntry(_ rows: [AppTrafficReport.Row]) -> AppTrafficReport.Entry? {
        for row in rows {
            if case let .entry(entry) = row { return entry }
        }
        expect(false, "没有渲染出应用行")
        return nil
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
        testDecodesExecPathAndToleratesItsAbsence()
        testFirstSnapshotProducesNoRatesAtAll()
        testRateComesFromTheDeltaBetweenTwoSnapshots()
        testRateKeyIsGroupAndApp()
        testAppThatAppearsBetweenFramesGetsNoRate()
        testCounterGoingBackwardsYieldsNoRateRatherThanZero()
        testZeroElapsedProducesNoRates()
        testEntryShowsNoRatePlaceholderOnTheFirstFrame()
        testEntryShowsRateWhenItIsKnown()
        testEntryCarriesTheExecutablePathForTheIcon()
        testIconPathClimbsToTheApplicationBundle()
        testNumericColumnsCoverEveryNumberAndNothingElse()
        testPreexistingConnectionsNoteStatesTheObservationOnly()
        testStaleNoticeOnlyAppearsAfterRepeatedFailures()
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
