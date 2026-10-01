#if DEBUG // test driver only (Driver.swift)
import Foundation
import Network

// A bare TCP connect to **our own server** — the only destination the kill-switch test is allowed
// to touch. If it completes while the tunnel is down, the packet left on the physical interface
// (a leak, though only to a machine that already sees our home address as its tunnel client).
// If it never becomes ready, the system held it back.
enum RawProbe {
    static func run(host: String, port: UInt16, seconds: Double) async -> [String: Any] {
        await withCheckedContinuation { cont in
            let conn = NWConnection(host: NWEndpoint.Host(host), port: NWEndpoint.Port(rawValue: port)!, using: .tcp)
            let queue = DispatchQueue(label: "bx.rawprobe")
            let state = ProbeState(conn: conn, started: Date(), cont: cont)
            conn.stateUpdateHandler = { s in
                state.states.append("\(s)")
                switch s {
                case .ready: state.finish(true)
                case .failed, .cancelled: state.finish(false)
                default: break
                }
            }
            conn.start(queue: queue)
            queue.asyncAfter(deadline: .now() + seconds) { state.finish(false) }
        }
    }
}

// Every touch happens on the connection's serial queue; the class only carries state across
// the two callbacks.
private final class ProbeState: @unchecked Sendable {
    let conn: NWConnection
    let started: Date
    let cont: CheckedContinuation<[String: Any], Never>
    var states: [String] = []
    private var done = false

    init(conn: NWConnection, started: Date, cont: CheckedContinuation<[String: Any], Never>) {
        self.conn = conn
        self.started = started
        self.cont = cont
    }

    func finish(_ ready: Bool) {
        guard !done else { return }
        done = true
        let path = conn.currentPath
        conn.cancel()
        cont.resume(returning: [
            "target_is_own_server": true,
            "ready": ready,
            "states": states,
            "interface": path?.availableInterfaces.first.map { "\($0.name)/\($0.type)" } ?? "",
            "elapsed_ms": Int(Date().timeIntervalSince(started) * 1000),
        ])
    }
}
#endif
