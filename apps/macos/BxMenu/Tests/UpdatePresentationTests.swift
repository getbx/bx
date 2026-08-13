import Foundation

@main
struct UpdatePresentationTests {
    static func main() {
        let available = UpdateCheck(current: "v0.1.0", latest: "v0.2.0", available: true, verified: true)
        expect(updateActionTitle(for: available) == "Update bx…", "verified update is actionable")

        let unverified = UpdateCheck(current: "v0.1.0", latest: "v0.2.0", available: true, verified: false)
        expect(updateActionTitle(for: unverified) == nil, "unverified update is hidden")

        let current = UpdateCheck(current: "v0.2.0", latest: "v0.2.0", available: false, verified: true)

        // 查不动(nil)不得抹掉上一次的已知答案:一次抖动就让 Update 入口消失、
        // 而下一次检查要等 24 小时,用户只会看到一个安静地少了一项的菜单。
        expect(mergedUpdateCheck(previous: available, fetched: nil) == available,
               "失败必须保留上一次的答案")
        // 拿到新答案就以新的为准 —— 装完之后那次刷新正是靠这条把入口收掉。
        expect(mergedUpdateCheck(previous: available, fetched: current) == current,
               "拿到新答案时必须覆盖旧的")
        expect(mergedUpdateCheck(previous: nil, fetched: nil) == nil,
               "从没成功过就还是不知道")
        expect(updateActionTitle(for: current) == nil, "current release has no action")

        expect(quitBxActionTitle == "Quit bx…", "protection shutdown action is explicit")

        let committedLine = #"{"from_version":"1.0.0","to_version":"1.1.0","phase":"committed","core_activated":true,"rolled_back":false,"protection_state":"protected"}"#
        expect(parseUpdateOutcome(Data(committedLine.utf8)) == .succeeded(to: "1.1.0"),
               "committed non-rolled-back line yields succeeded")

        let rolledBackLine = #"{"from_version":"1.0.0","to_version":"1.1.0","phase":"rolling_back","core_activated":false,"rolled_back":true,"protection_state":"protected"}"#
        expect(parseUpdateOutcome(Data(rolledBackLine.utf8)) == .rolledBack(from: "1.0.0"),
               "rolled_back line yields rolledBack")

        let mixedLog = """
        preparing update
        downloading artifact
        {"from_version":"1.0.0","to_version":"1.0.0","phase":"prepared","core_activated":false,"rolled_back":false,"protection_state":"protected"}
        activating core
        {"from_version":"1.0.0","to_version":"1.1.0","phase":"committed","core_activated":true,"rolled_back":false,"protection_state":"protected"}
        done
        """
        expect(parseUpdateOutcome(Data(mixedLog.utf8)) == .succeeded(to: "1.1.0"),
               "mixed log picks the last decodable JSON line")

        let garbage = "not json at all\nstill not json"
        expect(parseUpdateOutcome(Data(garbage.utf8)) == .failed, "garbage log yields failed")

        expect(updateConfirmTitle == "Update bx?", "update confirm title pinned")
        expect(updateConfirmMessage == "Internet access may pause briefly. bx will reconnect automatically.",
               "update confirm message pinned")
        expect(updateSucceededMessage == "bx is up to date", "update succeeded message pinned")
        expect(updateRolledBackMessage == "Update couldn't be completed. Previous version restored.",
               "update rolled back message pinned")
        testVersionRowCarriesTheUpdateInItsText()
    }

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }

    static func testVersionRowCarriesTheUpdateInItsText() {
        // 没有新版:一行普通的版本号。
        expect(versionRowTitle(current: "dev-abc", check: nil) == "Version: dev-abc",
               "无更新时应是纯版本号,实际 \(versionRowTitle(current: "dev-abc", check: nil))")
        expect(!versionRowOffersUpdate(check: nil), "无更新时这一行不该可点")

        // **有新版时,文字自己要说清楚** —— 只靠颜色的话,鼠标划过反色时提示就没了。
        let avail = UpdateCheck(current: "dev-abc", latest: "dev-xyz", available: true, verified: true)
        let title = versionRowTitle(current: "dev-abc", check: avail)
        expect(title.contains("dev-xyz"), "有新版时必须报出新版本号,实际 \(title)")
        expect(title.lowercased().contains("available"), "有新版时文字必须自己说明,实际 \(title)")
        expect(versionRowOffersUpdate(check: avail), "有新版时这一行应可点")

        // **未经签名校验的答案不许变成入口** —— 与 updateActionTitle 同一条判据。
        let unverified = UpdateCheck(current: "dev-abc", latest: "dev-xyz", available: true, verified: false)
        expect(versionRowTitle(current: "dev-abc", check: unverified) == "Version: dev-abc",
               "未校验的更新不该出现在版本行里")
        expect(!versionRowOffersUpdate(check: unverified), "未校验的更新不该可点")

        // available=true 但 latest 与当前相同:那不是新版,别喊。
        let same = UpdateCheck(current: "dev-abc", latest: "dev-abc", available: true, verified: true)
        expect(versionRowTitle(current: "dev-abc", check: same) == "Version: dev-abc",
               "latest 与当前相同时不该显示为有新版")
    }
}
