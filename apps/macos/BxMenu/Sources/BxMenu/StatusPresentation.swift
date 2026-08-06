import Foundation

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
