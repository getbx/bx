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

        // ③ .off 的显式裁决:**按来路分两侧**。一开始把两条路合并成 turnOffFirst
        //    是错的 —— 那个理由只对其中一条成立。
        //    route 1(menuProtectionVerdict == .off):bx status --json 跑通了、
        //    Guardian 就在那儿应答,它说保护是关的。这是一个**信念**,最长可能是
        //    30 秒前采的(关闭档轮询间隔),而信念与事实会分叉正是 internal/observe
        //    存在的理由;那里的 turnOff 幂等、几乎注定成功,万一失败那次失败本身
        //    就是该保留指示灯的证据。
        expect(quitPlan(state: .offGuardianResponding, inFlight: nil) == .turnOffFirst,
               "Guardian 还在应答时的 .off 是可能过时的信念(且 turnOff 幂等),不得跳过关闭直接退出")

        //    route 2(diagnoseStopped 的 service_active != ok):同一次刷新里的
        //    **两条新鲜否定观测** —— bx status --json 已经失败(控制 socket 不应答),
        //    doctor 又刚看到 launchd job 没装载。这与 .missing/.notInstalled/
        //    .setupNeeded 同属「可以当场核实的事实」,不是可能过时的信念。走关闭
        //    路径只会弹一个意外的授权框,用户一取消就 escape == .failed →
        //    finishQuit(turnedOff: false) → 菜单拒绝退出并断言「bx 还在跑 / 退出会让
        //    保护仍在运行却没有任何指示灯」,而那时**什么都没在跑**。
        //    界面不许断言不成立的事。
        expect(quitPlan(state: .offServiceStopped, inFlight: nil) == .terminateImmediately,
               "Guardian 服务已停是新鲜观测而非信念,Quit 必须直接退出,不能弹一个注定失败的授权框")

        //    两支必须真的分开:合并回同一个结论就是本轮修复前的行为。
        expect(quitPlan(state: .offGuardianResponding, inFlight: nil)
                != quitPlan(state: .offServiceStopped, inFlight: nil),
               ".off 的两条来路证据强度不同,不得再合并成同一个结论")

        // ④ .updateNeeded:菜单在跑 `bx status --json` **之前**就返回了,
        //    它对保护开没开一无所知 —— 不知道不等于没在跑。
        expect(quitPlan(state: .updateNeeded, inFlight: nil) == .turnOffFirst,
               ".updateNeeded 下菜单没读过状态,不得假定保护没在跑")

        // ⑤ 有动作在跑就一律先关:进行中说明 Guardian 就在那儿,而且退出前
        //    必须先让那次动作落定(quitDisposition 负责怎么落定)。这一条要盖到
        //    连「没东西可关」的三态也不例外 —— 否则一次在途的 turnOn 会被
        //    直接退出抛在身后,保护起来了而指示灯没了。
        for state in [MenuStateKind.notInstalled, .missing, .setupNeeded,
                      .offGuardianResponding, .offServiceStopped,
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
