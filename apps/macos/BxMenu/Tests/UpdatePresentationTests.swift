import Foundation

@main
struct UpdatePresentationTests {
    /// **失败时必须说出真正的原因 —— 它就在菜单自己刚读过的那份日志里。**
    ///
    /// 真机(2026-08-14):用户点 Update,转圈两分钟,然后只看到
    /// "bx could not complete the update. Run Doctor for details." ——
    /// 而日志里写着 `download stalled: no data received…`。
    static func testUpdateFailureDetailSurfacesTheRealReason() {
        let log = Data("""
        ⏳ 下载完整 macOS 包 bx-macos-arm64.tar.gz…
        2026/08/14 06:30:31 download stalled: no data received for a while — check the network and try again
        """.utf8)
        guard let detail = updateFailureDetail(log) else {
            expect(false, "有原因却没取出来")
            return
        }
        expect(detail.contains("stalled"), "取到的不是失败原因:\(detail)")
        // Go 的 log 时间戳对用户没有意义。
        expect(!detail.contains("2026/08/14"), "时间戳没剥掉:\(detail)")
        // 进度行不是原因。
        expect(!detail.contains("⏳"), "把进度行当成了原因:\(detail)")
    }

    /// 取不到就返回 nil —— **编一句比不说更糟**。
    static func testUpdateFailureDetailStaysSilentWhenThereIsNothingToSay() {
        expect(updateFailureDetail(Data()) == nil, "空日志编出了原因")
        expect(updateFailureDetail(Data("⏳ 下载中…\n\n".utf8)) == nil, "只有进度行时编出了原因")
        expect(updateFailureDetail(Data(#"{"phase":"done"}"#.utf8)) == nil, "把 JSON 当成了给人读的原因")
    }

    static func main() {
        testUpdateFailureDetailSurfacesTheRealReason()
        testUpdateFailureDetailStaysSilentWhenThereIsNothingToSay()
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
        expect(parseUpdateOutcome(Data(rolledBackLine.utf8)) == .rolledBack(from: "1.0.0", reason: nil),
               "rolled_back line yields rolledBack")

        // A3:回滚带着 Core 自报的原因时,那句话要说出它意味着什么。
        let withReason = #"{"from_version":"1.0.0","to_version":"1.1.0","phase":"rolled_back","core_activated":false,"rolled_back":true,"protection_state":"protected","core_start_failure":"tunnel_unreachable"}"#
        expect(parseUpdateOutcome(Data(withReason.utf8)) == .rolledBack(from: "1.0.0", reason: "tunnel_unreachable"),
               "core_start_failure 没有被解出来")
        let tunnel = updateRolledBackMessage(reason: "tunnel_handshake_failed")
        let other = updateRolledBackMessage(reason: "tun_open_failed")
        expect(tunnel.contains("not the update"), "隧道那一族没说「不是升级的问题」:\(tunnel)")
        expect(!other.contains("not the update"), "非隧道原因被说成「不是升级的问题」:\(other)")
        expect(tunnel != other && other != updateRolledBackMessage, "三种说法没分开")
        expect(updateRolledBackMessage(reason: nil) == updateRolledBackMessage, "没说原因时不许编")

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
        testDownloadProgressIsReadFromTheLastLine()
        testUpdateStageNeverGuessesWhichHalfItIsIn()
    }

    /// 进度是一行行追加的,第一行永远是 0% —— 取最后一行。
    static func testDownloadProgressIsReadFromTheLastLine() {
        let log = """
        ⏳ Downloading the full macOS package bx-macos-arm64.tar.gz…
        \(downloadProgressMarker)1.0 MB of 38.9 MB (3%)
        \(downloadProgressMarker)12.0 MB of 38.9 MB (31%)
        """
        expect(lastDownloadProgressLine(log) == "12.0 MB of 38.9 MB (31%)", "取最后一行进度")
        expect(lastDownloadProgressLine("nothing here\nstill nothing") == nil, "没有进度行就说没有")
        expect(lastDownloadProgressLine(nil) == nil, "读不到日志就说没有")
    }

    /// 三段各说各的,而**第三段是承重的**:问不出来时不许挑一段说。
    static func testUpdateStageNeverGuessesWhichHalfItIsIn() {
        let downloading = updateStageText(installing: false, progress: "12.0 MB of 38.9 MB (31%)", elapsedSeconds: 199)
        let installing = updateStageText(installing: true, progress: "38.9 MB of 38.9 MB (100%)", elapsedSeconds: 210)
        let unknown = updateStageText(installing: nil, progress: nil, elapsedSeconds: 199)

        // 下载那段必须说得出字节 —— 秒数答不了「是不是卡住了」。
        expect(downloading.contains("12.0 MB of 38.9 MB"), "下载段报字节")
        expect(!downloading.contains("Installing"), "下载段不许说在装")
        // 换文件那段必须预告网络会停 —— 那是用户唯一需要改变行为的几秒。
        expect(installing.contains("Installing"), "安装段说在装")
        expect(installing.contains("network pauses"), "安装段预告网络会停")
        // 问不出来:退回原来那句合并文案,一段都不猜。
        expect(unknown.contains("Downloading and installing"), "问不出来时退回合并文案")
        expect(!unknown.contains("Installing…"), "问不出来时不许断言在装")
        // 三种在屏幕上必须两两不同 —— 判据打在用户看得见的东西上。
        expect(downloading != installing && installing != unknown && downloading != unknown,
               "三段必须长得不一样")
        // 没有进度行时仍然要说点什么(退回秒数),不能是空白。
        expect(!updateStageText(installing: false, progress: nil, elapsedSeconds: 5).isEmpty,
               "没有进度行也要有话说")
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
