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
    /// A bx:// link opened from outside the app (the Camera's QR scan, a message). It is **never**
    /// applied directly: the home screen asks first, naming the address — any web page can open a
    /// bx:// URL, and it must not be able to swap someone's server silently.
    @Published var incoming: IncomingLink?
    /// The VPN-permission explainer was shown this run (so it is not shown twice in a row).
    @Published var vpnExplained = false

    /// Tailscale inside bx's tunnel (iOS runs one VPN at a time). Off unless the person turns it on.
    @Published private(set) var tailscaleEnabled = false
    @Published private(set) var tailscaleStatus: TailscaleStatus?
    var tailscalePhase: TailscalePhase { .from(enabled: tailscaleEnabled, protection: state, status: tailscaleStatus) }

    /// No VPN configuration yet ⇒ the next turn-on triggers iPhone's "add VPN configurations" alert.
    var needsVPNPermission: Bool { manager == nil && !vpnExplained }

    struct IncomingLink: Identifiable, Equatable {
        let id = UUID()
        let link: String
        let host: String?
        let problem: String?
    }

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
        tailscaleEnabled = defaults?.bool(forKey: "tailscale") ?? false
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

    /// Protection is wanted: on, on its way, or held up (the kill-switch blocks while it retries).
    var isOn: Bool {
        switch state {
        case .on, .connecting, .failed: return true
        case .off, .noServer: return false
        }
    }

    /// Paste → configure → store. Refuses links the phone cannot run, saying which kinds work.
    /// Something opened bx with a link. Work out the address (no side effects) and ask.
    func receive(link raw: String) {
        let link = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard link.lowercased().hasPrefix("bx://") || link.lowercased().hasPrefix("vless://") else { return }
        var error: NSError?
        let json = BxkitConfigure(link, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
        struct Preview: Decodable { let server_host: String }
        if error == nil, let p = try? JSONDecoder().decode(Preview.self, from: Data(json.utf8)) {
            incoming = IncomingLink(link: link, host: p.server_host, problem: nil)
        } else {
            incoming = IncomingLink(link: link, host: nil,
                                    problem: String(localized: "This link cannot be used on iPhone. bx on iPhone runs reality servers.", bundle: .bx))
        }
    }

    func acceptIncoming() {
        guard let pending = incoming, pending.problem == nil else { incoming = nil; return }
        incoming = nil
        do {
            if isOn { Task { await setProtection(false) } }
            try importLink(pending.link)
        } catch {
            lastError = error.localizedDescription
        }
    }

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
        let json = BxkitConfigureWithOptions(link, policyJSON, optionsJSON, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
        if let error {
            if error.localizedDescription.contains("no in-process sing-box outbound") {
                throw AppError(String(localized: "This server type is not supported on iPhone yet. bx on iPhone runs reality servers (vless:// links, or a bx:// link that contains one).", bundle: .bx))
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
        case .disconnecting, .disconnected, .invalid, .none, nil:
            // Wanted on (on-demand with the kill-switch) but not up: iOS keeps retrying and blocks
            // everything meanwhile. Saying "not protected — apps connect directly" here was false.
            state = manager?.isOnDemandEnabled == true
                ? .failed(String(localized: "Nothing leaves this iPhone while bx keeps trying. Turn protection off to use the internet without bx.", bundle: .bx))
                : .off
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
            do {
                try await apply(policy: policy, link: link)
            } catch {
                lastError = error.localizedDescription
                return
            }
            rules = .synced(version: v.version, updatedAt: v.updatedAt)
        } catch {
            // Transient failures keep whatever the phone already runs; only say so if it never synced.
            if syncedPolicy == nil { rules = .defaults(.serverCannotSync) }
        }
    }

    private func apply(policy: String, link: String) async throws {
        var error: NSError?
        let json = BxkitConfigureWithOptions(link, policy, optionsJSON, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
        if let error { throw error }
        struct Configured: Decodable { let config: String; let rule_sets: [String: String] }
        let c = try JSONDecoder().decode(Configured.self, from: Data(json.utf8))
        try SharedPaths.writeStartConfig(c.config, ruleSets: c.rule_sets)
        if let problem = await reloadTunnel() { throw AppError(problem) }
        try SharedPaths.writeSyncedPolicy(policy)
        syncedPolicy = policy
    }

    /// Ask the running tunnel to pick up the rewritten config. nil = done (or nothing running).
    /// A failed reload leaves the tunnel up with no service behind it — every connection blocked —
    /// so the caller must say so instead of reporting success.
    private func reloadTunnel() async -> String? {
        guard state == .on, let session = manager?.connection as? NETunnelProviderSession else { return nil }
        guard let reply = await send("reload", to: session) else {
            return String(localized: "bx could not apply the change. Turn protection off and on again.", bundle: .bx)
        }
        let text = String(decoding: reply, as: UTF8.self)
        return text == "ok" ? nil : String(localized: "bx could not apply the change. Turn protection off and on again.", bundle: .bx)
    }

    private var optionsJSON: String { tailscaleEnabled ? #"{"tailscale":true}"# : "{}" }

    /// Turn Tailscale on or off: regenerate the config and, if protection is on, reload the tunnel
    /// in place (the same path synced rules take). The sign-in itself is kept by the extension.
    func setTailscale(_ on: Bool) async {
        tailscaleEnabled = on
        tailscaleStatus = nil
        guard !fixture else { return }
        defaults?.set(on, forKey: "tailscale")
        guard let link = LinkStore.load() else { return }
        var error: NSError?
        let json = BxkitConfigureWithOptions(link, policyJSON, optionsJSON, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
        guard error == nil, let c = try? JSONDecoder().decode(ConfiguredFiles.self, from: Data(json.utf8)),
              (try? SharedPaths.writeStartConfig(c.config, ruleSets: c.rule_sets)) != nil else {
            lastError = String(localized: "bx could not apply the change. Turn protection off and on again.", bundle: .bx)
            return
        }
        lastError = await reloadTunnel()
    }

    private struct ConfiguredFiles: Decodable { let config: String; let rule_sets: [String: String] }

    /// Ask the tunnel what Tailscale says, every two seconds while the caller is on screen.
    func watchTailscale() async {
        while !Task.isCancelled {
            await refreshTailscale()
            try? await Task.sleep(nanoseconds: 2_000_000_000)
        }
    }

    func refreshTailscale() async {
        guard !fixture, tailscaleEnabled, state == .on, let session = manager?.connection as? NETunnelProviderSession else { return }
        let reply = await send("tailscale-status", to: session)
        if let reply, let status = try? JSONDecoder().decode(TailscaleStatus.self, from: reply) {
            tailscaleStatus = status
        }
    }

    func signOutOfTailscale() async {
        guard !fixture, let session = manager?.connection as? NETunnelProviderSession else { tailscaleStatus = nil; return }
        _ = await send("tailscale-logout", to: session)
        await refreshTailscale()
    }

    private func send(_ message: String, to session: NETunnelProviderSession) async -> Data? {
        await withCheckedContinuation { cont in
            do {
                try session.sendProviderMessage(Data(message.utf8)) { cont.resume(returning: $0) }
            } catch {
                cont.resume(returning: nil)
            }
        }
    }

    /// Fixture mode only: protection wanted but the tunnel cannot come up (the kill-switch blocks).
    func fixtureBlocked() {
        state = .failed(String(localized: "Nothing leaves this iPhone while bx keeps trying. Turn protection off to use the internet without bx.", bundle: .bx))
    }

    /// Fixture mode only: a Tailscale state to render (UI tests and snapshots).
    func fixtureTailscale(_ status: TailscaleStatus) {
        tailscaleEnabled = true
        tailscaleStatus = status
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
        proto.providerBundleIdentifier = SharedPaths.tunnelBundleID
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
