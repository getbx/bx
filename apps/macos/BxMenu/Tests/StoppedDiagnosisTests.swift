import Foundation

@main
struct StoppedDiagnosisTests {
    static func main() {
        // ── 本套件存在的理由 ────────────────────────────────────────────────
        // 菜单只在 **Guardian 的 socket 拨不通** 时才走到这条判定。Guardian 不在
        // 不等于 Core 不在:CLAUDE.md 记着 `launchctl bootout` 的 SIGTERM **不可靠地**
        // 投给 Core(强制拆除因此必须经 /v0/shutdown 单独关它),所以「Guardian 的
        // launchd job 没装载、而 Core 还在转发流量」是一个真实可达的状态。
        //
        // 那个状态下若判 `.off(.serviceStopped)`:菜单画灰盾、写 "Not running"、
        // 给一个 "Start Protection",而 split-default 路由与 DNS 都还活着;更糟的是
        // quitPlan 对 `.offServiceStopped` 判 terminateImmediately,用户点 Quit 后
        // 菜单直接消失、什么都没关 —— **保护在跑但没有任何指示灯**,本项目拒绝
        // 交付的那个状态。`.setupNeeded` 同样判 terminateImmediately,所以它也必须
        // 被同一道关卡挡住。
        let coreAlive = "Guardian not responding; protection may still be on"

        // ① 回归本体:Core 的控制 socket 还应答 → 绝不能说「没在跑」
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "ok", serviceActive: "fail", coreSocket: "ok"
        )) == .warning(coreAlive),
        "Guardian 的 job 没装载但 Core 还应答时,必须报告警而不是 off")

        // ② 同一道关卡也要挡住 setupNeeded(它同样 terminateImmediately)
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "fail", serviceActive: "fail", coreSocket: "ok"
        )) == .warning(coreAlive),
        "Core 还在应答时不得建议「去跑 setup」,那同样会让 Quit 直接退出")

        // ③ 真的都停了:Core socket 拨不通 + job 没装载 = 两条新鲜的否定观测
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "ok", serviceActive: "fail", coreSocket: "warn",
            coreSocketDetail: "dial unix /var/run/bx/core.sock: connect: no such file or directory"
        )) == .serviceStopped,
        "Core 与 Guardian 都不应答、job 也没装载 = off(serviceStopped)")

        // ④ 没配置过:service_installed=fail(且 Core 确实不在)
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "fail", serviceActive: "fail", coreSocket: "warn"
        )) == .setupNeeded,
        "没装 unit 且 Core 不在 = 要用户去跑 setup")

        // ⑤ job 装载着、Core socket 却拨不通:原样透出 doctor 的 detail
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "ok", serviceActive: "ok", coreSocket: "warn", coreSocketDetail: "permission denied"
        )) == .warning("permission denied"),
        "socket 有问题时把 doctor 的 detail 原样带给用户")
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "ok", serviceActive: "ok", coreSocket: "warn"
        )) == .warning("Status socket unavailable"),
        "socket 有问题但 doctor 没给 detail 时给一句兜底")

        // ⑥ 什么都说不上来
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "ok", serviceActive: "ok", coreSocket: "ok"
        )) == .warning(coreAlive),
        "Guardian 拨不通(能走到这条判定的前提)而其余都正常 —— Core 还在,报告警")

        // ⑦ **「问不出来」不得当成「Core 已经死了」。**
        // doctor 没给出 status_socket 这条检查时,我们**没有证据**说 Core 不在,
        // 于是不许说 off:`.off(.serviceStopped)` 的全部安全性都建立在「Core 的
        // socket 已被证实不应答」之上,少了这条证据它就退化成只信 launchd 对
        // Guardian job 的看法 —— 而本轮修的正是「那两件事不是一回事」。
        // 代价是这种(今天不可达的)情形下菜单显示 Needs Attention 而不是 Off,
        // 少一个 Start Protection 按钮;换来的是绝不会在保护还开着时说它没开。
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: "ok", serviceActive: "fail", coreSocket: nil
        )) == .warning("Needs attention"),
        "问不出 Core socket 时不得判 off —— 那是把「没证据」当成「证明了没有」")

        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
        print("StoppedDiagnosisTests passed")
    }

    private static var failures = 0

    private static func expect(_ condition: Bool, _ label: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(label)\n".utf8))
        }
    }
}
