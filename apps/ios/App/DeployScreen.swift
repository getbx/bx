import SwiftUI

/// "Set Up My Server": the details the provider gave you → bx installs (or reuses) the server and
/// keeps its link on this iPhone. Progress and the outcome are on this screen; no terminal.
struct DeployScreen: View {
    @ObservedObject var tunnel: TunnelController
    let runner: PhoneDeployRunner
    let onDone: () -> Void

    @State private var address = ""
    @State private var port = "22"
    @State private var user = "root"
    @State private var password = ""
    @State private var running = false
    @State private var states: [String: String] = [:]   // step id → running / done / failed
    @State private var failure: PhoneDeployFailure?
    @State private var detail: String?
    @State private var finished: (host: String, reused: Bool)?
    @State private var showConsoleHelp = false

    var body: some View {
        Form {
            if finished == nil {
                Section {
                    LabeledContent("Address") {
                        TextField("203.0.113.9", text: $address).multilineTextAlignment(.trailing)
                            .textInputAutocapitalization(.never).autocorrectionDisabled().keyboardType(.URL)
                            .accessibilityIdentifier("deploy.address")
                    }
                    LabeledContent("SSH port") {
                        TextField("22", text: $port).multilineTextAlignment(.trailing).keyboardType(.numberPad)
                    }
                    LabeledContent("Login") {
                        TextField("root", text: $user).multilineTextAlignment(.trailing)
                            .textInputAutocapitalization(.never).autocorrectionDisabled()
                    }
                    LabeledContent("Password") {
                        SecureField("Required", text: $password).multilineTextAlignment(.trailing)
                            .accessibilityIdentifier("deploy.password")
                    }
                } header: {
                    Text("Your server")
                } footer: {
                    Text("The details your provider gave you when you bought the server. The password is used once for this setup and is not saved.")
                }
                .disabled(running)
            }
            if running || !states.isEmpty {
                Section {
                    ForEach(phoneDeploySteps, id: \.self) { id in
                        HStack(spacing: 10) {
                            stepMark(states[id])
                            Text(phoneDeployStepTitle(id))
                                .foregroundStyle(states[id] == nil ? .secondary : .primary)
                        }
                    }
                }
            }
            if let finished {
                Section {
                    Text(finished.reused
                         ? "This server already ran bx, so its keys were kept — links you shared from it keep working. It is saved on this iPhone."
                         : "bx is installed on \(finished.host) and saved on this iPhone. Turn on protection when you are ready.")
                        .accessibilityIdentifier("deploy.result")
                } header: { Text("Ready") }
            }
            if let failure {
                Section {
                    Text(failure.advice)
                    if let detail, !detail.isEmpty {
                        Text(detail).font(.footnote.monospaced()).foregroundStyle(.secondary).textSelection(.enabled)
                    }
                } header: {
                    Text(failure.headline).accessibilityIdentifier("deploy.failure")
                }
            }
            Section {
                if finished != nil {
                    Button("Done", action: onDone).accessibilityIdentifier("deploy.done")
                } else if let retry = failure?.retry {
                    Button(retry == .reinstall ? "Reinstall (new keys)" : "I Reinstalled It") {
                        start(reinstall: retry == .reinstall, forgetHostKey: retry == .forgetHostKey)
                    }
                } else {
                    Button(running ? "Setting Up…" : (failure == nil ? "Set Up Server" : "Try Again")) { start() }
                        .disabled(running || address.trimmingCharacters(in: .whitespaces).isEmpty || password.isEmpty)
                        .accessibilityIdentifier("deploy.start")
                }
            }
            if finished == nil {
                Section {
                    DisclosureGroup("No password, or it will not connect?", isExpanded: $showConsoleHelp) {
                        Text("Open your provider's web console for the server, paste this command and run it. When it finishes, copy the line it prints that starts with sudo bx setup, and paste it into bx on this iPhone.")
                            .font(.footnote)
                        Text(webConsoleInstallCommand).font(.footnote.monospaced()).textSelection(.enabled)
                        Button("Copy Command") { UIPasteboard.general.string = webConsoleInstallCommand }
                    }
                }
            }
        }
        .navigationTitle("Set Up My Server")
        .navigationBarTitleDisplayMode(.inline)
    }

    @ViewBuilder private func stepMark(_ state: String?) -> some View {
        switch state {
        case "done": Image(systemName: "checkmark.circle.fill").foregroundStyle(.green)
        case "failed": Image(systemName: "xmark.circle.fill").foregroundStyle(.red)
        case "running": ProgressView().controlSize(.small)
        default: Image(systemName: "circle").foregroundStyle(.tertiary)
        }
    }

    private func start(reinstall: Bool = false, forgetHostKey: Bool = false) {
        let pw = password
        password = "" // cleared the moment it is handed over; a retry asks again
        failure = nil
        detail = nil
        states = [:]
        running = true
        runner.run(address: address.trimmingCharacters(in: .whitespaces), sshPort: Int(port) ?? 22,
                   user: user.trimmingCharacters(in: .whitespaces), password: pw,
                   reinstall: reinstall, forgetHostKey: forgetHostKey) { event in
            handle(event)
        }
    }

    private func handle(_ e: DeployEvent) {
        switch e.event {
        case "step":
            for (id, s) in states where s == "running" { states[id] = "done" }
            if let id = e.step { states[id] = "running" }
        case "done":
            for (id, s) in states where s == "running" { states[id] = "done" }
            running = false
            guard let link = e.link else { return }
            do {
                try tunnel.importLink(link)
                finished = (e.host ?? tunnel.serverHost ?? "", e.reused ?? false)
            } catch {
                failure = PhoneDeployFailure(headline: "The server is ready, but this iPhone cannot use it",
                                             advice: error.localizedDescription)
            }
        case "error":
            for (id, s) in states where s == "running" { states[id] = "failed" }
            running = false
            failure = phoneDeployFailure(e.code ?? "")
            detail = e.detail
        default:
            break
        }
    }
}
