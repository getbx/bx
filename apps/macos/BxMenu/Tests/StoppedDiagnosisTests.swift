import Foundation

@main
struct StoppedDiagnosisTests {
    static func main() {
        // ── 本套件存在的理由 ────────────────────────────────────────────────
        // 菜单只在 **Guardian 的 socket 拨不通** 时才走到这条判定。Guardian 不在
        // 不等于 Core 不在:CLAUDE.md 记着 `launchctl bootout` 的 SIGTERM **不可靠地**
        // 投给 Core(强制拆除因此必须经 /v0/shutdown 单独关它),所以「Guardian
        // 没在跑、而 Core 还在转发流量」是一个真实可达的状态。
        //
        // 那个状态下若判 `.off(.serviceStopped)`:菜单画灰盾、写 "Not running"、
        // 给一个 "Start Protection",而 split-default 路由与 DNS 都还活着;更糟的是
        // quitPlan 对 `.offServiceStopped` 判 terminateImmediately,用户点 Quit 后
        // 菜单直接消失、什么都没关 —— **保护在跑但没有任何指示灯**,本项目拒绝
        // 交付的那个状态。`.setupNeeded` 同样判 terminateImmediately,所以它也必须
        // 被同一道关卡挡住。
        //
        // 证据的**来源**在 Task 4 变了(doctor 的三条检查 → 菜单自己的直接观测:
        // 一次 stat + 两次 socket 连接),但判定要守的性质一字未改,下面七条断言
        // 因此逐条保留、只换了字段的表达方式。
        let coreAlive = "Guardian not responding; protection may still be on"

        // ① 回归本体:Core 的控制 socket 还应答 → 绝不能说「没在跑」
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: false, coreSocketAnswering: true
        )) == .warning(coreAlive),
        "Guardian 没人监听但 Core 还应答时,必须报告警而不是 off")

        // ② 同一道关卡也要挡住 setupNeeded(它同样 terminateImmediately)
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: false, guardianListening: false, coreSocketAnswering: true
        )) == .warning(coreAlive),
        "Core 还在应答时不得建议「去跑 setup」,那同样会让 Quit 直接退出")

        // ③ 真的都停了:Core socket 拨不通 + Guardian 也没人监听 = 两条新鲜的否定观测
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: false, coreSocketAnswering: false,
            coreSocketDetail: "connect /var/run/bx/core.sock: no such file or directory"
        )) == .serviceStopped,
        "Core 与 Guardian 都被证实不应答 = off(serviceStopped)")

        // ④ 没配置过:plist 不在盘上(且 Core 确实不在)
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: false, guardianListening: false, coreSocketAnswering: false
        )) == .setupNeeded,
        "没装 unit 且 Core 不在 = 要用户去跑 setup")

        // ⑤ Guardian 像是还在(只是没答上话)、Core socket 却拨不通:原样透出 detail
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: true, coreSocketAnswering: false,
            coreSocketDetail: "permission denied"
        )) == .warning("permission denied"),
        "socket 有问题时把观测到的 detail 原样带给用户")
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: true, coreSocketAnswering: false
        )) == .warning("Status socket unavailable"),
        "socket 有问题但没有 detail 时给一句兜底")

        // ⑥ 什么都说不上来
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: true, coreSocketAnswering: true
        )) == .warning(coreAlive),
        "Guardian 拨不通(能走到这条判定的前提)而其余都正常 —— Core 还在,报告警")

        // ⑦ **「问不出来」不得当成「Core 已经死了」。**
        // 观测不出 Core socket 的死活时,我们**没有证据**说 Core 不在,于是不许说
        // off:`.off(.serviceStopped)` 的全部安全性都建立在「Core 的 socket 已被
        // 证实不应答」之上。代价是这种情形下菜单显示 Needs Attention 而不是 Off,
        // 少一个 Start Protection 按钮;换来的是绝不会在保护还开着时说它没开。
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: false, coreSocketAnswering: nil
        )) == .warning("Needs attention"),
        "问不出 Core socket 时不得判 off —— 那是把「没证据」当成「证明了没有」")

        // ── Task 4 新增:Guardian 那一侧的「问不出来」同样不许被当成「不在」 ──
        //
        // 旧证据用的是 doctor 的 `service_active`(launchctl 对 job 装载状态的看法),
        // 那是个二值答案。换成直接观测之后多了一档真实存在的「拨不通但说不清为什么」
        // ——挂住的 Guardian 正落在这一档,而那时屏障很可能仍在生效。
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: nil, coreSocketAnswering: false,
            coreSocketDetail: "operation timed out"
        )) == .warning("operation timed out"),
        "问不出 Guardian 是否还在监听时不得判 off —— 挂住的 Guardian 与不存在的 Guardian 显示应当相反")

        // 两条否定观测缺一不可:Guardian 明确没人监听、Core 却问不出来,同样不判 off。
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: true, guardianListening: false, coreSocketAnswering: nil,
            guardianDetail: "Guardian connection failed (61)."
        )) == .warning("Guardian connection failed (61)."),
        "只有 Guardian 一条否定观测时,把拨号失败的原话透给用户,而不是判 off")

        // 什么都说不上来时,至少把 Guardian 拨号失败那句话带给用户,而不是一句
        // 无从下手的 "Needs attention"。
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: nil, guardianListening: nil, coreSocketAnswering: nil
        )) == .warning("Needs attention"),
        "连拨号原话都没有时才回落到 Needs attention")

        // plist 的「问不出来」也不得被读成「没装过」:那会把一台配置好的机器
        // 打回 Setup Required。
        expect(stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: nil, guardianListening: false, coreSocketAnswering: false
        )) == .serviceStopped,
        "问不出 plist 在不在时不得判 setupNeeded")

        // ── socketObservation:把 errno 翻译成三态 ────────────────────────────
        expect(socketObservation(connectErrno: nil) == true, "连上了就是在应答")
        expect(socketObservation(connectErrno: ENOENT) == false, "socket 文件不存在 = 明确没人在那儿")
        expect(socketObservation(connectErrno: ECONNREFUSED) == false, "没人 accept = 明确没人在那儿")
        expect(socketObservation(connectErrno: ETIMEDOUT) == nil, "超时说明不了对面死活")
        expect(socketObservation(connectErrno: EACCES) == nil, "权限不足说明不了对面死活")

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
