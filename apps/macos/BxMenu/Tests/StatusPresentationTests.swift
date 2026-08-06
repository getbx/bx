import Foundation

@main
struct StatusPresentationTests {
    static func main() {
        let managed = dnsPresentation(state: "managed", managed: true, service: "Wi-Fi")
        expect(managed.allowsProtected, "managed allows Protected")
        expect(managed.label == "Wi-Fi managed", "managed label")
        expect(managed.menuWarning == nil, "managed has no warning")

        let managedNoService = dnsPresentation(state: "managed", managed: true, service: nil)
        expect(managedNoService.allowsProtected, "managed without service allows Protected")
        expect(managedNoService.label == "Managed", "managed without service label")

        let unmanaged = dnsPresentation(state: "unmanaged", managed: false, service: nil)
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
