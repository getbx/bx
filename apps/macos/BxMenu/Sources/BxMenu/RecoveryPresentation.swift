import Foundation

struct RecoverySnapshot: Decodable, Equatable {
    let recoveryID: String
    let state: String
    let stage: String
    let reason: String
    let generation: String?
    let lastErrorCode: String?
    let detail: String?
    let attempt: Int
    let startedAt: String
    let updatedAt: String

    enum CodingKeys: String, CodingKey {
        case recoveryID = "recovery_id"
        case state, stage, reason, generation, detail, attempt
        case lastErrorCode = "last_error_code"
        case startedAt = "started_at"
        case updatedAt = "updated_at"
    }
}

struct RecoveryPresentation: Equatable {
    let title: String
    let indicator: StatusIndicator
    let shortReason: String?
    let showsSuccessAlert: Bool

    var isRunning: Bool {
        title == "Reconnecting"
    }
}

struct RecoveryFailureTransition: Equatable {
    let snapshot: RecoverySnapshot
    let reconnectInFlight: Bool
}

func recoveryFailureTransition(from snapshot: RecoverySnapshot, errorCode: String) -> RecoveryFailureTransition {
    RecoveryFailureTransition(
        snapshot: RecoverySnapshot(
            recoveryID: snapshot.recoveryID,
            state: "failed",
            stage: "failed",
            reason: snapshot.reason,
            generation: snapshot.generation,
            lastErrorCode: errorCode,
            detail: nil,
            attempt: snapshot.attempt,
            startedAt: snapshot.startedAt,
            updatedAt: snapshot.updatedAt
        ),
        reconnectInFlight: false
    )
}

func recoveryPresentation(for snapshot: RecoverySnapshot) -> RecoveryPresentation {
    switch snapshot.state {
    case "accepted":
        return RecoveryPresentation(
            title: "Reconnecting",
            indicator: .yellow,
            shortReason: recoveryStageReason(snapshot.stage),
            showsSuccessAlert: false
        )
    case "running":
        return RecoveryPresentation(
            title: "Reconnecting",
            indicator: .yellow,
            shortReason: recoveryStageReason(snapshot.stage),
            showsSuccessAlert: false
        )
    case "succeeded":
        return RecoveryPresentation(
            title: "Reconnected",
            indicator: .green,
            shortReason: nil,
            showsSuccessAlert: false
        )
    default:
        return RecoveryPresentation(
            title: "Reconnect Failed",
            indicator: .red,
            shortReason: recoveryFailureReason(snapshot.lastErrorCode),
            showsSuccessAlert: false
        )
    }
}

private func recoveryStageReason(_ stage: String) -> String {
    switch stage {
    case "queued":
        return "Waiting for Guardian"
    case "observe":
        return "Checking network path"
    case "validate_capture":
        return "Validating protected path"
    case "rebind_underlay":
        return "Rebinding network path"
    case "transport_health":
        return "Checking protected transport"
    case "commit":
        return "Applying protected path"
    case "verify":
        return "Verifying protection"
    default:
        return "Reconnecting protected path"
    }
}

private func recoveryFailureReason(_ code: String?) -> String {
    switch code {
    case "capture_invalid":
        return "Protected path changed"
    case "capture_missing":
        return "Protected path unavailable"
    case "network_unavailable":
        return "Network unavailable"
    case "recovery_canceled":
        return "Recovery canceled"
    case "recovery_unavailable":
        return "Recovery unavailable"
    case "transport_unavailable":
        return "Protected transport unavailable"
    case "underlay_rebind_failed":
        return "Could not rebind network path"
    case "underlay_unavailable":
        return "Network path unavailable"
    case "verification_failed":
        return "Protection verification failed"
    default:
        return "Recovery failed"
    }
}
