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
    private var pending = false

    /// true = 调用方应当真的去刷新;false = 已有一次在跑,这次丢掉。
    ///
    /// `userInitiated` 决定这次被丢掉后要不要补跑。**定时器那一拍绝不补**:
    /// 打开档 2 秒一拍、而一次刷新可能要 5 秒,每次结束时必然已经积压了两拍,
    /// 于是补跑接补跑 —— 菜单开着的整段时间里四个 bx 子进程首尾相接、占空比 100%,
    /// 比不补跑的老行为还糟(丢掉的那拍本来会留出空档到下一个边沿)。
    /// 用户刚做完动作那一次才补:那才是「结果必须尽快出现」的场合,而且它不重复。
    mutating func begin(userInitiated: Bool = false) -> Bool {
        if inFlight {
            skipped += 1
            pending = pending || userInitiated
            return false
        }
        inFlight = true
        return true
    }

    /// 刷新结束(成功与否都要调),否则闸门永久关死、菜单从此不再更新。
    ///
    /// 返回 true 表示**补跑一次**。用户刚做完某个动作(开/关/setup)后发起的刷新
    /// 若正好被丢掉,菜单会把用户自己那一下的结果报错到下一拍(关闭档下最长 30 秒)。
    /// 补跑是**一次性的**,且只补用户那一类:定时器那一拍被丢掉就让它丢,
    /// 下一个边沿自然会再来 —— 否则就成了首尾相接的满占空比(见 begin)。
    mutating func end() -> Bool {
        inFlight = false
        guard pending else { return false }
        pending = false
        return true
    }
}

/// 恢复快照的代际号。**每一次对 `recoverySnapshot` / `reconnectInFlight` 的写入都 +1。**
///
/// 采集移到后台线程之后出现了一个原来不可能有的窗口:`refresh()` 在 t 时刻采样这两个
/// 输入,`applyRefresh` 在 t+Δ(Δ 最长 5 秒)把据此算出来的结果写回去。窗口里有三个
/// 主线程写者(reconnectBx / publishRecovery / pollRecovery 的终态清理),它们的结果会
/// 被一份用陈旧输入算出来的值盖掉。**后果不是慢半拍,是假红**:被动恢复成功、轮询器
/// 已清掉浮层,而在途的那次刷新因为「当时被告知 reconnectInFlight 为真」跳过了
/// passiveStatusRecovery、把陈旧的 running 快照原样带回来 —— 浮层复活、observeRecovery
/// 被重新武装,若 Guardian 此时已经开始另一次恢复,recoveryID 对不上就会发布一条
/// 「Reconnect Failed / Recovery was replaced」,而那次恢复其实成功了。
///
/// 判定放在这里而不是 main.swift:它是一条规则(陈旧的不许写回),不是绘制。
struct RecoveryGeneration {
    private(set) var value = 0

    mutating func bump() {
        value += 1
    }

    /// 采集期间没人动过这两个输入,结果才配写回快照那半边。
    /// 变过就丢弃 —— 主线程上那个写者知道的比后台那次采集新。
    func acceptsWriteBack(captured: Int) -> Bool {
        captured == value
    }
}
