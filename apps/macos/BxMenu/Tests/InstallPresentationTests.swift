import Foundation

@main
struct InstallPresentationTests {
    static var failures = 0
    static func expect(_ c: Bool, _ m: String) {
        if !c { failures += 1; FileHandle.standardError.write(Data(("FAIL: " + m + "\n").utf8)) }
    }

    static func main() {
        expect(decodeRuntimeVersion(Data(#"{"schema_version":1,"version":"1.2.3","platform":"darwin/arm64","assets":{"bx-cli":"ab"}}"#.utf8)) == "1.2.3",
               "decode version from release.json")
        expect(decodeRuntimeVersion(Data("not json".utf8)) == nil, "garbage yields nil")

        expect(installActionTitle(runtimeInstalled: false, cliUsable: false) == "Install bx…", "fresh mac offers install")
        expect(installActionTitle(runtimeInstalled: false, cliUsable: true) == nil, "legacy CLI install shows no install action")
        expect(installActionTitle(runtimeInstalled: true, cliUsable: true) == nil, "healthy unified layout shows no install action")
        expect(installActionTitle(runtimeInstalled: true, cliUsable: false) == "Install bx…", "runtime present but cli broken offers repair-install")

        expect(turnOffActionTitle == "Turn Off bx", "turn off title pinned")

        let verifiedAvailable = UpdateCheck(current: "1.0.0", latest: "1.1.0", available: true, verified: true)
        expect(menuUpdateActionTitle(check: verifiedAvailable) == "Update bx…",
               "available verified update is actionable")
        let unverifiedAvailable = UpdateCheck(current: "1.0.0", latest: "1.1.0", available: true, verified: false)
        expect(menuUpdateActionTitle(check: unverifiedAvailable) == nil,
               "unverified update is hidden")
        let notAvailable = UpdateCheck(current: "1.0.0", latest: "1.0.0", available: false, verified: true)
        expect(menuUpdateActionTitle(check: notAvailable) == nil,
               "not-available update is hidden")
        expect(menuUpdateActionTitle(check: nil) == nil,
               "no check yields no update action")

        expect(updatingBanner(phase: "prepared") == "Updating bx…", "prepared phase shows updating banner")
        expect(updatingBanner(phase: "barrier_active") == "Updating bx…", "barrier_active phase shows updating banner")
        expect(updatingBanner(phase: "activating") == "Updating bx…", "activating phase shows updating banner")
        expect(updatingBanner(phase: "rolling_back") == "Updating bx…", "rolling_back phase shows updating banner")
        expect(updatingBanner(phase: "committed") == nil, "committed phase shows no updating banner")
        expect(updatingBanner(phase: nil) == nil, "no phase shows no updating banner")

        let dir = FileManager.default.temporaryDirectory
            .appendingPathComponent("bx-install-presentation-\(getpid())")
        let current = dir.appendingPathComponent("current")
        try? FileManager.default.createDirectory(at: current, withIntermediateDirectories: true)
        try? Data(#"{"schema_version":1,"version":"9.9.9","platform":"darwin/arm64","assets":{"bx-cli":"ab"}}"#.utf8)
            .write(to: current.appendingPathComponent("release.json"))
        expect(unifiedRuntimeVersion(root: dir.path) == "9.9.9", "reads version via current link dir")
        expect(unifiedRuntimeVersion(root: dir.path + "-missing") == nil, "missing root yields nil")

        if failures > 0 { exit(1) }
        print("InstallPresentationTests passed")
    }
}
