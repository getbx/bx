import Foundation

/// Quit 之前「有没有东西要关」。
///
/// 背景:阶段②把退出入口铺到了每一个状态(此前 .off/.setupNeeded/.missing/
/// .notInstalled/.updateNeeded 下菜单根本没有退出入口)。但 quitBx 走的是
/// quitDisposition → performToggle(.turnOff) → Guardian socket,而后三态的定义
/// 就是「Guardian 不在那儿」——socket 必然失败,逃生路径那次 sudo bx down 也
/// 必然失败,quitTerminatesAfterTurnOff 于是拒绝退出。**恰恰是在退出入口刚刚
/// 变得可见的那几个状态里,点它什么都不会发生。**
@main
struct QuitPlanTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // ① 没东西可关的三态:必须直接退出,不能先去敲一个不存在的 Guardian。
        for state in [MenuStateKind.notInstalled, .missing, .setupNeeded] {
            expect(quitPlan(state: state, inFlight: nil) == .terminateImmediately,
                   "\(state) 下没有任何东西在跑,Quit 必须直接退出,而不是发起一次注定失败的 turnOff")
        }

        // ② 保护可能在跑的状态:必须保留阶段①的「先关、关不掉就不退出」。
        //    退出会抹掉唯一的指示灯,而保护还开着 —— 这是本项目反复拒绝交付的
        //    隐形保护状态(Quit Menu 就因此被整个删掉)。
        for state in [MenuStateKind.connected, .warning] {
            expect(quitPlan(state: state, inFlight: nil) == .turnOffFirst,
                   "\(state) 下保护可能正在跑,Quit 必须先关")
        }

        // ③ .off 的显式裁决:它字面意思是「没在跑」,但仍走 turnOffFirst。
        //    .off 是一个关于「已安装、已配置、Guardian 就在那儿」的机器的**信念**,
        //    最长可能是 30 秒前采的(关闭档轮询间隔),而信念与事实会分叉正是
        //    internal/observe 存在的理由。另外三态说的是「bx 压根不在这台机器上」,
        //    那不是一个可能过时的信念。且 .off 下的 turnOff 是幂等的,几乎注定成功。
        expect(quitPlan(state: .off, inFlight: nil) == .turnOffFirst,
               ".off 是可能过时的信念(且 turnOff 幂等),不得跳过关闭直接退出")

        // ④ .updateNeeded:菜单在跑 `bx status --json` **之前**就返回了,
        //    它对保护开没开一无所知 —— 不知道不等于没在跑。
        expect(quitPlan(state: .updateNeeded, inFlight: nil) == .turnOffFirst,
               ".updateNeeded 下菜单没读过状态,不得假定保护没在跑")

        // ⑤ 有动作在跑就一律先关:进行中说明 Guardian 就在那儿,而且退出前
        //    必须先让那次动作落定(quitDisposition 负责怎么落定)。这一条要盖到
        //    连「没东西可关」的三态也不例外 —— 否则一次在途的 turnOn 会被
        //    直接退出抛在身后,保护起来了而指示灯没了。
        for state in [MenuStateKind.notInstalled, .missing, .setupNeeded, .off,
                      .updateNeeded, .connected, .warning] {
            for action in [ToggleAction.turnOn, .turnOff] {
                expect(quitPlan(state: state, inFlight: action) == .turnOffFirst,
                       "\(state) + 在途 \(action) 时 Quit 不得就地退出")
            }
        }

        if failures == 0 {
            print("QuitPlanTests passed")
        } else {
            exit(1)
        }
    }
}
