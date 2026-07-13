import Foundation

@main
struct StatusIndicatorTests {
    static func main() {
        expect(statusIndicator(for: .off) == .gray, "off is gray")
        expect(statusIndicator(for: .setupNeeded) == .gray, "setup is gray")
        expect(statusIndicator(for: .missing) == .gray, "missing is gray")
        expect(statusIndicator(for: .warning) == .yellow, "warning is yellow")
        expect(statusIndicator(for: .updateNeeded) == .yellow, "update is yellow")
        expect(statusIndicator(for: .connected) == .green, "connected is green")
        expect(statusIndicator(for: .failed) == .red, "explicit failure is red")
    }

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }
}
