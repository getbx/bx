import Foundation

/// 超过这个秒数就在菜单里追加一行「比预期久」并给出日志入口。
///
/// 20 秒的依据:正常的 up/down 在 3 秒内完成,而 Go 侧 guardianMutationTimeout
/// 是 60 秒 —— 阈值必须落在两者之间,让用户在服务端还没放弃之前就拿到线索。
let toggleSlowThresholdSeconds = 20

enum ToggleAction {
    case turnOn
    case turnOff

    /// 进行中的动词。用 Connecting/Disconnecting 而非 Starting/Stopping,
    /// 与菜单其余文案(Connected / Off)保持同一套说法。
    /// 菜单全英文:这是用户看得见的字,与 rebuildMenu 里的表头/数据行同一语言。
    var progressVerb: String {
        switch self {
        case .turnOn: return "Connecting"
        case .turnOff: return "Disconnecting"
        }
    }
}

/// 进行中的状态行文案,永远带已用秒数。
///
/// 秒数是这一期最核心的产出:2026-08-04 事故里 `bx down` 卡了 71 分钟,
/// 界面全程没有一个字。
func toggleProgressText(action: ToggleAction, elapsedSeconds: Int) -> String {
    "\(action.progressVerb)… \(max(0, elapsedSeconds))s"
}

/// 逾时提示;未达阈值返回 nil(调用方据此决定要不要多画一行)。
///
/// 不接受 `action` 参数:提示文案本身与「在连接还是在断开」无关(都是
/// 「比预期久」),硬塞一个不影响输出的参数只会制造一个看似有用实则
/// 恒定被忽略的入参。
func toggleSlowHint(elapsedSeconds: Int) -> String? {
    guard elapsedSeconds >= toggleSlowThresholdSeconds else { return nil }
    return "Taking longer than usual — this normally finishes within about 3 seconds"
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

/// `.off` 的两条来路。**证据强度不同,不能合并。**
///
/// 合并的代价是实打实的:route 2 下走关闭路径会弹一个意外的授权框,用户一取消
/// 就 `escape == .failed` → `finishQuit(turnedOff: false)` → 菜单拒绝退出,并且
/// 断言「bx 还在跑 / 退出会让保护仍在运行却没有任何指示灯」—— 而那时**什么都
/// 没在跑**。本项目的全部历史就是不让界面断言不成立的事。
enum OffOrigin: Equatable {
    /// Guardian 的 `/v1/status` 应答了,报告说保护是关的
    /// (`menuProtectionVerdict == .off`)。Guardian 就在那儿应答 —— 这是一个
    /// **信念**,而且最长可能是 30 秒前采的。
    case guardianResponding
    /// Guardian 的 socket **拨不通**,随后 doctor 观测到 `service_active != ok`
    /// (launchd job 没装载)。同一次刷新里的**两条新鲜的否定观测**。
    case serviceStopped
}

/// 菜单七态(`BxState`)的**种类**(去掉 payload),其中 `.off` 按来路分成两支,
/// 故这里是八种。
///
/// `BxState` 带着 `GuardianStatus`/版本号等 payload 住在 main.swift 里,而 main.swift
/// 编不进 scripts/test-macos-menu.sh(它要 AppKit)。判定要可测就必须吃一个不带
/// payload 的输入;main.swift 那边只剩一个逐 case 的映射,漏掉新 case 会被
/// Swift 的穷尽性检查当场拦下。
enum MenuStateKind: Equatable {
    case connected
    case warning
    case updateNeeded
    case setupNeeded
    case missing
    case notInstalled
    case offGuardianResponding
    case offServiceStopped
}

/// 点了 Quit 之后,退出之前**要不要先关一次**。
enum QuitPlan: Equatable {
    /// 保护可能正在跑:先 turnOff,关不掉就不退出(见 quitTerminatesAfterTurnOff)。
    case turnOffFirst
    /// 没有任何可关的东西:直接退出。
    case terminateImmediately
}

/// Quit 之前有没有东西要关。
///
/// 阶段②把退出入口铺到了每一个状态,于是 `.notInstalled` / `.missing` /
/// `.setupNeeded` 下点 Quit 会走 `performToggle(.turnOff)` → Guardian socket ——
/// 而这三个状态的定义就是「Guardian 不在那儿」(没装 app、没有 /usr/local/bin/bx、
/// 没跑过 setup 因而 service_installed 为 fail)。socket 必然失败,逃生路径那次
/// `sudo bx down` 也必然失败(二进制不在/服务没装),`quitTerminatesAfterTurnOff`
/// 于是拒绝退出 —— 用户被关在一个**关不掉的菜单**里,而根本没有任何保护需要被
/// 保护。阶段①「关不掉就不退出」的裁决是对的,但它的理由是「保护可能还在跑,
/// 退出会抹掉唯一的指示灯」;没有东西在跑的时候这个理由整个不成立,剩下的只是
/// 一个白白困住用户的拒绝。
///
/// **`.off` 按来路分两侧裁决**(见 `OffOrigin`)——一开始把两条路合并成一个
/// turnOffFirst 是错的,理由只对其中一条成立:
/// ① `.offGuardianResponding` 是一个**信念**,而且是关于一台已安装、已配置、
///    Guardian 就在那儿的机器的信念 —— 这正是 internal/observe 整个存在的理由:
///    信念与事实会分叉。这份 `.off` 最长可能是 30 秒前采的(关闭档轮询间隔),
///    而那里的 turnOff 是幂等的、几乎注定成功;万一失败,那次失败本身就是
///    「下面有东西不对劲」的证据 —— 恰恰是该保留指示灯的场合。→ turnOffFirst。
/// ② `.offServiceStopped` 不是信念,是**同一次刷新里的两条新鲜否定观测**:
///    `bx status --json` 没跑通(控制 socket 不应答),doctor 又刚刚看到 launchd
///    job 没装载。这与 `.missing`/`.notInstalled`/`.setupNeeded` 属同一类证据 ——
///    可以当场核实的事实,不是可能过时的信念。而那里 socket 关闭必然失败、只会
///    弹一个意外的授权框,用户一取消就换来一句不成立的「bx 还在跑」。
///    → terminateImmediately。
///
/// 有动作在跑时一律 turnOffFirst:进行中说明 Guardian 就在那儿,而且退出前必须
/// 先让那次动作落定(见 quitDisposition)。这一条盖过 state,连 `.offServiceStopped`
/// 也不例外 —— 一次在途的 turnOn 若被直接退出抛在身后,保护起来了而指示灯没了。
func quitPlan(state: MenuStateKind, inFlight: ToggleAction?) -> QuitPlan {
    if inFlight != nil { return .turnOffFirst }
    switch state {
    case .connected, .warning, .offGuardianResponding:
        return .turnOffFirst
    case .updateNeeded:
        // CLI 太旧 → 菜单在跑 `bx status --json` **之前**就返回了,它对保护开没开
        // 一无所知。不知道就不能当成「没在跑」。
        return .turnOffFirst
    case .setupNeeded, .missing, .notInstalled, .offServiceStopped:
        return .terminateImmediately
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
    "Will quit once the current operation finishes"
}

func toggleFailureHint(code: String?) -> String? {
    guard let code, !code.isEmpty else { return nil }
    switch code {
    case "core_ownership_uncertain":
        return "If no second bx is running, run sudo bx down then sudo bx up to clear this latched judgement"
    case "recovery_incomplete":
        return "The menu's direct call has no fallback. Run sudo bx down in Terminal " +
            "(not this toggle again) — the command line forces a teardown when Guardian " +
            "refuses to stop. Then try sudo bx up"
    case "guardian_busy":
        return "Guardian is still handling the previous request — retry shortly"
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
        return "Failure code \(code)"
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
        return "Guardian could not turn bx off; completed by forced teardown via sudo bx down"
    case .failed:
        let reason = base ?? "Turning bx off through Guardian failed"
        return reason + "; sudo bx down did not complete either — run sudo bx down in Terminal yourself"
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
    "bx did not stop, so the menu stays. Quitting now would leave protection running " +
        "with no indicator at all. Run sudo bx down in Terminal (it forces a teardown when " +
        "Guardian refuses to stop), then click Quit bx again."
}
