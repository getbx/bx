import SwiftUI

// Phase 2 has no UI: the Mac launches this app with `--scenario <name>` (scripts/ios-dev.sh),
// it drives the tunnel, runs the probe, prints one JSON line prefixed `BX-RESULT ` and exits.
@main
struct BxApp: App {
    @State private var status = "bx dev build"

    var body: some Scene {
        WindowGroup {
            Text(status)
                .font(.system(.body, design: .monospaced))
                .padding()
                .task {
                    guard let scenario = Scenario.fromArguments(CommandLine.arguments) else {
                        status = "bx dev build — no scenario given"
                        return
                    }
                    status = "running \(scenario.rawValue)…"
                    let result = await Driver().run(scenario)
                    Driver.emit(result)
                    status = "done: \(scenario.rawValue)"
                    try? await Task.sleep(nanoseconds: 500_000_000)
                    exit(0)
                }
        }
    }
}
