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
        let paths = try SharedPaths.make()
        // On-demand starts carry no options: the app leaves the config in the shared working
        // directory. A config the app marked broken must fail here — that is how the phase-3
        // test holds the tunnel in "not up" while on-demand keeps retrying.
        let raw: String
        if let fromOptions = options?["configContent"] as? String {
            raw = fromOptions
        } else if let persisted = try? String(contentsOf: paths.working.appendingPathComponent(SharedPaths.startConfigName), encoding: .utf8) {
            raw = persisted
        } else {
            throw TunnelError("no config: neither start options nor \(SharedPaths.startConfigName)")
        }
        if raw == SharedPaths.brokenMarker {
            throw TunnelError("config deliberately broken (fail-closed test)")
        }
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
