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
        expect(toggleProgressText(action: .turnOff, elapsedSeconds: 0) == "正在断开… 0 秒",
               "0 秒文案 = \(toggleProgressText(action: .turnOff, elapsedSeconds: 0))")
        expect(toggleProgressText(action: .turnOff, elapsedSeconds: 23) == "正在断开… 23 秒",
               "23 秒文案 = \(toggleProgressText(action: .turnOff, elapsedSeconds: 23))")
        expect(toggleProgressText(action: .turnOn, elapsedSeconds: 2) == "正在连接… 2 秒",
               "连接文案 = \(toggleProgressText(action: .turnOn, elapsedSeconds: 2))")

        // 逾时提示:阈值以下不出现,达到阈值才出现
        expect(toggleSlowHint(elapsedSeconds: 19) == nil, "19 秒不该有逾时提示")
        expect(toggleSlowHint(elapsedSeconds: 20) != nil, "20 秒必须有逾时提示")
        expect(toggleSlowHint(elapsedSeconds: 60)?.contains("通常") == true,
               "逾时提示要说明正常耗时")

        // 失败指引:必须与 Go 侧 guardianCodeHints 对齐。
        // core_ownership_uncertain 是锁存的,唯一出路是 down 再 up —— 不说这句用户无从下手。
        let uncertain = toggleFailureHint(code: "core_ownership_uncertain")
        expect(uncertain != nil, "core_ownership_uncertain 必须有指引")
        expect(uncertain?.contains("bx down") == true && uncertain?.contains("bx up") == true,
               "指引必须给出 down 再 up 这条唯一出路,实际 = \(String(describing: uncertain))")

        // 未知码与无码都不能编造指引,但也不能崩
        expect(toggleFailureHint(code: nil) == nil, "无码时不得编造指引")
        expect(toggleFailureHint(code: "some_future_code") == nil, "未知码不得编造指引")

        if failures == 0 {
            print("ToggleControllerTests passed")
        } else {
            exit(1)
        }
    }
}
