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
