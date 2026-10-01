import SwiftUI

/// What the tunnel extension relays from Tailscale itself (TailscaleRelay). Field names are the
/// relay's JSON keys.
struct TailscaleStatus: Decodable, Equatable {
    struct Peer: Decodable, Equatable, Hashable {
        let name: String
        let online: Bool
        let os: String
    }

    var backend_state: String?
    var state_text: String?
    var auth_url: String?
    var network_name: String?
    var self_name: String?
    var peers: [Peer]?
}

/// Where Tailscale stands, in the terms the screen speaks. Every case says what the person can
/// do next; nothing here is inferred beyond what Tailscale reported.
enum TailscalePhase: Equatable {
    case off
    case needsProtection            // Tailscale runs inside bx's tunnel
    case starting
    case signIn(URL?)               // nil while Tailscale is still fetching the link
    case awaitingApproval           // the tailnet admin must approve this device
    case connected(network: String, me: String, peers: [TailscaleStatus.Peer])
    case notConnected(String)       // any other Tailscale state, in Tailscale's own words

    static func from(enabled: Bool, protection: TunnelController.State, status: TailscaleStatus?) -> TailscalePhase {
        guard enabled else { return .off }
        switch protection {
        case .on: break
        case .connecting: return .starting
        default: return .needsProtection
        }
        guard let status, let backend = status.backend_state, !backend.isEmpty else { return .starting }
        switch backend {
        case "NeedsLogin":
            return .signIn(URL(string: status.auth_url ?? "").flatMap { $0.scheme == "https" ? $0 : nil })
        case "NeedsMachineAuth":
            return .awaitingApproval
        case "Running":
            let peers = (status.peers ?? []).sorted { ($0.online ? 0 : 1, $0.name.lowercased()) < ($1.online ? 0 : 1, $1.name.lowercased()) }
            return .connected(network: status.network_name ?? "", me: status.self_name ?? "", peers: peers)
        case "NoState", "Starting":
            if let url = URL(string: status.auth_url ?? ""), url.scheme == "https" { return .signIn(url) }
            return .starting
        default:
            return .notConnected(status.state_text?.isEmpty == false ? status.state_text! : backend)
        }
    }

    /// The one-word value on the home screen's row.
    var summary: String {
        switch self {
        case .off: return String(localized: "Off", bundle: .bx)
        case .needsProtection: return String(localized: "Waiting for protection", bundle: .bx)
        case .starting: return String(localized: "Connecting…", bundle: .bx)
        case .signIn: return String(localized: "Sign-in needed", bundle: .bx)
        case .awaitingApproval: return String(localized: "Waiting for approval", bundle: .bx)
        case .connected: return String(localized: "Connected", bundle: .bx)
        case .notConnected: return String(localized: "Not connected", bundle: .bx)
        }
    }
}

struct TailscaleScreen: View {
    @ObservedObject var tunnel: TunnelController
    let onClose: () -> Void
    @Environment(\.openURL) private var openURL
    @State private var confirmingSignOut = false

    private var phase: TailscalePhase { tunnel.tailscalePhase }

    var body: some View {
        Form {
            Section {
                Toggle("Connect Tailscale through bx", isOn: Binding(
                    get: { tunnel.tailscaleEnabled },
                    set: { on in Task { await tunnel.setTailscale(on) } }))
                    .accessibilityIdentifier("tailscale.toggle")
            } footer: {
                Text("iPhone can run only one VPN at a time, so turning on bx switches the Tailscale app off. bx can connect Tailscale itself, so your NAS and computers stay reachable.")
            }

            switch phase {
            case .off:
                EmptyView()
            case .needsProtection:
                Section { Text("Tailscale connects when protection is on.") }
            case .starting:
                Section { Label("Connecting to Tailscale…", systemImage: "hourglass") }
            case let .signIn(url):
                Section {
                    Button("Sign In to Tailscale") { if let url { openURL(url) } }
                        .disabled(url == nil)
                        .accessibilityIdentifier("tailscale.signIn")
                } header: {
                    Text("Sign in once")
                } footer: {
                    Text("Use the same account as the Tailscale app. This iPhone will appear in your Tailscale device list as a new device named bx-iphone.")
                }
            case .awaitingApproval:
                Section {
                    Label("Waiting for approval", systemImage: "person.badge.clock")
                } footer: {
                    Text("Your Tailscale network requires new devices to be approved. Approve bx-iphone in the Tailscale admin console.")
                }
            case let .connected(network, me, peers):
                Section {
                    LabeledContent("Network", value: network)
                    LabeledContent("This iPhone", value: me)
                } header: {
                    Text("Connected")
                }
                .accessibilityIdentifier("tailscale.connected")
                Section {
                    if peers.isEmpty {
                        Text("No other devices on this network yet.")
                    }
                    ForEach(peers, id: \.self) { peer in
                        LabeledContent(peer.name) {
                            Label(peer.online ? String(localized: "Online", bundle: .bx) : String(localized: "Offline", bundle: .bx), systemImage: "circle.fill")
                                .labelStyle(.titleAndIcon)
                                .foregroundStyle(peer.online ? .green : .secondary)
                        }
                    }
                } header: {
                    Text("Your devices")
                } footer: {
                    Text("An offline device can't be reached until it comes back online. Can't reach a whole home or office network? The device that shares that network must be online.")
                }
                Section {
                    Button("Sign Out of Tailscale", role: .destructive) { confirmingSignOut = true }
                }
            case let .notConnected(reason):
                Section {
                    Label("Tailscale is not connected", systemImage: "exclamationmark.triangle")
                        .foregroundStyle(.orange)
                } footer: {
                    Text(reason)
                }
            }

            if tunnel.tailscaleEnabled {
                Section {
                    Text("If the Tailscale app is set to connect automatically, turn that off in the Tailscale app. Otherwise it switches bx off.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
        }
        .navigationTitle("Tailscale")
        .navigationBarTitleDisplayMode(.inline)
        .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done", action: onClose) } }
        .confirmationDialog("Sign out of Tailscale?", isPresented: $confirmingSignOut, titleVisibility: .visible) {
            Button("Sign Out", role: .destructive) { Task { await tunnel.signOutOfTailscale() } }
        } message: {
            Text("This iPhone leaves your Tailscale network. To connect again, you sign in again.")
        }
    }
}
