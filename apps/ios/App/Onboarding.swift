import SwiftUI
import VisionKit

// The first screen for someone who has never used bx. It assumes nothing: not that they know what a
// server is, not that they have a link, not that they can copy and paste one. Three ways in, each
// saying in one line who it is for; typing a link is the last resort, not the first box on screen.

/// What the screen offers when there is no server yet.
struct WelcomeSections: View {
    @ObservedObject var tunnel: TunnelController
    let onScan: () -> Void
    let onDeploy: () -> Void
    let onGuide: () -> Void
    let onType: () -> Void

    var body: some View {
        Section {
            VStack(spacing: 12) {
                Image("BrandMark")
                    .resizable()
                    .scaledToFit()
                    .frame(height: 44)
                    .accessibilityHidden(true)
                Text("Protect this iPhone")
                    .font(.title2.weight(.semibold))
                Text("bx sends this iPhone's traffic through your own server. Sites in China stay direct.")
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .multilineTextAlignment(.center)
            }
            .frame(maxWidth: .infinity)
            .padding(.vertical, 2)
        }
        .listRowBackground(Color.clear)

        Section {
            choice("qrcode.viewfinder", "Scan a code",
                   "From bx on your Mac (Servers → Add to iPhone), or a code a friend shows you.",
                   id: "home.scan", action: onScan)
            choice("server.rack", "I have a server",
                   "You bought one and have its address and password. bx installs everything.",
                   id: "home.deploy", action: onDeploy)
            choice("questionmark.circle", "I don't have a server yet",
                   "What to buy, and what you will need — about three minutes.",
                   id: "home.guide", action: onGuide)
        } header: {
            Text("How do you want to start?")
        }

        Section {
            // The system Paste button: no "Allow Paste" prompt, one tap from a link sent in a chat.
            PasteButton(payloadType: String.self) { strings in
                if let text = strings.first {
                    Task { @MainActor in tunnel.receive(link: mainLinkFromPaste(text)) }
                }
            }
            .accessibilityIdentifier("home.paste")
            Button("Type a link instead", action: onType)
                .font(.footnote)
                .accessibilityIdentifier("home.typeLink")
        } header: {
            Text("Got a link in a message?")
        } footer: {
            Text("Copy the link in your chat app, then tap Paste. bx shows the server's address and asks before adding it.")
        }
    }

    private func choice(_ symbol: String, _ title: LocalizedStringKey, _ subtitle: LocalizedStringKey,
                        id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: 14) {
                Image(systemName: symbol)
                    .font(.title2)
                    .foregroundStyle(Color.accentColor)
                    .frame(width: 34)
                VStack(alignment: .leading, spacing: 2) {
                    Text(title).font(.headline).foregroundStyle(.primary)
                    Text(subtitle).font(.subheadline).foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
                Spacer(minLength: 0)
                Image(systemName: "chevron.right").font(.footnote).foregroundStyle(.tertiary)
            }
            .padding(.vertical, 6)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(id)
    }
}

/// "I don't have a server yet": what a server is, what to buy, what you will be given — then
/// straight on to setting it up. No provider is named: bx does not sell or recommend one.
struct NoServerGuide: View {
    let onHaveOne: () -> Void

    var body: some View {
        Form {
            Section {
                Text("bx needs a small rented computer outside mainland China — a \"VPS\" (virtual private server). Your traffic goes through it, so websites see the server, not you.")
            } header: { Text("What is a server?") }
            Section {
                Label("Located outside mainland China (Hong Kong, Japan, Singapore and the US are common)", systemImage: "globe.asia.australia")
                Label("Ubuntu or Debian, 64-bit", systemImage: "cpu")
                Label("The smallest plan is enough: 1 CPU, 1 GB memory", systemImage: "gauge.low")
                Label("Usually a few dollars a month", systemImage: "creditcard")
            } header: { Text("What to buy") }
            Section {
                Label("An address — numbers like 203.0.113.9", systemImage: "number")
                Label("A login, usually root", systemImage: "person")
                Label("A password (in the provider's email or console)", systemImage: "key")
            } header: { Text("What the provider gives you") } footer: {
                Text("If the provider has a \"security group\" or firewall page, allow port 443 (TCP and UDP) there.")
            }
            Section {
                Button("I have these now — set it up", action: onHaveOne)
                    .accessibilityIdentifier("guide.continue")
            }
        }
        .navigationTitle("Getting a Server")
        .navigationBarTitleDisplayMode(.inline)
    }
}

/// Scan a bx QR code inside the app (the Mac's "Add to iPhone", `bx server share --qr`). Whatever is
/// scanned goes through TunnelController.receive — the same confirmation as any opened link.
struct ScanSheet: View {
    @ObservedObject var tunnel: TunnelController
    let onClose: () -> Void
    /// UI tests and the simulator (no camera): pretend this was scanned.
    var simulated: String?

    var body: some View {
        NavigationStack {
            Group {
                if let simulated {
                    Button("Simulated scan") { found(simulated) }
                        .accessibilityIdentifier("scan.simulate")
                } else if DataScannerViewController.isSupported && DataScannerViewController.isAvailable {
                    QRScanner { found($0) }
                        .ignoresSafeArea()
                } else {
                    ContentUnavailableView("Camera not available",
                                           systemImage: "camera",
                                           description: Text("Allow bx to use the camera in Settings, or tap Paste Link on the first screen."))
                }
            }
            .navigationTitle("Scan the Code")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel", action: onClose) } }
        }
    }

    private func found(_ payload: String) {
        let link = mainLinkFromPaste(payload)
        guard link.lowercased().hasPrefix("bx://") || link.lowercased().hasPrefix("vless://") else { return }
        onClose()
        tunnel.receive(link: link)
    }
}

/// VisionKit's scanner, QR only.
struct QRScanner: UIViewControllerRepresentable {
    let onFound: (String) -> Void

    func makeUIViewController(context: Context) -> DataScannerViewController {
        let vc = DataScannerViewController(recognizedDataTypes: [.barcode(symbologies: [.qr])],
                                           qualityLevel: .balanced, isHighlightingEnabled: true)
        vc.delegate = context.coordinator
        try? vc.startScanning()
        return vc
    }

    func updateUIViewController(_ vc: DataScannerViewController, context: Context) {}

    func makeCoordinator() -> Coordinator { Coordinator(onFound: onFound) }

    final class Coordinator: NSObject, DataScannerViewControllerDelegate {
        let onFound: (String) -> Void
        private var done = false
        init(onFound: @escaping (String) -> Void) { self.onFound = onFound }

        func dataScanner(_ scanner: DataScannerViewController, didAdd items: [RecognizedItem], allItems: [RecognizedItem]) {
            for case let .barcode(code) in items {
                if !done, let payload = code.payloadStringValue {
                    done = true
                    scanner.stopScanning()
                    onFound(payload)
                    return
                }
            }
        }
    }
}

/// Said before iPhone's own "bx Would Like to Add VPN Configurations" alert — the moment many
/// people tap Don't Allow because nobody told them what it is.
struct VPNPermissionExplainer: View {
    let onContinue: () -> Void
    let onCancel: () -> Void

    var body: some View {
        VStack(spacing: 18) {
            Image(systemName: "lock.shield").font(.system(size: 52)).foregroundStyle(Color.accentColor)
                .padding(.top, 24)
            Text("iPhone will ask about a VPN").font(.title2.weight(.semibold))
            Text("Next, iPhone asks whether bx may add a VPN configuration. Tap **Allow** — that is how bx protects this iPhone's traffic. You may be asked for your passcode.")
                .multilineTextAlignment(.center)
                .foregroundStyle(.secondary)
            Spacer()
            Button(action: onContinue) {
                Text("Continue").font(.headline).frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .accessibilityIdentifier("vpn.continue")
            Button("Not Now", action: onCancel)
        }
        .padding(24)
        .presentationDetents([.medium])
    }
}
