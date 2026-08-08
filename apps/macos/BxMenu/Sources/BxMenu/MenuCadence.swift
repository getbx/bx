import Foundation

/// 菜单打开时的刷新间隔。用户正在看,数据要新鲜。
///
/// 不低于 2 秒是硬下限:macOS 上 `bx status --json` 会跑完整观测
/// (两次 `route -n get` + 一次 `networksetup` + 控制 socket 往返),整轮封顶 5 秒。
/// 间隔比它还短就会出现上一次未回、下一次已发起。
let menuPollOpenSeconds: TimeInterval = 2

/// 菜单关着时的刷新间隔。此时只有菜单栏图标需要更新,没人在读数据行。
///
/// 原实现无论有没有人看都固定 5 秒 spawn 两个进程,是纯浪费。
let menuPollClosedSeconds: TimeInterval = 30

func menuPollInterval(menuOpen: Bool) -> TimeInterval {
    menuOpen ? menuPollOpenSeconds : menuPollClosedSeconds
}

/// 刷新闸门:上一次刷新还没回来时,新的刷新被**丢弃**,不排队。
///
/// 一次刷新会 spawn 两个子进程,而 macOS 上 `bx status --json` 整轮观测封顶 5 秒,
/// 比菜单打开时的 2 秒间隔还长。排队会让慢的那一次后面堆起一串已经过期的刷新
/// ——它们拿到数据时都已经作废,只有最后一次有用;丢弃则代价为零,下一拍照样刷。
///
/// 判定放在这里而不是 main.swift:`refresh()` 那边只该「照做」,不该有规则。
struct RefreshGate {
    private(set) var inFlight = false
    /// 被丢掉的次数。持续增长说明刷新比间隔还慢,是调间隔的依据。
    private(set) var skipped = 0

    /// true = 调用方应当真的去刷新;false = 已有一次在跑,这次丢掉。
    mutating func begin() -> Bool {
        if inFlight {
            skipped += 1
            return false
        }
        inFlight = true
        return true
    }

    /// 刷新结束(成功与否都要调),否则闸门永久关死、菜单从此不再更新。
    mutating func end() {
        inFlight = false
    }
}
