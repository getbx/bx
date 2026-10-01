import Bxkit
import Foundation

// "Where does this go, and why" — answered by bxkit (the same router builder and the same
// wording table as `bx explain` on the Mac). The app only loads the inputs and shows the answer.
struct ExplainAnswer: Decodable, Equatable {
    let target: String
    let kind: String
    let verdict: String // tunnel | direct | blocked
    let because: String
    let rule: String?

    // Same three words the Mac menu uses for its traffic groups.
    var verdictTitle: String {
        switch verdict {
        case "tunnel": return String(localized: "Through the tunnel", bundle: .bx)
        case "direct": return String(localized: "Direct", bundle: .bx)
        case "blocked": return String(localized: "Blocked", bundle: .bx)
        default: return verdict
        }
    }

    var verdictSymbol: String {
        switch verdict {
        case "tunnel": return "lock.shield"
        case "direct": return "arrow.up.right"
        case "blocked": return "nosign"
        default: return "questionmark"
        }
    }
}

struct ExplainInputs {
    let policy: String
    let chinaDomain: String
    let chinaCIDR: String

    // `fixture` is synthetic and committed (apps/ios/App/Fixtures); otherwise the policy the
    // phone actually runs.
    static func load(fixture: Bool, policy: String = BxkitDefaultPolicy()) throws -> ExplainInputs {
        func read(_ url: URL?) throws -> String {
            guard let url else { throw DriverError("missing explain input") }
            return try String(contentsOf: url, encoding: .utf8)
        }
        if fixture {
            return try ExplainInputs(
                policy: read(Bundle.main.url(forResource: "demo-policy", withExtension: "json")),
                chinaDomain: read(Bundle.main.url(forResource: "demo-china-domain", withExtension: "txt")),
                chinaCIDR: read(Bundle.main.url(forResource: "demo-china-cidr", withExtension: "txt"))
            )
        }
        // Explain asks the policy the phone actually runs (synced from the Mac, or the defaults)
        // about the same bundled lists — the two cannot disagree.
        return ExplainInputs(policy: policy, chinaDomain: BundledLists.chinaDomain, chinaCIDR: BundledLists.chinaCIDR)
    }

    func explain(_ target: String) throws -> ExplainAnswer {
        var error: NSError?
        let raw = BxkitExplain(policy, chinaDomain, chinaCIDR, target, &error)
        if let error { throw error }
        return try JSONDecoder().decode(ExplainAnswer.self, from: Data(raw.utf8))
    }
}
