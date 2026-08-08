import Foundation

@main
struct MenuCadenceTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        let open = menuPollInterval(menuOpen: true)
        let closed = menuPollInterval(menuOpen: false)

        expect(open < closed, "菜单打开时必须刷得更勤,实际 open=\(open) closed=\(closed)")
        expect(closed >= 20, "菜单关着时只有图标要更新,间隔应显著放宽,实际 \(closed) 秒")

        // status --json 在 macOS 上整轮观测封顶 5 秒;间隔不得低于它,
        // 否则上一次还没回来下一次就发起了,等于常驻满占空比。
        expect(open >= 2, "打开时的间隔不得低于 2 秒,实际 \(open) 秒")

        // 刷新闸门:2 秒的间隔比一次刷新(status --json 封顶 5 秒)还短,
        // 慢的那一次必须让后来者**丢弃**而不是排队 —— 排队只会堆出一串
        // 拿到时已经作废的刷新,每一次还各 spawn 两个子进程。
        var gate = RefreshGate()
        expect(gate.begin(), "首次刷新必须放行")
        expect(!gate.begin(userInitiated: true), "已有刷新在跑时必须挡掉,不得再发起")
        expect(!gate.begin(userInitiated: true), "连续挡掉的刷新是丢弃,不是排队")
        expect(gate.skipped == 2, "被丢弃的次数应为 2,实际 \(gate.skipped)")
        expect(gate.end(), "用户动作发起的刷新被挡掉过,结束时必须要求补跑一次")
        expect(gate.begin(), "上一次结束后必须能再刷新")
        expect(gate.skipped == 2, "放行的一次不该计入丢弃,实际 \(gate.skipped)")

        // 补跑是一次性的,不是队列:挡掉两次也只补一次。
        expect(!gate.end(), "没有新的刷新被挡掉时不得再要求补跑")

        // 定时器那一拍被丢掉**不补**:打开档 2 秒一拍、刷新可能 5 秒,每次结束时
        // 必然已积压两拍,补跑接补跑就是首尾相接的满占空比。
        var ticks = RefreshGate()
        expect(ticks.begin(), "首次放行")
        expect(!ticks.begin(), "定时器那一拍照样挡掉")
        expect(!ticks.begin(), "再挡一拍")
        expect(!ticks.end(), "只有定时器的拍被丢掉时不得补跑,否则占空比 100%")
        expect(ticks.skipped == 2, "丢弃计数照记,实际 \(ticks.skipped)")

        // 混合:同一段窗口里既有定时器的拍也有用户动作,用户那一次必须补上。
        var mixed = RefreshGate()
        expect(mixed.begin(), "首次放行")
        expect(!mixed.begin(), "定时器的拍")
        expect(!mixed.begin(userInitiated: true), "用户动作那一次")
        expect(mixed.end(), "窗口里只要有一次是用户动作发起的,就必须补跑")

        // end() 之后闸门必须真的开着 —— 忘了这一条会让菜单永久停更。
        var reopened = RefreshGate()
        _ = reopened.begin()
        expect(!reopened.end(), "没被挡过就不该补跑")
        expect(!reopened.inFlight, "end() 之后不得仍标记为进行中")

        // 代际号:采集期间有人动过恢复状态,那次采集的结果就不许写回快照那半边。
        // 放行陈旧写回 = 复活已经结束的恢复浮层 = 对一次成功的恢复报红。
        var generation = RecoveryGeneration()
        let captured = generation.value
        expect(generation.acceptsWriteBack(captured: captured), "没人动过就必须允许写回")
        generation.bump()
        expect(!generation.acceptsWriteBack(captured: captured),
               "采集期间恢复状态变过,陈旧结果必须丢弃")
        expect(generation.acceptsWriteBack(captured: generation.value),
               "变更之后新采的一次仍要能写回,否则快照永远更新不了")

        if failures == 0 { print("MenuCadenceTests passed") } else { exit(1) }
    }
}
