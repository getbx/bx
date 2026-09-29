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
        case "tunnel": return "Through the tunnel"
        case "direct": return "Direct"
        case "blocked": return "Blocked"
        default: return verdict
        }
    }
}

struct ExplainInputs {
    let policy: String
    let chinaDomain: String
    let chinaCIDR: String

    // `fixture` is synthetic and committed (apps/ios/App/Fixtures); otherwise the Mac-generated
    // Dev folder, which carries the owner's real rules and is never committed.
    static func load(fixture: Bool) throws -> ExplainInputs {
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
        guard let dev = Bundle.main.url(forResource: "Dev", withExtension: nil) else {
            throw DriverError("no Dev folder in the app bundle; run scripts/ios-dev.sh config first")
        }
        return try ExplainInputs(
            policy: read(dev.appendingPathComponent("policy.json")),
            chinaDomain: read(dev.appendingPathComponent("china_domain.txt")),
            chinaCIDR: read(dev.appendingPathComponent("china_cidr.txt"))
        )
    }

    func explain(_ target: String) throws -> ExplainAnswer {
        var error: NSError?
        let raw = BxkitExplain(policy, chinaDomain, chinaCIDR, target, &error)
        if let error { throw error }
        return try JSONDecoder().decode(ExplainAnswer.self, from: Data(raw.utf8))
    }
}
