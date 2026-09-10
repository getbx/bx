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

/// 「N failed · M warning(s)」。**两数各自计,永远不合成一个总数**:合成的数在任何
/// 成熟配置上都不为零,会被训练成噪声,把真正的 fail 一起淹掉。
func doctorSummaryLine(_ checks: [DoctorCheck]) -> String {
    let failed = checks.filter { $0.status == "fail" }.count
    let warned = checks.filter { $0.status == "warn" }.count
    return "\(failed) failed · \(warned) warning\(warned == 1 ? "" : "s")"
}

/// check 名转成人话:与 `bx doctor` 文本路径同一个规则(下划线换空格)。
func doctorCheckTitle(_ name: String) -> String {
    name.replacingOccurrences(of: "_", with: " ")
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
    return "Checked at " + formatter.string(from: at)
}
