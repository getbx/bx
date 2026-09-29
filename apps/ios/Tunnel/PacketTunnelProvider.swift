import Foundation
import Libbox
import NetworkExtension

// The whole data plane on the phone is libbox (sing-box in-process). bx contributes the
// configuration (internal/mobileconfig, generated on the Mac this phase) and nothing else here.
// Design: docs/superpowers/plans/2026-09-29-mobile-phase2-ios-tunnel.md
final class PacketTunnelProvider: NEPacketTunnelProvider {
    private var commandServer: LibboxCommandServer?
    private lazy var platform = PlatformInterface(self)

    override func startTunnel(options: [String: NSObject]?) async throws {
        guard let raw = options?["configContent"] as? String else {
            throw TunnelError("missing configContent in start options")
        }
        let paths = try SharedPaths.make()
        let config = try SharedPaths.anchor(raw, workingDirectory: paths.working)

        let setup = LibboxSetupOptions()
        setup.basePath = paths.base.path
        setup.workingPath = paths.working.path
        setup.tempPath = paths.temp.path
        setup.logMaxLines = 3000
        setup.crashReportSource = "NetworkExtension"
        setup.oomKillerEnabled = true
        var setupError: NSError?
        LibboxSetup(setup, &setupError)
        if let setupError {
            throw TunnelError("libbox setup: \(setupError.localizedDescription)")
        }

        var serverError: NSError?
        guard let server = LibboxNewCommandServer(platform, platform, &serverError) else {
            throw TunnelError("command server: \(serverError?.localizedDescription ?? "nil")")
        }
        commandServer = server
        try server.start()
        do {
            try server.startOrReloadService(config, options: LibboxOverrideOptions())
        } catch {
            throw TunnelError("start service: \(error.localizedDescription)")
        }
    }

    override func stopTunnel(with _: NEProviderStopReason) async {
        try? commandServer?.closeService()
        platform.reset()
        commandServer?.close()
        commandServer = nil
    }

    override func sleep() async {
        commandServer?.pause()
    }

    override func wake() {
        commandServer?.wake()
    }

    func stopFromLibbox() {
        try? commandServer?.closeService()
        platform.reset()
    }
}

struct TunnelError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}

enum SharedPaths {
    static let appGroup = "group.com.getbx.bx"

    struct Paths {
        let base: URL
        let working: URL
        let temp: URL
    }

    static func make() throws -> Paths {
        guard let base = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) else {
            throw TunnelError("app group \(appGroup) is not available")
        }
        let working = base.appendingPathComponent("Working", isDirectory: true)
        let temp = base.appendingPathComponent("Temp", isDirectory: true)
        for dir in [working, temp] {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        }
        return Paths(base: base, working: working, temp: temp)
    }

    // The config refers to rule-set files by bare name and has no log file; both are
    // anchored to the shared working directory here, where the app put the files and
    // reads the log back.
    static func anchor(_ raw: String, workingDirectory: URL) throws -> String {
        guard var doc = try JSONSerialization.jsonObject(with: Data(raw.utf8)) as? [String: Any] else {
            throw TunnelError("configContent is not a JSON object")
        }
        var log = doc["log"] as? [String: Any] ?? [:]
        log["output"] = workingDirectory.appendingPathComponent("box.log").path
        log["timestamp"] = true
        doc["log"] = log
        if var route = doc["route"] as? [String: Any], let sets = route["rule_set"] as? [[String: Any]] {
            route["rule_set"] = sets.map { set in
                var set = set
                if let path = set["path"] as? String, !path.hasPrefix("/") {
                    set["path"] = workingDirectory.appendingPathComponent(path).path
                }
                return set
            }
            doc["route"] = route
        }
        let data = try JSONSerialization.data(withJSONObject: doc)
        return String(decoding: data, as: UTF8.self)
    }
}
