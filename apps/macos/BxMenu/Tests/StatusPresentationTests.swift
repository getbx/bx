import Foundation

@main
struct StatusPresentationTests {
    static func main() {
        let protected = StatusSnapshot.protected(
            latency: "287 ms",
            udpRelay: "On",
            dns: "Wi-Fi managed",
            active: "12",
            version: "dev"
        )
        expect(protected.title == "Protected", "protected title")
        expect(
            protected.rows == [
                StatusRow(label: "Tunnel", value: "287 ms"),
                StatusRow(label: "UDP Relay", value: "On"),
                StatusRow(label: "DNS", value: "Wi-Fi managed"),
                StatusRow(label: "Active", value: "12"),
                StatusRow(label: "Version", value: "dev"),
            ],
            "protected rows"
        )

        let off = StatusSnapshot.off()
        expect(off.title == "Off", "off title")
        expect(off.rows == [StatusRow(label: "Status", value: "Not running")], "off rows")
    }

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }
}
