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

struct DNSPresentation: Equatable {
    let allowsProtected: Bool
    let label: String
    /// Menu warning to surface when DNS blocks Protected; nil when it does not.
    let menuWarning: String?
}

/// Derives the menu's DNS verdict from the authoritative `bx status --json`
/// fields. Protected is allowed only when the state and the boolean agree that
/// DNS is managed; anything else — unmanaged, unknown, missing, or a core whose
/// two fields contradict each other — keeps the shield yellow.
func dnsPresentation(state: String?, managed: Bool, service: String?) -> DNSPresentation {
    if state == "managed" && managed {
        if let service, !service.isEmpty {
            return DNSPresentation(allowsProtected: true, label: "\(service) managed", menuWarning: nil)
        }
        return DNSPresentation(allowsProtected: true, label: "Managed", menuWarning: nil)
    }
    if state == "unmanaged" {
        return DNSPresentation(allowsProtected: false, label: "Not managed", menuWarning: "DNS not managed")
    }
    return DNSPresentation(
        allowsProtected: false,
        label: "Status unavailable",
        menuWarning: "DNS status unavailable"
    )
}
