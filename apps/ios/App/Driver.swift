import Foundation
import NetworkExtension

enum Scenario: String {
    case connect      // real server: the exit must be the server
    case deadserver   // proxy points at 192.0.2.1: every request must fail, nothing may leave
    case stop
    case remove       // delete the VPN configuration from Settings (run at the end of every session)

    static func fromArguments(_ args: [String]) -> Scenario? {
        guard let i = args.firstIndex(of: "--scenario"), i + 1 < args.count else { return nil }
        return Scenario(rawValue: args[i + 1])
    }
}

struct Driver {
    static let tunnelBundleID = "com.getbx.bx.ios.tunnel"
    static let appGroup = "group.com.getbx.bx"

    static func emit(_ result: [String: Any]) {
        let data = (try? JSONSerialization.data(withJSONObject: result, options: [.sortedKeys])) ?? Data()
        let line = "BX-RESULT " + String(decoding: data, as: UTF8.self)
        print(line)
        fflush(stdout)
        if let docs = FileManager.default.urls(for: .documentDirectory, in: .userDomainMask).first {
            try? data.write(to: docs.appendingPathComponent("result.json"))
        }
    }

    func run(_ scenario: Scenario) async -> [String: Any] {
        var out: [String: Any] = ["scenario": scenario.rawValue]
        do {
            switch scenario {
            case .connect, .deadserver:
                let configName = scenario == .connect ? "libbox-config.json" : "libbox-config-deadserver.json"
                let config = try stageFiles(configName: configName)
                let manager = try await loadOrCreateManager()
                out["tunnel_status_before"] = describe(manager.connection.status)
                try await stop(manager)
                try manager.connection.startVPNTunnel(options: ["configContent": config as NSString])
                out["tunnel_status"] = describe(await waitFor(manager, .connected, seconds: 20))
                try await Task.sleep(nanoseconds: 1_500_000_000)
                out["probe"] = await Probe.run()
                out["expect"] = expectation()
                out["box_log_tail"] = boxLogTail(lines: 25)
                try await stop(manager)
                out["tunnel_status_after"] = describe(manager.connection.status)
            case .stop:
                let manager = try await loadOrCreateManager()
                try await stop(manager)
                out["tunnel_status"] = describe(manager.connection.status)
            case .remove:
                let managers = try await NETunnelProviderManager.loadAllFromPreferences()
                for m in managers {
                    m.connection.stopVPNTunnel()
                    try await m.removeFromPreferences()
                }
                out["removed"] = managers.count
            }
        } catch {
            out["error"] = error.localizedDescription
        }
        return out
    }

    // Copies the Mac-generated config and rule-set files from the app bundle into the shared
    // working directory, where the extension reads them.
    private func stageFiles(configName: String) throws -> String {
        guard let dev = Bundle.main.url(forResource: "Dev", withExtension: nil) else {
            throw DriverError("no Dev folder in the app bundle; run scripts/ios-dev.sh config first")
        }
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: Self.appGroup) else {
            throw DriverError("app group \(Self.appGroup) is not available")
        }
        let working = group.appendingPathComponent("Working", isDirectory: true)
        try FileManager.default.createDirectory(at: working, withIntermediateDirectories: true)
        for name in ["bx-china-domain.json", "bx-china-cidr.json"] {
            let dst = working.appendingPathComponent(name)
            try? FileManager.default.removeItem(at: dst)
            try FileManager.default.copyItem(at: dev.appendingPathComponent(name), to: dst)
        }
        try? FileManager.default.removeItem(at: working.appendingPathComponent("box.log"))
        return try String(contentsOf: dev.appendingPathComponent(configName), encoding: .utf8)
    }

    private func expectation() -> [String: Any] {
        guard let dev = Bundle.main.url(forResource: "Dev", withExtension: nil),
              let data = try? Data(contentsOf: dev.appendingPathComponent("expect.json")),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return obj
    }

    private func boxLogTail(lines: Int) -> [String] {
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: Self.appGroup),
              let text = try? String(contentsOf: group.appendingPathComponent("Working/box.log"), encoding: .utf8) else { return [] }
        return Array(text.split(separator: "\n").suffix(lines)).map(String.init)
    }

    private func loadOrCreateManager() async throws -> NETunnelProviderManager {
        let existing = try await NETunnelProviderManager.loadAllFromPreferences()
        let manager = existing.first ?? NETunnelProviderManager()
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = "bx"
        manager.protocolConfiguration = proto
        manager.localizedDescription = "bx (dev)"
        manager.isEnabled = true
        manager.isOnDemandEnabled = false
        try await manager.saveToPreferences() // first time: iOS asks the owner to allow a VPN configuration
        try await manager.loadFromPreferences()
        return manager
    }

    private func stop(_ manager: NETunnelProviderManager) async throws {
        guard manager.connection.status != .disconnected, manager.connection.status != .invalid else { return }
        manager.connection.stopVPNTunnel()
        _ = await waitFor(manager, .disconnected, seconds: 10)
    }

    private func waitFor(_ manager: NETunnelProviderManager, _ target: NEVPNStatus, seconds: Double) async -> NEVPNStatus {
        let deadline = Date().addingTimeInterval(seconds)
        while Date() < deadline {
            if manager.connection.status == target { return target }
            try? await Task.sleep(nanoseconds: 200_000_000)
        }
        return manager.connection.status
    }

    private func describe(_ s: NEVPNStatus) -> String {
        switch s {
        case .invalid: return "invalid"
        case .disconnected: return "disconnected"
        case .connecting: return "connecting"
        case .connected: return "connected"
        case .reasserting: return "reasserting"
        case .disconnecting: return "disconnecting"
        @unknown default: return "unknown"
        }
    }
}

struct DriverError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}
