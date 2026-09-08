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

    // **速率现在由服务端按端口做差算出、随行一起发下来**(`bytes_up_rate`/
    // `bytes_down_rate`),不再是客户端拿相邻两次快照做差
    // (`AppTrafficRateTracker`,已删)。这两个键是 omitempty 的:还没有速率
    // 可报时(第一次采样之前)整个缺席,不能让整份应答解码失败;而键**在**
    // 时,哪怕值是 0(应用在,但这一拍没有字节增量),也必须解出一个非 nil
    // 的 Double —— 与「键缺席」是两件不同的事。
    static func testDecodesRateFieldsAndToleratesTheirAbsence() {
        let json = """
        {
          "subscribed": true,
          "report": {
            "groups": [
              { "path": "tunnel", "rows": [
                { "app": "Slack", "conns": 1, "bytes_up": 1, "bytes_down": 2,
                  "bytes_up_rate": 2048, "bytes_down_rate": 0 },
                { "app": "Zoom", "conns": 1, "bytes_up": 0, "bytes_down": 0 }
              ] }
            ]
          }
        }
        """
        guard let report = decode(json) else { return }
        let rows = report.report.groups.first { $0.path == .tunnel }?.rows ?? []
        expect(rows.count == 2, "行数不对:\(rows.count)")
        expect(rows.first?.bytesUpRate == 2048, "非零速率没解出来:\(String(describing: rows.first?.bytesUpRate))")
        // **量出来的 0 必须是非 nil 的 0,不是「没有」。**
        expect(rows.first?.bytesDownRate == 0, "指向 0 的速率没解出来,得到 \(String(describing: rows.first?.bytesDownRate))")
        expect(rows.last?.bytesUpRate == nil, "缺席的 bytes_up_rate 没有落成 nil —— 会被渲染层当成量出来的 0")
        expect(rows.last?.bytesDownRate == nil, "缺席的 bytes_down_rate 没有落成 nil")
    }

    // 界面上:没有速率的那一格必须是一句「没有」(破折号),不能是 0。
    // `oneRow` 默认不带速率(还没有第二次采样之前的正常状态)。
    static func testEntryShowsNoRatePlaceholderWhenTheKeyIsAbsent() {
        let report = oneRow(path: .tunnel, app: "Slack", up: 4096, down: 8192)
        guard let entry = firstEntry(report.rows()) else { return }
        expect(entry.upRate == appTrafficRateUnavailable,
               "缺席的速率被渲染成了 \(entry.upRate)")
        expect(entry.downRate == appTrafficRateUnavailable,
               "缺席的速率被渲染成了 \(entry.downRate)")
        expect(!entry.upRate.contains("0 B"), "没有速率被显示成了 0")
        // **累计值不许因为没有速率就跟着消失**:速率答「现在多快」,累计答「一共多少」。
        expect(entry.upTotal.contains("4.0 KB"), "累计上行没了:\(entry.upTotal)")
        expect(entry.downTotal.contains("8.0 KB"), "累计下行没了:\(entry.downTotal)")
        expect(entry.conns == "1", "连接数没有单独成列:\(entry.conns)")
    }

    // 键**在**、值是 0 —— 必须渲染成格式化后的 0,不是破折号。这一条与上一条
    // 一起才覆盖 nil/0 的两半;只测其中一半会让将来有人把这两种状态合并回去
    // 而没有任何测试转红。
    static func testEntryShowsFormattedZeroWhenTheRateIsKnownToBeZero() {
        let report = AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: .tunnel, rows: [
                AppTrafficRow(app: "Slack", conns: 1, bytesUp: 4096, bytesDown: 8192,
                              bytesUpRate: 2048, bytesDownRate: 0),
            ]),
        ]))
        guard let entry = firstEntry(report.rows()) else { return }
        expect(entry.upRate.contains("2.0 KB") && entry.upRate.hasSuffix("/s"),
               "速率没有按每秒渲染:\(entry.upRate)")
        expect(entry.downRate == "0 B/s", "量出来的 0 被渲染成了「没有」:\(entry.downRate)")
    }

    // —— 搜索(2026-08-20)。判据住在纯模型里,窗口只把查询串传进来。

    private static func searchReport() -> AppTrafficReport {
        AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: .tunnel, rows: [
                AppTrafficRow(app: "Slack", conns: 1, bytesUp: 0, bytesDown: 0,
                              dests: ["chat.slack.com", "edge.slack.com"]),
            ]),
            AppTrafficGroup(path: .direct, rows: [
                AppTrafficRow(app: "WeChat", conns: 1, bytesUp: 0, bytesDown: 0,
                              rules: ["*.qq.com"], dests: ["short.weixin.qq.com"]),
            ]),
            // **「存在但为空」的组是承重的 fixture,不是凑数。** 无过滤时它必须
            // 被整个跳过(连标题都不发);少了它,「没有过滤时不该出现
            // emptySection」那条断言就没有任何输入能触发 —— 恒真的断言与没有
            // 断言在输出上完全一样。
            AppTrafficGroup(path: .blocked, rows: []),
        ]))
    }

    private static func apps(_ rows: [AppTrafficReport.Row]) -> [String] {
        rows.compactMap { if case .entry(let e) = $0 { return e.app } else { return nil } }
    }

    private static func hasEmptySection(_ rows: [AppTrafficReport.Row]) -> Bool {
        rows.contains { if case .emptySection = $0 { return true } else { return false } }
    }

    // 空查询 ⇒ 与今天完全相同的行序列,**且不发 emptySection**。
    // 两半都要断言:只测「空查询照旧」测不出「无过滤时的空组也补了一行」。
    static func testEmptyQueryChangesNothing() {
        let report = searchReport()
        expect(report.rows(query: "") == report.rows(), "空查询改变了行序列")
        expect(!hasEmptySection(report.rows(query: "")), "没有过滤时不该出现 emptySection")
    }

    static func testFiltersByAppName() {
        expect(apps(searchReport().rows(query: "slack")) == ["Slack"], "按应用名过滤不对")
    }

    // **目的地参与匹配是刻意的**:输入一个域名反查「谁在连它」,这是「某些 app
    // 偷偷连别的服务器」这个用例的另一半。注意 `edge.slack.com` 是那一行的
    // **第二条**目的地 —— 界面上只显示第一条,而匹配必须看全部,否则搜索结果
    // 与用户看到的对不上,而那种不一致没有任何提示。
    static func testFiltersByDestinationIncludingOnesNotShown() {
        expect(apps(searchReport().rows(query: "edge.slack.com")) == ["Slack"],
               "没显示出来的那条目的地不参与匹配")
        expect(apps(searchReport().rows(query: "weixin")) == ["WeChat"], "按目的地过滤不对")
    }

    static func testFiltersByRuleText() {
        expect(apps(searchReport().rows(query: "*.qq.com")) == ["WeChat"], "按规则原文过滤不对")
    }

    static func testQueryIsTrimmedAndCaseInsensitive() {
        expect(apps(searchReport().rows(query: "  SLACK  ")) == ["Slack"],
               "查询串没有 trim 或没有忽略大小写")
    }

    // **组标题过滤时也不消失**,没有匹配的组补一条 emptySection —— 分组随着输入
    // 一个个消失再出现,读者无从判断「这个组里没有匹配」与「这个组本来就是空的」。
    static func testSectionsStayAndSayWhenTheyHaveNoMatch() {
        let rows = searchReport().rows(query: "slack")
        let headers = rows.compactMap { if case .sectionHeader(let t) = $0 { return t } else { return nil } }
        expect(headers.count == 2, "过滤之后组标题少了:\(headers)")
        expect(hasEmptySection(rows), "没有匹配的那一组没有说明自己为什么空着")
    }

    // —— 目的地摘要与 toolTip(2026-08-20)。

    static func testDestSummaryCountsEverythingNotShown() {
        expect(AppTrafficReport.destSummary(dests: ["a.com"], destsMore: 0) == "a.com",
               "只有一条目的地时不该写 +0")
        // 列表里还有 2 条没显示 + 上限之外还有 15 条 ⇒ +17。**漏掉 destsMore
        // 是这里最容易犯的错**,而它恰好让「连了很多个地方」这个信号消失。
        expect(AppTrafficReport.destSummary(dests: ["a.com", "b.com", "c.com"], destsMore: 15) == "a.com +17",
               "+N 少算了:\(String(describing: AppTrafficReport.destSummary(dests: ["a.com", "b.com", "c.com"], destsMore: 15)))")
    }

    // **nil 不是空串。** 空串会让窗口画出一行空白,而一行空白读作「这个应用
    // 没连任何地方」—— 那是另一句话。
    static func testDestSummaryIsNilWithoutDestinations() {
        expect(AppTrafficReport.destSummary(dests: [], destsMore: 0) == nil,
               "没有目的地时摘要不是 nil")
    }

    static func testDestTooltipListsEveryDestination() {
        expect(AppTrafficReport.destTooltip(dests: ["a.com", "b.com"], destsMore: 0) == "a.com\nb.com",
               "toolTip 没有一行一条")
        expect(AppTrafficReport.destTooltip(dests: ["a.com"], destsMore: 3).hasSuffix("…and 3 more"),
               "超出上限的那几条在 toolTip 里没有交代")
        expect(!AppTrafficReport.destTooltip(dests: ["a.com"], destsMore: 0).contains("more"),
               "没有更多时不该写「and 0 more」")
        expect(AppTrafficReport.destTooltip(dests: [], destsMore: 0).isEmpty,
               "没有目的地时 toolTip 该是空串(窗口据此不设 toolTip)")
    }

    // 目的地要真的从解码一路走到 Entry —— 只测那两个纯函数,接不上也不会红。
    static func testEntryCarriesTheDestinationSummary() {
        let report = AppTrafficReport(subscribed: true, report: AppTrafficReportBody(groups: [
            AppTrafficGroup(path: .tunnel, rows: [
                AppTrafficRow(app: "Slack", conns: 1, bytesUp: 0, bytesDown: 0,
                              dests: ["chat.slack.com", "edge.slack.com"], destsMore: 4),
            ]),
        ]))
        guard let entry = firstEntry(report.rows()) else { return }
        expect(entry.destSummary == "chat.slack.com +5", "摘要没接到 Entry 上:\(String(describing: entry.destSummary))")
        expect(entry.destTooltip.contains("edge.slack.com"), "toolTip 没接到 Entry 上")
    }

    static func testDecodesDestinationsAndToleratesTheirAbsence() {
        let json = """
        {
          "subscribed": true,
          "report": { "groups": [ { "path": "tunnel", "rows": [
            { "app": "Slack", "conns": 1, "bytes_up": 0, "bytes_down": 0,
              "dests": ["chat.slack.com"], "dests_more": 2 },
            { "app": "Safari", "conns": 1, "bytes_up": 0, "bytes_down": 0 }
          ] } ] }
        }
        """
        guard let report = decode(json) else { return }
        let rows = report.report.groups.first?.rows ?? []
        expect(rows.count == 2, "行数不对")
        expect(rows.first?.dests == ["chat.slack.com"], "dests 没解出来")
        expect(rows.first?.destsMore == 2, "dests_more 没解出来")
        // 键缺席 ⇒ 空数组 / 0,**不是整份应答解码失败**。
        expect(rows.last?.dests == [], "缺席的 dests 没有落成空数组")
        expect(rows.last?.destsMore == 0, "缺席的 dests_more 没有落成 0")
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
    // 应用名列、规则列(变长文本)不在数字列里,其余全在。
    //
    // **图标不再单独占一列**(2026-08-20 起它住在应用名那一格里),于是数字列的
    // 下标全部往前挪了一位。这一处是那次改动里最容易静默出错的地方:下标错位
    // 不会有任何编译错误,现象只是右对齐落在错的列上 —— 所以这里逐条钉死,
    // 而不是只数个数。
    static func testNumericColumnsCoverEveryNumberAndNothingElse() {
        expect(appTrafficColumnTitles == ["App", "Conns", "Up/s", "Down/s", "Up", "Down", "Rule"],
               "列变了:\(appTrafficColumnTitles)")
        expect(!appTrafficColumnTitles.contains(""), "又出现了一个没有标题的列 —— 图标列回来了?")
        expect(!appTrafficNumericColumns.contains(0), "应用名列被右对齐了")
        expect(!appTrafficNumericColumns.contains(appTrafficColumnTitles.count - 1),
               "规则列(变长文本)被右对齐了")
        expect(appTrafficNumericColumns == [1, 2, 3, 4, 5],
               "数字列的下标不对(图标列去掉后必须整体前移一位):\(appTrafficNumericColumns)")
        expect(appTrafficNumericColumns.count == appTrafficColumnTitles.count - 2,
               "有数字列没被右对齐:\(appTrafficNumericColumns) vs \(appTrafficColumnTitles)")
        expect(appTrafficNumericColumns.allSatisfy { $0 >= 0 && $0 < appTrafficColumnTitles.count },
               "数字列下标越界:\(appTrafficNumericColumns)")
    }

    // 「字节数是近似值」那句:**两个理由都要说出来**,因为用户会分别撞到它们 ——
    // 端口复用,以及「同一个应用在两个组里,字节全在其中一行、另一行是 0」。
    // 后者在并存的流被拆成两行之后才看得见(2026-08-24),而一个显示 0 B 却明明
    // 有活连接的行,不说明白就会被读成「这条流是闲的」。
    //
    // 措辞仍然**只陈述观测得到的现象、不解释实现**(与陈旧提示那句
    // 「Protection may be off.」同一条纪律):说的是「可能全算在一边」,不是
    // 「因为 Aggregate 把整份端口账记给了最近那条记录」。
    static func testApproximateNoteNamesBothReasons() {
        let note = appTrafficApproximateNote.lowercased()
        expect(note.contains("approximate"), "没说这是近似值:\(appTrafficApproximateNote)")
        expect(note.contains("reus"), "没提端口复用这个理由:\(appTrafficApproximateNote)")
        expect(note.contains("two sections") || note.contains("two groups"),
               "没提「同一个应用在两个组里」那个理由 —— 而那正是 0 B 那一行的来源:\(appTrafficApproximateNote)")
        expect(note.contains("may"), "把一个有条件的现象说成了必然:\(appTrafficApproximateNote)")
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


    // **规则的粒度是目的地,不是应用。** 一行右键弹出的候选按这一行的目的地生成:
    // 三段以上的域名给「精确 + *.父域」(cdn.steamstatic.com 通常是整个
    // steamstatic.com 都想直连),两段给 `*.host`(qq.com 本身也在 *.qq.com 里),
    // 裸 IP 原样。空串什么都不给 —— 一个空模式会被 Guardian 拒绝,但在这之前
    // 它已经出现在菜单里当了一个可点的项。
    static func testRuleCandidatesFollowTheShapeOfTheDestination() {
        expect(ruleCandidates(for: "cdn.steamstatic.com") == ["cdn.steamstatic.com", "*.steamstatic.com"],
               "三段域名应给精确 + 父域通配:\(ruleCandidates(for: "cdn.steamstatic.com"))")
        expect(ruleCandidates(for: "qq.com") == ["*.qq.com"],
               "两段域名只给通配:\(ruleCandidates(for: "qq.com"))")
        expect(ruleCandidates(for: "180.158.6.185") == ["180.158.6.185"],
               "裸 IP 原样:\(ruleCandidates(for: "180.158.6.185"))")
        expect(ruleCandidates(for: "2606:4700::1111") == ["2606:4700::1111"],
               "v6 字面量原样:\(ruleCandidates(for: "2606:4700::1111"))")
        expect(ruleCandidates(for: "") == [], "空目的地不该有候选")
        expect(ruleCandidates(for: "localhost") == ["localhost"], "单段名原样")
    }

    // 归一化:大小写与尾点 —— 与 Go 侧 config.NormalizeHostName 同向。
    // 报给 Guardian 的模式必须是配置里那一行会长的样子。
    static func testRuleCandidatesAreNormalized() {
        expect(ruleCandidates(for: "Cdn.SteamStatic.com.") == ["cdn.steamstatic.com", "*.steamstatic.com"],
               "没归一化:\(ruleCandidates(for: "Cdn.SteamStatic.com."))")
    }

    // 菜单项:每个目的地下面先 direct 再 proxy,一条候选两个动作;两个目的地推
    // 出同一个通配时**只出现一次**(两个 helper 域名同属一个父域是常态)。
    static func testRuleMenuOffersBothKindsPerCandidateWithoutDuplicates() {
        let items = appTrafficRuleMenu(dests: ["a.steamstatic.com", "b.steamstatic.com"])
        let patterns = items.map(\.pattern)
        expect(patterns == ["a.steamstatic.com", "a.steamstatic.com",
                            "*.steamstatic.com", "*.steamstatic.com",
                            "b.steamstatic.com", "b.steamstatic.com"],
               "候选顺序或去重不对:\(patterns)")
        let kinds = items.map(\.kind)
        expect(kinds == ["direct", "proxy", "direct", "proxy", "direct", "proxy"], "每条候选要先 direct 后 proxy:\(kinds)")
        expect(items.allSatisfy { $0.kind == "direct" || $0.kind == "proxy" }, "kind 只能是 direct / proxy")
        // 标题是用户看到的那句话,要点名模式 —— 两个「Always direct」并排时
        // 没有模式就分不出哪个是哪个。
        expect(items[0].title == "Always direct: a.steamstatic.com", "标题:\(items[0].title)")
        expect(items[1].title == "Always through tunnel: a.steamstatic.com", "标题:\(items[1].title)")
        expect(appTrafficRuleMenu(dests: []).isEmpty, "没有目的地就没有菜单项")
    }

    // Entry 要把原始目的地带给窗口 —— 右键菜单按它生成,摘要与 toolTip 都是
    // 格式化过的字符串,从它们反推域名是第二份解析。
    static func testEntryCarriesRawDestinations() {
        let json = """
        {"subscribed": true, "report": {"groups": [{"path": "direct", "rows": [
          {"app": "Steam", "conns": 1, "bytes_up": 1, "bytes_down": 1, "dests": ["cdn.steamstatic.com", "1.2.3.4"]}
        ]}]}}
        """
        guard let report = decode(json) else { return }
        guard case .entry(let entry)? = report.rows().last else {
            expect(false, "没有应用行"); return
        }
        expect(entry.dests == ["cdn.steamstatic.com", "1.2.3.4"], "Entry 没带原始目的地:\(entry.dests)")
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
        testDecodesRateFieldsAndToleratesTheirAbsence()
        testEntryShowsNoRatePlaceholderWhenTheKeyIsAbsent()
        testEntryShowsFormattedZeroWhenTheRateIsKnownToBeZero()
        testEntryCarriesTheExecutablePathForTheIcon()
        testIconPathClimbsToTheApplicationBundle()
        testNumericColumnsCoverEveryNumberAndNothingElse()
        testApproximateNoteNamesBothReasons()
        testEmptyQueryChangesNothing()
        testFiltersByAppName()
        testFiltersByDestinationIncludingOnesNotShown()
        testFiltersByRuleText()
        testQueryIsTrimmedAndCaseInsensitive()
        testSectionsStayAndSayWhenTheyHaveNoMatch()
        testDestSummaryCountsEverythingNotShown()
        testDestSummaryIsNilWithoutDestinations()
        testDestTooltipListsEveryDestination()
        testEntryCarriesTheDestinationSummary()
        testDecodesDestinationsAndToleratesTheirAbsence()
        testStaleNoticeOnlyAppearsAfterRepeatedFailures()
        testRuleCandidatesFollowTheShapeOfTheDestination()
        testRuleCandidatesAreNormalized()
        testRuleMenuOffersBothKindsPerCandidateWithoutDuplicates()
        testEntryCarriesRawDestinations()
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
