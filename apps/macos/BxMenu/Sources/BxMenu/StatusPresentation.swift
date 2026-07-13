import Foundation

struct StatusRow: Equatable {
    let label: String
    let value: String
}

struct StatusSnapshot: Equatable {
    let title: String
    let rows: [StatusRow]

    static func protected(latency: String, udpRelay: String, dns: String?, active: String, version: String) -> StatusSnapshot {
        var rows = [
            StatusRow(label: "Tunnel", value: latency),
            StatusRow(label: "UDP Relay", value: udpRelay),
        ]
        if let dns {
            rows.append(StatusRow(label: "DNS", value: dns))
        }
        rows.append(StatusRow(label: "Active", value: active))
        rows.append(StatusRow(label: "Version", value: version))
        return StatusSnapshot(title: "Protected", rows: rows)
    }

    static func off() -> StatusSnapshot {
        StatusSnapshot(title: "Off", rows: [StatusRow(label: "Status", value: "Not running")])
    }
}
