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
    var reachable: Bool = false
    var rttMS: Int = 0
    var error: String = ""

    enum CodingKeys: String, CodingKey {
        case reachable, error
        case rttMS = "rtt_ms"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        reachable = try c.decodeIfPresent(Bool.self, forKey: .reachable) ?? false
        rttMS = try c.decodeIfPresent(Int.self, forKey: .rttMS) ?? 0
        error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
    }

    init(reachable: Bool = false, rttMS: Int = 0, error: String = "") {
        self.reachable = reachable; self.rttMS = rttMS; self.error = error
    }
}

/// 清单里的一台。
///
/// **这里没有 `link` 字段,而且不该有。** 链接是凭据(uuid / 密码),服务端刻意
/// 只发主机名;界面要显示的是「流量从哪出去」,而那就是主机。
struct ServerEntry: Decodable, Equatable {
    let name: String
    /// 出口主机。空 = 服务端解析不出来(链接坏了),**不是** "没有主机"。
    var host: String = ""
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
        case name, host, current, probe
        case udpHost = "udp_host"
        case peakBPS = "peak_bps"
        case peakAgeSeconds = "peak_age_seconds"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        host = try c.decodeIfPresent(String.self, forKey: .host) ?? ""
        udpHost = try c.decodeIfPresent(String.self, forKey: .udpHost) ?? ""
        current = try c.decodeIfPresent(Bool.self, forKey: .current) ?? false
        probe = try c.decodeIfPresent(ProbeReport.self, forKey: .probe)
        peakBPS = try c.decodeIfPresent(Int.self, forKey: .peakBPS) ?? 0
        peakAgeSeconds = try c.decodeIfPresent(Int.self, forKey: .peakAgeSeconds) ?? 0
    }

    init(name: String, host: String = "", udpHost: String = "", current: Bool = false,
         probe: ProbeReport? = nil, peakBPS: Int = 0, peakAgeSeconds: Int = 0) {
        self.name = name; self.host = host; self.udpHost = udpHost; self.current = current
        self.probe = probe; self.peakBPS = peakBPS; self.peakAgeSeconds = peakAgeSeconds
    }
}

/// GET /v1/servers 的应答。
struct ServerList: Decodable, Equatable {
    var servers: [ServerEntry] = []
    var current: String = ""
    var configPath: String = ""

    enum CodingKeys: String, CodingKey {
        case servers, current
        case configPath = "config_path"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        servers = try c.decodeIfPresent([ServerEntry].self, forKey: .servers) ?? []
        current = try c.decodeIfPresent(String.self, forKey: .current) ?? ""
        configPath = try c.decodeIfPresent(String.self, forKey: .configPath) ?? ""
    }

    init(servers: [ServerEntry] = [], current: String = "", configPath: String = "") {
        self.servers = servers; self.current = current; self.configPath = configPath
    }
}

/// POST /v1/servers 的应答。
struct ServerSwitchResult: Decodable, Equatable {
    var name: String = ""
    var host: String = ""
    /// **「配置写好了」与「正在跑的实例也换过去了」是两件事。**
    /// 合成一个 ok 会让菜单说「已切换」而流量还从原来那台出去。
    var applied: Bool = false

    enum CodingKeys: String, CodingKey { case name, host, applied }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        host = try c.decodeIfPresent(String.self, forKey: .host) ?? ""
        // 缺席读作 false:**说不出「已生效」的时候就不许说**。
        applied = try c.decodeIfPresent(Bool.self, forKey: .applied) ?? false
    }

    init(name: String = "", host: String = "", applied: Bool = false) {
        self.name = name; self.host = host; self.applied = applied
    }
}

/// 这一版 Guardian 有没有 /v1/servers。**键缺席 = 旧版**,那时不该画出入口,
/// 否则用户对着一个每次点都失败的按钮(与 rulesEditingAvailable 同一判据)。
func serverSwitchingAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("servers")
}

/// 界面上的一行。
struct ServerRow: Equatable {
    let entry: ServerEntry
    var name: String { entry.name }
    var isCurrent: Bool { entry.current }

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
    var probeLine: String? {
        guard let probe = entry.probe else { return nil }
        if probe.reachable { return "\(probe.rttMS) ms" }
        // **失败必须说出原因。** 一个光秃秃的红叉让用户无从判断是服务器关了、
        // 还是自己这条网络的问题。
        return probe.error.isEmpty ? "unreachable" : probe.error
    }

    /// 这一行要不要标红。**只有「测过而且没通」才标** —— 没测过不标,
    /// 「没能测」(bx 没在跑)也不标:把那两种画成红的,等于把一台好服务器
    /// 说成坏的。
    var probeFailed: Bool {
        guard let probe = entry.probe else { return false }
        return !probe.reachable
    }

    /// 能不能点它切过去。当前那台不能点(点了是空操作,而空操作看起来像坏了);
    /// 主机都解析不出来的那台也不能点 —— 切过去必然失败。
    var isSelectable: Bool { !isCurrent && !entry.host.isEmpty }
}

func serverRows(from list: ServerList) -> [ServerRow] {
    list.servers.map { ServerRow(entry: $0) }
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

/// 换完之后说人话 —— **成功与「只写了配置」必须分开说**。
func serverSwitchOutcomeMessage(result: ServerSwitchResult) -> String {
    let where_ = result.host.isEmpty ? result.name : "\(result.name) (\(result.host))"
    if result.applied {
        // 走到这里说明服务端已经确认过(commit),死手不会再把它还原。
        return "Your traffic now leaves from \(where_)."
    }
    return "Saved \(where_) as your server, but the running tunnel did not switch. "
        + "Turn bx off and on again to use it."
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
