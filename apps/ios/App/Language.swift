import Foundation
import ObjectiveC
import SwiftUI

// The app's language, chosen in the app — the same three choices as the Mac menu (Localization.swift
// there): Follow System, English, 简体中文. A switch takes effect at once, without restarting.
//
// How: SwiftUI's Text literals look up through Bundle.main.localizedString(forKey:value:table:), whose
// class is swapped once at launch for a subclass that answers from the chosen language's table.
// String(localized:) does not go through that override, so every call passes `bundle: .bx`
// (TestIOSLocalizedStringsUseTheChosenLanguage). The root view is rebuilt on a change.
// English is the source language (the keys are the English sentences), so English is "the key itself".

enum AppLanguageChoice: String, CaseIterable, Identifiable {
    case system
    case english = "en"
    case simplifiedChinese = "zh-Hans"

    var id: String { rawValue }

    /// Each language in its own words: someone stuck in a language they cannot read still recognises
    /// the way back. Only "Follow System" is translated.
    var title: String {
        switch self {
        case .system: return String(localized: "Follow System", bundle: .bx)
        case .english: return "English"
        case .simplifiedChinese: return "简体中文"
        }
    }
}

enum AppLanguage: Equatable {
    case english
    case simplifiedChinese

    /// The same rule as the Mac: only Simplified Chinese has a table; Traditional falls back to
    /// English rather than handing a Traditional reader Simplified they did not choose.
    static func resolve(_ choice: AppLanguageChoice, preferred: [String]) -> AppLanguage {
        switch choice {
        case .english: return .english
        case .simplifiedChinese: return .simplifiedChinese
        case .system:
            guard let first = preferred.first?.lowercased() else { return .english }
            let parts = first.split(whereSeparator: { $0 == "-" || $0 == "_" }).map(String.init)
            guard parts.first == "zh" else { return .english }
            let rest = Set(parts.dropFirst())
            if rest.contains("hant") || rest.contains("tw") || rest.contains("hk") || rest.contains("mo") { return .english }
            return .simplifiedChinese
        }
    }

    var locale: Locale { Locale(identifier: self == .english ? "en" : "zh-Hans") }
}

@MainActor
final class LanguageSettings: ObservableObject {
    static let defaultsKey = "BxLanguage"

    @Published private(set) var choice: AppLanguageChoice
    @Published private(set) var language: AppLanguage

    /// Fixture runs (UI tests, snapshots) neither read nor write the choice: a test that switched to
    /// Chinese must not leave every later test in Chinese.
    private let persist: Bool

    init(persist: Bool = true) {
        self.persist = persist
        LocalizedBundle.install()
        let saved = persist ? UserDefaults.standard.string(forKey: Self.defaultsKey) : nil
        let stored = AppLanguageChoice(rawValue: saved ?? "") ?? .system
        choice = stored
        language = AppLanguage.resolve(stored, preferred: Locale.preferredLanguages)
        LocalizedBundle.active = language
    }

    func choose(_ new: AppLanguageChoice) {
        defer {
            choice = new
            language = AppLanguage.resolve(new, preferred: Locale.preferredLanguages)
            LocalizedBundle.active = language
        }
        guard persist else { return }
        UserDefaults.standard.set(new.rawValue, forKey: Self.defaultsKey)
        // System-drawn controls (the Paste button, alerts' own buttons) follow the process language,
        // which iOS reads at launch: set it too, so they match from the next launch on.
        if new == .system {
            UserDefaults.standard.removeObject(forKey: "AppleLanguages")
        } else {
            UserDefaults.standard.set([new.rawValue], forKey: "AppleLanguages")
        }
    }
}

extension Bundle {
    /// The chosen language's table, for `String(localized:bundle:)` — that path does not go through
    /// Bundle.main's override (seen on the simulator: the Text literals switched, these did not).
    static var bx: Bundle { LocalizedBundle.tableBundle }
}

/// Bundle.main's class after launch: answers from the chosen language's table.
final class LocalizedBundle: Bundle, @unchecked Sendable {
    nonisolated(unsafe) static var active: AppLanguage = .english
    nonisolated(unsafe) private static var chinese: Bundle?

    nonisolated(unsafe) private static var english: Bundle?

    static var tableBundle: Bundle {
        switch active {
        case .simplifiedChinese: return chinese ?? .main
        case .english: return english ?? .main
        }
    }

    static func install() {
        guard !(Bundle.main is LocalizedBundle) else { return }
        chinese = Bundle.main.path(forResource: "zh-Hans", ofType: "lproj").flatMap(Bundle.init(path:))
        english = Bundle.main.path(forResource: "en", ofType: "lproj").flatMap(Bundle.init(path:))
        object_setClass(Bundle.main, LocalizedBundle.self)
    }

    override func localizedString(forKey key: String, value: String?, table tableName: String?) -> String {
        guard self === Bundle.main else { return super.localizedString(forKey: key, value: value, table: tableName) }
        switch Self.active {
        case .simplifiedChinese:
            if let chinese = Self.chinese { return chinese.localizedString(forKey: key, value: value, table: tableName) }
            return super.localizedString(forKey: key, value: value, table: tableName)
        case .english:
            if let value, !value.isEmpty { return value }
            return key
        }
    }
}

/// The globe in the top bar: the three choices, the current one ticked.
struct LanguageMenu: View {
    @EnvironmentObject private var settings: LanguageSettings

    var body: some View {
        Menu {
            Picker("Language", selection: Binding(get: { settings.choice }, set: { settings.choose($0) })) {
                ForEach(AppLanguageChoice.allCases) { Text($0.title).tag($0) }
            }
        } label: {
            Image(systemName: "globe")
        }
        .accessibilityLabel(Text("Language"))
        .accessibilityIdentifier("language.menu")
    }
}
