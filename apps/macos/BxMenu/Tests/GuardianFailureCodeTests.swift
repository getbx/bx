import Foundation

/// Guardian 的失败码走完「500 响应体 → 解码 → 抛出的错误 → 菜单那一行」全程。
///
/// 这条链此前是断的:decodeGuardianHTTPResponse 在读响应体之前就抛
/// `.status(500)`,于是 toggleFailureHint 里那三条指引整套是死代码,用户只
/// 看得到 "Guardian request failed (500)."。本套件按整条链断言,不只断言
/// 某一段函数自己自洽。
@main
struct GuardianFailureCodeTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func response(status: Int, body: String) -> Data {
        Data("HTTP/1.1 \(status) X\r\nContent-Type: application/json\r\nContent-Length: \(body.utf8.count)\r\n\r\n".utf8)
            + Data(body.utf8)
    }

    /// 走一次真实的解码,把抛出的错误交给菜单要用的那两个函数。
    static func toggleOutcome(status: Int, body: String) -> (code: String?, message: String?) {
        do {
            _ = try decodeGuardianHTTPResponse(response(status: status, body: body),
                                               expectedStatus: 200,
                                               as: GuardianStatus.self)
            return (nil, nil)
        } catch {
            let code = guardianFailureCode(of: error)
            return (code, toggleFailureMessage(code: code, transportDescription: error.localizedDescription))
        }
    }

    static func main() {
        // ① 带码的 500:码必须一路走到指引。这是整个失败指引功能存在的理由。
        let uncertain = toggleOutcome(
            status: 500,
            body: #"{"error":"guardian operation failed","code":"core_ownership_uncertain"}"#
        )
        expect(uncertain.code == "core_ownership_uncertain",
               "500 的失败码必须能从错误里取出来,实际 = \(String(describing: uncertain.code))")
        expect(uncertain.message == toggleFailureHint(code: "core_ownership_uncertain"),
               "带码的失败必须显示该码的专属指引,实际 = \(String(describing: uncertain.message))")
        expect(uncertain.message?.contains("bx down") == true,
               "core_ownership_uncertain 的指引必须给出脱困命令")

        // recovery_incomplete / guardian_busy 同样是 500 上的短路哨兵,
        // Go 侧 failureCodeForError 由错误本身命名它们(不经 needsAttention)。
        for code in ["recovery_incomplete", "guardian_busy"] {
            let outcome = toggleOutcome(status: 500, body: #"{"error":"guardian operation failed","code":"\#(code)"}"#)
            expect(outcome.code == code, "\(code) 必须原样送达,实际 = \(String(describing: outcome.code))")
            expect(outcome.message == toggleFailureHint(code: code),
                   "\(code) 必须显示专属指引,实际 = \(String(describing: outcome.message))")
        }

        // ② 无码的 500(短路失败没走过 needsAttention,或更旧的 Guardian):
        //    缺席必须与「有码」可区分,且绝不能凭空造出一句指引。
        let codeless = toggleOutcome(status: 500, body: #"{"error":"guardian operation failed"}"#)
        expect(codeless.code == nil, "码缺席时必须是 nil,实际 = \(String(describing: codeless.code))")
        expect(codeless.message?.contains("bx down") != true,
               "无码时不得编造专属指引,实际 = \(String(describing: codeless.message))")
        expect(codeless.message?.isEmpty == false,
               "无码时仍要有一行可见的失败说明")

        // 空串码等同缺席 —— Go 侧永远不会写空 code,但如果写了,不能让菜单
        // 显示 "失败码 "。
        let emptyCode = toggleOutcome(status: 500, body: #"{"error":"x","code":""}"#)
        expect(emptyCode.code == nil, "空码等同无码,实际 = \(String(describing: emptyCode.code))")

        // ③ 响应体损坏/长度对不上:取不到码,但状态码本身仍要如实抛出。
        let brokenBody = Data("HTTP/1.1 500 X\r\nContent-Type: application/json\r\nContent-Length: 9\r\n\r\n{".utf8)
        do {
            _ = try decodeGuardianHTTPResponse(brokenBody, expectedStatus: 200, as: GuardianStatus.self)
            expect(false, "损坏的 500 响应必须抛错")
        } catch GuardianClientError.status(let status, let code) {
            expect(status == 500, "损坏响应的状态码要如实报出,实际 = \(status)")
            expect(code == nil, "读不出体就没有码,不能瞎猜,实际 = \(String(describing: code))")
        } catch {
            expect(false, "损坏的 500 应抛 .status 而非 \(error)")
        }

        // 非 500 的失败(403/503:body 里本来就没有 code)只报状态码。
        let forbidden = toggleOutcome(status: 403, body: #"{"error":"mutation requires root or owner peer"}"#)
        expect(forbidden.code == nil, "403 不带码")
        expect(forbidden.message?.contains("403") == true,
               "403 至少要把状态码报给用户,实际 = \(String(describing: forbidden.message))")

        // ④ 成功路径不受影响:期望的状态码照常解码,不抛错。
        do {
            let ok = try decodeGuardianHTTPResponse(
                response(status: 200, body: #"{"desired":"off","phase":"committed","protection_state":"off"}"#),
                expectedStatus: 200,
                as: GuardianStatus.self
            )
            expect(ok.protectionState == "off", "200 正常解码")
        } catch {
            expect(false, "200 不该抛错: \(error)")
        }

        if failures == 0 {
            print("GuardianFailureCodeTests passed")
        } else {
            exit(1)
        }
    }
}
