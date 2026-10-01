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
                        // Stable identifiers: VoiceOver and the UI tests (UITests/) address the rows
                        // by these, not by how SwiftUI happens to merge a label and its value.
                        HStack(spacing: 6) {
                            Text("Goes")
                            Spacer(minLength: 12)
                            Group {
                                Image(systemName: answer.verdictSymbol)
                                    .accessibilityHidden(true)
                                Text(answer.verdictTitle)
                            }
                            .foregroundStyle(answer.verdict == "tunnel" ? Color.accentColor : .primary)
                        }
                        .font(.headline)
                        .accessibilityElement(children: .combine)
                        .accessibilityIdentifier("explain.goes")
                        // The reason comes from Go's shared wording table; on a Chinese iPhone it is looked
                        // up in Localizable.strings (TestIOSEveryVisibleStringHasAChineseTranslation
                        // checks every phrase of that table has a translation).
                        LabeledContent("Because", value: Bundle.main.localizedString(forKey: answer.because, value: answer.because, table: nil))
                            .accessibilityIdentifier("explain.because")
                        if let rule = answer.rule, !rule.isEmpty {
                            LabeledContent("Rule", value: rule)
                                .accessibilityIdentifier("explain.rule")
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
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .principal) { BrandTitle() } }
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
