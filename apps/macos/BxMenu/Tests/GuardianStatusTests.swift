import Foundation

@main
struct GuardianStatusTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // Guardian 的 Status JSON 字段远多于菜单需要的几个;解码必须容忍未知字段,
        // 也必须容忍 last_error 缺席(omitempty:成功时它根本不出现)。
        // recovery 是 Go 侧的值类型字段(无 omitempty),真实响应里恒是完整对象——
        // 一个空 `{}` 从不会真的出现,写成完整形状避免跟 Task 2 新增的
        // `GuardianStatus.recovery: RecoverySnapshot?` 解码(必需子字段)打架。
        let fullBody = Data("""
        {"schema_version":1,"desired":"on","phase":"committed","protection_state":"protected",
         "network_generation":"wifi-a",
         "recovery":{"recovery_id":"","state":"","stage":"","reason":"","attempt":0,
                     "started_at":"0001-01-01T00:00:00Z","updated_at":"0001-01-01T00:00:00Z"},
         "dns_state":"managed","dns_managed":true,
         "guardian_version":"v0.2.9","runtime_version":"v0.2.9"}
        """.utf8)

        do {
            let status = try JSONDecoder().decode(GuardianStatus.self, from: fullBody)
            expect(status.desired == "on", "desired = \(status.desired)")
            expect(status.phase == "committed", "phase = \(status.phase)")
            expect(status.protectionState == "protected", "protectionState = \(status.protectionState)")
            expect(status.lastError == nil, "成功响应不该有 lastError,实际 \(String(describing: status.lastError))")
        } catch {
            expect(false, "完整响应解码失败: \(error)")
        }

        // 失败响应带 last_error,菜单要拿它去查指引
        let failureBody = Data(#"{"schema_version":1,"desired":"on","phase":"failed","protection_state":"needs_attention","last_error":"core_ownership_uncertain"}"#.utf8)
        do {
            let status = try JSONDecoder().decode(GuardianStatus.self, from: failureBody)
            expect(status.lastError == "core_ownership_uncertain", "lastError = \(String(describing: status.lastError))")
        } catch {
            expect(false, "失败响应解码失败: \(error)")
        }

        // 完整 HTTP 响应经泛型解码走通
        let httpBody = Data(#"{"schema_version":1,"desired":"off","phase":"committed","protection_state":"off"}"#.utf8)
        let http = Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(httpBody.count)\r\n\r\n".utf8)
            + httpBody
        do {
            let status = try decodeGuardianHTTPResponse(http, expectedStatus: 200, as: GuardianStatus.self)
            expect(status.desired == "off", "HTTP 解码 desired = \(status.desired)")
        } catch {
            expect(false, "HTTP 泛型解码失败: \(error)")
        }

        if failures == 0 {
            print("GuardianStatusTests passed")
        } else {
            exit(1)
        }
    }
}
