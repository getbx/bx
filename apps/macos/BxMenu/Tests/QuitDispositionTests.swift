import Foundation

@main
struct QuitDispositionTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // 没有动作在跑:quit 自己发起 turnOff,完成后退出——这是最常见的路径,
        // 也是唯一一个 performToggle 的 re-entrancy guard 不会拦截的路径。
        expect(quitDisposition(inFlight: nil) == .turnOffNow,
               "无动作在跑时 quit 应直接 turnOffNow")

        // 已经在关闭中:performToggle(.turnOff) 的 guard 会让第二次调用静默
        // 返回,quit 不能再发一次请求,只能排队等这次落定。
        expect(quitDisposition(inFlight: .turnOff) == .waitThenQuit,
               "turnOff 在跑时 quit 应该排队等它,而不是再发一次")

        // 正在打开中:不能让进程在"保护可能刚被打开"的时候消失——退出前必须
        // 已关闭是比"抢在动作前面退出"更硬的不变量,而客户端也没有办法真正
        // 取消一个已经发给 Guardian 的请求。所以要等 turnOn 落定,再补一次
        // turnOff,才能退出。
        expect(quitDisposition(inFlight: .turnOn) == .waitThenTurnOffThenQuit,
               "turnOn 在跑时 quit 必须落定后补一次 turnOff,不能就地退出")

        // chainsTurnOffBeforeQuitting 只对 waitThenTurnOffThenQuit 为真——
        // main.swift 靠这个布尔决定 completion 里要不要多发一次 turnOff。
        expect(QuitDisposition.turnOffNow.chainsTurnOffBeforeQuitting == false,
               "turnOffNow 不该被误判成需要链式补发")
        expect(QuitDisposition.waitThenQuit.chainsTurnOffBeforeQuitting == false,
               "waitThenQuit 不该链式补发 turnOff——已经在关闭了")
        expect(QuitDisposition.waitThenTurnOffThenQuit.chainsTurnOffBeforeQuitting == true,
               "waitThenTurnOffThenQuit 必须链式补发 turnOff")

        // 排队提示必须存在且非空——用户点了确认框,菜单不能看起来像什么都
        // 没发生。不锁定具体措辞,只锁定"确实有话可说"。
        expect(!quitQueuedStatusText().isEmpty, "排队退出必须有一行看得见的提示")

        if failures == 0 {
            print("QuitDispositionTests passed")
        } else {
            exit(1)
        }
    }
}
