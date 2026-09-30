import SwiftUI

struct HomeView: View {
    @ObservedObject var tunnel: TunnelController
    @State private var showingAdd = false

    var body: some View {
        NavigationStack {
            Form {
                if tunnel.state == .noServer {
                    Section {
                        VStack(spacing: 12) {
                            Image("BrandMark")
                                .resizable()
                                .scaledToFit()
                                .frame(height: 64)
                                .accessibilityHidden(true)
                            Text("Add your server")
                                .font(.title2.weight(.semibold))
                            Text("bx sends this iPhone's traffic through your own server, and China sites direct.")
                                .font(.subheadline)
                                .foregroundStyle(.secondary)
                                .multilineTextAlignment(.center)
                        }
                        .frame(maxWidth: .infinity)
                        .padding(.vertical, 12)
                    }
                    .listRowBackground(Color.clear)
                    Section {
                        AddServerForm(tunnel: tunnel, onDone: {})
                    } footer: {
                        Text("Paste the bx:// link your server gave you. It is stored in this iPhone's Keychain and never leaves the device.")
                    }
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
                    if let message = tunnel.lastError, !isFailed {
                        Section {
                            Text(message).foregroundStyle(.orange)
                        }
                    }
                }
            }
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .principal) { BrandTitle() } }
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
            Task { await tunnel.setProtection(!tunnel.isOn) }
        } label: {
            Text(buttonTitle).font(.headline).frame(maxWidth: .infinity)
        }
        .controlSize(.large)
        .disabled(tunnel.state == .connecting)
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
        case .on: return "Protected"
        case .connecting: return "Connecting…"
        case .failed: return "Couldn't turn on"
        case .off, .noServer: return "Not protected"
        }
    }

    private var stateDetail: String {
        switch tunnel.state {
        case .on: return "Traffic goes through \(tunnel.serverHost ?? "your server")."
        case .connecting: return "Nothing leaves this iPhone until the tunnel is up."
        case let .failed(why): return why
        case .off, .noServer: return "Apps connect directly, as if bx were not installed."
        }
    }

    private var buttonTitle: String {
        switch tunnel.state {
        case .on: return "Turn Off"
        case .connecting: return "Connecting…"
        case .failed: return "Try Again"
        case .off, .noServer: return "Turn On Protection"
        }
    }

    private var rulesTitle: String {
        switch tunnel.rules {
        case .synced: return "From your Mac"
        case .defaults: return "bx defaults"
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
            return "China direct, everything else through the tunnel. Your Mac's rules are fetched once protection is on."
        case .defaults(.notSyncedYet):
            return "China direct, everything else through the tunnel. Your Mac has not synced its rules yet."
        case .defaults(.serverCannotSync):
            return "China direct, everything else through the tunnel. Your server cannot sync rules yet — on the server run: sudo bx server enable-sync"
        case .defaults(.differentLink):
            return "China direct, everything else through the tunnel. The rules on your server were synced with a different link, so they were ignored."
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
            try tunnel.importLink(link)
            problem = nil
            link = ""
            onDone()
        } catch {
            problem = error.localizedDescription
        }
    }
}
