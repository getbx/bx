import Foundation

@main
struct TransitionNoticeTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static let t0 = Date(timeIntervalSince1970: 1_000_000)
    static func at(_ seconds: TimeInterval) -> Date { t0.addingTimeInterval(seconds) }

    // 信号投影:protection_state × tunnel_healthy 折成菜单关心的几档。
    // **tunnel_healthy 缺席不是「不健康」**(与 StatusReport 同一条:键缺席是
    // 「Guardian 没说」,不能凭空造出一句 "tunnel down")。
    static func testSignalProjection() {
        expect(protectionSignal(protectionState: "protected", tunnelHealthy: true) == .protectedHealthy, "protected+healthy")
        expect(protectionSignal(protectionState: "protected", tunnelHealthy: false) == .protectedTunnelDown, "protected+unhealthy")
        expect(protectionSignal(protectionState: "protected", tunnelHealthy: nil) == .protectedHealthy, "protected+未说 ⇒ 不编一句隧道断了")
        expect(protectionSignal(protectionState: "blocked", tunnelHealthy: nil) == .blocked, "blocked")
        expect(protectionSignal(protectionState: "needs_attention", tunnelHealthy: true) == .attention, "needs_attention")
        expect(protectionSignal(protectionState: "off", tunnelHealthy: nil) == .off, "off")
        expect(protectionSignal(protectionState: "starting", tunnelHealthy: nil) == .transient, "starting 是过渡态")
        expect(protectionSignal(protectionState: "recovering", tunnelHealthy: nil) == .transient, "recovering 是过渡态")
        expect(protectionSignal(protectionState: "something-new", tunnelHealthy: nil) == .transient, "认不出的状态按过渡态,不发通知")
    }

    // **主用例:受保护 → 阻断,持续 30 秒才响,只响一次;恢复时收一条「已恢复」。**
    static func testDegradationNotifiesOnceAfterHoldAndRecoveryOnce() {
        var tracker = TransitionNoticeTracker()
        expect(tracker.observe(.protectedHealthy, at: at(0)) == nil, "稳态不响")
        expect(tracker.observe(.blocked, at: at(1)) == nil, "刚变坏不响(要等 30 秒)")
        expect(tracker.observe(.blocked, at: at(20)) == nil, "20 秒还不到门槛")
        let notice = tracker.observe(.blocked, at: at(31))
        expect(notice?.kind == .degraded, "31 秒该响:\(String(describing: notice))")
        expect(notice?.title.contains("blocked") == true, "标题要说阻断:\(notice?.title ?? "")")
        expect(tracker.observe(.blocked, at: at(90)) == nil, "同一段故障不许再响")
        expect(tracker.observe(.blocked, at: at(600)) == nil, "十分钟后仍是同一段故障,不响")
        let back = tracker.observe(.protectedHealthy, at: at(700))
        expect(back?.kind == .recovered, "恢复要响一条:\(String(describing: back))")
        expect(tracker.observe(.protectedHealthy, at: at(701)) == nil, "恢复只响一次")
    }

    // 30 秒内就恢复的抖动:一条都不响 —— 睡醒后 Wi-Fi 起落几秒就好,响了就是噪声。
    static func testShortBlipIsSilent() {
        var tracker = TransitionNoticeTracker()
        _ = tracker.observe(.protectedHealthy, at: at(0))
        expect(tracker.observe(.protectedTunnelDown, at: at(1)) == nil, "刚断")
        expect(tracker.observe(.protectedTunnelDown, at: at(10)) == nil, "10 秒")
        expect(tracker.observe(.protectedHealthy, at: at(15)) == nil, "15 秒就恢复了,连「已恢复」都不该响 —— 没人被告知过它坏了")
    }

    // 隧道断开与阻断是同一段故障的两种说法:先 tunnelDown 再 blocked 不重开计时,
    // 也不当成两段故障响两次。
    static func testDegradedFlavoursShareOneEpisode() {
        var tracker = TransitionNoticeTracker()
        _ = tracker.observe(.protectedHealthy, at: at(0))
        _ = tracker.observe(.protectedTunnelDown, at: at(1))
        _ = tracker.observe(.blocked, at: at(20))
        expect(tracker.observe(.blocked, at: at(32))?.kind == .degraded, "从第一次变坏算起 31 秒就该响")
        expect(tracker.observe(.protectedTunnelDown, at: at(40)) == nil, "换一种坏法不再响")
        expect(tracker.observe(.attention, at: at(50)) == nil, "needs_attention 也是同一段")
    }

    // 过渡态(starting/recovering)不算变好也不算变坏:blocked → recovering →
    // blocked 仍是同一段故障;recovering 本身不触发「已恢复」。
    static func testTransientStatesNeitherStartNorEndAnEpisode() {
        var tracker = TransitionNoticeTracker()
        _ = tracker.observe(.protectedHealthy, at: at(0))
        _ = tracker.observe(.blocked, at: at(1))
        expect(tracker.observe(.transient, at: at(10)) == nil, "recovering 不响")
        expect(tracker.observe(.blocked, at: at(35))?.kind == .degraded, "经过 recovering 回到 blocked 仍按第一次变坏计时")
        expect(tracker.observe(.transient, at: at(40)) == nil, "recovering 不是「已恢复」")
        expect(tracker.observe(.protectedHealthy, at: at(45))?.kind == .recovered, "真回到 protected 才响恢复")
    }

    // **用户自己关掉的不是事件。** off 不算变坏;从 off 打开也不是「恢复」。
    // 开着保护时故障、用户中途手动关掉:故障段静默结束,不发「已恢复」——
    // 他手动关的,他知道。
    static func testUserOffIsNeverAnEvent() {
        var tracker = TransitionNoticeTracker()
        _ = tracker.observe(.protectedHealthy, at: at(0))
        expect(tracker.observe(.off, at: at(1)) == nil, "关掉不响")
        expect(tracker.observe(.off, at: at(100)) == nil, "关着不响")
        expect(tracker.observe(.protectedHealthy, at: at(200)) == nil, "打开不是恢复")
        _ = tracker.observe(.blocked, at: at(201))
        expect(tracker.observe(.blocked, at: at(240))?.kind == .degraded, "变坏照响")
        expect(tracker.observe(.off, at: at(250)) == nil, "用户关掉:故障段静默结束")
        expect(tracker.observe(.protectedHealthy, at: at(300)) == nil, "之后再打开不是「已恢复」")
    }

    // 第一次观测就是坏的(菜单刚启动、机器早已 blocked):不知道它之前好过,
    // 不响 —— 上一段的故事菜单没看见,不该替它讲。
    static func testFirstObservationNeverNotifies() {
        var tracker = TransitionNoticeTracker()
        expect(tracker.observe(.blocked, at: at(0)) == nil, "首次观测不响")
        expect(tracker.observe(.blocked, at: at(60)) == nil, "没见过它好过,不响")
        expect(tracker.observe(.protectedHealthy, at: at(120)) == nil, "没通知过坏,就没有「已恢复」")
    }

    // 从 off 打开后直接坏掉(没经过 protectedHealthy):用户正站在旁边等结果,
    // 菜单进度条已经在说话,不再叠一条通知。
    static func testFailureRightAfterTurnOnIsSilent() {
        var tracker = TransitionNoticeTracker()
        _ = tracker.observe(.off, at: at(0))
        _ = tracker.observe(.transient, at: at(1))
        _ = tracker.observe(.blocked, at: at(2))
        expect(tracker.observe(.blocked, at: at(60)) == nil, "开启失败由菜单进度条说,不发通知")
    }

    // 文案:三种坏法各说各的,恢复一句;没有中文(菜单文案统一英文,CJK 守卫
    // 在 Go 侧钉整个目录,这里只钉这几句)。
    static func testNoticeTextsAreEnglishAndSpecific() {
        let blocked = transitionNoticeText(kind: .degraded, signal: .blocked)
        let down = transitionNoticeText(kind: .degraded, signal: .protectedTunnelDown)
        let attention = transitionNoticeText(kind: .degraded, signal: .attention)
        let back = transitionNoticeText(kind: .recovered, signal: .protectedHealthy)
        expect(blocked.body.contains("kill-switch") || blocked.body.lowercased().contains("blocking"), "blocked 要说明流量被拦下:\(blocked.body)")
        expect(down.body.lowercased().contains("tunnel"), "tunnelDown 要点名隧道:\(down.body)")
        expect(attention.body.lowercased().contains("menu"), "attention 要指去菜单:\(attention.body)")
        expect(back.title.lowercased().contains("protected"), "恢复要说回到受保护:\(back.title)")
        for text in [blocked, down, attention, back] {
            for s in [text.title, text.body] {
                expect(s.unicodeScalars.allSatisfy { $0.value < 0x2E80 }, "文案混入了 CJK:\(s)")
                expect(!s.isEmpty, "文案不能为空")
            }
        }
    }

    // 门槛是常量、且是「至少 30 秒」—— 短于一次兜底轮询(3 秒兵底 / 60 秒兜底
    // 之间)的抖动本就不该被看见。
    static func testHoldThresholdIsThirtySeconds() {
        expect(transitionNoticeHoldSeconds == 30, "门槛 = \(transitionNoticeHoldSeconds)")
    }

    static func main() {
        testSignalProjection()
        testDegradationNotifiesOnceAfterHoldAndRecoveryOnce()
        testShortBlipIsSilent()
        testDegradedFlavoursShareOneEpisode()
        testTransientStatesNeitherStartNorEndAnEpisode()
        testUserOffIsNeverAnEvent()
        testFirstObservationNeverNotifies()
        testFailureRightAfterTurnOnIsSilent()
        testNoticeTextsAreEnglishAndSpecific()
        testHoldThresholdIsThirtySeconds()
        if failures == 0 {
            print("TransitionNoticeTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
