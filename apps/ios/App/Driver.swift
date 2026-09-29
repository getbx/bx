import Foundation
import NetworkExtension

enum Scenario: String {
    case connect      // real server: the exit must be the server
    case deadserver   // proxy points at 192.0.2.1: every request must fail, nothing may leave
    case armed        // kill-switch on (includeAllNetworks + on-demand), real server: normal traffic still works
    case armedbroken  // kill-switch on, tunnel cannot start: nothing may reach even our own server
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
            case .armed, .armedbroken:
                let broken = scenario == .armedbroken
                try stageStartConfig(broken: broken)
                let manager = try await loadOrCreateManager(killSwitch: true)
                out["kill_switch"] = describeKillSwitch(manager)
                do {
                    try manager.connection.startVPNTunnel()
                } catch {
                    out["start_error"] = error.localizedDescription
                }
                if broken {
                    // on-demand keeps retrying a start that always throws: the tunnel stays "not up"
                    try await Task.sleep(nanoseconds: 6_000_000_000)
                    out["tunnel_status"] = describe(manager.connection.status)
                } else {
                    out["tunnel_status"] = describe(await waitFor(manager, .connected, seconds: 20))
                    try await Task.sleep(nanoseconds: 1_500_000_000)
                    out["probe"] = await Probe.run()
                }
                let exp = expectation()
                out["expect"] = exp
                if let host = exp["server_host"] as? String {
                    out["raw_probe"] = await RawProbe.run(host: host, port: 443, seconds: 6)
                }
                out["box_log_tail"] = boxLogTail(lines: 12)
                out["disarm"] = await disarm()
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

    // The persisted config for on-demand starts (no start options). `broken` writes a marker the
    // extension refuses, holding the tunnel down.
    private func stageStartConfig(broken: Bool) throws {
        let live = try stageFiles(configName: "libbox-config.json")
        guard let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: Self.appGroup) else {
            throw DriverError("app group \(Self.appGroup) is not available")
        }
        let body = broken ? "BX-BROKEN-CONFIG" : live
        try body.write(to: group.appendingPathComponent("Working/start-config.json"), atomically: true, encoding: .utf8)
    }

    // Always the last step of an armed scenario, inside the same launch: if includeAllNetworks
    // cut the Mac's channel to the phone, the phone must still not be left offline.
    private func disarm() async -> [String: Any] {
        var out: [String: Any] = [:]
        do {
            let managers = try await NETunnelProviderManager.loadAllFromPreferences()
            for m in managers {
                m.isOnDemandEnabled = false
                try? await m.saveToPreferences()
                m.connection.stopVPNTunnel()
                try await m.removeFromPreferences()
            }
            out["removed"] = managers.count
        } catch {
            out["error"] = error.localizedDescription
        }
        if let group = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: Self.appGroup) {
            try? FileManager.default.removeItem(at: group.appendingPathComponent("Working/start-config.json"))
        }
        return out
    }

    private func describeKillSwitch(_ m: NETunnelProviderManager) -> [String: Any] {
        let p = m.protocolConfiguration
        var out: [String: Any] = [
            "include_all_networks": p?.includeAllNetworks ?? false,
            "exclude_local_networks": p?.excludeLocalNetworks ?? false,
            "on_demand": m.isOnDemandEnabled,
        ]
        if #available(iOS 17.4, *) { out["exclude_device_communication"] = p?.excludeDeviceCommunication ?? false }
        return out
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

    private func loadOrCreateManager(killSwitch: Bool = false) async throws -> NETunnelProviderManager {
        let existing = try await NETunnelProviderManager.loadAllFromPreferences()
        let manager = existing.first ?? NETunnelProviderManager()
        let proto = NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Self.tunnelBundleID
        proto.serverAddress = "bx"
        // The iOS kill-switch: while the tunnel is not up, traffic that would have entered it is
        // dropped instead of leaving on the physical interface. LAN and the USB/Wi-Fi link to the
        // Mac stay open (excludeLocalNetworks / excludeDeviceCommunication), otherwise the test
        // would cut its own control channel.
        proto.includeAllNetworks = killSwitch
        proto.excludeLocalNetworks = killSwitch
        if #available(iOS 17.4, *) { proto.excludeDeviceCommunication = killSwitch }
        manager.protocolConfiguration = proto
        manager.localizedDescription = "bx (dev)"
        manager.isEnabled = true
        manager.onDemandRules = killSwitch ? [NEOnDemandRuleConnect()] : nil
        manager.isOnDemandEnabled = killSwitch
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
