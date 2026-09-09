import Foundation

@main
struct LogsModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func decode(_ json: String) -> LogsReport? {
        do {
            return try JSONDecoder().decode(LogsReport.self, from: Data(json.utf8))
        } catch {
            expect(false, "解码失败:\(error)")
            return nil
        }
    }

    // 线上形状:lines 恒在场(可能是空数组),unavailable 只在读不到时出现。
    static func testDecodesTailsAndToleratesMissingUnavailable() {
        let json = """
        {"logs":[{"name":"guardian-errors","path":"/var/log/bx-guard.err.log","lines":["a","b"]},
                 {"name":"core","path":"/var/log/bx.log","lines":[],"unavailable":"could not read this log"}]}
        """
        guard let report = decode(json) else { return }
        expect(report.logs.count == 2, "两份来源")
        expect(report.logs[0].lines == ["a", "b"] && report.logs[0].unavailable.isEmpty, "第一份:\(report.logs[0])")
        expect(report.logs[1].lines.isEmpty && !report.logs[1].unavailable.isEmpty, "第二份要带 unavailable:\(report.logs[1])")
    }

    // 能力键缺席 = 旧版 Guardian,不是「不支持」的同义反复 —— 判据与 rulesEditingAvailable 同款。
    static func testLogsAvailableIsGatedByCapability() {
        expect(!logsAvailable(capabilities: nil), "nil ⇒ 旧版,不可用")
        expect(!logsAvailable(capabilities: ["rules"]), "没声明 logs ⇒ 不可用")
        expect(logsAvailable(capabilities: ["rules", "logs"]), "声明了 ⇒ 可用")
    }

    // 失败码定位:Guardian 日志的形状是 `guardian_xxx … code=… err=…`,按子串找。
    static func testLogLinesMatchingFindsTheFailureCode() {
        let lines = ["guardian_mutation_requested endpoint=/v1/up uid=501",
                     "guardian_mutation_result outcome=failed code=core_ownership_uncertain",
                     "guardian_core_scan enumerated=766 readable=765 cores=1"]
        expect(logLinesMatching(lines, code: "core_ownership_uncertain") == [1], "该命中第 1 行")
        expect(logLinesMatching(lines, code: nil).isEmpty, "没有码就不高亮任何行")
        expect(logLinesMatching(lines, code: "").isEmpty, "空码同上")
        expect(logLinesMatching(lines, code: "nope").isEmpty, "没命中就是空集,不是「全部」")
    }

    static func main() {
        testDecodesTailsAndToleratesMissingUnavailable()
        testLogsAvailableIsGatedByCapability()
        testLogLinesMatchingFindsTheFailureCode()
        if failures == 0 {
            print("LogsModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
