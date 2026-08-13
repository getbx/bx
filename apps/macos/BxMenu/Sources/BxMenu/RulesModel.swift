import Foundation

/// GET /v1/rules 的应答。
struct RuleList: Decodable, Equatable {
    var direct: [String] = []
    var proxy: [String] = []
    /// 服务端恒为 true。**键缺席读作 nil,不读作 false** —— 那意味着
    /// 「这一版 Guardian 没说」,与「不需要重启」是两回事。
    var requiresRestart: Bool?

    enum CodingKeys: String, CodingKey {
        case direct, proxy
        case requiresRestart = "requires_restart"
    }

    init(direct: [String] = [], proxy: [String] = [], requiresRestart: Bool? = nil) {
        self.direct = direct
        self.proxy = proxy
        self.requiresRestart = requiresRestart
    }

    /// **必须手写。** Swift 合成的解码器**不使用属性默认值** —— 缺键就抛错。
    /// 而服务端对空列表用的是 `omitempty`,于是「一条 proxy 规则都没有」这种
    /// 完全正常的配置会让整个界面解码失败。测试当场抓到的就是这个。
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        direct = try container.decodeIfPresent([String].self, forKey: .direct) ?? []
        proxy = try container.decodeIfPresent([String].self, forKey: .proxy) ?? []
        requiresRestart = try container.decodeIfPresent(Bool.self, forKey: .requiresRestart)
    }
}

/// 界面上的一行。
struct RuleRow: Equatable {
    let kind: RuleKind
    let pattern: String
    /// 非 nil 表示这条规则正在成片失败 —— 界面据此标红。
    let failure: FailingRule?

    /// 副标题。**一切正常时不说话**:每行都挂一句解释会把真正要紧的那一行淹掉。
    var detail: String? {
        guard let failure, failure.attempts > 0 else { return nil }
        let pct = Int((Double(failure.failures) / Double(failure.attempts) * 100).rounded())
        return "\(failure.failures) of \(failure.attempts) connections failed (\(pct)%) — this path is not working"
    }
}

/// 把规则列表与失败归因合成界面要显示的行。
///
/// **合并发生在这里而不是界面里**,是为了让它可测:`main.swift` 与 AppKit 的
/// 那部分在 CI 里编都不编,而这个仓库全部的事故都在接线上。
func ruleRows(from list: RuleList, failing: [FailingRule]) -> [RuleRow] {
    let index = Dictionary(
        failing.map { ($0.kind.rawValue + "|" + $0.rule.lowercased(), $0) },
        uniquingKeysWith: { first, _ in first }
    )
    func rows(_ patterns: [String], _ kind: RuleKind) -> [RuleRow] {
        patterns.map { pattern in
            RuleRow(
                kind: kind,
                pattern: pattern,
                failure: index[kind.rawValue + "|" + pattern.lowercased()]
            )
        }
    }
    // 失败的排在最前 —— 用户打开这个界面十有八九是因为有东西坏了。
    let all = rows(list.direct, .direct) + rows(list.proxy, .proxy)
    return all.sorted { lhs, rhs in
        let l = lhs.failure?.failures ?? 0
        let r = rhs.failure?.failures ?? 0
        if l != r { return l > r }
        if lhs.kind != rhs.kind { return lhs.kind == .direct }
        return lhs.pattern < rhs.pattern
    }
}

/// 客户端的规则写法校验。
///
/// **它不替代服务端那一份**(Guardian 会再验一次,那才是权威);它存在只是为了
/// 在用户敲完的当下就说话,而不是让他点了保存、断了一次网,才发现写错了。
func validateRulePattern(_ raw: String) -> String? {
    let pattern = raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    if pattern.isEmpty { return "Enter a domain, for example *.example.com" }
    if pattern.rangeOfCharacter(from: .whitespacesAndNewlines) != nil {
        return "Domains cannot contain spaces"
    }
    if pattern.contains("'") || pattern.contains("\"") {
        return "Leave out the quotes — bx adds them itself"
    }
    let body = pattern.hasPrefix("*.") ? String(pattern.dropFirst(2)) : pattern
    if !body.contains(".") || body.hasPrefix(".") || body.hasSuffix(".") || body.contains("..") {
        return "\(raw) is not a domain"
    }
    let allowed = CharacterSet(charactersIn: "abcdefghijklmnopqrstuvwxyz0123456789.-")
    if body.unicodeScalars.contains(where: { !allowed.contains($0) }) {
        return "\(raw) is not a domain"
    }
    return nil
}

/// 归一化成写进配置的形式。校验通过后才调用。
func normalizedRulePattern(_ raw: String) -> String {
    raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
}

/// 这一版 Guardian 支不支持规则编辑。
///
/// **能力键缺席 = 旧版**,不是「不支持」的同义反复:少了这个判断而菜单照样把
/// 入口画出来,用户会对着一个每次点都失败的按钮 —— 而失败的原因(404)
/// 在界面上根本表达不出来。
func rulesEditingAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("rules")
}
