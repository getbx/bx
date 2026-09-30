import SwiftUI

struct HomeView: View {
    @ObservedObject var tunnel: TunnelController
    @State private var showingAdd = false

    var body: some View {
        NavigationStack {
            Form {
                if tunnel.state == .noServer {
                    Section {
                        AddServerForm(tunnel: tunnel, onDone: {})
                    } header: {
                        Text("Add your server")
                    } footer: {
                        Text("Paste the bx:// link your server gave you. It is stored in this iPhone's Keychain and never leaves the device.")
                    }
                } else {
                    Section {
                        Toggle(isOn: Binding(
                            get: { tunnel.isOn },
                            set: { on in Task { await tunnel.setProtection(on) } }
                        )) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text("Protection").font(.headline)
                                Text(statusText).font(.subheadline).foregroundStyle(.secondary)
                            }
                        }
                        .accessibilityIdentifier("home.protection")
                        .disabled(tunnel.state == .connecting)
                    } footer: {
                        Text("While protection is on, nothing leaves this iPhone outside the tunnel — even while it reconnects.")
                    }
                    Section("Server") {
                        LabeledContent("Address", value: tunnel.serverHost ?? "")
                            .accessibilityIdentifier("home.server")
                        Button("Change Server…") { showingAdd = true }
                    }
                    if let message = tunnel.lastError {
                        Section {
                            Text(message).foregroundStyle(.orange)
                        }
                    }
                }
            }
            .navigationTitle("bx")
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
                    .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { showingAdd = false } } }
                }
            }
        }
    }

    private var statusText: String {
        switch tunnel.state {
        case .noServer: return "No server"
        case .off: return "Off"
        case .connecting: return "Connecting…"
        case .on: return "On — traffic goes through your server"
        case let .failed(why): return why
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
