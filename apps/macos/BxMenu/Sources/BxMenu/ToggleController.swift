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
/// 专用条目。它**不再**是一条只能靠 down 清掉的锁存(2026-08-11):
/// Manager.upLocked/Migrate 现在每次都经 recheckOwnershipUncertain 重新向系统
/// 求证,两次扫描都干净才释放。所以第一句要说的是「再试一次是有意义的」,
/// 而它仍然拒绝就意味着系统里真有一个 Core、或者根本扫不动 —— 那时唯一写着
/// 「扫到了谁」的地方是 Guardian 日志。
///
/// **两条都不许写成承诺**:一台真有第二个 Core 的机器上,重试与 down+up 都
/// 本该继续被拒;down+up 只是让 Guardian 忘掉这条判定,不改变系统里的事实。
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
///    doctor 刚刚证实 **Core** 的控制 socket 不应答(`status_socket != ok`),
///    且 launchd 说 Guardian 的 job 没装载。**两条缺一不可** —— Guardian 不在
///    不等于 Core 不在(`bootout` 的 SIGTERM 不可靠地投给 Core),只凭后者就判
///    off,就会在保护还开着时把指示灯连同保护一起「退出」掉;判定顺序因此住在
///    StoppedDiagnosis.swift 由单测钉着。这与 `.missing`/`.notInstalled`/
///    `.setupNeeded` 属同一类证据 ——
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
        // CLI 太旧 → 菜单在问 Guardian 的 `/v1/status` **之前**就返回了,它对保护
        // 开没开一无所知。不知道就不能当成「没在跑」。
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

/// 拼「Core 起不来」那句话所需的**最小**一条服务器事实。
///
/// **刻意不吃 `ServerList`**:这个文件是纯判据、被好几个测试 target 单独编译,
/// 拉进 ServersModel.swift 会把那份依赖铺到六个 target 上。main.swift 那边只做
/// 一次字段对拷,判定(谁是当前那台、host:port 怎么拼、谁算「另一台」)全在
/// 下面那个纯函数里。
struct CoreStartFailureServer: Equatable {
    let name: String
    /// 出口主机。空 = 服务端解析不出来,**不是**「没有主机」。
    let host: String
    /// 0 = 链接里看不出来(或旧 Guardian 没发)。那时只写主机 —— **绝不写 `:0`**。
    let port: Int
    let isCurrent: Bool

    init(name: String, host: String, port: Int, isCurrent: Bool) {
        self.name = name; self.host = host; self.port = port; self.isCurrent = isCurrent
    }
}

/// 那句话要用的两样事实:说的是哪一台,以及还有哪几台。
///
/// **只有名字与 host:port,没有链接** —— 链接是凭据,而这些字段会被拼进
/// 用户看得见的文本里(与 `ServerEntry` 刻意不带 link 同一条)。
struct CoreStartFailureServers: Equatable {
    var currentName: String = ""
    /// 空 = **没问出来**。那时那句话照说,只是不点名 —— 绝不编一个占位地址,
    /// 一句指着 `<unknown>:0` 的排查命令比不给更糟。
    var currentHostPort: String = ""
    var others: [String] = []

    init(currentName: String = "", currentHostPort: String = "", others: [String] = []) {
        self.currentName = currentName; self.currentHostPort = currentHostPort; self.others = others
    }
}

/// 把清单折成那两样事实。
func coreStartFailureServers(_ entries: [CoreStartFailureServer]) -> CoreStartFailureServers {
    var facts = CoreStartFailureServers()
    for entry in entries {
        if entry.isCurrent {
            facts.currentName = entry.name
            facts.currentHostPort = coreStartFailureHostPort(host: entry.host, port: entry.port)
            continue
        }
        facts.others.append(entry.host.isEmpty ? entry.name : "\(entry.name) (\(entry.host))")
    }
    return facts
}

func coreStartFailureHostPort(host: String, port: Int) -> String {
    if host.isEmpty { return "" }
    return port > 0 ? "\(host):\(port)" : host
}

/// Guardian 给这一族启动失败码加的前缀(Go 侧 coreStartFailureLastError)。
let coreStartFailureCodePrefix = "core_"

/// Core 起不来时那句可行动的话 —— **菜单自己在本地拼出来**。
///
/// Guardian 的应答体只带一个码,这是刻意的(spec §5):服务器地址与「你还有
/// 哪几台」菜单本来就经 /v1/servers 合法持有,于是这次改动不新增任何一个字节
/// 的发布面。
///
/// **措辞的每一条规矩都来自一次真实事故**,改之前先读:
///
/// - 只说 bx 观测到什么,**绝不断言那台服务器的状态**。这台 Mac 自己没网时
///   同样拨不通,而一句「that server is down」会让用户去重启一台好好的 VPS。
/// - 「连不上」与「连得上但没握上」的措辞必须**相反**。说反了就是把人派去修
///   一台好机器:reality 一度全挂,真因是默认 SNI www.microsoft.com 的证书
///   过大,而当时先误归因成 sing-box 同机问题、又误归因成网络 MITM。
/// - 「没判出来」有三个码、处置各不相同,但**没有一个可以被读成「服务器没事」**。
/// - 本机拨号失败那一档指着 **bx 自己的直连器**,不指着 VPS —— 2026-08-13 那次
///   事故的签名(IP_BOUND_IF 只查 scoped 路由表,而那条 scoped 默认路由由
///   Hijack 装,比这次判别拨号晚 572 行)。
/// - 只在真有另一台时才说「你还配了另一台」,**绝不打印链接**。
///
/// 认不出的码返回 nil —— 宁可不给,也不编一句错的(与 toggleFailureHint 同一条)。
/// 这个码是不是「Core 起不来」那一族。
///
/// 判据就是 `coreStartFailureHint` 认不认得它 —— **只有一份判据**:
/// 另写一张码清单会与那个 switch 漂开,而漂开的后果是静默的(去问了服务器
/// 清单却拼不出话,或者拼得出话却没去问)。
func isCoreStartFailureCode(_ code: String?) -> Bool {
    coreStartFailureHint(code: code, servers: CoreStartFailureServers()) != nil
}

func coreStartFailureHint(code: String?, servers: CoreStartFailureServers) -> String? {
    guard let code, code.hasPrefix(coreStartFailureCodePrefix) else { return nil }
    let bare = String(code.dropFirst(coreStartFailureCodePrefix.count))
    let where_ = servers.currentHostPort
    let named = !where_.isEmpty
    let logLine = "Full reason: sudo tail -50 /var/log/bx.log"

    var headline: String
    var steps: [String] = []
    var tunnelOutcome = true
    switch bare {
    case "tunnel_unreachable":
        // 「bx cannot reach X」是关于**这次尝试**的事实;「X is down」是关于
        // 那台服务器的断言,而本机自己没网时同样连不上。
        headline = "bx could not start: " +
            (named ? "bx cannot reach \(where_)" : "bx cannot reach your server") +
            " — no TCP connection was established to that address."
        steps.append(named
            ? "That machine may be down or may have changed IP, but this Mac's own network could be at fault too. Check it yourself: nc -z \(coreStartFailureHostOnly(where_)) \(coreStartFailurePortOnly(where_))"
            : "That machine may be down or may have changed IP, but this Mac's own network could be at fault too.")
    case "tunnel_handshake_failed":
        // 措辞与上面**相反**:那台机器活着,去修它是白费力气。
        headline = "bx could not start: " +
            (named ? "\(where_) is answering on TCP" : "your server is answering on TCP") +
            ", but the tunnel did not come up within the start-up window."
        steps.append("That machine is alive — look at the link, its credentials, the SNI, or interference on the way, not at whether the server is down.")
        steps.append(logLine)
    case "tunnel_unhealthy_undetermined_udp_transport":
        headline = "bx could not start: the tunnel did not come up, and bx could not tell whether that server is still there — it runs a UDP transport (hysteria2/QUIC), which a single TCP probe cannot observe."
        steps.append(named
            ? "To confirm that machine is alive, try ping or ssh to \(coreStartFailureHostOnly(where_))"
            : "To confirm that machine is alive, try ping or ssh to it")
        steps.append(logLine)
    case "tunnel_unhealthy_undetermined_local_dial":
        headline = "bx could not start: the tunnel did not come up, and bx could not tell whether that server is still there — its probe failed on this Mac before any SYN left it."
        steps.append("Check bx's own direct route first (the signature of the 2026-08-13 failure): route -n get -ifscope <your interface> 1.1.1.1 — \"not in table\" is the cause, and it has nothing to do with the server.")
        steps.append(logLine)
    case "tunnel_unhealthy_undetermined":
        headline = "bx could not start: the tunnel did not come up, and bx could not tell whether that server is still there (the check itself did not complete)."
        steps.append(named
            ? "To check the server yourself: nc -z \(coreStartFailureHostOnly(where_)) \(coreStartFailurePortOnly(where_))"
            : "Check that the server link in the configuration is still right")
        steps.append(logLine)
    case "config_unusable":
        tunnelOutcome = false
        headline = "bx could not start: something in the configuration is unusable (a rule, a CIDR, a hosts entry, or a server link). After editing it, run sudo bx down && sudo bx up."
        steps.append(logLine)
    case "provision_failed":
        tunnelOutcome = false
        headline = "bx could not start: the embedded transport binary could not be unpacked into data_dir (usually a full disk or an unwritable directory)."
        steps.append(logLine)
    case "tun_open_failed":
        tunnelOutcome = false
        headline = "bx could not start: the TUN device could not be opened (permissions, or the device is in use)."
        steps.append(logLine)
    case "hijack_failed":
        tunnelOutcome = false
        headline = "bx could not start: the TUN came up but hijacking the default route failed."
        steps.append(logLine)
    case "other":
        tunnelOutcome = false
        headline = "bx could not start: Core reported a failure this version of bx has no specific wording for."
        steps.append(logLine)
    default:
        // 认不出的码一个字都不编。
        return nil
    }

    // 换一台服务器只对隧道那几种结局有用。
    if tunnelOutcome, !servers.others.isEmpty {
        steps.append("You also have another server configured: \(servers.others.joined(separator: ", ")) — switch to it in Servers…")
    }
    return ([headline] + steps.map { "  • " + $0 }).joined(separator: "\n")
}

func coreStartFailureHostOnly(_ hostPort: String) -> String {
    guard let index = hostPort.lastIndex(of: ":") else { return hostPort }
    return String(hostPort[hostPort.startIndex..<index]).trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
}

func coreStartFailurePortOnly(_ hostPort: String) -> String {
    guard let index = hostPort.lastIndex(of: ":") else { return "" }
    return String(hostPort[hostPort.index(after: index)...])
}

func toggleFailureHint(code: String?) -> String? {
    guard let code, !code.isEmpty else { return nil }
    switch code {
    case "core_ownership_uncertain":
        return "bx re-checks this on every attempt and still cannot prove no second bx Core is running. " +
            "Quit any sudo bx run you have open, then try again. " +
            "sudo bx down then sudo bx up makes Guardian forget the judgement, but it will refuse just the same " +
            "while a Core really is running — see sudo tail -50 /var/log/bx-guard.err.log for the process it found"
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
func toggleFailureMessage(code: String?, transportDescription: String?, servers: CoreStartFailureServers) -> String? {
    // Core 起不来那一族排在最前:它是这几个码里唯一能配上「哪台服务器、
    // 你还能切到哪儿」的一族,而那正是用户此刻要的。
    if let hint = coreStartFailureHint(code: code, servers: servers) {
        return hint
    }
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
func toggleResultText(code: String?, transportDescription: String?, servers: CoreStartFailureServers, escape: ToggleEscapeOutcome) -> String? {
    let base = toggleFailureMessage(code: code, transportDescription: transportDescription, servers: servers)
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

/// Did the turn-off actually put protection into a state where quitting is safe?
///
/// An HTTP 200 is not the same answer. Guardian now asks the operating system
/// whether any Core is still running before it will call protection `off`; when
/// it cannot confirm that, it answers 200 with `protection_state` set to
/// something else and the reason in `last_error`. Treating the 200 alone as
/// success is how the menu would terminate while a Core still owns the TUN —
/// exactly the "protection running with no indicator at all" state that
/// `quitBlockedByFailedTurnOffMessage` exists to prevent.
///
/// `protectionState` being empty means we never got a body to read, which is
/// also not a confirmation.
func turnOffConfirmedProtectionStopped(protectionState: String?) -> Bool {
    guard let protectionState, !protectionState.isEmpty else { return false }
    return protectionState == "off"
}

/// 关不掉因而没有退出时,弹给用户的那句话。
func quitBlockedByFailedTurnOffMessage() -> String {
    "bx did not stop, so the menu stays. Quitting now would leave protection running " +
        "with no indicator at all. Run sudo bx down in Terminal (it forces a teardown when " +
        "Guardian refuses to stop), then click Quit bx again."
}
