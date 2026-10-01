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
                MainView(args: args)
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

// Two tabs: protection (what the phone does) and Explain (why a destination goes where it goes).
// `--fixture` swaps in committed synthetic inputs and makes no VPN framework calls (simulator
// snapshots and UI tests); `--fixture-server` starts with a synthetic server already added;
// `--target <x>` opens Explain on that question.
struct MainView: View {
    let args: [String]
    @StateObject private var tunnel: TunnelController
    @State private var tab: Int

    init(args: [String]) {
        self.args = args
        let fixture = args.contains("--fixture")
        let controller = TunnelController(fixture: fixture)
        if fixture, args.contains("--fixture-server") {
            try? controller.importLink(FixtureLinks.reality)
        }
        if fixture, args.contains("--fixture-synced") {
            controller.fixtureSynced(policy: #"{"version":1780000000,"updated_at":"2026-09-30T08:00:00Z","global":false,"direct":["*.apple.com"],"proxy":[]}"#)
        }
        if fixture, let opened = BxApp.value(after: "--fixture-open", in: args) {
            controller.receive(link: opened)
        }
        if fixture, args.contains("--fixture-on") {
            Task { await controller.setProtection(true) }
        }
        _tunnel = StateObject(wrappedValue: controller)
        _tab = State(initialValue: BxApp.value(after: "--target", in: args) == nil ? 0 : 1)
    }

    // UI tests script the deploy (no network); everyone else gets the real one.
    private var deployRunner: PhoneDeployRunner {
        if args.contains("--fixture"), let outcome = BxApp.value(after: "--fixture-deploy", in: args) {
            return ScriptedDeployRunner(outcome: outcome)
        }
        return GoDeployRunner()
    }

    var body: some View {
        let fixture = args.contains("--fixture")
        let loaded = Result { try ExplainInputs.load(fixture: fixture, policy: tunnel.policyJSON) }
        let target = BxApp.value(after: "--target", in: args)
        TabView(selection: $tab) {
            HomeView(tunnel: tunnel, deployRunner: deployRunner)
                .tabItem { Label("Protection", systemImage: "checkmark.shield") }
                .tag(0)
            ExplainView(
                inputs: try? loaded.get(),
                loadError: { if case let .failure(e) = loaded { return e.localizedDescription }; return nil }(),
                target: target ?? "",
                autoRun: target != nil
            )
            .tabItem { Label("Explain", systemImage: "questionmark.circle") }
            .tag(1)
        }
        // bx:// links (the Mac's "Add to iPhone" QR, scanned with the Camera) land here; the home
        // screen confirms before anything changes.
        .onOpenURL { url in
            tunnel.receive(link: url.absoluteString)
            tab = 0
        }
    }
}

// Synthetic, obviously fake (uuid 11111111-…, documentation address, zero key): the repo guard
// TestNoUsableServerLinksInTheRepo holds it to that.
enum FixtureLinks {
    static let reality = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd&fp=chrome&flow=xtls-rprx-vision"
}
