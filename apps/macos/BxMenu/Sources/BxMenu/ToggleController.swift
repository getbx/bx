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

/// socket 那条路失败之后还剩哪条路。
///
/// 只有**关闭**有后备,而且必须有:CLI 的 `bx down` 走
/// `macOSDownLifecycleDetailed`(internal/cli/guardian.go:396),它在 Guardian
/// 不可达、或应答了却拒绝关闭时都会落到 `forcedMacOSTeardown` 强制拆除——
/// 持久化 desired=Off、停 Core、bootout Guardian、清屏障阻断路由、还原 DNS。
/// 菜单改走 socket 之后把这条逃生路径整个丢了:Guardian 死掉而 Core 还活着时
/// `bx status --json` 仍以 0 退出并报 needs_attention,菜单画成 .warning——那个
/// 状态的菜单恰恰提供 Turn Off 与 Quit,而两者都会死在 connect() 上。
/// 「停止」不得依赖先成功做成别的事(CLAUDE.md,2026-08-04 事故)。
///
/// 打开没有对应的东西:不存在「强制打开」,失败就是失败,不能拿 UAC/密码框
/// 去骚扰一个只是没连上的用户。
enum ToggleEscape: Equatable {
    case none
    /// 回落到特权 CLI `bx down`(会弹一次管理员授权框)。
    case privilegedCLIDown
}

func toggleEscape(action: ToggleAction, socketSucceeded: Bool) -> ToggleEscape {
    guard !socketSucceeded else { return .none }
    switch action {
    case .turnOn: return .none
    case .turnOff: return .privilegedCLIDown
    }
}

/// 逃生路径实际跑了没有、跑成了没有。
enum ToggleEscapeOutcome: Equatable {
    case notAttempted
    case succeeded
    case failed
}

/// 逃生路径要执行的 AppleScript(`do shell script … with administrator privileges`)。
///
/// 写成纯函数是为了让引号转义可测:bxPath 里的单引号会先按 shell 规则闭合再转义
/// (`'\''`),整条命令再按 AppleScript 字符串规则转义反斜杠与双引号——两层顺序
/// 搞反就是一个命令注入口子,而它在 main.swift 里不可测。
func privilegedTurnOffScript(bxPath: String) -> String {
    let command = "'" + bxPath.replacingOccurrences(of: "'", with: "'\\''") + "' down"
    let escaped = command
        .replacingOccurrences(of: "\\", with: "\\\\")
        .replacingOccurrences(of: "\"", with: "\\\"")
    return "do shell script \"\(escaped)\" with administrator privileges"
}

/// 一次失败的开关在菜单里显示的那一行。
///
/// 三级递降,每一级都比下一级更能让用户照做:
/// ① 认识这个码 → 给出这个码专属的下一步;
/// ② 有码但不认识(将来新增的码、更旧/更新的 Guardian)→ 如实报码,用户能拿去搜或贴给我们;
/// ③ 没有码(Guardian 根本没回码,或压根没连上)→ 报传输层的描述。
///
/// 「没有码」不能被伪装成有码:`toggleFailureHint` 对 nil/空码返回 nil,这里
/// 也绝不替它补一句通用套话冒充专属指引。
func toggleFailureMessage(code: String?, transportDescription: String?) -> String? {
    if let hint = toggleFailureHint(code: code) {
        return hint
    }
    if let code, !code.isEmpty {
        return "失败码 \(code)"
    }
    return transportDescription
}

/// 一次开关落定之后菜单显示的那一行,含逃生路径的结局。
///
/// 逃生成功也要说话:用户点的是「Turn Off」,实际发生的是「Guardian 关不掉,
/// 改由特权 CLI 强制拆除」——这是两件不同的事,静默成功等于隐瞒 Guardian 已经
/// 不听话了。逃生失败则必须把最后一条人工出路(在终端敲 sudo bx down)说出来,
/// 那时菜单已经无路可走。
func toggleResultText(code: String?, transportDescription: String?, escape: ToggleEscapeOutcome) -> String? {
    let base = toggleFailureMessage(code: code, transportDescription: transportDescription)
    switch escape {
    case .notAttempted:
        return base
    case .succeeded:
        return "Guardian 关不掉,已改用 sudo bx down 强制拆除完成"
    case .failed:
        let reason = base ?? "Guardian 关闭失败"
        return reason + ";sudo bx down 也没能完成,请在终端手动执行 sudo bx down"
    }
}

/// turn-off 每条路都失败之后,Quit 该不该退出。
///
/// **不退出。** 终止进程会抹掉菜单栏图标,而保护仍在跑——正是 CLAUDE.md 反复
/// 拒绝交付的「保护在跑却没有任何指示灯」隐形状态(`Quit Menu` 就是因此被整个
/// 删掉的)。退出唯一能换来的是「界面看起来听话了」,代价是用户既看不到保护还
/// 开着、也再没有入口去关它,只能等下次登录。所以关不掉就留在原地,把失败连同
/// 唯一的人工出路显示出来——用户可以照做之后再点一次 Quit。
func quitTerminatesAfterTurnOff(turnedOff: Bool) -> Bool {
    turnedOff
}

/// 关不掉因而没有退出时,弹给用户的那句话。
func quitBlockedByFailedTurnOffMessage() -> String {
    "bx 没能关闭,菜单继续保留——退出会让保护仍在运行却没有任何指示灯。" +
        "请在终端执行 sudo bx down(它会在 Guardian 拒绝关闭时强制拆除),完成后再点 Quit bx。"
}
