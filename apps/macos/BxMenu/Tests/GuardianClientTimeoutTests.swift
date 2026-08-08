import Foundation

@main
struct GuardianClientTimeoutTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // Go 侧 guardianMutationTimeout = 60s。客户端必须严格更长,否则拿到的是自己的
        // 超时而不是服务端给的失败码——而用户最需要的恰恰是那个码。
        expect(
            GuardianEndpoint.turnOn.timeout > 60,
            "turnOn.timeout must exceed Go's 60s mutation ceiling, got \(GuardianEndpoint.turnOn.timeout)"
        )
        expect(
            GuardianEndpoint.turnOff.timeout > 60,
            "turnOff.timeout must exceed Go's 60s mutation ceiling, got \(GuardianEndpoint.turnOff.timeout)"
        )
        expect(
            GuardianEndpoint.turnOn.timeout == GuardianEndpoint.turnOff.timeout,
            "turnOn/turnOff must share the same mutation timeout"
        )

        // 只钉住 endpoint.timeout 本身不够——万一生产 init() 把 overrideTimeout 设成
        // 别的短值,单看 endpoint.timeout 的测试还是会绿。必须经生产 init() 走一遍
        // effectiveTimeout,坐实「真正建出来的客户端」确实解析到这个长超时。
        let production = GuardianClient()
        expect(
            production.effectiveTimeout(for: .turnOn) == GuardianEndpoint.turnOn.timeout,
            "production GuardianClient() must resolve turnOn to the endpoint's own timeout"
        )
        expect(
            production.effectiveTimeout(for: .turnOff) == GuardianEndpoint.turnOff.timeout,
            "production GuardianClient() must resolve turnOff to the endpoint's own timeout"
        )

        // 测试专用 init 的注入超时必须继续优先于端点默认值,不能被这次改动悄悄削弱
        // ——既有 GuardianClientTests.swift 的短超时用例全靠这条路径。
        let overridden = GuardianClient(connectSocket: { -1 }, ioTimeout: 5)
        expect(
            overridden.effectiveTimeout(for: .turnOn) == 5,
            "test-only init(connectSocket:ioTimeout:clock:) must still force its injected ioTimeout"
        )
        expect(
            overridden.effectiveTimeout(for: .currentRecovery) == 5,
            "test-only init override applies uniformly, not just to mutation endpoints"
        )

        if failures == 0 {
            print("GuardianClientTimeoutTests passed")
        } else {
            exit(1)
        }
    }
}
