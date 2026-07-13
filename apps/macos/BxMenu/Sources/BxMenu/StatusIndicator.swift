import Foundation

enum StatusIndicator: Equatable {
    case green
    case yellow
    case red
    case gray
}

enum StatusIndicatorState {
    case connected
    case warning
    case updateNeeded
    case off
    case setupNeeded
    case missing
    case failed
}

func statusIndicator(for state: StatusIndicatorState) -> StatusIndicator {
    switch state {
    case .connected:
        return .green
    case .warning, .updateNeeded:
        return .yellow
    case .failed:
        return .red
    case .off, .setupNeeded, .missing:
        return .gray
    }
}
