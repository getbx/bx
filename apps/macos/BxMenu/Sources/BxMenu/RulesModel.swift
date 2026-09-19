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
    /// 服务端写的那句话,**直接渲染**。
    ///
    /// 2026-09-17 之前它是中文(internal/rulereview 那几段),而这个菜单通篇英文,
    /// 所以界面上那句由本地的 `ruleVerdictText` 按 `cls` 另写一份 —— 同一句判断
    /// 在两处各写一遍。判据文案改成英文之后**那个理由不成立了**,`ruleVerdictText`
    /// 连同它的测试一起删掉:走英文这条路在这里是**减少**一份清单,不是增加。
    ///
    /// **空串仍要有出路**:旧 Guardian 可能不发这个键,那时按 `cls` 说一句
    /// 「这一版说不出为什么」,绝不冒充看懂了(见 `verdictText`)。
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
    /// 这台机器是不是 global 模式。**nil = 这一版 Guardian 没说**,不是「不是」。
    /// 它决定下面那份自定义规则读起来是什么意思,见 customRulesHeading。
    var global: Bool?

    enum CodingKeys: String, CodingKey {
        case direct, proxy, groups, custom, review, global
        case configPath = "config_path"
        case requiresRestart = "requires_restart"
    }

    init(
        direct: [String] = [], proxy: [String] = [], groups: [RuleGroup] = [],
        custom: [String] = [], configPath: String = "", requiresRestart: Bool? = nil,
        review: RuleReview? = nil, global: Bool? = nil
    ) {
        self.direct = direct
        self.proxy = proxy
        self.groups = groups
        self.custom = custom
        self.configPath = configPath
        self.requiresRestart = requiresRestart
        self.review = review
        self.global = global
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
        global = try container.decodeIfPresent(Bool.self, forKey: .global)
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
    ///
    /// **分类与失败是两件事,两件都有就两件都说。** 上一版在体检结论那里就
    /// `return` 了,于是一条「被你自己更宽的一条盖住」**并且** 8113/8113 全失败的
    /// 规则,只显示「covered by a broader rule of yours」还被画成红的 —— 一行红字
    /// 说的却是「删掉它不改变任何流量」。这不是边角情况:`DomainSet.MatchRule`
    /// 逐级往父域找,于是**正在累积失败的恰恰是被盖住的那条更窄的规则**。
    var detail: String? {
        var parts: [String] = []
        if let verdict {
            var text = verdictText(verdict)
            if !verdict.coveredBy.isEmpty { text += " ← " + verdict.coveredBy }
            parts.append(text)
        }
        if let failure, failure.attempts > 0 {
            let pct = Int((Double(failure.failures) / Double(failure.attempts) * 100).rounded())
            parts.append(
                "\(failure.failures) of \(failure.attempts) connections failed (\(pct)%) — this path is not working")
        }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}

/// 一条体检结论在界面上说的那句话。**英文住在客户端,按 `class` 映射。**
///
/// 服务端那份 `summary` 是中文(`internal/rulereview` 与 `deadFindings` 写的),
/// 而这个菜单的每一个用户可见字符串都是英文 —— 直接转发会让
/// 「已在内建 china 直连列表里,这条手写的没有额外作用。 ← *.apple.com」出现在一个
/// 通篇英文的窗口里。**选客户端映射而不是让服务端多发一个英文字段**:`summary`
/// 同时喂着 `bx doctor` 与 `bx status` 的中文输出(那一侧的读者是中文用户),
/// 让服务端为菜单再写一份英文,就是同一句判断在两处各写一遍、还得有人记得让它们
/// 保持同义;而 `class` 本来就是这条协议里稳定的那一项。
///
/// **认不出的类不许消失,也不许冒充自己看懂了**:如实说「bx 报了这一条」并把
/// 那个词原样带上,用户能拿它去搜、去问,而 `ruleRowSeverity` 已经给了它一个
/// 排在已知几类之后、健康之前的位置。
/// 这一行要显示的那句话。**服务端发什么就显示什么** —— 判据只有一份。
///
/// 只在服务端没发(旧 Guardian,或将来某一类忘了写 summary)时才由本地兜底,
/// 而兜底那句**绝不冒充看懂了**:它把认不出的那个 class 原样带上,
/// 与 leakcheck 那条「问不出来就说问不出来」同一条纪律。
func verdictText(_ finding: RuleFinding) -> String {
    let summary = finding.summary.trimmingCharacters(in: .whitespacesAndNewlines)
    if !summary.isEmpty { return summary }
    return finding.cls.isEmpty
        ? "bx flagged this rule, and this version could not say why."
        : "bx flagged this rule (\(finding.cls))."
}

/// 这张表**有半边是问不出来的**时,窗口顶上要说的那句话。两个半边:
/// 规则体检(`list.review`)与失败归因(Core 的 `failing_rules`)。
/// `nil` = 两半都收到了(哪怕两半都一条结论都没有)。
///
/// **`nil` 是「这版没说」/「没问出来」,不是「没有问题」。** 这个窗口的词汇表里
/// 「一行没有副标题」恰恰读作「查过了,健康」;不说这句话,窗口就等于替一份
/// 从没收到过的报告签了字。两个半边各有各的缺席方式:
/// - 体检:旧 Guardian 不发 `review`,Guardian 读得到配置但 `config.Parse` 拒了
///   它时也发 nil;
/// - 失败:Core 不应答时 `CoreRuntime.Reachable=false` 而**其余字段按构造全是
///   零值**(`internal/guardian/types.go`),于是 `failing_rules` 是空的 ——
///   空在那里是「没问出来」,不是「没有规则在失败」。判据因此是 `reachable`,
///   由调用方经 `answeringCore` 给出,**不是**「数组空不空」。
///
/// **一句话报两个半边,不是两条各自独立的横幅。** 两个理由:
/// ① 对用户要更正的是**同一件事** ——「这一行什么都没写」不等于「查过了」;
/// 把同一句更正并排说两遍,只会训练他把顶上那块整个跳过去,而这个窗口的全部
/// 设计纪律就是「只在真有问题时才占地方」。② 摆这张表的地方不止一处,而每一处
/// 都得记得把话带上;两条独立的横幅就是两次机会漏掉其中一条 —— 正是这次要修的
/// 那个缺陷的形状(`failing:` 那半在**每一处**都算错了)。
func ruleWindowCaveatNote(_ list: RuleList, coreAnswering: Bool) -> String? {
    var halves: [String] = []
    if list.review == nil {
        halves.append("this version of bx did not check these rules for problems")
    }
    if !coreAnswering {
        halves.append("bx's core is not answering, so it could not say which rules are failing")
    }
    guard !halves.isEmpty else { return nil }
    return "A rule with nothing written under it here has not been checked: "
        + halves.joined(separator: ", and ") + "."
}

/// 越小越靠前。**有问题的在前,健康的一个字不写** —— 与 Checks 页同一条纪律。
/// 认不出的结论排在已知几类之后、healthy 之前:不丢,也不冒充自己看懂了。
///
/// **Core 不应答时这个排序刻意一个字不改**,尽管那时每一行都落在最后一档。
/// 它读起来像「全都健康」,但那句话是由**并列关系**说出来的:某几行排在别人
/// 后面才等于「这几行更没事」。Core 不应答时失败那半对**每一行**都缺席,没有
/// 任何一行因此被排到另一行后面 —— 排序退化成配置里的原顺序,它什么也没断言;
/// 体检那半若还在,它的几类照旧排到最前,那部分仍然是真的。
/// 反过来,给纯排序再塞一个 `coreAnswering` 参数,就是把 `reachable` 那道判据
/// 抄成第二份(这个仓库反复罚过的形状),换来的东西横幅已经说了。
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
///
/// **它与 `ruleRowSeverity` 不是同一个事实的两面**(这句话此前写在这里,是假的):
/// 两者在 `failures == 0` 上判得不一样 —— 那边要 `failures > 0` 才算「在失败」,
/// 这边只看 `failure != nil`。今天那种行到不了界面(`internal/stats/outcome.go`
/// 的 `ruleWorthReporting` 要求 `Failures >= 5` 才发布一条 FailingRule),所以
/// **要修的是这句话不是这个行为** —— 为一个到不了的输入去改判据,只会让两处
/// 各自为了「一致」而漂。
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
/// 措辞只说**这一条规则会造成什么**与**出路在哪**,不做告诫:「这样有风险哦」
/// 只会被点穿,而「任何人都能在这个平台上注册子域」是可核对的事实。
///
/// **不许说成「用确切主机代替」**:bx 的规则是后缀匹配,`bucket.s3.amazonaws.com`
/// 照样覆盖它的子域,改窄并不能让这条规则通过 —— 那句话会把用户送进一个
/// 永远出不来的循环。真正的出路是旁边那个 Add Anyway。
let riskyDirectRuleWarning =
    "Anyone can register a subdomain on this platform, and a bx direct rule covers every "
    + "subdomain of what you write — so a stranger could make your real IP leave outside "
    + "the tunnel. Writing a deeper host narrows this but does not remove it. "
    + "Use Add Anyway only if you control that host."

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

// MARK: - 删掉了、但还能撤销的那几条

/// 一条已经删掉、而用户还能撤销的规则。
///
/// **它必须活过重画,这就是它存在的全部理由。** 删一条规则刻意不弹确认框
/// (要清掉十一条冗余规则就得点十一次确认,那是在惩罚正确的行为),那句
/// 「Removed … · Undo」是那个确认框的**替身**;而规则窗口自 2026-09-11 起
/// 跟着环境刷新重画(菜单开着时约每 2 秒一次),重画从新鲜数据重建整张表,
/// 而新鲜数据里已经没有这条规则了 —— 于是那个 Undo 会在**两秒后无声消失**。
/// 把一个刻意保留的安全出口的寿命绑在下一次刷新的节拍上,没有人做过这个决定。
struct PendingRuleRemoval: Equatable {
    let kind: RuleKind
    let pattern: String
    /// 删掉那一刻它在表里的行号。重画时按它插回**原位** —— 否则那个 Undo
    /// 会在光标底下跳到别处,而用户正要点它。
    let index: Int
}

/// 表里要摆的一行:一条规则,或者一条等着撤销的删除。
enum RuleTableEntry: Equatable {
    case rule(RuleRow)
    case removed(kind: RuleKind, pattern: String)
}

/// 挂起的删除的上限。**不是性能考虑**:一个只涨不消的集合会让窗口摆出一串
/// 早已不相干的「Removed …」。满了丢最旧的那条 —— 最近那次删除的 Undo 最
/// 可能真的被点到。
let maxPendingRuleRemovals = 16

/// `kind|pattern`。**方向必须进键**:同名规则可以同时出现在 direct 与 proxy 里
/// 且语义相反,只按模式比会让删掉其中一条把另一条也从表里抹掉。
func ruleEntryKey(_ kind: RuleKind, _ pattern: String) -> String {
    kind.rawValue + "|" + pattern
}

/// 挂起的删除对账:**服务端重新报出这条规则 = 那次 Undo 成功了**(从服务端
/// 那半看,成功的 Undo 就长这样),这条挂起退场。
///
/// 判据只有这一条 —— 窗口不去猜某个 Undo 请求的结局,它看数据。
func survivingRuleRemovals(_ pending: [PendingRuleRemoval], freshRows: [RuleRow])
    -> [PendingRuleRemoval]
{
    let present = Set(freshRows.map { ruleEntryKey($0.kind, $0.pattern) })
    return pending.filter { !present.contains(ruleEntryKey($0.kind, $0.pattern)) }
}

/// 把等着撤销的删除**插回**这份数据里,得到表上真正要摆的那几行。
///
/// 挂起的那几条不许同时以两种面目出现:删完那一刻窗口手里还是旧数据(那条
/// 规则仍在里头),刷新之后才没有 —— 两种情形都只摆一行「Removed … · Undo」。
func ruleTableEntries(rows: [RuleRow], pending: [PendingRuleRemoval]) -> [RuleTableEntry] {
    let pendingKeys = Set(pending.map { ruleEntryKey($0.kind, $0.pattern) })
    var out =
        rows
        .filter { !pendingKeys.contains(ruleEntryKey($0.kind, $0.pattern)) }
        .map(RuleTableEntry.rule)
    // 按行号升序插:先插行号小的,后面那条记下的行号才还算得准。
    for item in pending.sorted(by: { $0.index < $1.index }) {
        out.insert(
            .removed(kind: item.kind, pattern: item.pattern),
            at: min(max(item.index, 0), out.count))
    }
    return out
}

/// 一组的副标题:**回答「我该不该勾它」,不是「它叫什么」。**
///
/// 品牌名(Steam / Apple / Tencent)答的是后者,而用户站在这个窗口前想的是
/// **开了会怎样**;展开后的域名清单是证据,不是答案 —— 没有人靠读
/// `*.steamcontent.com` 认出「游戏下载会变快」。
///
/// 三条,每条都对应一次已经付过的学费:
///
///   - **恒非空。** 上一版这一行是「勾选框下面缩进一行小字」,被拿掉的理由是
///     它多半是空的,于是一屏参差不齐的留白。空一半的一列比没有这一列更糟。
///   - **在客户端映射成英文,绝不回显服务端那句 `summary`。** 那句是中文、
///     同时还喂着 `bx preset show`,把它摆进通篇英文的菜单是本仓库栽过的同一个坑
///     (规则窗口曾把 rulereview 的中文 summary 原样渲染出来)。
///   - **认不出的组回落到一句一定成立的话**(它管几个域名),而不是消失、
///     也不是冒充看懂了。新 Guardian 出了一组而菜单还没跟上时,这条路真会走到 ——
///     与 `verdictText` 对认不出的 class 的处置同向。
///
/// 这张表与 Go 那份预设清单的对齐由
/// `TestMacMenuEveryPresetHasAnEnglishSubtitle` 钉住:Go 加了一组而这里没跟上时
/// 它红一次,那正是回来写这句话的时刻 —— 否则新组会静默停在那句回落上,
/// 而回落与「我们想过了、就是没什么可说的」在屏幕上完全一样。
func ruleGroupSubtitle(_ group: RuleGroup) -> String {
    switch group.name {
    case "gaming":
        return "Game downloads and cloud saves come straight from the CDN"
    case "apple":
        return "iCloud sync, Game Center and the App Store stay responsive"
    case "tencent":
        return "WeChat, Tencent Meeting and QQ sign-in, chat and media"
    case "china-cdn":
        return "Chinese apps, video and shopping load from nearby servers"
    default:
        return "\(group.total) domains"
    }
}

/// 「Your own rules (N)」那一行该怎么写。
///
/// **模式改变的是这份列表的含义,所以它必须说在这里,而不是菜单里。**
///
///   - global:这几条 direct 规则**就是**全部的直连集合。删掉一条,那部分流量
///     立刻改走隧道。
///   - split:它们是叠在内建 china 列表(约一万两千条)之上的**例外**,大半可能
///     本来就被那份列表盖住。
///
/// 同一份列表,两种意思 —— 而这个混淆真实造成过一次错判:体检曾把 22 条正在
/// 工作的规则报成「被 china 列表覆盖」,而那台机器是 global、那份列表整个不生效,
/// 照着删会让 22 个域名改走隧道。
///
/// **问不出来时只说条数**(旧 Guardian 不发这个键)—— 绝不替它猜一个模式,
/// 因为猜错的那一半正好会把上面那句话说反。
func customRulesHeading(count: Int, global: Bool?) -> String {
    let head = "Your own rules (\(count))"
    switch global {
    case .some(true):
        return head + " — global mode: these are the only domains that go direct"
    case .some(false):
        return head + " — split mode: exceptions on top of the built-in China list"
    case .none:
        return head
    }
}
