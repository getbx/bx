import Foundation
import Libbox

// The app does not link libbox (it would grow by tens of MB just to read a status), so the
// extension subscribes to Tailscale's own status and answers the app's "tailscale-status"
// message with a small JSON document — what Tailscale says, never a guess.
final class TailscaleRelay: NSObject, LibboxTailscaleStatusHandlerProtocol, @unchecked Sendable {
    static let endpointTag = "tailscale" // mobileconfig.TailscaleTag

    private let lock = NSLock()
    private var latest = Data("{}".utf8)
    private var client: LibboxCommandClient?
    private var subscription: LibboxTailscaleStatusSubscription?
    private var stopped = false

    func start() {
        lock.lock(); stopped = false; lock.unlock()
        subscribe()
    }

    func stop() {
        lock.lock()
        stopped = true
        let s = subscription, c = client
        subscription = nil
        client = nil
        latest = Data("{}".utf8)
        lock.unlock()
        try? s?.close()
        try? c?.disconnect()
    }

    func status() -> Data {
        lock.lock(); defer { lock.unlock() }
        return latest
    }

    func logout() -> Data {
        lock.lock(); let c = client; lock.unlock()
        guard let c else { return Data("not running".utf8) }
        do { try c.tailscaleLogout(Self.endpointTag); return Data("ok".utf8) } catch { return Data(error.localizedDescription.utf8) }
    }

    private func subscribe() {
        guard let c = LibboxNewStandaloneCommandClient() else { return }
        do {
            let s = try c.subscribeTailscaleStatus(self)
            lock.lock()
            if stopped { lock.unlock(); try? s.close(); return }
            client = c
            subscription = s
            lock.unlock()
        } catch {
            retryLater()
        }
    }

    private func retryLater() {
        DispatchQueue.global().asyncAfter(deadline: .now() + 2) { [weak self] in
            guard let self else { return }
            self.lock.lock(); let stop = self.stopped; self.lock.unlock()
            if !stop { self.subscribe() }
        }
    }

    // MARK: LibboxTailscaleStatusHandlerProtocol

    func onError(_: String?) {
        lock.lock(); subscription = nil; client = nil; lock.unlock()
        retryLater()
    }

    func onStatusUpdate(_ update: LibboxTailscaleStatusUpdate?) {
        guard let it = update?.endpoints() else { return }
        var doc: [String: Any] = [:]
        while it.hasNext() {
            guard let ep = it.next(), ep.endpointTag == Self.endpointTag else { continue }
            var peers: [[String: Any]] = []
            if let groups = ep.userGroups() {
                while groups.hasNext() {
                    guard let g = groups.next(), let ps = g.peers() else { continue }
                    while ps.hasNext() {
                        guard let p = ps.next() else { continue }
                        peers.append(["name": p.hostName, "online": p.online, "os": p.os])
                    }
                }
            }
            doc = [
                "backend_state": ep.backendState,
                "state_text": ep.stateText,
                "auth_url": ep.authURL,
                "network_name": ep.networkName,
                "self_name": ep.self_?.hostName ?? "",
                "peers": peers,
            ]
        }
        let data = (try? JSONSerialization.data(withJSONObject: doc)) ?? Data("{}".utf8)
        lock.lock(); latest = data; lock.unlock()
    }
}
