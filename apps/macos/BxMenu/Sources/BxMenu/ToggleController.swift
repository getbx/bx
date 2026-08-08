import Foundation

/// 超过这个秒数就在菜单里追加一行「比预期久」并给出日志入口。
///
/// 20 秒的依据:正常的 up/down 在 3 秒内完成,而 Go 侧 guardianMutationTimeout
/// 是 60 秒 —— 阈值必须落在两者之间,让用户在服务端还没放弃之前就拿到线索。
let toggleSlowThresholdSeconds = 20

enum ToggleAction {
    case turnOn
    case turnOff

    /// 进行中的动词。用「正在连接/正在断开」而非「启动中/停止中」,
    /// 与菜单其余文案(已保护/未保护)保持同一套说法。
    var progressVerb: String {
        switch self {
        case .turnOn: return "正在连接"
        case .turnOff: return "正在断开"
        }
    }
}

/// 进行中的状态行文案,永远带已用秒数。
///
/// 秒数是这一期最核心的产出:2026-08-04 事故里 `bx down` 卡了 71 分钟,
/// 界面全程没有一个字。
func toggleProgressText(action: ToggleAction, elapsedSeconds: Int) -> String {
    "\(action.progressVerb)… \(max(0, elapsedSeconds)) 秒"
}

/// 逾时提示;未达阈值返回 nil(调用方据此决定要不要多画一行)。
///
/// 不接受 `action` 参数:提示文案本身与「在连接还是在断开」无关(都是
/// 「比预期久」),硬塞一个不影响输出的参数只会制造一个看似有用实则
/// 恒定被忽略的入参。
func toggleSlowHint(elapsedSeconds: Int) -> String? {
    guard elapsedSeconds >= toggleSlowThresholdSeconds else { return nil }
    return "比预期久,通常 3 秒内完成"
}

/// 失败码 → 用户能照做的下一步。
///
/// 与 Go 侧 internal/guardian/client.go 对齐:Guardian 的响应体刻意只回传
/// 失败码、不外传原始错误串(可能含路径/链接/凭据),所以具体说法必须写
/// 在客户端。`core_ownership_uncertain` 是 guardianCodeHints 里目前唯一的
/// 专用条目,且这条判定是锁存的(Manager.upLocked/Migrate 在再次调用
/// Existing() 之前就先看缓存的 Uncertain 标记)——down 再 up 是唯一出路,
/// 不说这句用户无从下手。
///
/// `recovery_incomplete`/`guardian_busy` 不在 guardianCodeHints 表里(Go
/// 侧目前只让它们落回通用的 "sudo bx doctor" 提示),这里补的两条都各自
/// 有源码依据,而不是照抄 core_ownership_uncertain 的套话:
///
/// - `recovery_incomplete`:`Manager.Up`(manager.go:274-275)、`.Down`
///   (:437-438)、`.Migrate`(:293-294)在 `m.recoveryBlocked` 为真时都用
///   同一个 errRecoveryIncomplete 短路——菜单调的 `/v1/down`/`/v1/up`
///   直接打这堵墙,"再点一次" 或 "去菜单里 down 再 up" 只会拿到同一个
///   码,是死循环,不能这么建议。但 CLI 的 `sudo bx down` 走的是另一条
///   路径(internal/cli/guardian.go 的 `macOSDownLifecycleDetailed` →
///   `cleanGuardianDown` 撞见这同一个 errRecoveryIncomplete 后,会自动
///   落入 `forcedMacOSTeardown` 强制拆除——停 Guardian 服务、清除阻断
///   路由,注释里明确写着这就是为了兜底 "recoveryBlocked 被一次网络中断
///   期间的 Guardian 重启变成永久状态,socket 仍应答但 Down 永远失败"
///   这种情况)。菜单的直接 API 调用没有这条后备,所以指引必须点名
///   "去终端敲命令"而不是"再点一次开关"。
/// - `guardian_busy`:`acquireMutation`(manager.go:1010 起)只是在等
///   `m.mutation` 这个 1 容量 channel 腾出来,持锁方 `defer
///   m.releaseMutation()` 保证操作结束必放锁——这是瞬时排队,不是锁存
///   状态,"稍候重试" 如实描述了会发生什么。
///
/// 未知码/无码一律返回 nil —— 宁可不给指引,也不编一句错的。
/// 用户点了 Quit 之后菜单该做什么。
///
/// 这条规则原本(第一轮实现)是"没有动作在跑就直接开始 turnOff、否则什么都不做"——
/// 而 `performToggle` 对已有动作在跑时的 guard 会让第二次调用直接静默返回,
/// 于是"已经在关闭/打开中时点 Quit"会吞掉确认框之后的一切:没有报错、没有退出、
/// 也没有任何界面提示,是 CLAUDE.md「拆除/停止不得依赖先成功做成别的事」这条
/// 不变量的直接违反。三种情况都必须以退出收尾,不需要用户再点一次:
enum QuitDisposition: Equatable {
    /// 没有动作在跑:现在就发起 turnOff,完成后退出。
    case turnOffNow
    /// 已经在关闭中:不发起新请求,原地搭车等它落定,退出。
    case waitThenQuit
    /// 正在打开中:不能眼睁睁让进程在保护可能刚开启时消失(退出前必须已关闭
    /// 是比"抢在动作前面退出"更硬的不变量)。而客户端也没有办法真正取消一个
    /// 已经发到 Guardian socket 上的请求——服务端可能已经在执行 turnOn 了,
    /// 只是标记"已取消"并不能让保护真的关掉。所以等这次 turnOn 落定(不论成
    /// 败),再补发一次 turnOff,等它也落定后才退出。
    case waitThenTurnOffThenQuit

    /// 落定之后(即当前那个动作的 completion 里)要不要在退出前再补一次 turnOff。
    var chainsTurnOffBeforeQuitting: Bool {
        self == .waitThenTurnOffThenQuit
    }
}

/// 根据"现在有没有动作在跑、跑的是哪一个"决定 Quit 的处置方式。
func quitDisposition(inFlight: ToggleAction?) -> QuitDisposition {
    switch inFlight {
    case nil:
        return .turnOffNow
    case .turnOff:
        return .waitThenQuit
    case .turnOn:
        return .waitThenTurnOffThenQuit
    }
}

/// Quit 排队等待当前动作完成时,菜单该显示的一行——不能让界面看起来
/// 像没事发生:用户已经确认退出,必须能看到"退出请求收到了"。
func quitQueuedStatusText() -> String {
    "将在当前操作完成后退出"
}

func toggleFailureHint(code: String?) -> String? {
    guard let code, !code.isEmpty else { return nil }
    switch code {
    case "core_ownership_uncertain":
        return "若确认没有第二个 bx 在跑,执行 sudo bx down 再 sudo bx up 可清除这条已锁存的判定"
    case "recovery_incomplete":
        return "菜单直接调用没有后备,请改在终端执行 sudo bx down(不是再点一次开关)——" +
            "命令行在 Guardian 拒绝关闭时会自动强制拆除脱困;拆除后再试 sudo bx up"
    case "guardian_busy":
        return "Guardian 正在处理上一个请求,稍候重试"
    default:
        return nil
    }
}
