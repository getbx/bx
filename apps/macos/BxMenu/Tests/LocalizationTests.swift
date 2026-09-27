import Foundation

@main
struct LocalizationTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    // 「跟随系统」只认简体:繁体没有词表,硬塞简体是替用户做了他没做过的选择。
    static func testSystemChoiceFollowsOnlySimplifiedChinese() {
        let cases: [([String], MenuLanguage)] = [
            (["zh-Hans-CN", "en-US"], .simplifiedChinese),
            (["zh-Hans"], .simplifiedChinese),
            (["zh-CN"], .simplifiedChinese),
            (["zh_SG"], .simplifiedChinese),
            (["zh"], .simplifiedChinese),
            (["zh-Hant-TW"], .english),
            (["zh-TW"], .english),
            (["zh-HK"], .english),
            (["en-US", "zh-Hans"], .english),
            (["ja-JP"], .english),
            ([], .english),
        ]
        for (preferred, want) in cases {
            let got = resolveMenuLanguage(choice: .system, preferred: preferred)
            expect(got == want, "\(preferred) 应解析成 \(want),得到 \(got)")
        }
    }

    // 显式选择压过系统语言,两个方向都要。
    static func testExplicitChoiceWinsOverTheSystem() {
        expect(resolveMenuLanguage(choice: .english, preferred: ["zh-Hans"]) == .english,
               "选了 English 却跟着系统显示中文")
        expect(resolveMenuLanguage(choice: .simplifiedChinese, preferred: ["en-US"]) == .simplifiedChinese,
               "选了简体中文却跟着系统显示英文")
    }

    // 存下来的值与枚举的 rawValue 是同一份:改了 rawValue,用户的选择会在升级后悄悄丢掉。
    static func testStoredValuesAreStable() {
        expect(MenuLanguageChoice(rawValue: "system") == .system, "system 的存储值变了")
        expect(MenuLanguageChoice(rawValue: "en") == .english, "en 的存储值变了")
        expect(MenuLanguageChoice(rawValue: "zh-Hans") == .simplifiedChinese, "zh-Hans 的存储值变了")
        expect(MenuLanguageChoice(rawValue: "klingon") == nil, "认不出的值应当是 nil(回落到 system)")
    }

    // 每种语言用它自己的文字写:选错了、看不懂界面的人也认得出回去的那一项。
    static func testLanguageNamesAreWrittenInTheirOwnScript() {
        expect(MenuLanguageChoice.english.menuTitle == "English", "English 那一项被翻译了")
        expect(MenuLanguageChoice.simplifiedChinese.menuTitle == "简体中文", "简体中文那一项被翻译了")
    }

    // 占位符按位置填,译文可以调换语序;没有译文时退回英文原句,不露出 key 之外的东西。
    static func testPlaceholdersAndFallback() {
        expect(fillPlaceholders("{1} then {0}", ["a", "b"]) == "b then a", "占位符没按位置填")
        expect(fillPlaceholders("{0} ms", [293]) == "293 ms", "数字实参没填进去")
        expect(fillPlaceholders("rule {0} on {1}", ["{1}", "h"]) == "rule {1} on h",
               "填进去的值被当成占位符又替换了一次")
        expect(fillPlaceholders("{ok} {0}", ["x"]) == "{ok} x", "不是占位符的花括号被吃掉了")
        expect(localized("Follow System", args: [], language: .simplifiedChinese) == "跟随系统",
               "有译文却没用上")
        expect(localized("Follow System", args: [], language: .english) == "Follow System",
               "英文界面被改动了")
        expect(localized("No such sentence {0}", args: ["x"], language: .simplifiedChinese) == "No such sentence x",
               "漏翻的句子应当退回英文原句(并照样填值)")
    }

    // 测试脚本把语言钉在英文;这一条确认钉住真的生效了,否则整套按英文写的断言
    // 在一台中文系统的机器上会整片变红。
    static func testTheSuiteRunsPinnedToEnglish() {
        expect(currentMenuLanguage == .english, "测试没有钉在英文(BX_MENU_LANGUAGE=en 没生效)")
        expect(L("Follow System") == "Follow System", "钉在英文时 L 仍返回了译文")
    }

    static func main() {
        testSystemChoiceFollowsOnlySimplifiedChinese()
        testExplicitChoiceWinsOverTheSystem()
        testStoredValuesAreStable()
        testLanguageNamesAreWrittenInTheirOwnScript()
        testPlaceholdersAndFallback()
        testTheSuiteRunsPinnedToEnglish()
        if failures == 0 {
            print("LocalizationTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
