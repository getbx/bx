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
