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

        if failures == 0 { print("MenuCadenceTests passed") } else { exit(1) }
    }
}
