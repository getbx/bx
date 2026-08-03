import Foundation

@main
struct InstanceGateTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition { failures += 1; FileHandle.standardError.write(Data(("FAIL: " + message + "\n").utf8)) }
    }

    static func main() {
        let canonical = "/Applications/Bx.app"
        expect(resolveInstanceConflict(selfPath: canonical, peerPath: nil, canonicalPath: canonical)
            == .keepSelf(terminatePeer: false), "no peer keeps self")
        expect(resolveInstanceConflict(selfPath: canonical, peerPath: "/Users/a/Downloads/Bx.app", canonicalPath: canonical)
            == .keepSelf(terminatePeer: true), "canonical self terminates stray peer")
        expect(resolveInstanceConflict(selfPath: "/Users/a/Downloads/Bx.app", peerPath: canonical, canonicalPath: canonical)
            == .yieldToPeer, "stray self yields to canonical peer")
        expect(resolveInstanceConflict(selfPath: "/Users/a/Desktop/Bx.app", peerPath: "/Users/a/Downloads/Bx.app", canonicalPath: canonical)
            == .yieldToPeer, "newcomer yields when neither canonical")

        let plist = menuLaunchAgentPlist(executablePath: "/Applications/Bx.app/Contents/MacOS/BxMenu",
                                         logDirectory: "/Users/a/Library/Logs/bx")
        expect(plist.contains("<string>com.getbx.bx.menu</string>"), "agent label present")
        expect(plist.contains("<string>/Applications/Bx.app/Contents/MacOS/BxMenu</string>"), "agent points at canonical app")
        expect(plist.contains("<string>/Users/a/Library/Logs/bx/menu.log</string>"), "stdout log path")
        expect(!plist.contains("KeepAlive"), "menu agent must not keep-alive")

        if failures > 0 { exit(1) }
        print("InstanceGateTests passed")
    }
}
