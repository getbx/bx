import Bxkit
import Foundation
import NetworkExtension

// What the home screen drives. The kill-switch is on whenever protection is on: while the
// tunnel is not up, traffic that would have entered it is dropped instead of leaving on the
// physical interface (includeAllNetworks + on-demand; verified on device 2026-09-29).
@MainActor
final class TunnelController: ObservableObject {
    enum State: Equatable {
        case noServer
        case off
        case connecting
        case on
        case failed(String)
    }

    @Published private(set) var state: State = .noServer
    @Published private(set) var serverHost: String?
    @Published var lastError: String?

    private var manager: NETunnelProviderManager?
    private var observer: NSObjectProtocol?
    private let defaults: UserDefaults?

    /// Fixture mode (simulator snapshots and UI tests): no VPN framework calls, no Keychain, and
    /// **no persistent settings** — a fixture run that wrote the synthetic server into the shared
    /// app-group settings made the next run start "with a server" (caught by the UI tests).
    let fixture: Bool

    init(fixture: Bool) {
        self.fixture = fixture
        defaults = fixture ? nil : UserDefaults(suiteName: SharedPaths.appGroup)
        serverHost = defaults?.string(forKey: "serverHost")
        if fixture {
            state = serverHost == nil ? .noServer : .off
            return
        }
        Task { await refresh() }
        observer = NotificationCenter.default.addObserver(forName: .NEVPNStatusDidChange, object: nil, queue: .main) { [weak self] _ in
            Task { @MainActor in self?.syncStatus() }
        }
    }

    var isOn: Bool { state == .on || state == .connecting }

    /// Paste → configure → store. Refuses links the phone cannot run, saying which kinds work.
    func importLink(_ raw: String) throws {
        let link = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        var error: NSError?
        let json = BxkitConfigure(link, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
        if let error {
            if error.localizedDescription.contains("no in-process sing-box outbound") {
                throw DriverError("This server type is not supported on iPhone yet. bx on iPhone runs reality servers (vless:// links, or a bx:// link that contains one).")
            }
            throw error
        }
        struct Configured: Decodable { let config: String; let rule_sets: [String: String]; let server_host: String }
        let c = try JSONDecoder().decode(Configured.self, from: Data(json.utf8))
        if !fixture {
            try SharedPaths.writeStartConfig(c.config, ruleSets: c.rule_sets)
            try LinkStore.save(link)
        }
        defaults?.set(c.server_host, forKey: "serverHost")
        serverHost = c.server_host
        if state == .noServer { state = .off }
    }

    func setProtection(_ on: Bool) async {
        guard !fixture else { state = on ? .on : .off; return }
        lastError = nil
        do {
            let m = try await loadManager()
            if on {
                configureKillSwitch(m, enabled: true)
                try await m.saveToPreferences() // first time: iOS asks to allow a VPN configuration
                try await m.loadFromPreferences()
                try m.connection.startVPNTunnel()
            } else {
                // On-demand first, or iOS immediately brings the tunnel back up.
                configureKillSwitch(m, enabled: false)
                try await m.saveToPreferences()
                m.connection.stopVPNTunnel()
            }
            manager = m
        } catch {
            lastError = error.localizedDescription
        }
        syncStatus()
    }

    func forgetServer() async {
        await setProtection(false)
        if !fixture {
            LinkStore.delete()
            try? await manager?.removeFromPreferences()
            SharedPaths.removeStartConfig()
        }
        defaults?.removeObject(forKey: "serverHost")
        serverHost = nil
        state = .noServer
    }

    private func refresh() async {
        manager = try? await NETunnelProviderManager.loadAllFromPreferences().first
        syncStatus()
    }

    private func syncStatus() {
        guard serverHost != nil else { state = .noServer; return }
        switch manager?.connection.status {
        case .connected: state = .on
        case .connecting, .reasserting: state = .connecting
        case .disconnecting, .disconnected, .invalid, .none: state = .off
        @unknown default: state = .off
        }
    }

    private func loadManager() async throws -> NETunnelProviderManager {
        if let manager { return manager }
        return try await NETunnelProviderManager.loadAllFromPreferences().first ?? NETunnelProviderManager()
    }

    private func configureKillSwitch(_ m: NETunnelProviderManager, enabled: Bool) {
        let proto = (m.protocolConfiguration as? NETunnelProviderProtocol) ?? NETunnelProviderProtocol()
        proto.providerBundleIdentifier = Driver.tunnelBundleID
        proto.serverAddress = serverHost ?? "bx"
        proto.includeAllNetworks = true
        proto.excludeLocalNetworks = true
        if #available(iOS 17.4, *) { proto.excludeDeviceCommunication = true }
        m.protocolConfiguration = proto
        m.localizedDescription = "bx"
        m.isEnabled = true
        m.onDemandRules = [NEOnDemandRuleConnect()]
        m.isOnDemandEnabled = enabled
    }
}

enum BundledLists {
    static let chinaDomain = read("china_domain", "txt")
    static let chinaCIDR = read("china_cidr4", "txt")
    private static func read(_ name: String, _ ext: String) -> String {
        guard let url = Bundle.main.url(forResource: name, withExtension: ext) else { return "" }
        return (try? String(contentsOf: url, encoding: .utf8)) ?? ""
    }
}
