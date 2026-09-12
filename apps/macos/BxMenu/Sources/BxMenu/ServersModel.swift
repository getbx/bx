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
    /// 失败原因的**机器可读**形式(`supervisor.ProbeErr*`)。英文那句话由
    /// `probeFailureText` 在这一侧生成。
    ///
    /// **服务端那个 `error` 键刻意不解。** 它是中文的(它的第一个消费方是
    /// `bx server list`,CLI 通篇中文),而这个菜单通篇英文;把它解出来就迟早
    /// 有人把它显示出去 —— 真机上「服务器关着」这条最常见的路径此前正是这样
    /// 在全英文界面里显示一句中文,而 CJK 守卫只扫菜单自己的源码、看不见它。
    /// 不解这个键,是让「显示它」在构造上不可能,而不是靠下一个人自觉。
    var errorCode: String = ""

    enum CodingKeys: String, CodingKey {
        case measured, reachable
        case rttMS = "rtt_ms"
        case errorCode = "error_code"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        measured = try c.decodeIfPresent(Bool.self, forKey: .measured) ?? false
        reachable = try c.decodeIfPresent(Bool.self, forKey: .reachable) ?? false
        rttMS = try c.decodeIfPresent(Int.self, forKey: .rttMS) ?? 0
        errorCode = try c.decodeIfPresent(String.self, forKey: .errorCode) ?? ""
    }

    init(measured: Bool = false, reachable: Bool = false, rttMS: Int = 0, errorCode: String = "") {
        self.measured = measured; self.reachable = reachable
        self.rttMS = rttMS; self.errorCode = errorCode
    }
}

/// 探测失败的码 → 一句英文。**这张表是那条跨语言契约的客户端一半**:
/// `internal/supervisor.ProbeErrorCodes` 是另一半,由
/// `TestProbeErrorCodesAllHaveAnEnglishSentenceInTheMenu` 双向对账 ——
/// 少一边,界面就会静默退回下面那句笼统的兜底,而没有任何东西会报错。
///
/// 认不出的码(以及旧 Guardian 那种压根不发码的)走 `fallback`:**一句笼统的
/// 英文,而不是服务端那句话** —— 说得不够细好过说错语言。
func probeFailureText(code: String, fallback: String) -> String {
    switch code {
    case "timeout": return "no answer (timed out)"
    case "canceled": return "canceled"
    case "dns": return "could not resolve that host name"
    case "refused": return "connection refused (nothing is listening)"
    case "network_unreachable": return "network unreachable"
    case "no_route": return "no route to that host"
    case "no_host": return "no host to test"
    case "bad_port": return "the port in that link is not valid"
    case "core_unreachable": return "could not measure (is bx running?)"
    case "link_unparsed": return "could not read a host from that link"
    // 服务端归不了类的那一档。它**仍然要有自己的一句话**:退回 `fallback`
    // 读起来与「这一版 Guardian 压根没发码」一模一样,而那是两件事。
    case "unknown": return "could not connect"
    default: return fallback
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
    ///
    /// **`reason` 跟着这一档走,而不是让渲染方回头去翻 `ProbeReport`。**
    /// 少了它,这个类型就不足以画出一行 —— 而不足以画出一行的呈现类型,
    /// 只会逼下一个人绕过它、自己去判 `reachable`,那正是它要消灭的东西。
    /// 通了的时候是空串(没有失败可说)。
    case measured(reachable: Bool, rttMS: Int, reason: String)

    /// 要不要画红。**只有实测失败才算** —— 另外两态画红等于把一台好服务器
    /// 说成坏的,而用户会据此去换服务器。
    ///
    /// 判据住在这里而不是窗口里:窗口那一半在本仓库一行 Swift 测试都盖不到。
    var isFailure: Bool {
        if case let .measured(reachable, _, _) = self { return !reachable }
        return false
    }
}

/// 一台的探测结论怎么讲。**nil = 没测过**,与「测了没通」是两回事。
func probePresentation(_ probe: ProbeReport?) -> ProbePresentation {
    guard let probe else { return .notChecked }
    guard probe.measured else {
        return .notMeasured(probeFailureText(code: probe.errorCode, fallback: "could not measure"))
    }
    return .measured(
        reachable: probe.reachable,
        rttMS: probe.rttMS,
        // 通了就没有原因可说;没通时**必须说出原因** —— 一个光秃秃的红叉让用户
        // 无从判断是服务器关了、还是自己这条网络的问题。
        reason: probe.reachable
            ? ""
            : probeFailureText(code: probe.errorCode, fallback: "unreachable"))
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

/// **改清单**那两个动词有没有:Remove 与 Replace Link…。判据是 `servers_edit`,
/// **不是 `servers`**。
///
/// **Add 表单里那个 UDP 框刻意不在这道门后面**,而这不是漏了:`action:add` 认
/// `udp` 这个键**早于**这一支(Guardian 一直在收它,只是 Swift 客户端从不发),
/// 所以对着一台只声明 `servers` 的旧 Guardian 发它,得到的是正确的行为。
/// 把它一并门控只会在那种机器上**拿掉一个本来能用的功能** —— 而这个门存在的
/// 理由恰恰相反:remove / replace 在那种机器上会做出一件危险的事。
///
/// 两个能力必须分开,而这不是洁癖:`CapabilityServers` 的含义**早于**这些动词。
/// 一台只声明 `servers` 的旧 Guardian 收到 `{"action":"remove"}` 时,走的是它
/// 那一版唯一的行为 —— **换到那一台**。于是「文件换了、进程没换」那个记录在案
/// 的升级窗口里,用户点一下 Delete,出口 IP 与国家换到了他想删掉的那一台,
/// 而那是 2026-08-09 multi-server 设计里唯一明令禁止的事。
///
/// 能力名写成字面量,与 `logsAvailable` 同一先例;它的**值本身**由 Go 侧
/// `TestServersEditCapabilityIsDeclared` 钉住 —— 改了值这里就永久看不见那几个
/// 动词,而两侧都不报错。
func serverEditingAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("servers_edit")
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

    /// `host:port` **之外**还要说的那些:UDP 走了别处、探测结论、吞吐峰值。
    ///
    /// **主机不在这里面了。** 窗口如今把 `endpoint` 摆成自己的一格(spec §4 的
    /// 候选行是「名字、`host:port`、探测呈现」),再在副标题里写一遍主机就是
    /// 同一个串并排两次 —— 真机截图上的 `195.133.192.92   195.133.192.92` 正是
    /// 那么来的,只是当时的原因是名字与主机相同。链接解析不出主机那一句也归
    /// `endpoint`(`endpointText`),不在这儿重复。
    ///
    /// **没话说时返回 nil,不返回空串** —— 空串会让窗口摆一个空 label,一屏
    /// 参差不齐的留白正是上一版「太丑」的来源。
    var note: String? {
        var parts: [String] = []
        if !entry.udpHost.isEmpty, entry.udpHost != entry.host {
            parts.append("UDP → \(entry.udpHost)")
        }
        if let line = probeLine { parts.append(line) }
        if let line = throughputLine { parts.append(line) }
        return parts.isEmpty ? nil : parts.joined(separator: "   ")
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
        case let .measured(reachable, rttMS, reason):
            if reachable { return "\(rttMS) ms" }
            return reason.isEmpty ? "unreachable" : reason
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

    /// 传输 · 实时延迟 · 隧道健不健康,拼成一行。
    ///
    /// **「tunnel healthy / unhealthy」这个映射住在这里,不在窗口里。** 它把一个
    /// 三态的 `Bool?` 折成一句话,而那是判据:窗口写 `healthy ?? false` 就会把
    /// 「没说」显示成「不健康」—— 一台好机器被说成坏的。三项全缺席时返回 nil
    /// (Core 静默那一档,上面那句 `coreSilentNote` 已经把话说完了)。
    var statusLine: String? {
        var parts: [String] = []
        if let transport { parts.append(transport) }
        if let latencyMS { parts.append("\(latencyMS) ms") }
        if let tunnelHealthy { parts.append(tunnelHealthy ? "tunnel healthy" : "tunnel unhealthy") }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// 这一行要不要标红。**只有「明确说了不健康」才算** —— `nil` 是没说,
    /// 把它画红等于替一份从没收到过的观测下结论。
    var statusLineIsBad: Bool { tunnelHealthy == false }

    /// UDP 那一行:走哪条传输、出口是不是另一台、当前是哪个档。
    /// 三样都问不出来就一个字都不说。
    var udpLine: String? {
        var parts: [String] = []
        if let udpTransport { parts.append(udpTransport) }
        if let udpHost { parts.append("→ \(udpHost)") }
        if let udpMode { parts.append(udpMode) }
        return parts.isEmpty ? nil : "UDP  " + parts.joined(separator: " · ")
    }
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

/// 候选那一段为空时说哪一句。**它与 `serverListEmptyReason` 的条件不是同一个,
/// 而这正是它必须存在的理由。**
///
/// 这句话此前住在 `ServersWindow.swift` 的 AppKit 那半边(`emptyReason ?? "No
/// servers to switch to."`),一行测试都盖不到 —— 而它恰恰是**最常见**的那句:
/// `servers:` 里只有一台时,`list.servers` 不为空 ⇒ `serverListEmptyReason`
/// 返回 nil ⇒ 那句兜底就是用户真正读到的东西。
///
/// **候选为不为空由 `otherServerRows` 说了算,这里不另写一遍过滤条件** ——
/// 两份「谁是候选」的判据漂开之后,会出现「一行都没摆、也一个字都不说」的空白。
func otherServersEmptyNote(list: ServerList, core: CoreRuntime?) -> String? {
    // 一台都没有那一档归 serverListEmptyReason:它分得清「单服务器配置」与
    // 「清单是空的」,而这里说不出那个区别。两句都摆就是同一件事说两遍。
    guard !list.servers.isEmpty else { return nil }
    guard otherServerRows(list: list, core: core).isEmpty else { return nil }
    return "No servers to switch to. Add another one to switch your exit between them."
}

/// 删掉一台之前那句确认。
///
/// **它必须说清链接会跟着没**(spec §7.1):链接是凭据,`/v1/servers` 刻意
/// 从不发它,所以菜单手里从来没有那条链接 —— 删掉之后它**无法**把服务器加
/// 回去。这与规则窗口刻意不弹确认、只留 Undo 是相反的处置,理由正是这一条:
/// 一个撤不回的 Undo 比没有 Undo 更糟。
func serverRemoveConfirmMessage(name: String, host: String) -> String {
    let where_ = host.isEmpty ? name : "\(name) (\(host))"
    return "Remove \(where_) from your list?\n\n"
        + "Its link goes with it. bx never hands the link to this menu, so this "
        + "cannot be undone from here — you would have to paste the link again."
}

/// 改清单那两个动词(remove / replace)的失败**码**翻成一句用户做得了的话。
///
/// 与 `addServerFailureMessage` 同一条不对称:**认不出的码返回 nil**,由调用方
/// 退回那个通用漏斗。多映射一条错的比不映射糟得多 —— 一句像模像样的解释会让
/// 用户去改一件没坏的东西。
///
/// `status` 今天不参与判定(每个码各自唯一),仍然是参数:码是可以复用的,
/// 而等到真要按状态分的时候,调用点已经不在手边了。
func serverEditFailureMessage(code: String?, status: Int?) -> String? {
    _ = status
    switch code {
    case "servers_remove_current":
        return "That is the server your traffic uses right now. Switch to another one first, "
            + "then remove this one."
    case "servers_unknown_name":
        return "That server is not in your list any more — it may already be gone."
    case "servers_remove_failed":
        return "bx could not remove that server from the config file."
    case "servers_replace_failed":
        return "bx could not save that link. Check that you pasted a complete bx link."
    case "servers_read_failed":
        return "bx could not read the config file, so nothing was changed."
    default:
        return nil
    }
}

/// 换完链接之后说什么,以及给不给那个「现在就重连」。
struct ReplaceLinkFollowUp: Equatable {
    let message: String
    /// 给不给「现在就重连」那个按钮。**给了也绝不替他按** —— 与规则热生效那条
    /// 收尾同一条纪律:重连会断掉正在跑的连接,那必须是他自己的一下。
    let offersReconnect: Bool
}

/// 换的是**当前那台**时,配置改了而跑着的隧道还连着旧地址(spec §7.2)。
/// 如实说「已写入,重连后生效」,并给一条现在就重连的路。
///
/// 换的是别的那台时**不提重连** —— 那条隧道压根没在跑,一句「重连才生效」
/// 会让用户去重连一件与它无关的事。
func replaceLinkFollowUp(name: String, isCurrent: Bool) -> ReplaceLinkFollowUp {
    if isCurrent {
        return ReplaceLinkFollowUp(
            message: "Saved the new link for \(name). The tunnel that is running still uses the "
                + "old address — bx picks up the new one when it reconnects.",
            offersReconnect: true)
    }
    return ReplaceLinkFollowUp(
        message: "Saved the new link for \(name). Your exit does not change: press Use on it "
            + "when you want to switch over.",
        offersReconnect: false)
}

/// Add / Replace 表单里那个 UDP 框下面那句话。**两句刻意不同,而这不是措辞
/// 上的讲究。**
///
/// Guardian 的 replace 对空 UDP 的处置是**保持原样**(底层原语里空 UDP 是
/// 「删掉 `udp:` 这一行」,而 UDP 传输一旦消失就静默回落到主传输,没有任何
/// 一处会报错)。也就是说**这个菜单今天清不掉一条 UDP 链接** —— 那要 Guardian
/// 侧另加一个显式的「清空」意图,超出这一轮的范围。把它写成「留空 = 没有 UDP」
/// 就是一句用户当场验不出、而后果是静默的假话。
func udpFieldHint(replacing: Bool) -> String {
    replacing
        ? "Optional. Leave it empty to keep the UDP link this server already has — "
            + "the menu cannot clear one."
        : "Optional. A second link for UDP/QUIC traffic, if your server has one."
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
