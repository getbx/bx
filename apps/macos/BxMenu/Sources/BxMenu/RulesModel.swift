import Foundation

/// 拉取(规则/服务器)失败时弹窗的说明文案。**只陈述观测到的事实,原因只给
/// 可能性** —— 与「Protection may be off.」同一条纪律。
///
/// 上一版对**任何**失败都断言「bx could not read its configuration」并把人支去
/// /var/log/bx-guard.err.log:2026-08-29 生产 Mac 上真的弹了一次,而当时
/// /v1/rules 实测 200 —— 那次失败是暂时性的(超时/连接),guardian 日志里根本
/// 不会有它,「配置读不出来」是编出来的原因。编一个原因比不给原因更糟:用户
/// 会去修一个不存在的配置问题,还会连带不再相信那个日志指引。
///
/// 指路 Show Details **只在 HTTP 500 时成立**:按「故障可观测性不变量」,只有
/// 500 的完整原因会被 Guardian 写进自己的日志;403 按设计不记,客户端侧失败
/// (连接不上/超时/答不完整)发生在到达 Guardian 之前,日志里没有那一次。
///
/// **`logsAvailable` 是必填的,不给默认值。** 「Show Details」那个按钮由
/// `logsAvailable(capabilities:)` 这道能力门决定画不画(旧 Guardian 没有
/// /v1/logs);文案若无条件许诺它,在旧 Guardian 上就是**指向一个不存在的按钮** ——
/// 用户会在弹窗里找一个找不到的东西,然后以为是自己看漏了。调用方传的必须是
/// 与 showGuardianFailure 那道门**同一个**判据,两处不许各算各的。
/// 门关着时改说日志里有原因(那句话在任何一版上都成立:Guardian 按纪律写了自己的
/// 日志,只是这一版不发布它),失败码照旧带上 —— 它是唯一可检索的线索。
///
/// 刻意吃已抽取的事实而不是 Error:本文件被多个测试套件独立编译,引用
/// GuardianClientError 会把 GuardianClient.swift 拖进每份源文件清单。
func guardianFetchFailureInfo(
    httpStatus: Int?, failureCode: String?, describedError: String?, logsAvailable: Bool
) -> String {
    if let status = httpStatus {
        if status == 500 {
            var info = "bx answered with an error (HTTP 500"
            if let code = failureCode, !code.isEmpty {
                info += ", code=\(code)"
            }
            info += logsAvailable
                ? "). Use Show Details for the reason."
                : "). bx recorded the reason in its log."
            return info
        }
        var info = "bx answered HTTP \(status)"
        if let code = failureCode, !code.isEmpty {
            info += " (code=\(code))"
        }
        info += "."
        return info
    }
    if let described = describedError, !described.isEmpty {
        return "The menu could not fetch this from bx: \(described)"
    }
    return "The menu could not fetch this from bx, and the reason was not recorded."
}

/// 一组规则在配置里的状态。**三态,不是布尔。**
///
/// 真实配置里一组常常只装了一半(用户手工删过几条,或者 preset 后来加了新域名)。
/// 把半装的组画成「开」,用户会以为那几条在生效;画成「关」更糟。
enum RuleGroupState: String, Decodable {
    case on, off, partial
}

/// 界面上的一组。
struct RuleGroup: Decodable, Equatable {
    let name: String
    let title: String
    var summary: String = ""
    let state: RuleGroupState
    var installed: Int = 0
    var total: Int = 0
    /// 这一组的完整域名清单,由服务端发下来。**不在客户端存第二份** ——
    /// 两份清单漂开时,界面会把失败标到错误的组上,或者标不上而看起来一切正常。
    var domains: [String] = []

    enum CodingKeys: String, CodingKey {
        case name, title, summary, state, installed, total, domains
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decode(String.self, forKey: .name)
        title = try c.decode(String.self, forKey: .title)
        summary = try c.decodeIfPresent(String.self, forKey: .summary) ?? ""
        // 认不出的状态按 partial 处理:那是三态里**唯一不会撒谎**的一个 ——
        // 说「装了一部分」在任何情况下都不构成一句关于生效与否的断言。
        state = (try? c.decode(RuleGroupState.self, forKey: .state)) ?? .partial
        installed = try c.decodeIfPresent(Int.self, forKey: .installed) ?? 0
        total = try c.decodeIfPresent(Int.self, forKey: .total) ?? 0
        domains = try c.decodeIfPresent([String].self, forKey: .domains) ?? []
    }

    init(name: String, title: String, summary: String = "", state: RuleGroupState,
         installed: Int = 0, total: Int = 0, domains: [String] = []) {
        self.name = name; self.title = title; self.summary = summary
        self.state = state; self.installed = installed; self.total = total
        self.domains = domains
    }
}

/// 体检里的一条结论。**手写解码**:Go 侧 covered_by 是 omitempty,合成解码器
/// 对缺键会抛,而「没有被谁盖住」(危险规则那一类)是正常情形。
struct RuleFinding: Decodable, Equatable {
    let kind: String
    let rule: String
    /// 线上取值(Go 的 Class.String(),逐字):risky_direct / shadowed_by_user_rule /
    /// overridden_by_opposite_kind / shadowed_by_builtin_list / dead。
    /// **认不出的词不许丢掉这一行** —— 新版 Guardian 发来一类旧菜单不认识的结论时,
    /// 这一行仍然要显示,只是排在已知的几类后面。
    let cls: String
    let summary: String
    let coveredBy: String

    enum CodingKeys: String, CodingKey {
        case kind, rule, summary
        case cls = "class"
        case coveredBy = "covered_by"
    }

    init(kind: String, rule: String, cls: String, summary: String, coveredBy: String) {
        self.kind = kind; self.rule = rule; self.cls = cls; self.summary = summary; self.coveredBy = coveredBy
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? ""
        rule = try c.decodeIfPresent(String.self, forKey: .rule) ?? ""
        cls = try c.decodeIfPresent(String.self, forKey: .cls) ?? ""
        summary = try c.decodeIfPresent(String.self, forKey: .summary) ?? ""
        coveredBy = try c.decodeIfPresent(String.self, forKey: .coveredBy) ?? ""
    }
}

/// GET /v1/rules 里的规则体检。**空报告与缺席不是同一件事**,见 `RuleList.review`。
struct RuleReview: Decodable, Equatable {
    let findings: [RuleFinding]

    enum CodingKeys: String, CodingKey { case findings }

    init(findings: [RuleFinding]) { self.findings = findings }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        findings = try c.decodeIfPresent([RuleFinding].self, forKey: .findings) ?? []
    }
}

/// GET /v1/rules 的应答。
struct RuleList: Decodable, Equatable {
    var direct: [String] = []
    var proxy: [String] = []
    var groups: [RuleGroup] = []
    /// 不属于任何组的规则(用户手写的)。**组开关绝不能碰它们。**
    var custom: [String] = []
    /// 配置文件路径,供「在 Finder 中显示」。空 = 服务端没给(旧 Guardian)。
    var configPath: String = ""
    /// 服务端恒为 true。**键缺席读作 nil,不读作 false** —— 那意味着
    /// 「这一版 Guardian 没说」,与「不需要重启」是两回事。
    var requiresRestart: Bool?
    /// 规则体检。**nil = 这一版 Guardian 不做体检;非 nil 但 findings 为空 =
    /// 查过了、规则都健康。** 压成同一个东西是这个功能最贵的教训。
    var review: RuleReview?

    enum CodingKeys: String, CodingKey {
        case direct, proxy, groups, custom, review
        case configPath = "config_path"
        case requiresRestart = "requires_restart"
    }

    init(
        direct: [String] = [], proxy: [String] = [], groups: [RuleGroup] = [],
        custom: [String] = [], configPath: String = "", requiresRestart: Bool? = nil,
        review: RuleReview? = nil
    ) {
        self.direct = direct
        self.proxy = proxy
        self.groups = groups
        self.custom = custom
        self.configPath = configPath
        self.requiresRestart = requiresRestart
        self.review = review
    }

    /// **必须手写。** Swift 合成的解码器**不使用属性默认值** —— 缺键就抛错。
    /// 而服务端对空列表用的是 `omitempty`,于是「一条 proxy 规则都没有」这种
    /// 完全正常的配置会让整个界面解码失败。测试当场抓到的就是这个。
    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        direct = try container.decodeIfPresent([String].self, forKey: .direct) ?? []
        proxy = try container.decodeIfPresent([String].self, forKey: .proxy) ?? []
        groups = try container.decodeIfPresent([RuleGroup].self, forKey: .groups) ?? []
        custom = try container.decodeIfPresent([String].self, forKey: .custom) ?? []
        configPath = try container.decodeIfPresent(String.self, forKey: .configPath) ?? ""
        requiresRestart = try container.decodeIfPresent(Bool.self, forKey: .requiresRestart)
        review = try container.decodeIfPresent(RuleReview.self, forKey: .review)
    }
}

/// 界面上的一行。
struct RuleRow: Equatable {
    let kind: RuleKind
    let pattern: String
    /// 非 nil 表示这条规则正在成片失败 —— 界面据此标红。
    let failure: FailingRule?
    /// 非 nil 表示规则体检对这一行有话说(危险/从没生效/被盖住……)。
    let verdict: RuleFinding?

    /// 副标题。**一切正常时不说话**:每行都挂一句解释会把真正要紧的那一行淹掉。
    var detail: String? {
        if let verdict {
            if verdict.coveredBy.isEmpty { return verdict.summary }
            return verdict.summary + " ← " + verdict.coveredBy
        }
        guard let failure, failure.attempts > 0 else { return nil }
        let pct = Int((Double(failure.failures) / Double(failure.attempts) * 100).rounded())
        return "\(failure.failures) of \(failure.attempts) connections failed (\(pct)%) — this path is not working"
    }
}

/// 越小越靠前。**有问题的在前,健康的一个字不写** —— 与 Checks 页同一条纪律。
/// 认不出的结论排在已知几类之后、健康之前:不丢,也不冒充自己看懂了。
func ruleRowSeverity(_ row: RuleRow) -> Int {
    switch row.verdict?.cls {
    case "risky_direct": return 0
    case "overridden_by_opposite_kind": return 1
    case "shadowed_by_user_rule": return 2
    case "shadowed_by_builtin_list": return 3
    case "dead": return 4
    case .some: return 5
    case nil: break
    }
    if let failure = row.failure, failure.failures > 0 { return 6 }
    return 7
}

/// 那一行的说明该不该用「严重」的颜色画。
///
/// **不能按「有没有体检结论」分**。那种分法读起来很顺(有结论 = 建议 = 橙,
/// 在失败 = 故障 = 红),而它与这个文件自己的排序**正好打架**:
/// `ruleRowSeverity` 把 `risky_direct` 放在 **0**(最靠前),把成片失败放在
/// **6**。于是「陌生人能把你的真实 IP 送出隧道」会被画得比「一条连不通的规则」
/// **更轻** —— 一个把最重的那条说成最轻的界面,比不上色更糟。
///
/// 判据因此是两条:去匿名化那一类,以及真的在失败的那一类。其余体检结论
/// (被盖住、从没生效、认不出的新类)都是**建议**,轻处理是对的。
func ruleRowNoteIsSevere(_ row: RuleRow) -> Bool {
    if row.verdict?.cls == "risky_direct" { return true }
    return row.failure != nil
}

/// 把规则列表与失败归因、规则体检合成界面要显示的行。
///
/// **合并发生在这里而不是界面里**,是为了让它可测:`main.swift` 与 AppKit 的
/// 那部分在 CI 里编都不编,而这个仓库全部的事故都在接线上。
///
/// - Parameter customOnly: true 时 direct 只保留不属于任何预设的规则
///   (`list.custom`)—— 预设在窗口顶上已经有勾选框,再把它们的域名摊一遍,
///   普通用户第一眼看到的就是四十行域名。**只对 direct 生效**:预设只定义
///   direct 域名,所以所有 proxy 规则按定义都是自定义的。
func ruleRows(from list: RuleList, failing: [FailingRule], customOnly: Bool) -> [RuleRow] {
    let failureIndex = Dictionary(
        failing.map { ($0.kind.rawValue + "|" + $0.rule.lowercased(), $0) },
        uniquingKeysWith: { first, _ in first }
    )
    let verdictIndex = Dictionary(
        (list.review?.findings ?? []).map { ($0.kind + "|" + $0.rule.lowercased(), $0) },
        uniquingKeysWith: { first, _ in first }
    )
    let directPatterns = customOnly ? list.custom : list.direct
    func rows(_ patterns: [String], _ kind: RuleKind) -> [RuleRow] {
        patterns.map { pattern in
            let key = kind.rawValue + "|" + pattern.lowercased()
            return RuleRow(kind: kind, pattern: pattern, failure: failureIndex[key], verdict: verdictIndex[key])
        }
    }
    let all = rows(directPatterns, .direct) + rows(list.proxy, .proxy)
    // 有问题的排最前(严重程度),同级按原顺序稳定 —— 用户打开这个界面
    // 十有八九是因为有东西坏了。
    return all.enumerated().sorted { a, b in
        let sa = ruleRowSeverity(a.element), sb = ruleRowSeverity(b.element)
        if sa != sb { return sa < sb }
        return a.offset < b.offset
    }.map(\.element)
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

/// Guardian 拒绝一条危险的 direct 规则时,界面上说的那句话。
///
/// **它写在客户端,是因为服务端按纪律只回失败码、不回原文**
/// (`policy.DirectRuleHazard` 的 reason/suggestion 到不了这里);两边保持
/// **同义**即可,不必逐字相同 —— 逐字相同要靠一条谁都想不起来维护的守卫。
///
/// 措辞只说**这一条规则会造成什么**与**怎么改窄**,不做告诫:「这样有风险哦」
/// 只会被点穿,而「任何人都能在这个平台上注册子域」是可核对的事实。
let riskyDirectRuleWarning =
    "Anyone can register a subdomain on this platform, so a wildcard rule lets a stranger "
    + "send your real IP outside the tunnel. Use the exact host you need instead, "
    + "for example bucket.s3.amazonaws.com."

/// 归一化成写进配置的形式。校验通过后才调用。
func normalizedRulePattern(_ raw: String) -> String {
    raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
}

/// 改完一条规则之后界面该说哪句话。
///
/// **由服务端的事实决定,不再是常量。** Guardian 改完会叫 Core 热重载
/// (与 `bx direct add` 同一条 /v0/reload 路):`false` = 已生效;`true` = 没
/// 重载成(规则已落盘,重连才生效);`nil` = 这一版 Guardian 没说(旧版),
/// 按要重连处理 —— 「没说」读成「不用」是把用户送去排除掉真正的原因。
enum RuleChangeFollowUp: Equatable {
    case applied
    case reconnectNeeded
}

func ruleChangeFollowUp(requiresRestart: Bool?) -> RuleChangeFollowUp {
    requiresRestart == false ? .applied : .reconnectNeeded
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

/// 一组在界面上要显示的那一行。
struct RuleGroupRow: Equatable {
    let group: RuleGroup
    /// 这一组名下正在成片失败的规则数(0 = 没有值得报的)。
    let failing: Int
    /// 这一组名下失败的连接总数,用来说人话。
    let failures: Int

    var isOn: Bool { group.state == .on }
    /// 半装的组要在界面上**看得出来**是半装的。
    var isMixed: Bool { group.state == .partial }

    /// 行尾那一列。**没话说就不说** —— nil,而不是一句凑数的解释。
    ///
    /// 从「勾选框下面缩进一行小字」改成**右对齐的一列**:竖着堆是上一版显得乱的
    /// 根源 —— 每组占两行、而第二行多半是空的,于是一屏里全是参差不齐的留白。
    /// 一组一行、状态右对齐之后,眼睛只需要扫一列。
    ///
    /// 只有两种情况会说话:有东西在失败(要行动),或者装了一半(状态本身含混)。
    var trailing: String? {
        if failing > 0 {
            return "\(failures) failed"
        }
        if isMixed {
            return "\(group.installed)/\(group.total)"
        }
        return nil
    }
}

/// 把组、失败归因合成界面要显示的行。
///
/// **归因按组汇总,而不是逐条列域名** —— 十行域名对普通用户没有意义,
/// 「Steam 相关的走不通」有。
func ruleGroupRows(from list: RuleList, failing: [FailingRule]) -> [RuleGroupRow] {
    var rows: [RuleGroupRow] = []
    for group in list.groups {
        let members = Set(group.domains.map { $0.lowercased() })
        var count = 0
        var failures = 0
        for rule in failing where rule.kind == .direct && members.contains(rule.rule.lowercased()) {
            count += 1
            failures += rule.failures
        }
        rows.append(RuleGroupRow(group: group, failing: count, failures: failures))
    }
    // 有问题的排最前 —— 用户打开这个窗口十有八九是因为有东西坏了。
    return rows.sorted { lhs, rhs in
        if (lhs.failing > 0) != (rhs.failing > 0) { return lhs.failing > 0 }
        return lhs.group.title < rhs.group.title
    }
}

/// 换配置前给用户看的那句话。
///
/// **显示事实,不做告诫。** 一个讲道理的确认框只会被点穿,而且会训练出用在
/// 下一个真正要紧的框上的点穿反射。「你的出口将从 A 变成 B」是可核对的、
/// 具体的,用户读完能自己判断;「切换服务器有风险哦」不是。
///
/// 认不出旧出口时只说新的 —— **不编一个「未知」当作对照**。
func replaceConfigurationMessage(currentServer: String?, pastedFrom: ReplaceLinkOrigin) -> String {
    var lines: [String] = []
    if let current = currentServer, !current.isEmpty {
        lines.append("Your traffic leaves from \(current) today.")
    }
    lines.append("After this change it will leave from the server in the link you just provided.")
    switch pastedFrom {
    case .clipboard:
        lines.append("The link was read from your clipboard.")
    case .typed:
        break
    }
    lines.append("bx reconnects to apply it.")
    return lines.joined(separator: "\n\n")
}

/// 链接是从哪来的。**剪贴板那一路要说出来** —— 用户该知道自己正在装的是
/// 刚刚复制的那条东西,而不是别的什么。
enum ReplaceLinkOrigin {
    case clipboard
    case typed
}

/// 剪贴板里那段文本能不能当成一条 bx 链接用。
///
/// 用户十有八九刚从聊天窗口复制过来,预填省掉一次粘贴 —— 这是行业默认行为
/// (Windows 托盘、各家客户端都做),不是加分项。
func clipboardCandidateLink(_ raw: String?) -> String? {
    guard let raw else { return nil }
    let text = raw.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !text.isEmpty, text.count < 8192 else { return nil }
    // 只吃**单行**:多行文本多半是聊天记录,把整段塞进输入框只会让人困惑。
    guard !text.contains("\n") else { return nil }
    return looksLikeClientLinkText(text) ? text : nil
}

/// 认得出的客户端链接前缀。与 CLI 侧 `tunnel.IsClientLink` 同一套取值。
func looksLikeClientLinkText(_ text: String) -> Bool {
    let lowered = text.lowercased()
    for prefix in ["bx://", "blink://", "vless://", "hysteria2://", "hy2://", "trojan://", "ss://", "vmess://", "brook://"] {
        if lowered.hasPrefix(prefix) {
            return true
        }
    }
    return false
}
