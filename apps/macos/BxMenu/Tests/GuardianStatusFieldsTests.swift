import Foundation

/// Task 2: Guardian `/v1/status` 里菜单要用的字段能不能装进 `GuardianStatus`。
///
/// 三件事,呼应设计里反复强调的那条:**缺席不是错误,`core` 为 nil 与
/// `core.reachable == false` 是两回事,谁都不能替谁。**
@main
struct GuardianStatusFieldsTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // 1) 完整响应:全部新字段(含嵌套 core)都要解出来,值要对。
        let fullBody = Data("""
        {"schema_version":1,"desired":"on","phase":"committed","protection_state":"protected",
         "network_generation":"wifi-a",
         "recovery":{"recovery_id":"r1","state":"succeeded","stage":"done","reason":"manual",
                     "attempt":1,"started_at":"2026-08-08T00:00:00Z","updated_at":"2026-08-08T00:00:05Z"},
         "dns_state":"managed","dns_managed":true,"dns_service":"Wi-Fi",
         "core_version":"v0.3.1",
         "core":{"reachable":true,"tunnel_healthy":true,"latency_ms":42,
                 "server":"vps.example.com","transport":"reality","udp_mode":"proxy"}}
        """.utf8)
        do {
            let status = try JSONDecoder().decode(GuardianStatus.self, from: fullBody)
            expect(status.phase == "committed", "phase = \(status.phase)")
            expect(status.protectionState == "protected", "protectionState = \(status.protectionState)")
            expect(status.recovery?.recoveryID == "r1", "recovery.recoveryID = \(String(describing: status.recovery?.recoveryID))")
            expect(status.dnsState == "managed", "dnsState = \(String(describing: status.dnsState))")
            expect(status.dnsManaged == true, "dnsManaged = \(String(describing: status.dnsManaged))")
            expect(status.dnsService == "Wi-Fi", "dnsService = \(String(describing: status.dnsService))")
            expect(status.coreVersion == "v0.3.1", "coreVersion = \(String(describing: status.coreVersion))")
            if let core = status.core {
                expect(core.reachable == true, "core.reachable = \(String(describing: core.reachable))")
                expect(core.tunnelHealthy == true, "core.tunnelHealthy = \(String(describing: core.tunnelHealthy))")
                expect(core.latencyMS == 42, "core.latencyMS = \(String(describing: core.latencyMS))")
                expect(core.server == "vps.example.com", "core.server = \(String(describing: core.server))")
                expect(core.transport == "reality", "core.transport = \(String(describing: core.transport))")
                expect(core.udpMode == "proxy", "core.udpMode = \(String(describing: core.udpMode))")
            } else {
                expect(false, "core 不该是 nil,响应里明明带了")
            }
        } catch {
            expect(false, "完整响应解码失败: \(error)")
        }

        // 2) Guardian 没接 CoreRuntime provider 时,`core` 键整个不出现——
        //    这不该报错,`status.core` 必须是 nil(而不是某个假装健康的默认值)。
        let noCoreBody = Data("""
        {"schema_version":1,"desired":"off","phase":"idle","protection_state":"off",
         "network_generation":"","recovery":{"recovery_id":"","state":"","stage":"",
         "reason":"","attempt":0,"started_at":"2026-08-08T00:00:00Z","updated_at":"2026-08-08T00:00:00Z"},
         "dns_state":"unknown","dns_managed":false}
        """.utf8)
        do {
            let status = try JSONDecoder().decode(GuardianStatus.self, from: noCoreBody)
            expect(status.core == nil, "core 键缺席时应为 nil,实际 \(String(describing: status.core))")
            expect(status.dnsService == nil, "dns_service 省略时应为 nil,实际 \(String(describing: status.dnsService))")
            expect(status.coreVersion == nil, "core_version 省略时应为 nil,实际 \(String(describing: status.coreVersion))")
        } catch {
            expect(false, "Guardian 省略 core 时不该解码失败: \(error)")
        }

        // 3) core 存在但 reachable=false:Go 侧承诺其余字段是零值——这不是
        //    「隧道不健康」的信号,是「压根没问到」。菜单必须能分清,不能把它
        //    读成 tunnelHealthy=false 就完事、更不能悄悄当成健康。
        let unreachableBody = Data("""
        {"schema_version":1,"desired":"on","phase":"committed","protection_state":"protected",
         "network_generation":"wifi-a","recovery":{"recovery_id":"","state":"","stage":"",
         "reason":"","attempt":0,"started_at":"2026-08-08T00:00:00Z","updated_at":"2026-08-08T00:00:00Z"},
         "dns_state":"managed","dns_managed":true,
         "core":{"reachable":false,"tunnel_healthy":false,"latency_ms":0}}
        """.utf8)
        do {
            let status = try JSONDecoder().decode(GuardianStatus.self, from: unreachableBody)
            if let core = status.core {
                expect(core.reachable == false, "core.reachable = \(String(describing: core.reachable))")
                expect(core.tunnelHealthy == false, "core.tunnelHealthy = \(String(describing: core.tunnelHealthy))")
                expect(core.latencyMS == 0, "core.latencyMS = \(String(describing: core.latencyMS))")
                expect(core.server == nil, "core.server 应为 nil,实际 \(String(describing: core.server))")
                expect(core.transport == nil, "core.transport 应为 nil,实际 \(String(describing: core.transport))")
                expect(core.udpMode == nil, "core.udpMode 应为 nil,实际 \(String(describing: core.udpMode))")
                // reachable 与 tunnelHealthy 这里碰巧同为 false,但那是 Go 侧的零值
                // 承诺,不是可以互相替代的同一件事——下一行钉住不能读反。
                expect(!(core.reachable == false && core.tunnelHealthy == true), "reachable=false 时不该出现 tunnelHealthy=true 这种自相矛盾的读法")
            } else {
                expect(false, "core 键存在时不该解成 nil")
            }
        } catch {
            expect(false, "core.reachable=false 时解码失败: \(error)")
        }

        // 4) **键缺席也必须解得动。** Go 侧今天没给 reachable/tunnel_healthy/
        //    latency_ms 加 omitempty,但那是生产者当下的选择:哪天加上,Go 的测试
        //    照样全绿(它们 unmarshal 回同一个结构体),而菜单会因为缺一个键就整份
        //    解码失败 → .warning("Status unreadable"),即 2026-08-06 那个失明 bug
        //    换个层级重演。缺席 ⇒ nil ⇒ 「不知道」,绝不许被读成 false。
        let partialCoreBody = Data("""
        {"schema_version":1,"desired":"on","phase":"committed","protection_state":"protected",
         "network_generation":"","recovery":{"recovery_id":"","state":"","stage":"",
         "reason":"","attempt":0,"started_at":"2026-08-08T00:00:00Z","updated_at":"2026-08-08T00:00:00Z"},
         "dns_state":"managed","dns_managed":true,
         "core":{"reachable":true}}
        """.utf8)
        do {
            let status = try JSONDecoder().decode(GuardianStatus.self, from: partialCoreBody)
            guard let core = status.core else {
                expect(false, "core 只带一个键时不该解成 nil")
                exit(1)
            }
            expect(core.reachable == true, "在场的键照常读出")
            expect(core.tunnelHealthy == nil, "tunnel_healthy 缺席 ⇒ nil(不知道),不得读成 false")
            expect(core.latencyMS == nil, "latency_ms 缺席 ⇒ nil,不得读成 0")
        } catch {
            expect(false, "core 缺键时不该让整份状态解码失败: \(error)")
        }

        // 5) `.status` 端点走 GET /v1/status,期望 200,用短默认超时——
        //    与 currentRecovery 同档,不该套用 mutation 端点的长超时。
        expect(GuardianEndpoint.status.expectedStatus == 200, "status.expectedStatus = \(GuardianEndpoint.status.expectedStatus)")
        expect(GuardianEndpoint.status.timeout == GuardianEndpoint.currentRecovery.timeout, "status.timeout 应与 currentRecovery 同档")

        if failures == 0 {
            print("GuardianStatusFieldsTests passed")
        } else {
            exit(1)
        }
    }
}
