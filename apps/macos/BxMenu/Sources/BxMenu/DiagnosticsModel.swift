import Foundation

/// /v1/doctor 应答(与 `bx doctor --json` 同形状)的纯模型:解码、排序、合计。
/// **判据都在这里**,AppKit 那半只摆。

struct DoctorCheck: Decodable, Equatable {
    let name: String
    let status: String
    let detail: String
    let hint: String

    enum CodingKeys: String, CodingKey { case name, status, detail, hint }

    /// 手写:detail/hint 是 omitempty,合成解码器对缺键会抛。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        status = try c.decodeIfPresent(String.self, forKey: .status) ?? ""
        detail = try c.decodeIfPresent(String.self, forKey: .detail) ?? ""
        hint = try c.decodeIfPresent(String.self, forKey: .hint) ?? ""
    }

    init(name: String, status: String, detail: String, hint: String) {
        self.name = name
        self.status = status
        self.detail = detail
        self.hint = hint
    }
}

struct DoctorReport: Decodable, Equatable {
    let ok: Bool
    let version: String
    let checks: [DoctorCheck]

    enum CodingKeys: String, CodingKey { case ok, version, checks }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok) ?? false
        version = try c.decodeIfPresent(String.self, forKey: .version) ?? ""
        checks = try c.decodeIfPresent([DoctorCheck].self, forKey: .checks) ?? []
    }

    init(ok: Bool, version: String, checks: [DoctorCheck]) {
        self.ok = ok
        self.version = version
        self.checks = checks
    }
}

/// 这一版 Guardian 有没有 /v1/doctor。**能力键缺席 = 旧版**,那时「Check for
/// Problems」退回终端那条路,不画一个每次点都 404 的页。
func doctorAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("doctor")
}

/// 坏的排前(fail > warn > info > ok);同一档保持服务端顺序 —— 那是 --json 契约的
/// 顺序,用户在 CLI 里看到的就是它。认不出的状态与 info 同档,**不丢**。
func sortedDoctorChecks(_ checks: [DoctorCheck]) -> [DoctorCheck] {
    func rank(_ status: String) -> Int {
        switch status {
        case "fail": return 0
        case "warn": return 1
        // not_checked 与 info 同档,**排在 ok 之前**:它不是故障,但它是一句
        // 用户必须看见的话(「这一项根本没查」),埋在一堆绿行下面等于没说。
        case "not_checked": return 2
        case "ok": return 3
        default: return 2
        }
    }
    return checks.enumerated()
        .sorted { a, b in
            let ra = rank(a.element.status), rb = rank(b.element.status)
            return ra != rb ? ra < rb : a.offset < b.offset
        }
        .map(\.element)
}

/// 「N failed · M warning(s) · K not checked」。**三数各自计,永远不合成一个总数**:
/// 合成的数在任何成熟配置上都不为零,会被训练成噪声,把真正的 fail 一起淹掉
/// (与 leakcheck 的 path/identity/surface 三段计数同一条纪律)。
///
/// **第三个数是 2026-09-12 补的,它才是这一行不撒谎的那一半。** 在它之前,一份
/// 「流量成败一整类从没检查过」的报告在这里读出来是加粗的 `0 failed · 0 warnings`
/// —— 一句在最坏情况下最令人安心的话。异常数为 0 完全可能是因为一条都没查成,
/// 这一行必须自己说出来。
///
/// **K == 0 时也照写。** 「0 not checked」是一句有内容的话(全查过了),而一个
/// 时有时无的字段会让读的人无从知道它这次是 0 还是这版根本不报。
func doctorSummaryLine(_ checks: [DoctorCheck]) -> String {
    let failed = checks.filter { $0.status == "fail" }.count
    let warned = checks.filter { $0.status == "warn" }.count
    let notChecked = checks.filter { $0.status == "not_checked" }.count
    return warned == 1
        ? L("{0} failed · {1} warning · {2} not checked", failed, warned, notChecked)
        : L("{0} failed · {1} warnings · {2} not checked", failed, warned, notChecked)
}

/// check 名转成人话:下划线换空格、首字母大写、缩写写成缩写(`guardian_dns` →
/// 「Guardian DNS」)。2026-09-26 之前这里只换下划线,Checks 页因此满屏
/// `guardian dns` / `traffic failing rules` 这样的机器名,与泄漏检测页、菜单其余
/// 地方的措辞不是一个产品。
func doctorCheckTitle(_ name: String) -> String {
    // 认得的检查项走词表(英文与下面那条通用规则算出来的逐字相同,中文界面上
    // 才有译文);认不出的(新 Guardian 多发了一项)退回通用规则,仍是一句人话。
    switch name {
    case "guardian_dns": return L("Guardian DNS")
    case "guardian_installed": return L("Guardian installed")
    case "service_installed": return L("Service installed")
    case "service_enabled": return L("Service enabled")
    case "service_active": return L("Service active")
    case "config": return L("Config")
    case "config_readable": return L("Config readable")
    case "config_parse": return L("Config parse")
    case "config_permissions": return L("Config permissions")
    case "server_link": return L("Server link")
    case "transports": return L("Transports")
    case "status_socket": return L("Status socket")
    case "udp_policy": return L("UDP policy")
    case "udp_transport": return L("UDP transport")
    case "udp_traffic": return L("UDP traffic")
    case "network_recovery": return L("Network recovery")
    case "traffic_failing_rules": return L("Traffic failing rules")
    case "traffic_outcomes": return L("Traffic outcomes")
    case "direct_egress": return L("Direct egress")
    case "tunnel": return L("Tunnel")
    case "probe": return L("Probe")
    case "desired_off": return L("Desired off")
    case "failing_rules": return L("Failing rules")
    case "terminal_proxy": return L("Terminal proxy")
    case "tunnel_claims": return L("Tunnel claims")
    case "permission_fallback": return L("Permission fallback")
    default: return humanizedCheckName(name)
    }
}

private func humanizedCheckName(_ name: String) -> String {
    let acronyms: [String: String] = [
        "dns": "DNS", "ipv6": "IPv6", "ipv4": "IPv4", "ip": "IP", "udp": "UDP", "tcp": "TCP",
        "api": "API", "tun": "TUN", "cli": "CLI", "vpn": "VPN",
    ]
    let words = name.split(separator: "_").map(String.init).filter { !$0.isEmpty }
    guard !words.isEmpty else { return name }
    return words.enumerated().map { i, w in
        if let a = acronyms[w.lowercased()] { return a }
        return i == 0 ? w.prefix(1).uppercased() + w.dropFirst() : w
    }.joined(separator: " ")
}

/// 一条 check 状态在界面上的样子:SF Symbol + 给读屏与悬停用的一个词。
/// 认不出的状态**原样**给出(不猜它是好是坏),符号用问号。
struct DoctorStatusLook: Equatable {
    let symbol: String
    let label: String
}

func doctorStatusLook(_ status: String) -> DoctorStatusLook {
    switch status {
    case "fail": return DoctorStatusLook(symbol: "xmark.circle.fill", label: L("Failed"))
    case "warn": return DoctorStatusLook(symbol: "exclamationmark.triangle.fill", label: L("Warning"))
    case "not_checked": return DoctorStatusLook(symbol: "minus.circle", label: L("Not checked"))
    case "ok": return DoctorStatusLook(symbol: "checkmark.circle.fill", label: L("OK"))
    case "info": return DoctorStatusLook(symbol: "info.circle", label: L("Info"))
    default: return DoctorStatusLook(symbol: "questionmark.circle", label: status)
    }
}

/// Checks 页那行「上次检查」的时间。**它是这个页面唯一的「刚才那一下发生过」的
/// 证据**:一台健康的机器连着检查两次,两份报告逐字相同,重画完画面毫无变化 ——
/// 2026-09-10 真机上所有者点了两次 Run again,以为按钮坏了。
///
/// 带秒是必需的:只到分钟的话连着点两次仍然看不出差别,这一行就白加了。
/// 时间取的是**本机点下去的那一刻**,不是 Guardian 的采集时刻 —— 后者报告里没有,
/// 而这一行要回答的问题是「我刚才点的那一下」,本机时钟正是对的答案。
func doctorCheckedAtLine(_ at: Date, timeZone: TimeZone = .current) -> String {
    let formatter = DateFormatter()
    formatter.locale = Locale(identifier: "en_US_POSIX")
    formatter.timeZone = timeZone
    formatter.dateFormat = "HH:mm:ss"
    return L("Checked at {0}", formatter.string(from: at))
}
