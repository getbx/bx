import Foundation

// Two facts, both taken only while the tunnel is up (no "baseline without the tunnel": that
// request would itself be the leak we are testing for):
//   1. what the system resolver answers for the probe host — a 198.18/15 fake IP means DNS
//      went to bx, not to the network;
//   2. what the echo service says our address is — must be the server's, or no answer at all.
enum Probe {
    static let host = "ipv4.icanhazip.com" // not on the china direct list (TestPublicIPProbeDomainsAreNotChinaDirect)

    static func run() async -> [String: Any] {
        var out: [String: Any] = ["host": host, "resolved": resolve(host)]
        let config = URLSessionConfiguration.ephemeral
        config.timeoutIntervalForRequest = 15
        config.timeoutIntervalForResource = 20
        config.requestCachePolicy = .reloadIgnoringLocalCacheData
        let session = URLSession(configuration: config)
        let started = Date()
        do {
            let (data, response) = try await session.data(from: URL(string: "https://\(host)/")!)
            out["http_status"] = (response as? HTTPURLResponse)?.statusCode ?? -1
            out["exit_ip"] = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        } catch {
            out["error"] = (error as NSError).localizedDescription
            out["error_code"] = (error as NSError).code
        }
        out["elapsed_ms"] = Int(Date().timeIntervalSince(started) * 1000)
        return out
    }

    static func resolve(_ name: String) -> [String] {
        var hints = addrinfo(ai_flags: 0, ai_family: AF_INET, ai_socktype: SOCK_STREAM, ai_protocol: 0, ai_addrlen: 0, ai_canonname: nil, ai_addr: nil, ai_next: nil)
        var res: UnsafeMutablePointer<addrinfo>?
        guard getaddrinfo(name, nil, &hints, &res) == 0, let first = res else { return [] }
        defer { freeaddrinfo(first) }
        var out: [String] = []
        var cur: UnsafeMutablePointer<addrinfo>? = first
        while let ai = cur {
            var buf = [CChar](repeating: 0, count: Int(NI_MAXHOST))
            if getnameinfo(ai.pointee.ai_addr, ai.pointee.ai_addrlen, &buf, socklen_t(buf.count), nil, 0, NI_NUMERICHOST) == 0 {
                out.append(String(cString: buf))
            }
            cur = ai.pointee.ai_next
        }
        return Array(Set(out)).sorted()
    }
}
