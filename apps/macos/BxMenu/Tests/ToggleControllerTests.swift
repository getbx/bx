import Foundation

@main
struct ToggleControllerTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // 进行中必须显示已用时。2026-08-04 事故里 bx down 卡了 71 分钟,
        // 界面全程没有一个字 —— 秒数是这一期最核心的产出。
        expect(toggleProgressText(action: .turnOff, elapsedSeconds: 0) == "Disconnecting… 0s",
               "0 秒文案 = \(toggleProgressText(action: .turnOff, elapsedSeconds: 0))")
        expect(toggleProgressText(action: .turnOff, elapsedSeconds: 23) == "Disconnecting… 23s",
               "23 秒文案 = \(toggleProgressText(action: .turnOff, elapsedSeconds: 23))")
        expect(toggleProgressText(action: .turnOn, elapsedSeconds: 2) == "Connecting… 2s",
               "连接文案 = \(toggleProgressText(action: .turnOn, elapsedSeconds: 2))")

        // 逾时提示:阈值以下不出现,达到阈值才出现
        expect(toggleSlowHint(elapsedSeconds: 19) == nil, "19 秒不该有逾时提示")
        expect(toggleSlowHint(elapsedSeconds: 20) != nil, "20 秒必须有逾时提示")
        // 意义不变:必须同时说「比预期久」与「正常 3 秒内完成」。分成两条断言
        // 是收紧而非放松——原来的单条只验了「正常耗时」那一半。
        let slow = toggleSlowHint(elapsedSeconds: 60)
        expect(slow?.contains("longer than usual") == true,
               "逾时提示必须说明这次比预期久,实际 = \(String(describing: slow))")
        expect(slow?.contains("3 seconds") == true,
               "逾时提示必须说明正常约 3 秒内完成,实际 = \(String(describing: slow))")

        // 失败指引:必须与 Go 侧 guardianCodeHints 对齐。
        //
        // core_ownership_uncertain 不再是「一条只能靠 down 清掉的锁存」:用户发起的
        // up/migrate 每次都会重新向系统求证(两次扫描都干净才释放),所以「再试一次」
        // 本身就常常够了;它仍然拒绝就意味着系统里真有一个 Core、或者根本扫不动,
        // 那时该看的是 Guardian 日志。指引给的是下一步,**不是承诺** —— 一台真有
        // Core 在跑的机器上,重试与 down+up 都该继续被拒。
        let uncertain = toggleFailureHint(code: "core_ownership_uncertain")
        expect(uncertain != nil, "core_ownership_uncertain 必须有指引")
        expect(uncertain?.contains("every attempt") == true,
               "指引必须说出新行为:每次 up 都会重新求证,实际 = \(String(describing: uncertain))")
        expect(uncertain?.contains("bx-guard.err.log") == true,
               "指引必须把人送到唯一写着完整原因的地方:Guardian 日志,实际 = \(String(describing: uncertain))")
        expect(uncertain?.contains("latched") == false,
               "指引还在把它描述成一条靠 down 清掉的锁存,实际 = \(String(describing: uncertain))")

        // 未知码、无码、空码都不能编造指引,但也不能崩
        expect(toggleFailureHint(code: nil) == nil, "无码时不得编造指引")
        expect(toggleFailureHint(code: "") == nil, "空码不得编造指引")
        expect(toggleFailureHint(code: "some_future_code") == nil, "未知码不得编造指引")

        // recovery_incomplete:Manager.Up/Down/Migrate 在 recoveryBlocked 时用同一个
        // errRecoveryIncomplete 短路,菜单的 /v1/down 直接撞这堵墙 —— 指引不能建议
        // "再点一次开关"(死循环),必须点名终端命令 sudo bx down,因为只有 CLI 的
        // 清理路径撞见这个错误时会自动强制拆除脱困(菜单直连 API 没有这条后备)。
        let recoveryHint = toggleFailureHint(code: "recovery_incomplete")
        expect(recoveryHint != nil, "recovery_incomplete 必须有指引")
        expect(recoveryHint?.contains("bx down") == true,
               "指引必须点名终端命令 sudo bx down,实际 = \(String(describing: recoveryHint))")

        // guardian_busy:acquireMutation 只是排队等 1 容量 mutation channel 腾出来,
        // 持锁方 defer 释放,是瞬时状态而非锁存 —— "稍候重试" 必须出现。
        let busyHint = toggleFailureHint(code: "guardian_busy")
        expect(busyHint != nil, "guardian_busy 必须有指引")
        expect(busyHint?.contains("retry") == true,
               "指引必须说明可以重试,实际 = \(String(describing: busyHint))")

        if failures == 0 {
            print("ToggleControllerTests passed")
        } else {
            exit(1)
        }
    }
}
