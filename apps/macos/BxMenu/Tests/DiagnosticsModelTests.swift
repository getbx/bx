import Foundation

@main
struct DiagnosticsModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func decode(_ json: String) -> DoctorReport? {
        do {
            return try JSONDecoder().decode(DoctorReport.self, from: Data(json.utf8))
        } catch {
            expect(false, "解码失败:\(error)")
            return nil
        }
    }

    // 线上形状 = bx doctor --json:detail/hint 是 omitempty,缺席不许抛。
    static func testDecodesReportWithOptionalDetailAndHint() {
        let json = """
        {"ok":false,"kind":"client","version":"v1","secrets_redacted":true,"changes_system":false,
         "changes_network":false,"requires_root":false,
         "checks":[{"name":"config","status":"info","detail":"/etc/bx/config.yaml"},
                   {"name":"config_readable","status":"fail","detail":"permission denied","hint":"sudo bx setup <client-link>"},
                   {"name":"status_socket","status":"ok"}]}
        """
        guard let report = decode(json) else { return }
        expect(!report.ok, "ok 要解出来")
        expect(report.checks.count == 3, "三条 check")
        expect(report.checks[2].detail.isEmpty && report.checks[2].hint.isEmpty, "缺席的 detail/hint 落成空串")
        expect(report.checks[1].hint == "sudo bx setup <client-link>", "hint 原样")
    }

    // 能力键缺席 = 旧版 Guardian ⇒ 不可用(与 logsAvailable / rulesEditingAvailable 同款)。
    static func testDoctorAvailableIsGatedByCapability() {
        expect(!doctorAvailable(capabilities: nil), "nil ⇒ 旧版")
        expect(!doctorAvailable(capabilities: ["logs"]), "没声明 doctor")
        expect(doctorAvailable(capabilities: ["logs", "doctor"]), "声明了")
    }

    // 坏的排前:fail > warn > info > ok;同一档保持服务端顺序(那是 --json 契约的顺序)。
    static func testSortedChecksPutBadFirstAndKeepOrderWithinATier() {
        let checks = [
            DoctorCheck(name: "a", status: "ok", detail: "", hint: ""),
            DoctorCheck(name: "b", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "c", status: "info", detail: "", hint: ""),
            DoctorCheck(name: "d", status: "fail", detail: "", hint: ""),
            DoctorCheck(name: "e", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "f", status: "weird", detail: "", hint: ""),
        ]
        let names = sortedDoctorChecks(checks).map(\.name)
        expect(names == ["d", "b", "e", "c", "f", "a"], "排序 = \(names)(认不出的状态与 info 同档,不丢)")
    }

    // 合计句三数各自计,**永远不合成一个总数**(与 leakcheck 的三段计数同一条纪律)。
    static func testSummaryLineCountsFailuresAndWarningsSeparately() {
        let checks = [
            DoctorCheck(name: "a", status: "fail", detail: "", hint: ""),
            DoctorCheck(name: "b", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "c", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "d", status: "ok", detail: "", hint: ""),
        ]
        expect(doctorSummaryLine(checks) == "1 failed · 2 warnings · 0 not checked", "合计 = \(doctorSummaryLine(checks))")
        expect(doctorSummaryLine([checks[3]]) == "0 failed · 0 warnings · 0 not checked", "全绿也要说清是 0/0/0,不说「没问题」")
        expect(doctorSummaryLine([checks[1]]) == "0 failed · 1 warning · 0 not checked", "单数")
    }

    // **「没查」必须出现在合计句里,而且不许被算成 ok。**
    //
    // 这是 2026-09-12 那个缺陷的用户可见面:升级之后菜单的 Checks 页走
    // /v1/doctor,而那条路上一整类结论(哪条规则在成片失败)从没被检查过 ——
    // 页面顶上却是一句加粗的 `0 failed · 0 warnings`。异常数为 0 完全可能是
    // 因为一条都没查成,这一行必须自己把这件事说出来。
    static func testSummaryLineSaysHowManyWereNotChecked() {
        let skipped = [
            DoctorCheck(name: "traffic_outcomes", status: "not_checked", detail: "", hint: ""),
            DoctorCheck(name: "config_readable", status: "ok", detail: "", hint: ""),
        ]
        let line = doctorSummaryLine(skipped)
        expect(line == "0 failed · 0 warnings · 1 not checked", "合计 = \(line)")
        // 一份「没查」的报告与一份「查了、全好」的报告在这一行上必须不同 ——
        // 否则用户没有任何办法分辨,而两者的处置完全相反。
        let healthy = [DoctorCheck(name: "traffic_outcomes", status: "ok", detail: "", hint: ""), skipped[1]]
        expect(doctorSummaryLine(healthy) != line, "没查与全好的合计句一模一样")
        // 也不许被折进 warning 里:一台用户自己 `bx down` 的机器上「没查」是
        // 正常的,报成警告会让它长期挂着一个找不到出处的黄字。
        expect(!line.hasPrefix("0 failed · 1 warning"), "not_checked 被算成了 warning")
    }

    // 「没查」排在绿行之前:它不是故障,但埋在一堆 ok 下面等于没说。
    static func testNotCheckedSortsAboveTheGreenRows() {
        let checks = [
            DoctorCheck(name: "a", status: "ok", detail: "", hint: ""),
            DoctorCheck(name: "b", status: "not_checked", detail: "", hint: ""),
        ]
        expect(sortedDoctorChecks(checks).map(\.name) == ["b", "a"], "not_checked 要排在 ok 前面")
    }

    static func testCheckTitleReadsLikeProse() {
        expect(doctorCheckTitle("config_readable") == "Config readable", "下划线换空格、首字母大写")
        expect(doctorCheckTitle("rule_rules_never_in_effect") == "Rule rules never in effect", "全部下划线")
        expect(doctorCheckTitle("guardian_dns") == "Guardian DNS", "缩写写成缩写")
        expect(doctorCheckTitle("ipv6_leak") == "IPv6 leak", "首词是缩写时照写缩写")
        // 状态词汇:不许再出现 NOT_CHECKED 这种枚举原文;认不出的状态原样给出,不猜好坏。
        expect(doctorStatusLook("not_checked").label == "Not checked", "not_checked 的人话")
        expect(doctorStatusLook("fail") == DoctorStatusLook(symbol: "xmark.circle.fill", label: "Failed"), "fail 的符号")
        expect(doctorStatusLook("weird").label == "weird" && doctorStatusLook("weird").symbol == "questionmark.circle",
               "认不出的状态原样给出")
    }

    // Run again 之后数据常常与上一次一模一样,页面重画完看起来毫无变化 ——
    // 2026-09-10 真机:所有者点了两次,以为按钮坏了。这一行是「刚才那一下确实
    // 发生过」的唯一证据,所以它必须带秒;只到分钟的话连着点两次仍然看不出。
    static func testCheckedAtLineCarriesSeconds() {
        var utc = TimeZone(identifier: "UTC")!
        let at = Date(timeIntervalSince1970: 1_757_400_425) // 2026-09-09 06:47:05 UTC
        let line = doctorCheckedAtLine(at, timeZone: utc)
        expect(line == "Checked at 06:47:05", "时间戳行 = \(line)")
        utc = TimeZone(secondsFromGMT: 8 * 3600)!
        let shifted = doctorCheckedAtLine(at, timeZone: utc)
        expect(shifted == "Checked at 14:47:05", "跟随时区:\(shifted)")
    }

    static func main() {
        testDecodesReportWithOptionalDetailAndHint()
        testDoctorAvailableIsGatedByCapability()
        testSortedChecksPutBadFirstAndKeepOrderWithinATier()
        testSummaryLineCountsFailuresAndWarningsSeparately()
        testSummaryLineSaysHowManyWereNotChecked()
        testNotCheckedSortsAboveTheGreenRows()
        testCheckTitleReadsLikeProse()
        testCheckedAtLineCarriesSeconds()
        if failures == 0 {
            print("DiagnosticsModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
