import SwiftUI

// With `--scenario <name>` (scripts/ios-dev.sh) the app drives the tunnel headlessly, prints one
// JSON line prefixed `BX-RESULT ` and exits. Without it, it shows the Explain screen.
// `--fixture` swaps the owner's rules for the committed synthetic ones (simulator snapshots);
// `--target <x>` prefills and runs the question.
@main
struct BxApp: App {
    private let args = CommandLine.arguments

    var body: some Scene {
        WindowGroup {
            if let scenario = Scenario.fromArguments(args) {
                HeadlessView(scenario: scenario, args: args)
            } else {
                let fixture = args.contains("--fixture")
                let loaded = Result { try ExplainInputs.load(fixture: fixture) }
                let target = Self.value(after: "--target", in: args)
                ExplainView(
                    inputs: try? loaded.get(),
                    loadError: { if case let .failure(e) = loaded { return e.localizedDescription }; return nil }(),
                    target: target ?? "",
                    autoRun: target != nil
                )
            }
        }
    }

    static func value(after flag: String, in args: [String]) -> String? {
        guard let i = args.firstIndex(of: flag), i + 1 < args.count else { return nil }
        return args[i + 1]
    }
}

struct HeadlessView: View {
    let scenario: Scenario
    let args: [String]
    @State private var status = "running…"

    var body: some View {
        Text(status)
            .font(.system(.body, design: .monospaced))
            .padding()
            .task {
                status = "running \(scenario.rawValue)…"
                let result = await Driver().run(scenario, args: args)
                Driver.emit(result)
                status = "done: \(scenario.rawValue)"
                try? await Task.sleep(nanoseconds: 500_000_000)
                exit(0)
            }
    }
}
