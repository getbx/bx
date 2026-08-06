import Foundation

struct UpdateCheck: Decodable, Equatable {
    let current: String
    let latest: String
    let available: Bool
    let verified: Bool
}

func updateActionTitle(for check: UpdateCheck?) -> String? {
    guard let check, check.available, check.verified else { return nil }
    return "Update bx…"
}

let quitBxActionTitle = "Quit bx…"
let quitMenuActionTitle = "Quit Menu"
let quitBxConfirmMessage = "bx will stop protecting system traffic, restore managed DNS settings, and close this menu. To start bx again, open Bx.app from Applications."

struct UpdateResultJSON: Decodable, Equatable {
    let fromVersion: String
    let toVersion: String
    let phase: String
    let coreActivated: Bool
    let rolledBack: Bool
    let protectionState: String
    enum CodingKeys: String, CodingKey {
        case fromVersion = "from_version"
        case toVersion = "to_version"
        case phase
        case coreActivated = "core_activated"
        case rolledBack = "rolled_back"
        case protectionState = "protection_state"
    }
}

enum UpdateOutcome: Equatable {
    case succeeded(to: String)
    case rolledBack(from: String)
    case failed
}

func parseUpdateOutcome(_ logData: Data) -> UpdateOutcome {
    guard let text = String(data: logData, encoding: .utf8) else { return .failed }
    let lines = text.split(separator: "\n", omittingEmptySubsequences: true)
    for line in lines.reversed() {
        guard let result = try? JSONDecoder().decode(UpdateResultJSON.self, from: Data(line.utf8)) else {
            continue
        }
        if result.rolledBack {
            return .rolledBack(from: result.fromVersion)
        }
        if result.phase == "committed" {
            return .succeeded(to: result.toVersion)
        }
        return .failed
    }
    return .failed
}

let updateConfirmTitle = "Update bx?"
let updateConfirmMessage = "Internet access may pause briefly. bx will reconnect automatically."
let updateSucceededMessage = "bx is up to date"
let updateRolledBackMessage = "Update couldn't be completed. Previous version restored."
