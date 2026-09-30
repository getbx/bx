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

    /// Where the phone's routing rules come from (rule sync, step 4). The home screen says it
    /// plainly — never implying the Mac's rules are here when they are not.
    enum RulesSource: Equatable {
        case defaults(DefaultsReason)
        case synced(version: Int64, updatedAt: String)
    }

    enum DefaultsReason: Equatable {
        case notCheckedYet      // protection has not been on since the server was added
        case notSyncedYet       // the store answered, but the Mac has not pushed yet
        case serverCannotSync   // the store is not reachable through the tunnel
        case differentLink      // a blob is there, sealed with another link
    }

    @Published private(set) var state: State = .noServer
    @Published private(set) var serverHost: String?
    @Published private(set) var rules: RulesSource = .defaults(.notCheckedYet)
    @Published var lastError: String?

    /// The policy the phone runs and Explain asks: synced if we have it, otherwise the defaults.
    var policyJSON: String { syncedPolicy ?? BxkitDefaultPolicy() }
    private var syncedPolicy: String?
    private var syncTimer: Timer?

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
        if !fixture, let saved = SharedPaths.readSyncedPolicy(), let v = Self.version(of: saved) {
            syncedPolicy = saved
            rules = .synced(version: v.version, updatedAt: v.updatedAt)
        }
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
        // A new server means a new link, and rules synced for the old link do not apply to it.
        let sameLink = !fixture && LinkStore.load() == link
        if !sameLink {
            syncedPolicy = nil
            rules = .defaults(.notCheckedYet)
            if !fixture { SharedPaths.removeSyncedPolicy() }
        }
        var error: NSError?
        let json = BxkitConfigureWithPolicy(link, policyJSON, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
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
            SharedPaths.removeSyncedPolicy()
        }
        syncedPolicy = nil
        rules = .defaults(.notCheckedYet)
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
        let wasOn = state == .on
        switch manager?.connection.status {
        case .connected: state = .on
        case .connecting, .reasserting: state = .connecting
        case .disconnecting, .disconnected, .invalid, .none: state = .off
        @unknown default: state = .off
        }
        // Pull when protection comes on, then every 30 minutes while it stays on. Only through the
        // tunnel: the sync name resolves nowhere without it.
        if state == .on, !wasOn {
            Task { await pullRules() }
            syncTimer?.invalidate()
            syncTimer = Timer.scheduledTimer(withTimeInterval: 30 * 60, repeats: true) { [weak self] _ in
                Task { @MainActor in await self?.pullRules() }
            }
        } else if state != .on {
            syncTimer?.invalidate()
            syncTimer = nil
        }
    }

    /// Fetch the Mac's rules from the user's own server, and if they are newer than what the
    /// phone runs, regenerate the config and reload the tunnel in place.
    func pullRules() async {
        guard !fixture, state == .on, let link = LinkStore.load() else { return }
        var error: NSError?
        let address = BxkitSyncURL(link, &error)
        guard error == nil, let url = URL(string: address) else { return }
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 15
        do {
            let (data, response) = try await URLSession(configuration: config).data(from: url)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            if status == 404 {
                if syncedPolicy == nil { rules = .defaults(.notSyncedYet) }
                return
            }
            guard status == 200 else { if syncedPolicy == nil { rules = .defaults(.serverCannotSync) }; return }
            var openError: NSError?
            let policy = BxkitOpenSynced(link, data, &openError)
            if openError != nil {
                if syncedPolicy == nil { rules = .defaults(.differentLink) }
                return
            }
            guard let v = Self.version(of: policy) else { return }
            if case let .synced(current, _) = rules, v.version <= current { return }
            try apply(policy: policy, link: link)
            rules = .synced(version: v.version, updatedAt: v.updatedAt)
        } catch {
            // Transient failures keep whatever the phone already runs; only say so if it never synced.
            if syncedPolicy == nil { rules = .defaults(.serverCannotSync) }
        }
    }

    private func apply(policy: String, link: String) throws {
        var error: NSError?
        let json = BxkitConfigureWithPolicy(link, policy, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
        if let error { throw error }
        struct Configured: Decodable { let config: String; let rule_sets: [String: String] }
        let c = try JSONDecoder().decode(Configured.self, from: Data(json.utf8))
        try SharedPaths.writeStartConfig(c.config, ruleSets: c.rule_sets)
        try SharedPaths.writeSyncedPolicy(policy)
        syncedPolicy = policy
        if let session = manager?.connection as? NETunnelProviderSession {
            try? session.sendProviderMessage(Data("reload".utf8)) { _ in }
        }
    }

    static func version(of policy: String) -> (version: Int64, updatedAt: String)? {
        struct V: Decodable { let version: Int64; let updated_at: String? }
        guard let v = try? JSONDecoder().decode(V.self, from: Data(policy.utf8)), v.version > 0 else { return nil }
        return (v.version, v.updated_at ?? "")
    }

    /// Fixture only: pretend the Mac's rules arrived (UI tests and snapshots).
    func fixtureSynced(policy: String) {
        guard fixture, let v = Self.version(of: policy) else { return }
        syncedPolicy = policy
        rules = .synced(version: v.version, updatedAt: v.updatedAt)
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
