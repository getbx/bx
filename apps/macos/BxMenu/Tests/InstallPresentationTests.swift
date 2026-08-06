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

        expect(repairActionTitle == "Repair bx…", "repair action title pinned")

        expect(repairActionNeeded(bundleVersion: "1.0.0", runtimeVersion: "1.0.0", coreVersion: "1.0.0", phase: nil) == false,
               "all three versions agree needs no repair")
        expect(repairActionNeeded(bundleVersion: "1.0.0", runtimeVersion: "1.1.0", coreVersion: "1.1.0", phase: nil) == true,
               "runtime differing from bundle needs repair")
        expect(repairActionNeeded(bundleVersion: "1.0.0", runtimeVersion: "1.0.0", coreVersion: "0.9.0", phase: nil) == true,
               "core differing from runtime needs repair")
        expect(repairActionNeeded(bundleVersion: "1.0.0", runtimeVersion: "1.0.0", coreVersion: nil, phase: nil) == false,
               "core off (nil) and bundle==runtime needs no repair")
        expect(repairActionNeeded(bundleVersion: "1.0.0", runtimeVersion: "1.1.0", coreVersion: nil, phase: nil) == true,
               "core off (nil) but bundle!=runtime needs repair")
        expect(repairActionNeeded(bundleVersion: "1.0.0", runtimeVersion: "1.1.0", coreVersion: "0.9.0", phase: "activating") == false,
               "in-flight update phase suppresses repair despite mismatch")
        expect(repairActionNeeded(bundleVersion: "1.0.0", runtimeVersion: nil, coreVersion: "1.0.0", phase: nil) == false,
               "runtime not installed defers to Install flow, not Repair")

        // Quit 会同时停掉保护并关掉菜单,而菜单是普通用户唯一的非命令行入口。
        // 文案必须告诉他们怎么回来,否则只剩 CLI 这条路——对普通用户等于没有路。
        expect(quitBxConfirmMessage.contains("Bx.app"), "quit confirm names the app to reopen")
        expect(quitBxConfirmMessage.contains("stop protecting"), "quit confirm still states protection stops")

        if failures > 0 { exit(1) }
        print("InstallPresentationTests passed")
    }
}
