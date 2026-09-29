import SwiftUI

struct ExplainView: View {
    let inputs: ExplainInputs?
    let loadError: String?
    @State var target: String
    @State private var answer: ExplainAnswer?
    @State private var error: String?
    var autoRun = false

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField("Domain, IP or URL", text: $target)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .keyboardType(.URL)
                        .submitLabel(.go)
                        .onSubmit(run)
                    Button("Explain", action: run)
                        .disabled(inputs == nil || target.trimmingCharacters(in: .whitespaces).isEmpty)
                } header: {
                    Text("Where does it go?")
                } footer: {
                    Text("The same answer bx explain gives on your Mac, from the same rules.")
                }
                if let answer {
                    Section {
                        LabeledContent("Goes", value: answer.verdictTitle)
                            .font(.headline)
                        LabeledContent("Because", value: answer.because)
                        if let rule = answer.rule, !rule.isEmpty {
                            LabeledContent("Rule", value: rule)
                        }
                    } header: {
                        Text(answer.target)
                    }
                }
                if let message = error ?? loadError {
                    Section {
                        Text(message).foregroundStyle(.orange)
                    }
                }
            }
            .navigationTitle("bx")
        }
        .onAppear { if autoRun { run() } }
    }

    private func run() {
        guard let inputs else { return }
        do {
            answer = try inputs.explain(target)
            error = nil
        } catch {
            answer = nil
            self.error = error.localizedDescription
        }
    }
}
