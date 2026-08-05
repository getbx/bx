import Foundation

@main
struct RecoveryPresentationTests {
    static func main() {
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
