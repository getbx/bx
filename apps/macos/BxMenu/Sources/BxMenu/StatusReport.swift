import Foundation

/// MenuProtectionVerdict 是「这份状态该让菜单显示什么」的判定。
enum MenuProtectionVerdict: Equatable {
    /// 保护被用户主动关掉。菜单应显示 Off 并提供 Start Protection。
    case off
    /// 出了需要用户知道的问题;附带的字符串就是指示灯 tooltip 里那句原因。
    case attention(String)
    /// 一切正常,可以继续走 DNS 判定与 connected。
    case healthy
}

/// menuProtectionVerdict 把一份 Guardian 状态归到三类之一。
///
/// 输入是 Guardian `/v1/status` 的响应,不再是 `bx status --json`:菜单不该为了
/// 拿同一件事再 spawn 一个可能是旧版的 CLI 去重新推导一遍(见控制面架构设计)。
///
/// 顺序是有讲究的:`off` 必须先判,因为 Core 已经退出、隧道当然不健康——
/// 若先看 tunnelHealthy 就会把「用户自己关的」报成「隧道坏了」,而那个分支
/// 不提供 Start Protection,用户就被困住了。
///
/// 反过来,「Guardian 说在保护但 Core 不见了」绝不能报成 off:那是 Core 崩了,
/// 报成 off 会让用户以为是自己关的,从而不去排查。
func menuProtectionVerdict(_ status: GuardianStatus) -> MenuProtectionVerdict {
    switch status.protectionState {
    case "off":
        return .off
    case "needs_attention":
        return .attention("Repair Required")
    case "blocked":
        return .attention("Blocked")
    default:
        break
    }
    // 此前这里看的是 `bx status --json` 的 `core_available` —— 那是 **CLI 推导**
    // 出来的:CLI 自己去拨 Core 的控制 socket,拨通即 true。`core.reachable` 是
    // 同一个事实的**原产地**:Guardian 拨的是同一个控制 socket,只是这次由它
    // 代问、并把答案随状态一起发布。所以这是等价替换,不是近似。
    //
    // 但两种「没有健康数据」必须分开说:
    //   `core == nil`      Guardian 压根没问过 Core(没接取数函数,比如升级窗口
    //                      里旧 Guardian 还在跑)。它对 Core 一无所知 —— 断言
    //                      「Core 不在」是一句它无权说的话。
    //   `reachable == false` 问了,Core 没答。这才是「Core 不在」。
    // 二者都不得说成 healthy,也都不得说成「隧道不健康」:那是把「问不出来」压成
    // 「答案是坏的」,正是 internal/observe 的三态 Tristate 存在的理由。
    guard let core = status.core else {
        return .attention("Core status unavailable")
    }
    if !core.reachable {
        return .attention("Core unavailable")
    }
    if !core.tunnelHealthy {
        return .attention("Tunnel unhealthy")
    }
    return .healthy
}
