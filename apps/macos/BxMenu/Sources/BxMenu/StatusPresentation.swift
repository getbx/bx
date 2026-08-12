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
/// `servers` 是系统此刻实际配置的解析器地址(Guardian 的 `dns_servers`)。
///
/// **它只在 DNS 没被 bx 接管时进入文案,这是刻意的。** 「已接管」的判据本身就是
/// `servers == ["127.0.0.1"]`,所以在那一支里显示它等于永远打印同一个常数 ——
/// 一条恒定不变的行不是信息,是噪声,而菜单刚刚才因为这个理由删掉三行占位符。
///
/// 反过来,**没被接管时「现在到底是谁在解析」正是用户唯一能据以行动的那个值**:
/// `192.168.1.1` 说明查询交给了本地路由器,而那台路由器把它交给了谁,bx 不知道。
func dnsPresentation(state: String?, managed: Bool, service: String?, servers: [String] = []) -> DNSPresentation {
    let resolvers = servers.filter { !$0.isEmpty }.joined(separator: ", ")
    let suffix = resolvers.isEmpty ? "" : " · \(resolvers)"
    if state == "managed" && managed {
        if let service, !service.isEmpty {
            return DNSPresentation(allowsProtected: true, label: "\(service) managed", menuWarning: nil)
        }
        return DNSPresentation(allowsProtected: true, label: "Managed", menuWarning: nil)
    }
    if state == "unmanaged" {
        return DNSPresentation(
            allowsProtected: false,
            label: "Not managed" + suffix,
            menuWarning: "DNS not managed" + suffix
        )
    }
    return DNSPresentation(
        allowsProtected: false,
        label: "Status unavailable",
        menuWarning: "DNS status unavailable"
    )
}
