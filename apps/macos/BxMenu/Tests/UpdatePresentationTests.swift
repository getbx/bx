import Foundation

@main
struct UpdatePresentationTests {
    static func main() {
        let available = UpdateCheck(current: "v0.1.0", latest: "v0.2.0", available: true, verified: true)
        expect(updateActionTitle(for: available) == "Update bx…", "verified update is actionable")

        let unverified = UpdateCheck(current: "v0.1.0", latest: "v0.2.0", available: true, verified: false)
        expect(updateActionTitle(for: unverified) == nil, "unverified update is hidden")

        let current = UpdateCheck(current: "v0.2.0", latest: "v0.2.0", available: false, verified: true)
        expect(updateActionTitle(for: current) == nil, "current release has no action")

        expect(quitBxActionTitle == "Quit bx…", "protection shutdown action is explicit")

        let committedLine = #"{"from_version":"1.0.0","to_version":"1.1.0","phase":"committed","core_activated":true,"rolled_back":false,"protection_state":"protected"}"#
        expect(parseUpdateOutcome(Data(committedLine.utf8)) == .succeeded(to: "1.1.0"),
               "committed non-rolled-back line yields succeeded")

        let rolledBackLine = #"{"from_version":"1.0.0","to_version":"1.1.0","phase":"rolling_back","core_activated":false,"rolled_back":true,"protection_state":"protected"}"#
        expect(parseUpdateOutcome(Data(rolledBackLine.utf8)) == .rolledBack(from: "1.0.0"),
               "rolled_back line yields rolledBack")

        let mixedLog = """
        preparing update
        downloading artifact
        {"from_version":"1.0.0","to_version":"1.0.0","phase":"prepared","core_activated":false,"rolled_back":false,"protection_state":"protected"}
        activating core
        {"from_version":"1.0.0","to_version":"1.1.0","phase":"committed","core_activated":true,"rolled_back":false,"protection_state":"protected"}
        done
        """
        expect(parseUpdateOutcome(Data(mixedLog.utf8)) == .succeeded(to: "1.1.0"),
               "mixed log picks the last decodable JSON line")

        let garbage = "not json at all\nstill not json"
        expect(parseUpdateOutcome(Data(garbage.utf8)) == .failed, "garbage log yields failed")

        expect(updateConfirmTitle == "Update bx?", "update confirm title pinned")
        expect(updateConfirmMessage == "Internet access may pause briefly. bx will reconnect automatically.",
               "update confirm message pinned")
        expect(updateSucceededMessage == "bx is up to date", "update succeeded message pinned")
        expect(updateRolledBackMessage == "Update couldn't be completed. Previous version restored.",
               "update rolled back message pinned")
    }

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }
}
