import Foundation

@main
struct RecoveryPresentationTests {
    static func main() {
        // —— 真机抓到的那个 bug(2026-08-22):`bx down` 拆路由 ⇒ 底层变化 ⇒
        // NetworkObserver 请求一次路径恢复 ⇒ desired 此刻是 off ⇒ Guardian 记下
        // 一条 `ignored`/`off` 快照并**故意什么都不做**。它一直留到被覆盖为止,
        // 于是紧接着的 `bx up` 之后仍在,而菜单把它渲染成 **Blocked / Recovery
        // failed** —— 在一台 `bx status` 报 Protected、隧道健康的机器上。
        // 后果不止难看:恢复浮层会把 Turn Off / Traffic by App / 退出全部挤掉。
        let ignored = recoverySnapshot(state: "ignored", stage: "off", reason: "underlay_changed")
        expect(
            recoverySnapshotForDisplay(ignored, allowsTerminalSuccess: false) == nil,
            "ignored 是 Guardian 说「我故意什么都没做」,不该占着界面"
        )
        expect(
            recoverySnapshotForDisplay(ignored, allowsTerminalSuccess: true) == nil,
            "allowsTerminalSuccess 只放行 succeeded,不该顺带把 ignored 也放进来"
        )
        expect(visibleStatusRecovery(ignored) == nil, "状态栏那条路也要滤掉 ignored")

        // **认不出来的 state 不许被说成「失败」。** 这条钉的是那个 `default` 分支
        // 本身:只把 ignored 过滤掉是治一个实例,把 default 留在「失败」上,下一个
        // Swift 这边没列举到的 Go 状态会原样重演同一个假告警。
        let unknown = recoveryPresentation(for: recoverySnapshot(state: "some_future_state", stage: "off"))
        expect(unknown.indicator == .yellow, "没认出来的状态不是红色 —— 红色带着一个它给不出的断言")
        expect(unknown.title == "Recovery", "没认出来的状态不许宣告 Reconnect Failed / Blocked")
        expect(
            unknown.shortReason == "Unrecognized recovery state (some_future_state)",
            "得把实际的 state 名字打出来,否则看到的人无从查起"
        )
        // 真失败仍然要红,别为了修上面那条把这条一起放走。
        let blocked = recoveryPresentation(
            for: recoverySnapshot(state: "blocked", stage: "blocked", errorCode: "transport_unavailable")
        )
        expect(blocked.indicator == .red, "blocked 仍然是失败")
        expect(blocked.shortReason == "Protected transport unavailable", "blocked 的失败原因")

        // 换网络之后够不着服务器 —— 单独说话(酒店/咖啡店 Wi-Fi 的强制门户)。
        // 判据是 reason 与 error code **两个都要对**:一条到处出现的提示会被训练
        // 成墙纸,而它要提醒的事一年遇不到几次。
        let captive = recoveryPresentation(for: recoverySnapshot(
            state: "failed", stage: "transport_health",
            errorCode: "transport_unavailable", reason: "underlay_changed"))
        expect(captive.shortReason?.contains("sign-in") == true,
               "换网络之后够不着服务器,没有提示可能要登录:\(String(describing: captive.shortReason))")
        expect(captive.indicator == .red, "它仍然是一次真失败,不许降级")
        // 同一个错误码、但**不是**换网络触发的(用户自己点的重连)⇒ 不给这句话。
        let manual = recoveryPresentation(for: recoverySnapshot(
            state: "failed", stage: "transport_health",
            errorCode: "transport_unavailable", reason: "manual"))
        expect(manual.shortReason == "Protected transport unavailable",
               "手动重连失败被安上了门户的说法:\(String(describing: manual.shortReason))")
        // 换网络触发、但别的失败码 ⇒ 也不给。
        let otherCode = recoveryPresentation(for: recoverySnapshot(
            state: "failed", stage: "observe",
            errorCode: "capture_invalid", reason: "underlay_changed"))
        expect(otherCode.shortReason == "Protected path changed",
               "别的失败码被安上了门户的说法:\(String(describing: otherCode.shortReason))")

        let accepted = recoveryPresentation(for: recoverySnapshot(state: "accepted", stage: "queued"))
        expect(accepted.title == "Reconnecting", "accepted title")
        expect(accepted.indicator == .yellow, "accepted indicator")
        expect(accepted.shortReason == "Waiting for Guardian", "accepted reason")

        let running = recoveryPresentation(for: recoverySnapshot(state: "running", stage: "rebind_underlay"))
        expect(running.title == "Reconnecting", "running title")
        expect(running.indicator == .yellow, "running indicator")
        expect(running.shortReason == "Rebinding network path", "running reason")

        let succeeded = recoveryPresentation(for: recoverySnapshot(state: "succeeded", stage: "succeeded"))
        expect(succeeded.title == "Reconnected", "succeeded title")
        expect(succeeded.indicator == .green, "succeeded indicator")
        expect(succeeded.shortReason == nil, "succeeded reason")
        expect(!succeeded.showsSuccessAlert, "succeeded has no modal alert")

        let failed = recoveryPresentation(
            for: recoverySnapshot(
                state: "failed",
                stage: "observe",
                errorCode: "network_unavailable",
                detail: "secret route and server detail"
            )
        )
        expect(failed.title == "Reconnect Failed", "failed title")
        expect(failed.indicator == .red, "failed indicator")
        expect(failed.shortReason == "Network unavailable", "failed reason")
        expect(!(failed.shortReason?.contains("secret") ?? true), "failed reason is redacted")

        let transition = recoveryFailureTransition(
            from: recoverySnapshot(state: "running", stage: "transport_health"),
            errorCode: "recovery_unavailable"
        )
        expect(!transition.reconnectInFlight, "observation failure resets reconnect in-flight state")
        expect(transition.snapshot.state == "failed", "observation failure remains visible")
        let timeoutPresentation = recoveryPresentation(for: transition.snapshot)
        expect(timeoutPresentation.title == "Reconnect Failed", "observation failure title")
        expect(timeoutPresentation.shortReason == "Recovery unavailable", "observation failure has Details reason")

        let submitted = recoverySnapshot(state: "running", stage: "transport_health")
        let replacement = RecoverySnapshot(
            recoveryID: "recovery-2",
            state: "succeeded",
            stage: "succeeded",
            reason: "manual",
            generation: nil,
            lastErrorCode: nil,
            detail: nil,
            attempt: 1,
            startedAt: "2026-07-27T12:00:00Z",
            updatedAt: "2026-07-27T12:00:01Z"
        )
        guard let replacementTransition = recoveryObservationFailure(
            submitted: submitted,
            observed: replacement
        ) else {
            expect(false, "replacement recovery becomes an observation failure")
            return
        }
        expect(!replacementTransition.reconnectInFlight, "replacement resets reconnect in-flight state")
        expect(replacementTransition.snapshot.recoveryID == submitted.recoveryID, "replacement does not replace submitted recovery")
        expect(replacementTransition.snapshot.state == "failed", "replacement is not reported as success")
        let replacementPresentation = recoveryPresentation(for: replacementTransition.snapshot)
        expect(replacementPresentation.title == "Reconnect Failed", "replacement failure title")
        expect(replacementPresentation.shortReason == "Recovery was replaced", "replacement failure short reason")

        expect(
            visibleStatusRecovery(recoverySnapshot(state: "running", stage: "verify"))?.stage == "verify",
            "automatic status recovery is visible"
        )
        expect(
            visibleStatusRecovery(recoverySnapshot(state: "idle", stage: "idle")) == nil,
            "idle status recovery is hidden"
        )
        expect(
            visibleStatusRecovery(recoverySnapshot(state: "succeeded", stage: "succeeded")) == nil,
            "passive status refresh cannot resurrect terminal success"
        )
        expect(
            recoverySnapshotForDisplay(
                recoverySnapshot(state: "succeeded", stage: "succeeded"),
                allowsTerminalSuccess: false
            ) == nil,
            "automatic observation cannot display terminal success"
        )
        expect(
            recoverySnapshotForDisplay(
                recoverySnapshot(state: "succeeded", stage: "succeeded"),
                allowsTerminalSuccess: true
            )?.state == "succeeded",
            "direct action may display terminal success"
        )
        expect(
            passiveStatusRecovery(
                protectionState: "blocked",
                recovery: recoverySnapshot(state: "running", stage: "verify")
            ) == nil,
            "passive recovery cannot obscure blocked protection"
        )
        expect(
            passiveStatusRecovery(
                protectionState: "needs_attention",
                recovery: recoverySnapshot(state: "running", stage: "verify")
            ) == nil,
            "passive recovery cannot obscure repair-required protection"
        )
        let automaticFailure = recoveryPresentation(
            for: recoverySnapshot(
                state: "failed",
                stage: "transport_health",
                errorCode: "transport_unavailable",
                reason: "underlay_changed"
            )
        )
        expect(automaticFailure.title == "Blocked", "automatic recovery failure is blocked")

        // A snapshot that paints the shield green must not outlive a warning
        // verdict, or the icon contradicts the verdict. Non-green ones say more
        // than the generic warning does, so they survive.
        expect(
            recoverySnapshotSurvivingWarning(recoverySnapshot(state: "succeeded", stage: "succeeded")) == nil,
            "green succeeded snapshot is dropped under a warning"
        )
        let stillRunning = recoverySnapshot(state: "running", stage: "rebind_underlay")
        expect(
            recoverySnapshotSurvivingWarning(stillRunning) == stillRunning,
            "yellow reconnecting snapshot survives a warning"
        )
        let failedSnapshot = recoverySnapshot(state: "failed", stage: "failed", errorCode: "transport_unavailable")
        expect(
            recoverySnapshotSurvivingWarning(failedSnapshot) == failedSnapshot,
            "red failed snapshot survives a warning and keeps its severity"
        )
        expect(recoverySnapshotSurvivingWarning(nil) == nil, "no snapshot stays no snapshot")
    }

    private static func recoverySnapshot(
        state: String,
        stage: String,
        errorCode: String? = nil,
        detail: String? = nil,
        reason: String = "manual"
    ) -> RecoverySnapshot {
        RecoverySnapshot(
            recoveryID: "recovery-1",
            state: state,
            stage: stage,
            reason: reason,
            generation: nil,
            lastErrorCode: errorCode,
            detail: detail,
            attempt: 1,
            startedAt: "2026-07-27T12:00:00Z",
            updatedAt: "2026-07-27T12:00:01Z"
        )
    }

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }
}
