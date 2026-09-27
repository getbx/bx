import Foundation

// 菜单的界面语言。
//
// **英文是基准,不是其中一种翻译。** 源码里每一句用户看得见的话都写英文原句,
// 包在 `L(…)` 里;原句本身就是查表的 key(与 `NSLocalizedString` 的惯例相同)。
// 于是:① 读源码的人看到的就是英文界面上的那句话;② 某一句漏翻时退回英文,
// 界面仍然说得通,而不是露出一个 key;③ 按英文逐字断言的既有测试在英文下照旧成立。
//
// **刻意不用 String Catalog / `.lproj`**:测试与快照是 `swiftc` 逐文件直编的,
// 没有资源包;而切语言要立刻生效,`AppleLanguages` 那条路要重启 App。
// 词表是普通 Swift 字典(`Localization_zhHans.swift`),哪里编得进源码哪里就有它。

/// 用户在 Language 子菜单里选的那一项。`system` 跟着 macOS 的首选语言走。
enum MenuLanguageChoice: String, CaseIterable {
    case system
    case english = "en"
    case simplifiedChinese = "zh-Hans"

    /// 子菜单里的标题。**每种语言用它自己的文字写**:选错了、界面看不懂的人
    /// 也认得出回去的那一项。`system` 那一项跟着当前界面语言翻译。
    var menuTitle: String {
        switch self {
        case .system: return L("Follow System")
        case .english: return "English"
        case .simplifiedChinese: return simplifiedChineseEndonym
        }
    }
}

/// 实际用来显示的语言。只有两种:词表里没有的语言一律回到英文。
enum MenuLanguage: Equatable {
    case english
    case simplifiedChinese
}

/// 存用户选择的 UserDefaults 键。只存在菜单 App 自己的域里,不碰系统设置。
let menuLanguageDefaultsKey = "BxMenuLanguage"

/// 测试与快照用的覆盖:设了就压过一切(包括用户的选择与系统语言)。
///
/// **测试必须钉住语言**:否则同一套按英文断言的测试,在一台系统语言是中文的
/// 机器上会整片变红 —— 而那正是这个项目所有者的机器。
let menuLanguageEnvironmentKey = "BX_MENU_LANGUAGE"

/// 把「选择」与「系统首选语言」折成实际显示的语言。纯函数,单独可测。
///
/// 系统那一支只认**简体**:`zh-Hans`、`zh-CN`、`zh-SG` 与不带区域的 `zh`。
/// 繁体(`zh-Hant`、`zh-TW`、`zh-HK`、`zh-MO`)没有词表,回到英文 —— 给一个
/// 繁体用户硬塞简体,是替他做了一个他没做过的选择。
func resolveMenuLanguage(choice: MenuLanguageChoice, preferred: [String]) -> MenuLanguage {
    switch choice {
    case .english: return .english
    case .simplifiedChinese: return .simplifiedChinese
    case .system:
        guard let first = preferred.first?.lowercased() else { return .english }
        let parts = first.split(whereSeparator: { $0 == "-" || $0 == "_" }).map(String.init)
        guard parts.first == "zh" else { return .english }
        let rest = Set(parts.dropFirst())
        if rest.contains("hant") || rest.contains("tw") || rest.contains("hk") || rest.contains("mo") {
            return .english
        }
        return .simplifiedChinese
    }
}

/// 当前的选择。读不到、认不出都是 `system`。
func currentMenuLanguageChoice() -> MenuLanguageChoice {
    if let forced = ProcessInfo.processInfo.environment[menuLanguageEnvironmentKey],
       let choice = MenuLanguageChoice(rawValue: forced) {
        return choice
    }
    let stored = UserDefaults.standard.string(forKey: menuLanguageDefaultsKey) ?? ""
    return MenuLanguageChoice(rawValue: stored) ?? .system
}

/// 记下用户的选择,并让下一次 `L` 立刻按新语言查表。
func setMenuLanguageChoice(_ choice: MenuLanguageChoice) {
    UserDefaults.standard.set(choice.rawValue, forKey: menuLanguageDefaultsKey)
    cachedMenuLanguage = nil
}

/// 每次 `L` 都去读 UserDefaults 与 `Locale` 太贵(菜单每 2 秒重画一次,上百句话);
/// 缓存到下一次 `setMenuLanguageChoice`。
private var cachedMenuLanguage: MenuLanguage?

var currentMenuLanguage: MenuLanguage {
    if let cached = cachedMenuLanguage { return cached }
    let resolved = resolveMenuLanguage(choice: currentMenuLanguageChoice(),
                                       preferred: Locale.preferredLanguages)
    cachedMenuLanguage = resolved
    return resolved
}

/// 按当前语言取一句话。`english` 既是英文界面上的原句,也是查表的 key。
///
/// 需要填值的句子用位置占位符 `{0}`、`{1}`……而不是字符串插值:插值出来的
/// 句子每次都不一样,查不了表;也不用 `%@`,那要求实参是 `CVarArg`,传错类型
/// 在运行时才崩。占位符按位置编号,译文可以调换语序(中文常常要)。
func L(_ english: String, _ args: Any...) -> String {
    localized(english, args: args, language: currentMenuLanguage)
}

/// `L` 的纯函数内核:语言显式传入,测试不必改全局状态。
func localized(_ english: String, args: [Any], language: MenuLanguage) -> String {
    let template: String
    switch language {
    case .english: template = english
    case .simplifiedChinese: template = zhHansTranslations[english] ?? english
    }
    return fillPlaceholders(template, args)
}

/// 一遍扫完,不逐个 `replacingOccurrences`:填进去的值本身可能长得像 `{1}`
/// (一条规则、一个主机名都是用户写的),逐个替换会把它再替换一次。
func fillPlaceholders(_ template: String, _ args: [Any]) -> String {
    guard !args.isEmpty else { return template }
    var out = ""
    var rest = Substring(template)
    while let open = rest.firstIndex(of: "{") {
        out += rest[..<open]
        let afterOpen = rest.index(after: open)
        if let close = rest[afterOpen...].firstIndex(of: "}"),
           let n = Int(rest[afterOpen..<close]), n >= 0, n < args.count {
            out += String(describing: args[n])
            rest = rest[rest.index(after: close)...]
        } else {
            out += "{"
            rest = rest[afterOpen...]
        }
    }
    return out + rest
}
