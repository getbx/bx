import Foundation
import Libbox
import NetworkExtension
import os

// The whole data plane on the phone is libbox (sing-box in-process). bx contributes the
// configuration (internal/mobileconfig, generated on the Mac this phase) and nothing else here.
// Design: docs/superpowers/plans/2026-09-29-mobile-phase2-ios-tunnel.md
final class PacketTunnelProvider: NEPacketTunnelProvider {
    private var commandServer: LibboxCommandServer?
    private lazy var platform = PlatformInterface(self)
    private var memoryTimer: DispatchSourceTimer?
    private var peakFootprint: UInt64 = 0

    /// Every 5 s: this process's footprint (what iOS judges), its peak, and what is left.
    private func startMemorySampler() {
        guard let base = SharedPaths.container else { return }
        let url = base.appendingPathComponent(SharedPaths.tunnelMemoryName)
        let timer = DispatchSource.makeTimerSource(queue: .global(qos: .utility))
        timer.schedule(deadline: .now(), repeating: 5)
        timer.setEventHandler { [weak self] in
            guard let self else { return }
            var info = task_vm_info_data_t()
            var count = mach_msg_type_number_t(MemoryLayout<task_vm_info_data_t>.size / MemoryLayout<natural_t>.size)
            let kr = withUnsafeMutablePointer(to: &info) {
                $0.withMemoryRebound(to: integer_t.self, capacity: Int(count)) { task_info(mach_task_self_, task_flavor_t(TASK_VM_INFO), $0, &count) }
            }
            guard kr == KERN_SUCCESS else { return }
            self.peakFootprint = max(self.peakFootprint, info.phys_footprint)
            let sample: [String: Any] = [
                "footprint_mb": Double(info.phys_footprint) / 1_048_576,
                "peak_mb": Double(self.peakFootprint) / 1_048_576,
                "available_mb": Double(os_proc_available_memory()) / 1_048_576,
                "at": Date().timeIntervalSince1970,
            ]
            if let data = try? JSONSerialization.data(withJSONObject: sample) { try? data.write(to: url, options: .atomic) }
        }
        timer.resume()
        memoryTimer = timer
    }

    override func startTunnel(options: [String: NSObject]?) async throws {
        // iOS hands the app only an opaque NSError for a failed start; the words go to the
        // shared container so the app can say why.
        if let base = SharedPaths.container { try? FileManager.default.removeItem(at: base.appendingPathComponent(SharedPaths.tunnelStartErrorName)) }
        do {
            try await start(options: options)
        } catch {
            if let base = SharedPaths.container {
                try? Data(error.localizedDescription.utf8).write(to: base.appendingPathComponent(SharedPaths.tunnelStartErrorName), options: .atomic)
            }
            throw error
        }
    }

    private func start(options: [String: NSObject]?) async throws {
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
        startMemorySampler()
        try server.start()
        do {
            try server.startOrReloadService(config, options: LibboxOverrideOptions())
        } catch {
            throw TunnelError("start service: \(error.localizedDescription)")
        }
    }

    override func stopTunnel(with _: NEProviderStopReason) async {
        memoryTimer?.cancel()
        memoryTimer = nil
        try? commandServer?.closeService()
        platform.reset()
        commandServer?.close()
        commandServer = nil
    }

    // The app rewrote start-config.json (rules synced from the Mac): reload the service in place,
    // without dropping the tunnel.
    override func handleAppMessage(_ messageData: Data) async -> Data? {
        guard String(decoding: messageData, as: UTF8.self) == "reload", let server = commandServer else { return nil }
        do {
            let paths = try SharedPaths.make()
            let raw = try String(contentsOf: paths.working.appendingPathComponent(SharedPaths.startConfigName), encoding: .utf8)
            try server.startOrReloadService(try SharedPaths.anchor(raw, workingDirectory: paths.working), options: LibboxOverrideOptions())
            return Data("ok".utf8)
        } catch {
            return Data(error.localizedDescription.utf8)
        }
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
