import Foundation

/// 服务器清单那一半的**纯逻辑**。
///
/// 判断放这里、摆放放 ServersWindow —— `main.swift` 与 AppKit 那一半在 CI 里编都不编,
/// 逻辑放那边等于没测(本仓库反复记录过的形状)。

/// 一台的探测结论。
///
/// **reachable 与 rttMS 分开,而且 rtt 缺席读作 0 而不是「很快」。** 把「没通」
/// 表达成 0 毫秒会让界面显示一个漂亮的零 —— 零值读起来像一切正常。
struct ProbeReport: Decodable, Equatable {
    /// **这一轮到底测成了没有。** false ⇒ `reachable` 无意义,别去读它。
    ///
    /// **键缺席读作 false,而那是刻意的**:旧 Guardian 不发这个键,它的
    /// `reachable` 可能只是「Core 拨不通」的零值 —— 按「测过」渲染就会把一台
    /// 好服务器画成红的,正是这一整轮要消灭的那句假话。与 `Status.Capabilities`
    /// 同一条纪律:缺席是「这一版没说」,而「没说」不许被读成一个答案。
    var measured: Bool = false
    var reachable: Bool = false
    var rttMS: Int = 0
    var error: String = ""

    enum CodingKeys: String, CodingKey {
        case measured, reachable, error
        case rttMS = "rtt_ms"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        measured = try c.decodeIfPresent(Bool.self, forKey: .measured) ?? false
        reachable = try c.decodeIfPresent(Bool.self, forKey: .reachable) ?? false
        rttMS = try c.decodeIfPresent(Int.self, forKey: .rttMS) ?? 0
        error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
    }

    init(measured: Bool = false, reachable: Bool = false, rttMS: Int = 0, error: String = "") {
        self.measured = measured; self.reachable = reachable
        self.rttMS = rttMS; self.error = error
    }
}

/// 探测的**三态**。
///
/// 这个类型存在的全部理由是:「没测过」/「没测成」/「测了不通」是三个不同的
/// 答案,而此前只有两个位置放它们 —— 于是 bx 没在跑的时候点一下 Test All,
/// 一整排好服务器全被画成红的,各配一句它自己也解释不了的错误。
enum ProbePresentation: Equatable {
    /// 用户还没点过 Test。**一个字都不说** —— 一行「未测试」在每台后面重复
    /// 是墙纸不是信息。
    case notChecked
    /// 这一轮没测成(最常见的原因是 bx 没在跑:直连拨号器在 Core 手里)。
    /// 附带的是服务端给的原因,**它是英文的**(Guardian 那两处产地已改)。
    case notMeasured(String)
    /// 真的测了。`reachable == false` 才是「这台服务器有问题」。
    case measured(reachable: Bool, rttMS: Int)

    /// 要不要画红。**只有实测失败才算** —— 另外两态画红等于把一台好服务器
    /// 说成坏的,而用户会据此去换服务器。
    ///
    /// 判据住在这里而不是窗口里:窗口那一半在本仓库一行 Swift 测试都盖不到。
    var isFailure: Bool {
        if case let .measured(reachable, _) = self { return !reachable }
        return false
    }
}

/// 一台的探测结论怎么讲。**nil = 没测过**,与「测了没通」是两回事。
func probePresentation(_ probe: ProbeReport?) -> ProbePresentation {
    guard let probe else { return .notChecked }
    guard probe.measured else {
        return .notMeasured(probe.error.isEmpty ? "could not measure" : probe.error)
    }
    return .measured(reachable: probe.reachable, rttMS: probe.rttMS)
}

/// 清单里的一台。
///
/// **这里没有 `link` 字段,而且不该有。** 链接是凭据(uuid / 密码),服务端刻意
/// 只发主机名;界面要显示的是「流量从哪出去」,而那就是主机。
struct ServerEntry: Decodable, Equatable {
    let name: String
    /// 出口主机。空 = 服务端解析不出来(链接坏了),**不是** "没有主机"。
    var host: String = ""
    /// 出口端口。0 = 链接里看不出来(或旧 Guardian 没发),那时只写主机 ——
    /// **绝不写一个 `:0`**。同一台主机上两台不同端口的服务器此前渲染得一模一样:
    /// 服务端一直在发这个键,客户端根本没解。
    var port: Int = 0
    /// UDP 单独走的那台(空 = 跟主传输同一台)。
    var udpHost: String = ""
    var current: Bool = false
    /// 这一次测的结果。**nil = 没测过**,与「测了没通」是两回事 —— 后者会让
    /// 一台从没测过的服务器显示成红的。
    var probe: ProbeReport?
    /// 观测到的峰值吞吐(字节/秒)。**只有当前那台会有** —— 吞吐是被动观测,
    /// 没在用的服务器没有产生过流量。0 = 没观测到,**不是「跑不动」**。
    var peakBPS: Int = 0
    /// 那次观测有多久了。**和 peakBPS 成对** —— 一个不带年龄的历史数字读起来
    /// 像现状,而存历史的前提正是界面要标出来这是以前的。0 = 就是现在。
    var peakAgeSeconds: Int = 0

    enum CodingKeys: String, CodingKey {
        case name, host, port, current, probe
        case udpHost = "udp_host"
        case peakBPS = "peak_bps"
        case peakAgeSeconds = "peak_age_seconds"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        host = try c.decodeIfPresent(String.self, forKey: .host) ?? ""
        port = try c.decodeIfPresent(Int.self, forKey: .port) ?? 0
        udpHost = try c.decodeIfPresent(String.self, forKey: .udpHost) ?? ""
        current = try c.decodeIfPresent(Bool.self, forKey: .current) ?? false
        probe = try c.decodeIfPresent(ProbeReport.self, forKey: .probe)
        peakBPS = try c.decodeIfPresent(Int.self, forKey: .peakBPS) ?? 0
        peakAgeSeconds = try c.decodeIfPresent(Int.self, forKey: .peakAgeSeconds) ?? 0
    }

    init(name: String, host: String = "", port: Int = 0, udpHost: String = "", current: Bool = false,
         probe: ProbeReport? = nil, peakBPS: Int = 0, peakAgeSeconds: Int = 0) {
        self.name = name; self.host = host; self.port = port
        self.udpHost = udpHost; self.current = current
        self.probe = probe; self.peakBPS = peakBPS; self.peakAgeSeconds = peakAgeSeconds
    }
}

/// GET /v1/servers 的应答。
struct ServerList: Decodable, Equatable {
    var servers: [ServerEntry] = []
    var current: String = ""
    var configPath: String = ""
    /// 刚加进清单的那台的**最终名字**(用户给的,或 Guardian 按链接推导的)。
    /// **只有 add 应答里有它**;别的应答(以及旧 Guardian)缺席读作空串,不抛 ——
    /// 界面靠它知道接下来该切到哪台,自己再推一遍推导规则就是第二份判据。
    var added: String = ""
    /// Core **此刻真正在用**的那一台,与 `current`(配置里的选择)**并列,
    /// 绝不合并**。
    ///
    /// 两者不同正是这里最有价值的一条诊断:热切换是先写配置再切,所以切换
    /// 失败的那一刻配置已经是新那台了 —— 此时给它加粗打点,就是断言用户的
    /// 流量从一台其实没在用的服务器出去(与 desired / observed / divergence
    /// 同一条纪律)。**空 = 问不出来,不许退回 `current`。**
    var running: String = ""
    /// 配置里**根本没有** `servers:` 清单(单服务器配置 / `transports:` 配置)。
    /// 与「有清单但它是空的」是两句不同的话,见 `serverListEmptyReason`。
    /// 缺席读作 false:旧 Guardian 没说过,那时退回既有措辞。
    var singleServer: Bool = false

    enum CodingKeys: String, CodingKey {
        case servers, current, added, running
        case configPath = "config_path"
        case singleServer = "single_server"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        servers = try c.decodeIfPresent([ServerEntry].self, forKey: .servers) ?? []
        current = try c.decodeIfPresent(String.self, forKey: .current) ?? ""
        configPath = try c.decodeIfPresent(String.self, forKey: .configPath) ?? ""
        added = try c.decodeIfPresent(String.self, forKey: .added) ?? ""
        running = try c.decodeIfPresent(String.self, forKey: .running) ?? ""
        singleServer = try c.decodeIfPresent(Bool.self, forKey: .singleServer) ?? false
    }

    init(servers: [ServerEntry] = [], current: String = "", configPath: String = "",
         added: String = "", running: String = "", singleServer: Bool = false) {
        self.servers = servers; self.current = current; self.configPath = configPath
        self.added = added; self.running = running; self.singleServer = singleServer
    }
}

/// POST /v1/servers 的应答。
struct ServerSwitchResult: Decodable, Equatable {
    var name: String = ""
    var host: String = ""
    /// **「配置写好了」与「正在跑的实例也换过去了」是两件事。**
    /// 合成一个 ok 会让菜单说「已切换」而流量还从原来那台出去。
    var applied: Bool = false
    /// 热切失败时的**结局码**,四种各一个(见 `switchOutcomeMessage`)。
    ///
    /// **空 = 这一版 Guardian 不说结局**(或者服务端自己也说不出是哪一种),
    /// 那时只能给一句诚实的兜底 —— 认不出的码套用四种里的任何一种都比不说更糟:
    /// 一句「已回滚」会让用户以为流量还好好地走在原来那台上。
    var outcome: String = ""

    enum CodingKeys: String, CodingKey { case name, host, applied, outcome }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        host = try c.decodeIfPresent(String.self, forKey: .host) ?? ""
        // 缺席读作 false:**说不出「已生效」的时候就不许说**。
        applied = try c.decodeIfPresent(Bool.self, forKey: .applied) ?? false
        outcome = try c.decodeIfPresent(String.self, forKey: .outcome) ?? ""
    }

    init(name: String = "", host: String = "", applied: Bool = false, outcome: String = "") {
        self.name = name; self.host = host; self.applied = applied; self.outcome = outcome
    }
}

/// 这一版 Guardian 有没有 /v1/servers。**键缺席 = 旧版**,那时不该画出入口,
/// 否则用户对着一个每次点都失败的按钮(与 rulesEditingAvailable 同一判据)。
func serverSwitchingAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("servers")
}

/// 「Replace Configuration…」住在哪:有服务器窗口时它是窗口里的一个按钮(与
/// 「New Server…」并排,那是它们的归属),一级菜单不再占一行;旧 Guardian 没有
/// /v1/servers、窗口开不出来,那时它必须留在菜单里 —— 否则换服务器又只能开终端
/// (2026-08-14 那次抱怨)。判据只看能力声明,绝不试着拨。
func replaceConfigurationLivesInMenu(capabilities: [String]?) -> Bool {
    !serverSwitchingAvailable(capabilities: capabilities)
}

/// 界面上的一行(候选那几台;当前那台另有 `CurrentServerPanel`)。
struct ServerRow: Equatable {
    let entry: ServerEntry
    /// Core **此刻真的在用**这一台吗。
    ///
    /// 它只在两件事同时成立时为真:清单说 Core 在跑这一台,**而且** Core 此刻
    /// 在答话(见 `otherServerRows`)。热切换失败之后,用户真正的出口就在这一行 ——
    /// 不点名的话,他会盯着上面那块加粗的当前那台找原因。
    var isRunningNow: Bool = false

    init(entry: ServerEntry, isRunningNow: Bool = false) {
        self.entry = entry
        self.isRunningNow = isRunningNow
    }

    var name: String { entry.name }
    var isCurrent: Bool { entry.current }

    /// `host:port`。**端口问不出来时只写主机** —— 一个 `:0` 是编出来的答案,
    /// 而它看起来像一个真的端口号。
    var endpoint: String { endpointText(host: entry.host, port: entry.port) }

    /// 这一行的探测结论(三态)。窗口按它决定说什么、画不画红,
    /// **不自己判 `reachable`**。
    var probe: ProbePresentation { probePresentation(entry.probe) }

    /// 「它正在被用」那句话。不成立时 nil —— 每一行都挂一句是墙纸。
    var runningNote: String? {
        isRunningNow ? "in use right now" : nil
    }

    /// 副标题:**出口主机比名字重要** —— 名字是用户随便起的,他真正关心的是
    /// 流量从哪出去。UDP 走另一台时必须单独标出来,否则 UDP 会静默走别的出口。
    var detail: String {
        var parts: [String] = []
        // **名字与主机相同时只显示一次。** 名字多半是从链接里的主机推出来的
        // (bx setup 不给 --name 时就是这样),于是同一个串被并排写了两遍 ——
        // 真机截图上就是 `195.133.192.92   195.133.192.92`。
        if entry.host.isEmpty {
            parts.append("Link could not be parsed")
        } else if entry.host != entry.name {
            parts.append(entry.host)
        }
        if !entry.udpHost.isEmpty, entry.udpHost != entry.host {
            parts.append("UDP → \(entry.udpHost)")
        }
        if let line = probeLine { parts.append(line) }
        if let line = throughputLine { parts.append(line) }
        return parts.joined(separator: "   ")
    }

    /// 吞吐那一段。**没观测到就一个字都不说** —— 「0 B/s」读起来像这条隧道
    /// 死了,而真相是这段时间没人用它传东西。
    ///
    /// 措辞是「peak」不是「speed」:这是**已经发生过的流量**里最快的那一秒,
    /// 不是一次测速,也不是承诺。
    var throughputLine: String? {
        guard entry.peakBPS > 0 else { return nil }
        let rate = "peak \(humanBytesPerSecond(entry.peakBPS))"
        guard let age = relativeAge(seconds: entry.peakAgeSeconds) else { return rate }
        return "\(rate) · \(age)"
    }

    /// 探测那一段。**没测过就一个字都不说** —— 一行「未测试」在每台后面重复,
    /// 是墙纸不是信息。
    ///
    /// 三态各有各的话:没测成说「没测成」并附原因(它**不是**红的),
    /// 测了不通才报失败 —— 而**失败必须说出原因**,一个光秃秃的红叉让用户
    /// 无从判断是服务器关了、还是自己这条网络的问题。
    var probeLine: String? {
        switch probe {
        case .notChecked:
            return nil
        case let .notMeasured(why):
            return why.isEmpty ? "not measured" : "not measured — \(why)"
        case let .measured(reachable, rttMS):
            if reachable { return "\(rttMS) ms" }
            let why = entry.probe?.error ?? ""
            return why.isEmpty ? "unreachable" : why
        }
    }

    /// 能不能点它切过去。当前那台不能点(点了是空操作,而空操作看起来像坏了);
    /// 主机都解析不出来的那台也不能点 —— 切过去必然失败。
    var isSelectable: Bool { !isCurrent && !entry.host.isEmpty }
}

/// `host:port`,端口问不出来时只写主机。主机也没有时给一句人话,不给一个空格。
func endpointText(host: String, port: Int) -> String {
    if host.isEmpty { return "Link could not be parsed" }
    return port > 0 ? "\(host):\(port)" : host
}

/// 当前那台的一整块:配置给出身份,Core 给出纵深。
///
/// **凡是来自 Core 的字段,Core 不答话时一律缺席而不是零值。** 0 毫秒、
/// 「tunnel unhealthy」都是编出来的答案,而这个窗口存在的理由正是
/// 「我这条隧道现在怎么样」—— 在那儿放一个假的数字比什么都不放糟得多。
struct CurrentServerPanel: Equatable {
    let name: String
    let host: String
    let port: Int
    /// 传输(`reality@203.0.113.10`)。nil = Core 没答话或没说。
    let transport: String?
    /// **实时**隧道延迟(毫秒)。nil = 没量到。
    let latencyMS: Int?
    /// 三态:nil = Guardian 没说过隧道好不好,**不是**「不健康」。
    let tunnelHealthy: Bool?
    let udpTransport: String?
    /// UDP 单独走的那台主机(来自配置,与 Core 无关)。
    let udpHost: String?
    let udpMode: String?
    /// 带年龄的吞吐峰值;没观测到就一个字都不说。
    let throughput: String?
    /// Core 没答话时的那句话。**它在,下面那几行就不该有值。**
    let coreSilentNote: String?
    /// 「实际在跑的不是这一台」/「没问出来」那句话。都不成立时 nil。
    let runningNote: String?
    /// `●` 敢不敢加粗:只有 Core 在答话**且**它报的就是这一台时才敢。
    let runningConfirmed: Bool

    var endpoint: String { endpointText(host: host, port: port) }
}

/// 把清单里当前那台与 `/v1/status` 的 Core 运行时合成上面那一块。
///
/// 判据取 `answeringCore`(`MenuRows.swift`)—— **不许写第二份**:
/// `reachable == false` 时 Core 报的每一项都是零值,当真就会画出一行撒谎的 0 ms,
/// 而规则窗口刚因为「同一个问题的第二份判据」出过一模一样的 bug。
func currentServerPanel(list: ServerList, core: CoreRuntime?) -> CurrentServerPanel? {
    guard let entry = list.servers.first(where: { $0.current }) else { return nil }
    let live = answeringCore(core)
    let row = ServerRow(entry: entry)

    // **只有 Core 在答话时才敢说「在跑的就是它」。** 那份 running 来自上一次
    // 取清单,可能已经陈旧 —— 而它陈旧的那一刻,恰好就是保护刚被关掉的时候。
    let running = list.running.trimmingCharacters(in: .whitespaces)
    let confirmed = live != nil && !running.isEmpty
        && running.caseInsensitiveCompare(entry.name) == .orderedSame

    var runningNote: String?
    if live != nil, !confirmed {
        runningNote = running.isEmpty
            ? "bx could not confirm which server is running."
            : "bx is actually using \(running) right now."
    }

    return CurrentServerPanel(
        name: entry.name,
        host: entry.host,
        port: entry.port,
        transport: nonEmpty(live?.transport),
        latencyMS: live?.latencyMS.map { Int($0) },
        tunnelHealthy: live?.tunnelHealthy,
        udpTransport: nonEmpty(live?.udpTransport),
        udpHost: nonEmpty(entry.udpHost),
        udpMode: nonEmpty(live?.udpMode),
        throughput: row.throughputLine,
        coreSilentNote: live == nil
            ? "Core not answering — nothing below was measured."
            : nil,
        runningNote: runningNote,
        runningConfirmed: confirmed)
}

/// 候选那几台:清单里除了当前那台以外的全部。
///
/// `core` 在这里只有一个用途,而它承重:**Core 静默时不许点名「正在用」**。
/// 那句话来自清单里的 `running`,而清单是按需取的 —— 拿一份可能陈旧的答案
/// 去断言此刻的出口,正是这个仓库反复禁止的那种谎。
func otherServerRows(list: ServerList, core: CoreRuntime?) -> [ServerRow] {
    let live = answeringCore(core) != nil
    let running = list.running.trimmingCharacters(in: .whitespaces)
    return list.servers.filter { !$0.current }.map { entry in
        ServerRow(
            entry: entry,
            isRunningNow: live && !running.isEmpty
                && running.caseInsensitiveCompare(entry.name) == .orderedSame)
    }
}

/// 清单为空时说哪一句。**有服务器时返回 nil**,那时不该有任何一句空状态文案。
///
/// 两种「空」是两件事:`bx setup` 从不写 `servers:` 清单,所以每一个正常装好
/// bx 的用户打开这个窗口时都是**单服务器配置**那一种 —— 而 bx 此刻正跑着一台
/// 服务器,对他说「还没有服务器」是一句当场就能被证伪的假话。
func serverListEmptyReason(list: ServerList) -> String? {
    guard list.servers.isEmpty else { return nil }
    if list.singleServer {
        return "This config has a single server, not a server list. "
            + "Adding a second one turns it into a list you can switch between."
    }
    return "No servers yet. Add one to switch between exits."
}

/// 空串读作「没说」。
private func nonEmpty(_ value: String?) -> String? {
    guard let value, !value.isEmpty else { return nil }
    return value
}

/// 换服务器之前的确认文案。
///
/// **必须点明它会改变出口 IP。** 这正是项目所有者拒绝自动容灾的理由:换出口是
/// 一件有后果的事(正在登录的会话、风控、正在下载的东西),必须是用户明知的一下。
func serverSwitchConfirmMessage(name: String, host: String) -> String {
    let where_ = host.isEmpty ? name : "\(name) (\(host))"
    return "Switch your exit to \(where_)?\n\n"
        + "Your public IP changes immediately. Sites you are signed in to may "
        + "ask you to verify again, and downloads in flight will break."
}

/// 换完之后说人话。**四种结局四句话,而其中两句此前是错的。**
///
/// 服务端把 `supervisor.SwitchServer` 的四种结局各给了一个码
/// (`internal/guardian/servers.go` 的 `switchOutcomeCode`);此前四种共用一个
/// 常量,于是菜单对**已生效但确认失败**说「没切过去」—— 那是假的,它切过去了,
/// 而死手可能在超时后把它还原,用户必须**立刻**动手;对**回滚也失败了**则用同一句
/// 「关了再开就行」轻描淡写了一次正在发生的断网。
///
/// **认不出的码走一句诚实的兜底,绝不折进四种里的任何一种。** 说错了比不说更糟:
/// 一句「已回滚」会让用户以为流量还好好地走在原来那台上。旧 Guardian(键缺席)与
/// 服务端自己那个「说不出是哪一种」的兜底码,走的是同一句话 —— 它们说的确实是
/// 同一件事:不知道。
func switchOutcomeMessage(_ result: ServerSwitchResult) -> String {
    let where_ = result.host.isEmpty ? result.name : "\(result.name) (\(result.host))"
    if result.applied {
        // 走到这里说明服务端已经确认过(commit),死手不会再把它还原。
        return "Your traffic now leaves from \(where_)."
    }
    // 逃生命令与 `bx server use` 在终端里给的是同一条 —— 它今天比 GUI 诚实,
    // 两边说的必须是同一件事。
    let escape = "Run `sudo bx down && sudo bx up` in Terminal"
    switch result.outcome {
    case "arm_failed":
        return "Saved \(where_) as your server, but bx could not start the switch. "
            + "Your traffic still leaves from the previous server. "
            + "Turn bx off and on again to use it."
    case "rolled_back":
        return "Saved \(where_) as your server, but its tunnel did not come up, "
            + "so bx switched back. Your traffic still leaves from the previous server."
    case "rollback_failed":
        return "Saved \(where_) as your server. Its tunnel did not come up and bx could "
            + "not switch back, so your connection may be down right now. "
            + escape + " to recover."
    case "commit_failed":
        return "Your traffic already leaves from \(where_), but bx could not confirm the "
            + "switch, so a safety timer may put it back on the previous server. "
            + escape + " now to make it stick."
    default:
        return "Saved \(where_) as your server, but bx could not tell whether the running "
            + "tunnel switched. Check `bx status`; if it is still on the previous server, "
            + "turn bx off and on again."
    }
}

/// 「Add Server…」做完之后那句话。三种结局分开说,**绝不合成「已添加并切换」**:
/// add 成功 + switch 生效 / add 成功 + switch 没生效(已回滚,原样在旧那台)/
/// add 成功 + switch 那一步根本没成(抛错)—— 第三种要告诉他清单里已经有了、可以手动 Use。
func addServerOutcomeMessage(added: String, switched: ServerSwitchResult?) -> String {
    guard let switched else {
        return "Added \(added) to your servers, but could not switch to it. Open Servers… and press Use to try again."
    }
    // **结局那句话只有一份。** 这条路上此前自己写了一句「它停在原来那台」——
    // 对「已生效但确认失败」那种结局,那句话是假的,而它与切换那条路上刚被
    // 修好的是同一个谎。
    return "Added \(added). " + switchOutcomeMessage(switched)
}

/// 把 Add Server 这条路上的失败**码**翻成一句用户做得了的话。
///
/// **认不出的码返回 nil**,由调用方退回那个通用漏斗(它至少诚实地说出了状态码)。
/// 这个不对称是刻意的:多映射一条错的比不映射糟得多 —— 一句像模像样的解释会让
/// 用户去改一件没坏的东西,而通用文案只是不够好懂。
///
/// `status` 今天不参与判定(两个码各自唯一)。**它仍然是参数**,因为码是可以复用
/// 的:同一个 `servers_add_failed` 将来若也出现在别的状态上,判据要能分开,而那时
/// 才想起来要传状态码就晚了 —— 调用点已经不在手边。
func addServerFailureMessage(code: String?, status: Int?) -> String? {
    _ = status
    switch code {
    case "servers_name_exists":
        return "A server with that name is already in your list. "
            + "Pick another name, or use it from Servers…."
    case "servers_add_failed":
        return "bx could not add that server. Check the link, and use only letters, digits, "
            + "dots, underscores or hyphens in the name."
    default:
        return nil
    }
}

/// 「测一下现在从哪出去」的结果。
///
/// **这次探测由菜单自己发,不是 Guardian 发。** 菜单以普通用户身份跑,它的流量
/// 和浏览器走同一条路 —— 那才是「网站看到的是什么」这个问题的忠实答案;而让一个
/// root 守护进程多长一条对外请求的能力,换不来更准的结果。
enum ExitIPProbe: Equatable {
    case unknown
    case checking
    case address(String)
    case failed
}

/// 探测结果那一行。
///
/// **失败说成「没问出来」,绝不说成某个具体答案。** 与这个仓库里 Tristate 同一条
/// 纪律:问不出来不是「没有泄漏」,也不是「没换过去」。
func exitIPLine(_ probe: ExitIPProbe, expected: String = "") -> String {
    switch probe {
    case .unknown: return "Exit IP: not checked"
    case .checking: return "Exit IP: checking…"
    case .failed: return "Exit IP: could not check"
    case let .address(ip):
        // 只有**两边都知道**的时候才敢比。expected 为空是常态(链接里是主机名,
        // 而服务器的出口 IP 未必等于它的入口地址),那时只报事实、不下判断。
        guard !expected.isEmpty else { return "Exit IP: \(ip)" }
        return ip == expected ? "Exit IP: \(ip) — matches \(expected)" : "Exit IP: \(ip)"
    }
}

/// 把 icanhazip 的应答变成一个地址。**校验过才认**,否则一段 HTML 错误页会被
/// 原样当成「你的出口 IP」显示出来。
func parseExitIPResponse(_ body: String) -> String? {
    let text = body.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !text.isEmpty, text.count <= 45 else { return nil }
    let parts = text.split(separator: ".", omittingEmptySubsequences: false)
    guard parts.count == 4 else { return nil }
    for part in parts {
        guard !part.isEmpty, part.count <= 3, part.allSatisfy({ $0.isASCII && $0.isNumber }),
              let value = Int(part), value <= 255
        else { return nil }
    }
    return text
}

/// 与 Go 侧 stats.HumanBPS 同一套单位:**十进制**(MB = 10^6)。
///
/// 用户拿这个数去跟宽带套餐、跟别的测速工具比,那些全是十进制。两边不一致的话,
/// 同一个数在 `bx status` 与菜单里显示成两个值。
func humanBytesPerSecond(_ bps: Int) -> String {
    if bps >= 1_000_000 {
        return String(format: "%.1f MB/s", Double(bps) / 1_000_000)
    }
    if bps >= 1_000 {
        return String(format: "%.0f kB/s", Double(bps) / 1_000)
    }
    return "\(bps) B/s"
}

/// 「多久以前」。**返回 nil 表示「就是现在」** —— 那时不该在界面上写一个
/// 「0 秒前」,它只会让人怀疑这个数字是不是坏的。
///
/// 门槛取两分钟:菜单每 2 秒拉一次,而当前那台的观测年龄恒为 0;历史那些
/// 至少隔着一轮调谐环(30 秒起)。两分钟以内的差别对用户没有意义。
func relativeAge(seconds: Int) -> String? {
    guard seconds >= 120 else { return nil }
    if seconds < 3600 {
        return "\(seconds / 60)m ago"
    }
    if seconds < 86_400 {
        return "\(seconds / 3600)h ago"
    }
    return "\(seconds / 86_400)d ago"
}
