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

    // 合计句两数各自计,**永远不合成一个总数**(与 leakcheck 的三段计数同一条纪律)。
    static func testSummaryLineCountsFailuresAndWarningsSeparately() {
        let checks = [
            DoctorCheck(name: "a", status: "fail", detail: "", hint: ""),
            DoctorCheck(name: "b", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "c", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "d", status: "ok", detail: "", hint: ""),
        ]
        expect(doctorSummaryLine(checks) == "1 failed · 2 warnings", "合计 = \(doctorSummaryLine(checks))")
        expect(doctorSummaryLine([checks[3]]) == "0 failed · 0 warnings", "全绿也要说清是 0/0,不说「没问题」")
        expect(doctorSummaryLine([checks[1]]) == "0 failed · 1 warning", "单数")
    }

    static func testCheckTitleReadsLikeProse() {
        expect(doctorCheckTitle("config_readable") == "config readable", "下划线换空格")
        expect(doctorCheckTitle("rule_rules_never_in_effect") == "rule rules never in effect", "全部下划线")
    }

    static func main() {
        testDecodesReportWithOptionalDetailAndHint()
        testDoctorAvailableIsGatedByCapability()
        testSortedChecksPutBadFirstAndKeepOrderWithinATier()
        testSummaryLineCountsFailuresAndWarningsSeparately()
        testCheckTitleReadsLikeProse()
        if failures == 0 {
            print("DiagnosticsModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
