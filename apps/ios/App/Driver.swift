import Bxkit
import Foundation
import Network
import NetworkExtension

enum Scenario: String {
    case connect      // real server: the exit must be the server
    case deadserver   // proxy points at 192.0.2.1: every request must fail, nothing may leave
    case armed        // kill-switch on (includeAllNetworks + on-demand), real server: normal traffic still works
    case armedbroken  // kill-switch on, tunnel cannot start: nothing may reach even our own server
    case explain      // headless explain: --target <x> [--fixture]
    case app          // the home screen's own path: paste link → protection on → probe → off → forget
    case tailscale    // bx carries the tailnet: --ts-probe <url> (repeatable) [--ts-wait <s>]; prints BX-TS-LOGIN <url>
    case stop
    case remove       // delete the VPN configuration from Settings (run at the end of every session)

    static func fromArguments(_ args: [String]) -> Scenario? {
        guard let i = args.firstIndex(of: "--scenario"), i + 1 < args.count else { return nil }
        return Scenario(rawValue: args[i + 1])
    }
}

struct Driver {
    static let quick: URLSession = {
        let c = URLSessionConfiguration.ephemeral
        c.timeoutIntervalForRequest = 4
        return URLSession(configuration: c)
    }()

    static func value(after flag: String, in args: [String]) -> String? {
        guard let i = args.firstIndex(of: flag), i + 1 < args.count else { return nil }
        return args[i + 1]
    }

    /// One HTTP/1.0 GET over a bare TCP connection; the response text, or nil.
    static func rawGET(host: String, port: UInt16, seconds: Double) async -> String? {
        await withCheckedContinuation { (cont: CheckedContinuation<String?, Never>) in
            let conn = NWConnection(host: NWEndpoint.Host(host), port: NWEndpoint.Port(rawValue: port)!, using: .tcp)
            let queue = DispatchQueue(label: "bx.rawget")
            var done = false
            var received = Data()
            func finish(_ s: String?) {
                guard !done else { return }
                done = true
                conn.cancel()
                cont.resume(returning: s)
            }
            func read() {
                conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { data, _, isComplete, error in
                    if let data { received.append(data) }
                    if isComplete || error != nil { finish(String(decoding: received, as: UTF8.self)) } else { read() }
                }
            }
            conn.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    conn.send(content: Data("GET / HTTP/1.0\r\nHost: \(host)\r\n\r\n".utf8), completion: .contentProcessed { _ in read() })
                case .failed, .cancelled: finish(nil)
                default: break
                }
            }
            conn.start(queue: queue)
            queue.asyncAfter(deadline: .now() + seconds) { finish(received.isEmpty ? nil : String(decoding: received, as: UTF8.self)) }
        }
    }

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

    func run(_ scenario: Scenario, args: [String] = []) async -> [String: Any] {
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
            case .app:
                guard let dev = Bundle.main.url(forResource: "Dev", withExtension: nil),
                      let link = try? String(contentsOf: dev.appendingPathComponent("server-link.txt"), encoding: .utf8)
                else { throw DriverError("no Dev/server-link.txt; run scripts/ios-dev.sh config first") }
                let controller = await MainActor.run { TunnelController(fixture: false) }
                try await MainActor.run { try controller.importLink(link) }
                out["server_host"] = await controller.serverHost ?? ""
                await controller.setProtection(true)
                let deadline = Date().addingTimeInterval(20)
                while await controller.state != .on, Date() < deadline {
                    try await Task.sleep(nanoseconds: 200_000_000)
                }
                out["state_after_on"] = "\(await controller.state)"
                out["error_after_on"] = await controller.lastError ?? ""
                // Rule sync: the pull starts when protection comes on; wait for its verdict, then ask
                // Explain with the policy the tunnel now runs — and with the defaults, for contrast.
                let rulesDeadline = Date().addingTimeInterval(20)
                while await controller.rules == .defaults(.notCheckedYet), Date() < rulesDeadline {
                    try await Task.sleep(nanoseconds: 200_000_000)
                }
                out["rules"] = "\(await controller.rules)"
                let running = try ExplainInputs.load(fixture: false, policy: await controller.policyJSON)
                let fallback = try ExplainInputs.load(fixture: false)
                var asked: [String: String] = [:]
                for target in ["www.apple.com", "www.icloud.com"] {
                    let r = try running.explain(target)
                    let d = try fallback.explain(target)
                    asked[target] = "\(r.verdict) via \(r.rule ?? "-") (defaults: \(d.verdict))"
                }
                out["explain"] = asked
                try await Task.sleep(nanoseconds: 1_500_000_000)
                out["probe"] = await Probe.run()
                out["expect"] = expectation()
                await controller.setProtection(false)
                // stopVPNTunnel returns before iOS has disconnected; the state follows the status
                // notification, so wait for it the same way as for "on" above.
                let offDeadline = Date().addingTimeInterval(10)
                while await controller.state == .on, Date() < offDeadline {
                    try await Task.sleep(nanoseconds: 200_000_000)
                }
                out["state_after_off"] = "\(await controller.state)"
                await controller.forgetServer()
                out["forgot"] = true
            case .tailscale:
                guard let dev = Bundle.main.url(forResource: "Dev", withExtension: nil),
                      let link = try? String(contentsOf: dev.appendingPathComponent("server-link.txt"), encoding: .utf8)
                else { throw DriverError("no Dev/server-link.txt; run scripts/ios-dev.sh config first") }
                var error: NSError?
                let json = BxkitConfigureWithOptions(link.trimmingCharacters(in: .whitespacesAndNewlines), BxkitDefaultPolicy(),
                                                     #"{"tailscale":true}"#, BundledLists.chinaDomain, BundledLists.chinaCIDR, &error)
                if let error { throw error }
                let parsed = try JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any] ?? [:]
                let config = parsed["config"] as? String ?? ""
                try SharedPaths.writeStartConfig(config, ruleSets: parsed["rule_sets"] as? [String: String] ?? [:])
                guard let base = SharedPaths.container else { throw DriverError("no app group") }
                let loginURL = base.appendingPathComponent(SharedPaths.tailscaleLoginURLName)
                let memoryURL = base.appendingPathComponent(SharedPaths.tunnelMemoryName)
                try? FileManager.default.removeItem(at: loginURL)
                try? FileManager.default.removeItem(at: memoryURL)

                let manager = try await loadOrCreateManager()
                try await stop(manager)
                try manager.connection.startVPNTunnel(options: ["configContent": config as NSString])
                let started = await waitFor(manager, .connected, seconds: 20)
                out["tunnel_status"] = describe(started)
                if started != .connected {
                    // Why the extension stopped: its startTunnel error, as iOS kept it.
                    do { try await manager.connection.fetchLastDisconnectError() } catch { out["start_error"] = "\(error)" }
                    out["start_error_text"] = (try? String(contentsOf: base.appendingPathComponent(SharedPaths.tunnelStartErrorName), encoding: .utf8)) ?? ""
                    out["box_log_tail"] = boxLogTail(lines: 15)
                    return out // probing now would measure the phone without bx, not bx
                }
                try await Task.sleep(nanoseconds: 1_500_000_000)
                out["probe"] = await Probe.run() // the internet through bx, with Tailscale alongside

                // Reach the tailnet: the targets are on the user's own tailnet, given at run time.
                var targets: [String] = []
                for (i, a) in args.enumerated() where a == "--ts-probe" && i + 1 < args.count { targets.append(args[i + 1]) }
                let wait = Double(Self.value(after: "--ts-wait", in: args) ?? "") ?? 240
                var reached: [String: String] = [:]
                var announced = false
                let deadline = Date().addingTimeInterval(wait)
                while Date() < deadline {
                    if !announced, let url = try? String(contentsOf: loginURL, encoding: .utf8) {
                        print("BX-TS-LOGIN \(url)")
                        fflush(stdout)
                        announced = true
                    }
                    for t in targets where reached[t] == nil {
                        // Raw TCP, not URLSession: App Transport Security refuses plain http before
                        // a single packet leaves, which reads exactly like "unreachable". The reply
                        // must carry the page's own text — a tunnel can complete a handshake locally.
                        if let u = URL(string: t), let host = u.host,
                           let body = await Self.rawGET(host: host, port: UInt16(u.port ?? 80), seconds: 6),
                           body.contains("bx tailnet ok") {
                            reached[t] = "page answered"
                        }
                    }
                    if !targets.isEmpty, reached.count == targets.count { break }
                    try await Task.sleep(nanoseconds: 2_000_000_000)
                }
                out["login_url_seen"] = announced
                // What the app's Tailscale screen reads: the extension's relay of Tailscale's own status.
                if let session = manager.connection as? NETunnelProviderSession {
                    let reply: Data? = await withCheckedContinuation { cont in
                        do { try session.sendProviderMessage(Data("tailscale-status".utf8)) { cont.resume(returning: $0) } } catch { cont.resume(returning: nil) }
                    }
                    if let reply, let st = try? JSONDecoder().decode(TailscaleStatus.self, from: reply) {
                        out["relay"] = ["backend_state": st.backend_state ?? "", "self_name": st.self_name ?? "",
                                        "peers": st.peers?.count ?? -1, "online": st.peers?.filter(\.online).count ?? -1,
                                        "phase": "\(TailscalePhase.from(enabled: true, protection: .on, status: st))".prefix(40)]
                    } else {
                        out["relay"] = "no reply"
                    }
                }
                out["tailnet"] = Dictionary(uniqueKeysWithValues: targets.map { ($0, reached[$0] ?? "unreachable") })
                out["probe_after"] = await Probe.run()
                if let data = try? Data(contentsOf: memoryURL), let m = try? JSONSerialization.jsonObject(with: data) {
                    out["memory"] = m
                }
                out["box_log_tail"] = boxLogTail(lines: 30)
                let needles = ["tailscale", "magicdns", "ts.net"] + targets.compactMap { URL(string: $0)?.host }
                out["box_log_tailnet"] = boxLogTail(lines: 3000).filter { line in needles.contains { line.lowercased().contains($0.lowercased()) } }.suffix(60).map { $0 }
                try await stop(manager)
                out["tunnel_status_after"] = describe(manager.connection.status)
            case .explain:
                let target = BxApp.value(after: "--target", in: args) ?? ""
                let answer = try ExplainInputs.load(fixture: args.contains("--fixture")).explain(target)
                out["answer"] = ["target": answer.target, "kind": answer.kind, "verdict": answer.verdict, "because": answer.because, "rule": answer.rule ?? ""]
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
