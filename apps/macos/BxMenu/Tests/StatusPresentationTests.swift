import Foundation

@main
struct StatusPresentationTests {
    static func main() {
        let managed = dnsPresentation(state: "managed", managed: true, service: "Wi-Fi")
        expect(managed.allowsProtected, "managed allows Protected")
        // **主语必须是 bx。** 「Wi-Fi managed」会被读成「DNS 归 Wi-Fi 管」——
        // 真人第一次读到它时就是这么问的,而那正好是事实的反面。
        expect(managed.label == "Handled by bx (Wi-Fi)", "managed label,实际 \(managed.label)")
        expect(!managed.label.hasPrefix("Wi-Fi"),
               "网络服务名不许当主语:那样读起来是「DNS 归 Wi-Fi 管」,与事实相反")
        expect(managed.menuWarning == nil, "managed has no warning")

        let managedNoService = dnsPresentation(state: "managed", managed: true, service: nil)
        expect(managedNoService.allowsProtected, "managed without service allows Protected")
        expect(managedNoService.label == "Handled by bx", "managed without service label,实际 \(managedNoService.label)")

        // **没被接管时必须说出现在是谁在解析。** 那是用户唯一能据以行动的值,
        // 而它此前一个字都没显示过。已接管那一支刻意不显示 —— 判据本身就是
        // servers == ["127.0.0.1"],显示它等于永远打印同一个常数。
        let unmanagedWithServers = dnsPresentation(state: "unmanaged", managed: false, service: nil,
                                                   servers: ["192.168.1.1"])
        expect(unmanagedWithServers.label.contains("192.168.1.1"),
               "未接管时行内必须带出真实解析器,实际 \(unmanagedWithServers.label)")
        expect(unmanagedWithServers.menuWarning?.contains("192.168.1.1") == true,
               "未接管的告警必须带出真实解析器,实际 \(String(describing: unmanagedWithServers.menuWarning))")
        // 问不出解析器时措辞必须原样退回去 —— 不许留一个空的 " · " 尾巴,
        // 那读起来像「解析器是空的」,而真相是「没问出来」。
        let unmanaged = dnsPresentation(state: "unmanaged", managed: false, service: nil)
        let managedWithServers = dnsPresentation(state: "managed", managed: true, service: "Wi-Fi",
                                                 servers: ["127.0.0.1"])
        expect(!managedWithServers.label.contains("127.0.0.1"),
               "已接管那一支不该打印恒定的 127.0.0.1,实际 \(managedWithServers.label)")
        expect(!unmanaged.allowsProtected, "unmanaged is attention")
        expect(unmanaged.label == "Not managed", "unmanaged label")
        expect(unmanaged.menuWarning == "DNS not managed", "unmanaged warning")

        let unknown = dnsPresentation(state: nil, managed: false, service: nil)
        expect(!unknown.allowsProtected, "missing old status is attention")
        expect(unknown.label == "Status unavailable", "unknown label")
        expect(unknown.menuWarning == "DNS status unavailable", "unknown warning")

        let unknownState = dnsPresentation(state: "unknown", managed: false, service: nil)
        expect(!unknownState.allowsProtected, "unknown state is attention")
        expect(unknownState.label == "Status unavailable", "unknown state label")

        // A core that reports state=managed while dns_managed=false is internally
        // inconsistent, so it must never light the shield green.
        let contradictory = dnsPresentation(state: "managed", managed: false, service: "Wi-Fi")
        expect(!contradictory.allowsProtected, "contradictory status is attention")
        expect(contradictory.label == "Status unavailable", "contradictory label")
        expect(contradictory.menuWarning == "DNS status unavailable", "contradictory warning")
    }

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }
}
