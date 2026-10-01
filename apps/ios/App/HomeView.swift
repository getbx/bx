import SwiftUI

struct HomeView: View {
    @ObservedObject var tunnel: TunnelController
    var deployRunner: PhoneDeployRunner = GoDeployRunner()
    var scanSimulated: String?
    var openTailscale = false // fixture only: start with the Tailscale screen open (snapshots)
    @State private var showingAdd = false
    @State private var showingDeploy = false
    @State private var showingScan = false
    @State private var showingGuide = false
    @State private var showingType = false
    @State private var showingVPNExplainer = false
    @State private var showingTailscale = false

    var body: some View {
        NavigationStack {
            Form {
                if tunnel.state == .noServer {
                    WelcomeSections(tunnel: tunnel,
                                    onScan: { showingScan = true },
                                    onDeploy: { showingDeploy = true },
                                    onGuide: { showingGuide = true },
                                    onType: { showingType = true })
                } else {
                    Section {
                        hero
                    } footer: {
                        Text("While protection is on, nothing leaves this iPhone outside the tunnel — even while it reconnects.")
                            .frame(maxWidth: .infinity)
                            .multilineTextAlignment(.center)
                    }
                    .listRowBackground(Color.clear)
                    Section {
                        LabeledContent("Rules", value: rulesTitle)
                            .accessibilityIdentifier("home.rules")
                    } footer: {
                        if let note = rulesNote { Text(note) }
                    }
                    Section("Server") {
                        LabeledContent("Address", value: tunnel.serverHost ?? "")
                            .accessibilityIdentifier("home.server")
                        Button("Change Server…") { showingAdd = true }
                    }
                    Section {
                        Button { showingTailscale = true } label: {
                            HStack {
                                Text("Tailscale").foregroundStyle(.primary)
                                Spacer()
                                Text(tunnel.tailscalePhase.summary).foregroundStyle(.secondary)
                                Image(systemName: "chevron.right").font(.footnote.weight(.semibold)).foregroundStyle(.tertiary)
                            }
                        }
                        .accessibilityIdentifier("home.tailscale")
                    } header: {
                        Text("Home and office devices")
                    } footer: {
                        if !tunnel.tailscaleEnabled {
                            Text("Use Tailscale to reach your NAS or computers? iPhone runs one VPN at a time, so bx can connect Tailscale for you.")
                        }
                    }
                    if let message = tunnel.lastError, !isFailed {
                        Section {
                            Text(message).foregroundStyle(.orange)
                        }
                    }
                }
            }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) { BrandTitle() }
                ToolbarItem(placement: .topBarTrailing) { LanguageMenu() }
            }
            .sheet(item: $tunnel.incoming) { pending in
                IncomingLinkSheet(pending: pending, current: tunnel.serverHost,
                                  onAdd: { tunnel.acceptIncoming() },
                                  onCancel: { tunnel.incoming = nil })
            }
            .sheet(isPresented: $showingScan) {
                ScanSheet(tunnel: tunnel, onClose: { showingScan = false }, simulated: scanSimulated)
            }
            .sheet(isPresented: $showingGuide) {
                NavigationStack {
                    NoServerGuide(onHaveOne: {
                        showingGuide = false
                        // One sheet at a time: let the guide finish closing before the form opens.
                        DispatchQueue.main.asyncAfter(deadline: .now() + 0.5) { showingDeploy = true }
                    })
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Close") { showingGuide = false } } }
                }
            }
            .sheet(isPresented: $showingType) {
                NavigationStack {
                    Form {
                        Section {
                            AddServerForm(tunnel: tunnel, onDone: { showingType = false })
                        } footer: {
                            Text("Paste the bx:// link, or the whole line your server printed. It is stored in this iPhone's Keychain and never leaves the device.")
                        }
                    }
                    .navigationTitle("Add a Link")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { showingType = false } } }
                }
            }
            // Tailscale's state comes from the tunnel; ask while this screen (or the sheet over it) is up.
            .task(id: "\(tunnel.state)-\(tunnel.tailscaleEnabled)") {
                if tunnel.tailscaleEnabled, tunnel.state == .on { await tunnel.watchTailscale() }
            }
            .onAppear { if openTailscale { showingTailscale = true } }
            .sheet(isPresented: $showingTailscale) {
                NavigationStack { TailscaleScreen(tunnel: tunnel, onClose: { showingTailscale = false }) }
            }
            .sheet(isPresented: $showingVPNExplainer) {
                VPNPermissionExplainer(onContinue: {
                    showingVPNExplainer = false
                    tunnel.vpnExplained = true
                    Task { await tunnel.setProtection(true) }
                }, onCancel: { showingVPNExplainer = false })
            }
            .sheet(isPresented: $showingDeploy) {
                NavigationStack {
                    DeployScreen(tunnel: tunnel, runner: deployRunner, onDone: { showingDeploy = false })
                        .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Close") { showingDeploy = false } } }
                }
            }
            .sheet(isPresented: $showingAdd) {
                NavigationStack {
                    Form {
                        Section {
                            AddServerForm(tunnel: tunnel, onDone: { showingAdd = false })
                        } footer: {
                            Text("Protection turns off while the server changes.")
                        }
                    }
                    .navigationTitle("Change Server")
                    .navigationBarTitleDisplayMode(.inline)
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { showingAdd = false } } }
                }
            }
        }
    }

    // The one thing this screen is for: am I protected, and the one control that changes it.
    private var hero: some View {
        VStack(spacing: 10) {
            ShieldMark(form: shieldForm, tint: shieldTint)
                .frame(width: 88, height: 88)
                .padding(.bottom, 6)
            Text(stateTitle)
                .font(.title2.weight(.semibold))
                .accessibilityIdentifier("home.state")
            Text(stateDetail)
                .font(.subheadline)
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .fixedSize(horizontal: false, vertical: true)
            protectionButton
                .padding(.top, 10)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 8)
    }

    @ViewBuilder private var protectionButton: some View {
        let button = Button {
            // The first time, say what iPhone is about to ask before it asks.
            if !tunnel.isOn && tunnel.needsVPNPermission {
                showingVPNExplainer = true
                return
            }
            Task { await tunnel.setProtection(!tunnel.isOn) }
        } label: {
            Text(buttonTitle).font(.headline).frame(maxWidth: .infinity)
        }
        .controlSize(.large)
        .accessibilityIdentifier("home.protection")
        // Turning on is the action this screen invites; turning off is available, not advertised.
        if tunnel.isOn {
            button.buttonStyle(.bordered)
        } else {
            button.buttonStyle(.borderedProminent)
        }
    }

    private var isFailed: Bool {
        if case .failed = tunnel.state { return true }
        return false
    }

    private var shieldForm: ShieldForm {
        switch tunnel.state {
        case .on: return .filled
        case .connecting: return .dashed
        case .failed: return .cracked
        case .off, .noServer: return .hollow
        }
    }

    private var shieldTint: Color {
        switch tunnel.state {
        case .on, .connecting: return .accentColor
        case .failed: return .orange
        case .off, .noServer: return .secondary
        }
    }

    private var stateTitle: String {
        switch tunnel.state {
        case .on: return String(localized: "Protected", bundle: .bx)
        case .connecting: return String(localized: "Connecting…", bundle: .bx)
        case .failed: return String(localized: "Can't reach your server", bundle: .bx)
        case .off, .noServer: return String(localized: "Not protected", bundle: .bx)
        }
    }

    private var stateDetail: String {
        switch tunnel.state {
        case .on:
            guard let host = tunnel.serverHost else { return String(localized: "Traffic goes through your server.", bundle: .bx) }
            return String(localized: "Traffic goes through \(host).", bundle: .bx)
        case .connecting: return String(localized: "Nothing leaves this iPhone until the tunnel is up.", bundle: .bx)
        case let .failed(why): return why
        case .off, .noServer: return String(localized: "Apps connect directly, as if bx were not installed.", bundle: .bx)
        }
    }

    private var buttonTitle: String {
        switch tunnel.state {
        case .on: return String(localized: "Turn Off", bundle: .bx)
        case .connecting, .failed: return String(localized: "Turn Off", bundle: .bx)
        case .off, .noServer: return String(localized: "Turn On Protection", bundle: .bx)
        }
    }

    private var rulesTitle: String {
        switch tunnel.rules {
        case .synced: return String(localized: "From your Mac", bundle: .bx)
        case .defaults: return String(localized: "bx defaults", bundle: .bx)
        }
    }

    private var rulesNote: String? {
        switch tunnel.rules {
        case let .synced(_, updatedAt):
            let when = ISO8601DateFormatter().date(from: updatedAt).map {
                RelativeDateTimeFormatter().localizedString(for: $0, relativeTo: Date())
            } ?? ""
            return when.isEmpty ? "Synced through your server." : "Synced through your server, updated \(when)."
        case .defaults(.notCheckedYet):
            return String(localized: "China direct, everything else through the tunnel. Your Mac's rules are fetched once protection is on.", bundle: .bx)
        case .defaults(.notSyncedYet):
            return String(localized: "China direct, everything else through the tunnel. Your Mac has not synced its rules yet.", bundle: .bx)
        case .defaults(.serverCannotSync):
            return String(localized: "China direct, everything else through the tunnel. Your server cannot sync rules yet — on the server run: sudo bx server enable-sync", bundle: .bx)
        case .defaults(.differentLink):
            return String(localized: "China direct, everything else through the tunnel. The rules on your server were synced with a different link, so they were ignored.", bundle: .bx)
        }
    }
}

struct AddServerForm: View {
    @ObservedObject var tunnel: TunnelController
    let onDone: () -> Void
    @State private var link = ""
    @State private var problem: String?

    var body: some View {
        TextField("bx:// link", text: $link, axis: .vertical)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .font(.system(.body, design: .monospaced))
            .lineLimit(1...4)
            .accessibilityIdentifier("add.link")
        HStack {
            Button("Paste") { link = UIPasteboard.general.string ?? link }
            Spacer()
            Button("Add Server") { add() }
                .buttonStyle(.borderedProminent)
                .disabled(link.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                .accessibilityIdentifier("add.submit")
        }
        if let problem {
            Text(problem).foregroundStyle(.orange).font(.footnote)
                .accessibilityIdentifier("add.problem")
        }
    }

    private func add() {
        do {
            if tunnel.isOn { Task { await tunnel.setProtection(false) } }
            try tunnel.importLink(mainLinkFromPaste(link))
            problem = nil
            link = ""
            onDone()
        } catch {
            problem = error.localizedDescription
        }
    }
}

/// "Add this server?" for a link opened from outside the app. It names the address, says what it
/// replaces, and does nothing until Add is tapped.
struct IncomingLinkSheet: View {
    let pending: TunnelController.IncomingLink
    let current: String?
    let onAdd: () -> Void
    let onCancel: () -> Void

    var body: some View {
        NavigationStack {
            Form {
                if let host = pending.host {
                    Section {
                        LabeledContent("Address", value: host)
                            .accessibilityIdentifier("incoming.host")
                    } footer: {
                        if let current, current != host {
                            Text("This replaces your current server, \(current). Protection turns off while it changes.")
                                .accessibilityIdentifier("incoming.replaces")
                        } else {
                            Text("Only add servers you set up yourself or got from someone you trust — all your traffic will go through it.")
                        }
                    }
                } else if let problem = pending.problem {
                    Section { Text(problem).foregroundStyle(.orange) }
                }
            }
            .navigationTitle(pending.host == nil ? "Link Not Supported" : "Add This Server?")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel", action: onCancel).accessibilityIdentifier("incoming.cancel")
                }
                if pending.host != nil {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Add", action: onAdd).accessibilityIdentifier("incoming.add")
                    }
                }
            }
        }
        .presentationDetents([.medium])
    }
}
