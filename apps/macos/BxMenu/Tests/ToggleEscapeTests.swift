import Foundation

/// 关闭的逃生路径,以及「关不掉时 Quit 怎么办」。
///
/// 背景:菜单从 AppleScript `sudo bx down` 改走 Guardian socket 之后,把 CLI 那条
/// 后备整个丢了——`bx down` 的 macOSDownLifecycleDetailed 在 Guardian 不可达或
/// 拒绝关闭时会落到 forcedMacOSTeardown 强制拆除,而 socket 调用没有任何对应物。
/// Guardian 死掉、Core 还活着时菜单画成 .warning,那个状态的菜单恰恰提供 Turn Off
/// 与 Quit,两者都会死在 connect() 上;更糟的是 Quit 当时无论如何都 terminate,
/// 于是留下「保护还开着、菜单栏没有任何图标」。
@main
struct ToggleEscapeTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // 关闭失败必须有后备,且后备就是特权 CLI `bx down`(forcedMacOSTeardown
        // 的拥有者)。这一条是「停止不得依赖先成功做成别的事」的落地。
        expect(toggleEscape(action: .turnOff, socketSucceeded: false) == .privilegedCLIDown,
               "socket 关不掉时必须回落到特权 bx down")
        // 成功了就别弹密码框。
        expect(toggleEscape(action: .turnOff, socketSucceeded: true) == .none,
               "关成功了不该再走特权路径")
        // 打开没有「强制打开」这种东西,失败就是失败——不能拿授权框骚扰用户。
        expect(toggleEscape(action: .turnOn, socketSucceeded: false) == .none,
               "打开失败不该回落到特权路径")

        // 逃生命令必须真的是 `bx down`,且必须带管理员授权(bx down 要 root)。
        let script = privilegedTurnOffScript(bxPath: "/usr/local/bin/bx")
        expect(script.contains("with administrator privileges"),
               "逃生脚本必须请求管理员授权,实际 = \(script)")
        expect(script.contains("'/usr/local/bin/bx' down"),
               "逃生脚本必须执行 bx down,实际 = \(script)")

        // 两层引号转义的顺序不能搞反,否则是一个命令注入口子。
        let hostile = privilegedTurnOffScript(bxPath: "/tmp/a'b")
        expect(hostile == #"do shell script "'/tmp/a'\\''b' down" with administrator privileges"#,
               "路径里的单引号必须先按 shell 闭合转义、再按 AppleScript 转义反斜杠,实际 = \(hostile)")

        // 逃生成功也要说话:用户点的是 Turn Off,实际发生的是「Guardian 关不掉、
        // 改由 CLI 强制拆除」,静默成功等于隐瞒 Guardian 已经不听话。
        let escaped = toggleResultText(code: nil, transportDescription: "Guardian connection failed (61).",
                                       escape: .succeeded)
        expect(escaped?.contains("forced teardown") == true,
               "逃生成功必须说明是强制拆除完成的,实际 = \(String(describing: escaped))")

        // 逃生也失败:必须给出最后一条人工出路。
        let stuck = toggleResultText(code: "recovery_incomplete", transportDescription: nil, escape: .failed)
        expect(stuck?.contains("sudo bx down") == true,
               "两条路都失败时必须点名终端命令,实际 = \(String(describing: stuck))")

        // 没走逃生路径时文案与原来一致(打开失败、或关闭直接成功)。
        expect(toggleResultText(code: "guardian_busy", transportDescription: nil, escape: .notAttempted)
                == toggleFailureHint(code: "guardian_busy"),
               "没走逃生路径时不该改动原有文案")

        // 裁决:关不掉就**不退出**。终止会抹掉唯一的指示灯而保护仍在跑,
        // 正是 CLAUDE.md 反复拒绝交付的隐形保护状态(Quit Menu 就因此被删)。
        expect(quitTerminatesAfterTurnOff(turnedOff: true) == true,
               "关成功了当然要退出")
        expect(quitTerminatesAfterTurnOff(turnedOff: false) == false,
               "每条路都关不掉时必须留在菜单栏,不能退出成隐形保护")
        expect(quitBlockedByFailedTurnOffMessage().contains("sudo bx down"),
               "不退出时必须告诉用户唯一的人工出路")
        expect(!quitBlockedByFailedTurnOffMessage().isEmpty,
               "不退出必须有一句解释,不能什么都不说")

        // HTTP 200 不等于「保护关掉了」。
        //
        // Guardian 现在会在报 off 之前向系统求证还有没有 Core 在跑;求证不了就回 200
        // 但 protection_state != off、原因写在 last_error 里。把 200 本身当成关掉了,
        // 菜单就会在一个 Core 还占着 TUN 的时候退出 —— 正是
        // quitBlockedByFailedTurnOffMessage 存在要防的那个「保护在跑却没有指示灯」。
        expect(turnOffConfirmedProtectionStopped(protectionState: "off") == true,
               "Guardian 说 off 才算关掉了")
        expect(turnOffConfirmedProtectionStopped(protectionState: "needs_attention") == false,
               "没能确认时不许当成关掉了 —— 那正是要防的无指示灯状态")
        expect(turnOffConfirmedProtectionStopped(protectionState: "blocked") == false,
               "屏障还在也不是关掉了")
        // 空 / nil = 根本没读到状态,同样不是确认。观测不到 ≠ 观测到没有。
        expect(turnOffConfirmedProtectionStopped(protectionState: "") == false,
               "读不到状态时不许当成关掉了")
        expect(turnOffConfirmedProtectionStopped(protectionState: nil) == false,
               "没有状态时不许当成关掉了")

        if failures == 0 {
            print("ToggleEscapeTests passed")
        } else {
            exit(1)
        }

    }
}
