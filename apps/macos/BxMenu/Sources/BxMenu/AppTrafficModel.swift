import Foundation

/// GET /v1/apps 里三条路径的固定顺序。**永远这三个,永远这个顺序** ——
/// 渲染层按下标摆行,顺序漂移会让「隧道」「直连」「已拦截」在界面上跳来跳去。
enum AppTrafficPath: String, Decodable, Equatable {
    case tunnel, direct, blocked
}

/// 一个应用在某条路径上的流量统计。
struct AppTrafficRow: Decodable, Equatable {
    let app: String // 空串 = unknown,渲染层必须显式处理,不能留空白
    let conns: Int
    let bytesUp: Int64
    let bytesDown: Int64
    let rules: [String]

    /// 这一行的**代表**可执行路径,空串 = 问不出来(unknown 行、或 Core 侧读
    /// kern.procargs2 失败)。唯一的用途是取图标;**空串时不许画占位** ——
    /// 一格空白的占位图不是「没有图标」,是「这个应用的图标长这样」。
    ///
    /// Go 侧是 omitempty,所以这个键**经常整个缺席**,必须 decodeIfPresent。
    let execPath: String

    /// 这一行**此刻**的速率(每秒字节数),由服务端按端口做差算出
    /// (`appattr.DiffPortRates`)。**不是客户端拿相邻两次报告的行做差** ——
    /// 那是这个字段取代的旧做法(`AppTrafficRateTracker`,已删):60 秒滚动
    /// 窗口下,一行的累计字节会因为记录滑出窗口而下降,客户端把"变小"误读成
    /// "计数器复位"从而显示成破折号,而什么都没出错。
    ///
    /// nil = 还没有速率可报(第一次采样之前,或这次报告根本还没就绪);
    /// 指向 0 的值 = 量出来的 0(应用在,但这一拍没有字节增量)。两者不是
    /// 同一件事,压成同一个 0 会让界面把"不知道"显示成"闲着"。
    let bytesUpRate: Double?
    let bytesDownRate: Double?

    /// 这一行**最近**连过的目的地,去重、最多 8 条(与 Go 侧
    /// `appattr.maxDestsPerRow` 同一个上限,这里不重抄那个数字,只解码结果)。
    /// omitempty:一条目的地都没有(这一行还没连过任何地方,或问不出来)时
    /// 键整个缺席,必须落成空数组,不能让整份应答解码失败。
    let dests: [String]
    /// 超出上限、没能列出来的**去重后**目的地条数。omitempty:0 时键缺席——
    /// 「没有更多」与「问不出来」都落成 0,窗口那半不需要区分。
    let destsMore: Int

    enum CodingKeys: String, CodingKey {
        case app, conns
        case bytesUp = "bytes_up"
        case bytesDown = "bytes_down"
        case rules
        case execPath = "exec_path"
        case bytesUpRate = "bytes_up_rate"
        case bytesDownRate = "bytes_down_rate"
        case dests
        case destsMore = "dests_more"
    }

    /// **必须手写。** 服务端对 `rules` 用 omitempty——一条规则都没有命中是
    /// 完全正常的状态,这个键会整个缺席。合成的 Decodable 不认默认值,会让
    /// 这种正常状态直接解码失败(RulesModel 那次就是这么被抓到的)。
    /// `bytes_up_rate`/`bytes_down_rate` 同样是 omitempty(见 Go 侧
    /// AppRow),键缺席 ⇒ nil ⇒ 渲染成 appTrafficRateUnavailable;`dests`/
    /// `dests_more` 也是 omitempty,键缺席 ⇒ 空数组 / 0。**不要**给它们写
    /// 属性默认值再指望合成的 Decodable。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        app = try c.decode(String.self, forKey: .app)
        conns = try c.decode(Int.self, forKey: .conns)
        bytesUp = try c.decode(Int64.self, forKey: .bytesUp)
        bytesDown = try c.decode(Int64.self, forKey: .bytesDown)
        rules = try c.decodeIfPresent([String].self, forKey: .rules) ?? []
        execPath = try c.decodeIfPresent(String.self, forKey: .execPath) ?? ""
        bytesUpRate = try c.decodeIfPresent(Double.self, forKey: .bytesUpRate)
        bytesDownRate = try c.decodeIfPresent(Double.self, forKey: .bytesDownRate)
        dests = try c.decodeIfPresent([String].self, forKey: .dests) ?? []
        destsMore = try c.decodeIfPresent(Int.self, forKey: .destsMore) ?? 0
    }

    init(app: String, conns: Int, bytesUp: Int64, bytesDown: Int64, rules: [String] = [],
         execPath: String = "", bytesUpRate: Double? = nil, bytesDownRate: Double? = nil,
         dests: [String] = [], destsMore: Int = 0) {
        self.app = app
        self.conns = conns
        self.bytesUp = bytesUp
        self.bytesDown = bytesDown
        self.rules = rules
        self.execPath = execPath
        self.bytesUpRate = bytesUpRate
        self.bytesDownRate = bytesDownRate
        self.dests = dests
        self.destsMore = destsMore
    }
}

/// 三组里的一组。
struct AppTrafficGroup: Decodable, Equatable {
    let path: AppTrafficPath
    let rows: [AppTrafficRow]

    enum CodingKeys: String, CodingKey { case path, rows }

    /// **必须手写。** `rows` 为空时服务端序列化成 `null`(Go 里未赋值的切片),
    /// 不是 `[]` —— 合成的 Decodable 对 `null` 会解成功但只有当类型是
    /// Optional 时;这里的字段不是 Optional,必须显式用 decodeIfPresent 兜底,
    /// 否则「这条路径上一个应用都没有」这种正常状态会解码失败。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = try c.decode(AppTrafficPath.self, forKey: .path)
        rows = try c.decodeIfPresent([AppTrafficRow].self, forKey: .rows) ?? []
    }

    init(path: AppTrafficPath, rows: [AppTrafficRow] = []) {
        self.path = path
        self.rows = rows
    }
}

/// `report` 键底下那一层。
struct AppTrafficReportBody: Decodable, Equatable {
    let groups: [AppTrafficGroup]

    enum CodingKeys: String, CodingKey { case groups }

    /// **必须手写。** 出错那一路服务端发布的是 Go 里 `appattr.Report` 的零值,
    /// `groups` 序列化成 `null`,不是 `[]`。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        groups = try c.decodeIfPresent([AppTrafficGroup].self, forKey: .groups) ?? []
    }

    init(groups: [AppTrafficGroup] = []) {
        self.groups = groups
    }
}

/// GET /v1/apps 的完整应答。**三态刻意分开发布,这一层绝不合并或压平**:
/// 没人订阅 / 订阅了但问不出来 / 订阅了且确实没有连接,是三种不同的事实,
/// 合成一句话就是把「没在采集」说成「没有流量」这样一句自洽的假话。
struct AppTrafficReport: Decodable, Equatable {
    let subscribed: Bool
    let report: AppTrafficReportBody
    let error: String

    enum CodingKeys: String, CodingKey { case subscribed, report, error }

    /// **必须手写。** `error` 用 omitempty——没有错误(绝大多数情况)时这个键
    /// 整个缺席,合成的 Decodable 会让这个完全正常的应答直接解码失败。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        subscribed = try c.decode(Bool.self, forKey: .subscribed)
        report = try c.decodeIfPresent(AppTrafficReportBody.self, forKey: .report) ?? AppTrafficReportBody()
        error = try c.decodeIfPresent(String.self, forKey: .error) ?? ""
    }

    init(subscribed: Bool, report: AppTrafficReportBody = AppTrafficReportBody(), error: String = "") {
        self.subscribed = subscribed
        self.report = report
        self.error = error
    }

    /// 界面上的一行:要么是一组的标题,要么是一条应用记录,要么是一句说明
    /// (三种空状态各自的那一句)。三者互斥,用 enum 而不是一堆可选字段,
    /// 免得渲染层自己去猜「现在该显示哪个」。
    enum Row: Equatable {
        case sectionHeader(String)
        case entry(Entry)
        case notice(String)
        /// 一个组在**当前过滤**下没有匹配。**只在有查询串时才发这一行** ——
        /// 没有过滤时的空组保持原有行为(整个跳过,连标题都不发),别顺手改。
        ///
        /// 三个分组的标题永远都在,过滤时也不消失:分组随着输入一个个消失再
        /// 出现,读者无从判断「这个组里没有匹配」与「这个组本来就是空的」。
        case emptySection(String)
    }

    /// 界面上的一条应用记录,**字段是分开的,不是一句散文**。
    ///
    /// 上一版把它拼成 `1 connection · 48 B up · 48 B down · your rule: …`,
    /// 于是两行之间没法比大小 —— 一屏参差不齐的散文正是「感觉 iStat 做得更好」
    /// 的那一半。分开之后窗口才能把它们摆成右对齐的列;列的定义在
    /// `appTrafficColumnTitles`/`appTrafficNumericColumns`,窗口只照着摆。
    ///
    /// 全是**已经格式化好的字符串**:格式化(单位、每秒、没有速率时那一横)是
    /// 判断,判断归纯模型;窗口那半在 CI 里编不了,放过去等于没测。
    struct Entry: Equatable {
        let app: String
        /// 取图标用;空串 = 不画图标(不是画一个空白占位)。
        let execPath: String
        let conns: String
        /// **速率与累计并存,两个都要**:速率答「现在多快」,累计答「一共多少」。
        /// 没有前一份快照时速率是 `appTrafficRateUnavailable`,不是 0。
        let upRate: String
        let downRate: String
        let upTotal: String
        let downTotal: String
        /// 用户自己写的那条规则的原文;空串 = 内建列表判的,用户改不了、不提。
        let rule: String
        /// 应用名格下面那一行暗色小字:第一条目的地 + 其余去重后的条数
        /// (`+N`,N = `dests.count - 1 + destsMore`,N 为 0 时不写 `+0`)。
        /// **nil = 没有目的地,这一行不加**(不是留一行空白 —— 空串会让窗口
        /// 画出一行空白)。判据全在 `AppTrafficReport.destSummary`,窗口只把
        /// 算好的字符串塞进 label。
        let destSummary: String?
        /// 完整目的地清单,给 toolTip 用(整格):每行一条 `dests`,
        /// `destsMore > 0` 时末尾加一行 `…and N more`。没有目的地时是空串——
        /// 与规则列同一惯例,窗口那半对空串不设 toolTip。
        let destTooltip: String
        /// 原始目的地(去重、最多 8 条,与报告同源)。**右键菜单按它生成**
        /// (`appTrafficRuleMenu(dests:)`);摘要与 toolTip 都是格式化过的字符串,
        /// 从它们反推域名是第二份解析。
        let dests: [String]
    }

    /// 组固定顺序(tunnel / direct / blocked),渲染层照抄这个顺序摆。
    private static let order: [AppTrafficPath] = [.tunnel, .direct, .blocked]

    static func sectionTitle(for path: AppTrafficPath) -> String {
        switch path {
        case .tunnel: return "Through the tunnel"
        case .direct: return "Direct"
        case .blocked: return "Blocked"
        }
    }

    /// 把三态 + 三组渲染成界面要显示的行。
    ///
    /// **三种「空」互不相同,顺序也刻意如此(先判有没有在采集,再判查不查得
    /// 出来,最后才轮到「确实没有」)**:
    /// ① `subscribed == false`——没在采集,不能说成没有流量;
    /// ② `error` 非空——采集着但这一次问不出来,不能说成没有流量;
    /// ③ 采集着、没错误、三组全空——这才是真的「现在没有连接」。
    /// 把任意两句合并,就是把「没在采集」或「没查到」悄悄说成「没有流量」,
    /// 那是一句自洽的假话。
    ///
    /// `query` 为空串(默认)时**与今天完全相同的行序列**——空组照旧整个跳过,
    /// 不发 `emptySection`;非空时按应用名 / 目的地 / 规则原文过滤,组标题
    /// 永远都在,过滤后没有匹配的组补一条 `.emptySection`(而不是让标题自己
    /// 消失,那会让读者分不清「这个组没有匹配」与「这个组本来就是空的」)。
    func rows(query: String = "") -> [Row] {
        guard subscribed else {
            return [.notice("Not collecting app traffic right now.")]
        }
        guard error.isEmpty else {
            return [.notice("Couldn't read app traffic: \(error)")]
        }
        let needle = query.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let byPath = Dictionary(
            report.groups.map { ($0.path, $0) },
            uniquingKeysWith: { first, _ in first }
        )
        var out: [Row] = []
        for path in AppTrafficReport.order {
            guard let group = byPath[path], !group.rows.isEmpty else { continue }
            let matched = needle.isEmpty ? group.rows : group.rows.filter { Self.matches($0, needle: needle) }
            out.append(.sectionHeader(AppTrafficReport.sectionTitle(for: path)))
            if matched.isEmpty {
                // 只有 needle 非空时才可能走到这里(上面的 guard 已经保证
                // group.rows 本身非空,needle 为空时 matched == group.rows)。
                out.append(.emptySection("No matches"))
            } else {
                for row in matched {
                    out.append(.entry(AppTrafficReport.entry(for: row)))
                }
            }
        }
        guard !out.isEmpty else {
            return [.notice("No app traffic seen yet.")]
        }
        return out
    }

    /// 搜索框的匹配判据:应用名、任一目的地、任一规则原文,命中其一即可,
    /// 大小写不敏感(`needle` 由调用方先 trim 再转小写)。
    ///
    /// **目的地参与匹配是刻意的**:输入一个域名就能反查「谁在连它」,这是
    /// 「某些 app 偷偷连别的服务器」这个用例的另一半。**匹配要看全部目的地,
    /// 不只看界面上显示的第一条**——只匹配显示出来的那条会让搜索结果与用户
    /// 看到的对不上,而那种不一致没有任何提示。
    private static func matches(_ row: AppTrafficRow, needle: String) -> Bool {
        if displayName(row.app).lowercased().contains(needle) { return true }
        if row.dests.contains(where: { $0.lowercased().contains(needle) }) { return true }
        if row.rules.contains(where: { $0.lowercased().contains(needle) }) { return true }
        return false
    }

    /// 把一行报告折成界面上那一行的**各个格子**。
    ///
    /// **`rules` 有话说时才占地方。** 服务端只在**用户自己写的规则**命中时填它
    /// (命中内建 china 列表时给空串,见 `appattr.ConnRecord.Rule`),所以它天然
    /// 稀疏,而且恰好是唯一可行动的那一半:用户能去改的只有自己写的那几行。
    /// 无差别地给每一行都加一句会让它变成墙纸(与项目所有者否掉「Direct rules:
    /// N unreachable」常驻红字同一条判断);反过来,把它整个丢掉就是把「你的
    /// Steam 之所以直连,是因为你自己写的 *.steamstatic.com」这条唯一的归因扔掉。
    /// 内建列表判的那些行 `rule` 是空串,窗口那一格就留空 —— 说成 rule 会让用户
    /// 去找一条配置文件里根本不存在的行。
    ///
    /// **rate 为 nil 不是 0**:那一格渲染成 `appTrafficRateUnavailable`。速率
    /// 现在由**服务端**按端口做差算出、随行一起发下来(`row.bytesUpRate`/
    /// `row.bytesDownRate`),不再需要客户端拿相邻两次报告做差 —— 也就不再需要
    /// 单独的速率表或按 (组, 应用名) 的键。
    private static func entry(for row: AppTrafficRow) -> Entry {
        Entry(
            app: displayName(row.app),
            // unknown 行按构造没有路径(Core 侧那一行的 owner 是零值),这里不用
            // 再判一次 —— 但也不去替它编一个。
            execPath: row.execPath,
            conns: "\(row.conns)",
            upRate: formatRate(row.bytesUpRate),
            downRate: formatRate(row.bytesDownRate),
            upTotal: formatByteCount(row.bytesUp),
            downTotal: formatByteCount(row.bytesDown),
            rule: row.rules.joined(separator: ", "),
            destSummary: destSummary(dests: row.dests, destsMore: row.destsMore),
            destTooltip: destTooltip(dests: row.dests, destsMore: row.destsMore),
            dests: row.dests
        )
    }

    /// 空串 app 是 unknown,渲染成一句人话,不是空白行——搜索框按应用名匹配
    /// 时用的也是这个同一份显示名,免得「显示的是 Unknown app,搜 unknown 却
    /// 搜不到」这种对不上的情况。
    private static func displayName(_ app: String) -> String {
        app.isEmpty ? "Unknown app" : app
    }

    /// 应用名格下面那一行暗色小字:第一条目的地 + 其余去重后的条数。
    ///
    /// `remaining` = 未显示出来的目的地条数 = `dests.count - 1`(那一份列表里
    /// 除了已经显示的第一条之外还有几条)+ `destsMore`(超出上限、连列表里都
    /// 没放的条数)。**`+N` 是承重的,不是装饰**:一个应用连了 1 个地方和
    /// 连了 23 个地方是完全不同的两件事,这正是「偷偷连别的服务器」最直接的
    /// 信号,所以 N 只在真的 > 0 时才写(0 就不写 `+0`)。
    ///
    /// **nil = 没有目的地,这一行不加** —— 不是留一行空白,那是另一句话。
    static func destSummary(dests: [String], destsMore: Int) -> String? {
        guard let first = dests.first else { return nil }
        let remaining = dests.count - 1 + destsMore
        guard remaining > 0 else { return first }
        return "\(first) +\(remaining)"
    }

    /// 完整目的地清单,给 toolTip 用:一行一条 `dests`,`destsMore > 0` 时
    /// 末尾加一行 `…and N more`。与规则列同一条纪律(「凡是会截断的格子必须
    /// 同时给出看全的办法」,这个窗口不横向滚动)——`+N` 那一行本身就是被
    /// 摘要压缩过的,toolTip 是唯一能看到完整清单的地方。
    ///
    /// 没有目的地时是空串,不是某种「没有」的占位文案:窗口那半对空串的既有
    /// 惯例是不设 toolTip(见 `rule(_:)`/`appName(_:)`),这里保持一致。
    static func destTooltip(dests: [String], destsMore: Int) -> String {
        guard !dests.isEmpty else { return "" }
        var lines = dests
        if destsMore > 0 {
            lines.append("…and \(destsMore) more")
        }
        return lines.joined(separator: "\n")
    }
}

/// 右键一行时可加的规则候选,按**目的地**生成。
///
/// **规则的粒度是目的地,不是应用** —— bx 没有按应用的规则,而窗口每行本来就
/// 带着这个应用最近连过的目的地;腾讯会议那半小时排查要的正是「把这些域名直连」。
///
/// 形状:三段以上的域名给「精确 + `*.父域`」(`cdn.steamstatic.com` 通常是整个
/// `steamstatic.com` 都想直连,但也可能只想钉这一个);两段给 `*.host`(bx 的
/// `*.a.com` 语义含 `a.com` 本身);单段名与 IP 字面量原样。归一化(小写、去尾点)
/// 与 Go 侧 config.NormalizeHostName 同向 —— 报给 Guardian 的必须是配置里那一行
/// 会长的样子。空串什么都不给:一个空模式会被 Guardian 拒绝,但在这之前它已经
/// 出现在菜单里当了一个可点的项。
func ruleCandidates(for dest: String) -> [String] {
    var host = dest.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    while host.hasSuffix(".") { host.removeLast() }
    guard !host.isEmpty else { return [] }
    // IP 字面量(v4 只有数字和点;v6 含冒号)原样 —— 通配对 IP 没有意义。
    if host.contains(":") || host.allSatisfy({ $0.isNumber || $0 == "." }) {
        return [host]
    }
    let labels = host.split(separator: ".", omittingEmptySubsequences: false).map(String.init)
    guard !labels.contains(where: \.isEmpty) else { return [] }
    let candidates: [String]
    switch labels.count {
    case 1:
        candidates = [host]
    case 2:
        candidates = ["*." + host]
    default:
        candidates = [host, "*." + labels.dropFirst().joined(separator: ".")]
    }
    // 右键是**一键动作**,没有确认框 —— 把 Guardian 会拒绝的通配候选摆上去,
    // 用户点下去只看到一句失败。故在生成候选这一步就不摆出来;确切主机不受影响。
    return candidates.filter { !wildcardOnOpenPlatform($0) }
}

/// 任何人都能注册子域的平台。**与 internal/policy 的 riskyDirect 逐字相同**,
/// 由 TestOpenPlatformListMatchesPolicy 钉住。
let openSubdomainPlatforms: [String] = [
    "aliyuncs.com", "myqcloud.com", "bcebos.com", "qiniucdn.com", "qbox.me", "clouddn.com", "upaiyun.com", "myhuaweicloud.com",
    "amazonaws.com", "cloudfront.net", "core.windows.net", "googleapis.com", "r2.dev", "workers.dev", "pages.dev", "github.io", "vercel.app", "netlify.app", "b-cdn.net",
]

/// 这条通配规则会不会落在「任何人都能注册子域」的平台上。
///
/// **它不是第二道门** —— 门在 Guardian(policy.DirectRuleHazard)。这里只决定
/// 右键要不要把某个候选摆出来:右键是一键动作、没有确认框,把危险选项摆上去再
/// 靠服务端拒绝,用户看到的是「点了只弹一句失败」。
func wildcardOnOpenPlatform(_ pattern: String) -> Bool {
    var p = pattern.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    while p.hasSuffix(".") { p.removeLast() }
    guard p.hasPrefix("*.") else { return false }
    let body = String(p.dropFirst(2))
    return openSubdomainPlatforms.contains { body == $0 || body.hasSuffix("." + $0) }
}

/// 右键菜单里的一项:一条候选模式 × 一个方向。`kind` 是 Guardian /v1/rules
/// 认的字面量(direct / proxy),这里不引 RuleKind —— 纯模型要能单独编进套件。
struct AppTrafficRuleMenuItem: Equatable {
    let title: String
    let kind: String
    let pattern: String
}

/// 一行的右键菜单:每个目的地的每条候选,先 direct 再 proxy。同一个通配被两个
/// 目的地推出来时**只出现一次**(两个 helper 域名同属一个父域是常态)。
func appTrafficRuleMenu(dests: [String]) -> [AppTrafficRuleMenuItem] {
    var seen = Set<String>()
    var out: [AppTrafficRuleMenuItem] = []
    for dest in dests {
        for pattern in ruleCandidates(for: dest) where seen.insert(pattern).inserted {
            out.append(AppTrafficRuleMenuItem(title: "Always direct: \(pattern)", kind: "direct", pattern: pattern))
            out.append(AppTrafficRuleMenuItem(title: "Always through tunnel: \(pattern)", kind: "proxy", pattern: pattern))
        }
    }
    return out
}

/// 速率那一格在**没有速率可报**时显示的东西。
///
/// 一个破折号,不是 `0 B/s` —— 后者是一句断言(「此刻没有流量」),而这里的事实
/// 是「还没有第二份快照可以跟它做差」。
let appTrafficRateUnavailable = "—"

/// 把每秒字节数渲染成一格;nil = 没有速率。
func formatRate(_ bytesPerSecond: Double?) -> String {
    guard let bytesPerSecond else { return appTrafficRateUnavailable }
    return formatByteCount(Int64(bytesPerSecond.rounded())) + "/s"
}

/// 列标题。**图标不再单独占一列** —— 图标搬进应用名那一格(与名字同一个
/// `NSStackView`),消灭的是「图标列宽」这一整类问题:上一版那一列被真机截图
/// 撑到 ~350pt,把规则列挤没了。Finder、活动监视器都是把图标和名字放在同一格里。
///
/// 标题只出现一次(在整张表最上面),不是每组重复一遍 —— 三组各来一行标题会把
/// 这个窗口变成一屏表头。
let appTrafficColumnTitles = ["App", "Conns", "Up/s", "Down/s", "Up", "Down", "Rule"]

/// 哪几列是数字列 —— 也就是**必须右对齐**的那几列。
///
/// 判据住在这里而不是窗口里,是为了让它可测:窗口那半在 CI 里编不了。窗口的义务
/// 只有一条 —— 遍历这个下标表,把每一列摆成 trailing;由 Go 侧的文本守卫钉住。
///
/// **图标列去掉之后,下标全部往前挪了一位** —— 这是这个改动里最容易静默出错
/// 的一处:下标错位不会有任何编译错误,现象只是「右对齐落在错的列上」。
/// 应用名列(0)、规则列(最后一列,变长文本)不在其中。
let appTrafficNumericColumns = [1, 2, 3, 4, 5]

/// 从可执行路径回到用户认得的那个 **.app 包**。
///
/// `NSWorkspace.icon(forFile:)` 对 `…/Contents/MacOS/Slack` 给的是通用可执行文件
/// 图标 —— 一整列长一个样,等于没有图标。取**最外层**的 `.app`:Chrome 的 helper
/// 嵌在外层包里(`…/Google Chrome.app/…/Google Chrome Helper.app/…`),取里层会得到
/// 一个用户没见过的图标。
///
/// 不是 app bundle 的普通可执行文件(ssh、node)原样返回,由系统给它一个通用图标。
/// **空串原样返回** —— 不给一个不存在的路径编出一个存在的来。
func appIconPath(forExecutable execPath: String) -> String {
    guard let range = execPath.range(of: ".app/") else { return execPath }
    return String(execPath[execPath.startIndex..<range.lowerBound]) + ".app"
}

/// 简单的、跟 locale 无关的字节数格式化 —— 只为这个纯模型服务,不依赖
/// `ByteCountFormatter`(它的输出会随系统 locale/单位偏好变化,测试与界面
/// 都不该依赖一个会漂移的格式)。
func formatByteCount(_ n: Int64) -> String {
    let units = ["B", "KB", "MB", "GB", "TB"]
    var value = Double(n)
    var unitIndex = 0
    while value >= 1024, unitIndex < units.count - 1 {
        value /= 1024
        unitIndex += 1
    }
    if unitIndex == 0 {
        return "\(n) B"
    }
    return String(format: "%.1f %@", value, units[unitIndex])
}

/// 这一版 Guardian 提不提供 /v1/apps。
///
/// **绝不「试着拨一下看看」**:旧 Guardian 不认识这条路径,回的是 404 —— 而
/// 客户端无从区分「这版不支持」与「这版支持但此刻没数据」,画出来的菜单项每次
/// 点都失败,而 404 在菜单上根本表达不出来。
///
/// **nil 与 [] 是两件事**:nil 是「这版压根没声明过能力」(旧版,键缺席),
/// [] 是「声明了、一个都没有」。两者都不开这个入口,但不是同一件事,而
/// `GuardianStatus.capabilities` 刻意保留了这个区分。
///
/// 判据与 `rulesEditingAvailable` / `serverSwitchingAvailable` 同源;能力名
/// 与 Go 侧 `guardian.CapabilityApps` 手抄对齐(跨语言,没有守卫能同时钉住两边)。
func appTrafficAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("apps")
}

/// 窗口开着时的心跳间隔。
///
/// **它同时是订阅的续期节拍**:Core 侧的采集订阅靠每一次拉取续期,TTL 30 秒、
/// 惰性结算。窗口关掉就没有人再拉,订阅在一个 TTL 内自己过期、采集停掉、
/// 缓冲清空 —— 「没人看时不问内核、不记字节、不攒历史」就是这么成立的,
/// 不需要一条退订路径(菜单被强杀时也没人来退订,而 TTL 一视同仁)。
/// **旧说法「开销精确为零」在 2026-08-20 之后不再成立,别照抄**:Core 侧未订阅时
/// 仍维护一张活连接表(否则订阅前建好的长连接永远看不见),隐私前提不受影响 ——
/// 表里只有端口与判定,没有应用名。
/// **5 秒不是「越勤越好」里挑的一个数,是两个下界之间的取值。** 上界是订阅
/// TTL(必须留出数倍余量,否则订阅会在两次刷新之间过期);下界是**代价** ——
/// 每一拍都让 root 侧的 Core 跑一次 `OwnersByPort()`(两张 pcblist + 逐 PID
/// 解名)。3 秒对 TTL 是 10 倍余量,比续期真正需要的密一个数量级;5 秒仍有
/// 6 倍余量,开销减半,而界面上看不出区别。
///
/// **上下界都由守卫钉住**(`TestMenuAppTrafficRefreshIntervalMatchesTheGoTTL`):
/// 只钉上界会让「为了更跟手」调到 0.5 秒这种改动一路绿灯,而 root 侧的扫描
/// 频率翻十倍。
let appTrafficRefreshSeconds: TimeInterval = 5

/// Core 侧 `appTrafficTTL`。**跨语言手抄的一个数**(Go 侧未导出,Swift 拿不到),
/// 只用来在测试里钉住上面那个间隔留了足够余量;它变了这边不会红,与
/// `guardianStatusWatchTimeout` 那一对是同一个局限。
let appTrafficSubscriptionTTLSeconds: TimeInterval = 30

/// 界面底部那句免责声明。
///
/// **不是可选的。** 归因把字节数记在源端口上,而端口会被复用 —— 上一条连接的
/// 残留字节会算到新连接头上。spec 明写「界面不该把它显示成精确账」:只说
/// 「近似」会被读成「四舍五入」,所以这句话必须同时点明来源(端口复用),
/// 那是用户发现数字对不上时唯一能自洽的解释。
/// 界面底部那句「字节数是近似值」。**两个理由都写出来,因为用户会分别撞到它们。**
///
/// ① 端口复用:按源端口记账,端口换主人时旧账可能算到新连接头上。
/// ② **同一个 socket 上并存的多条流共享一份字节账**,而 `appattr.Aggregate` 把
///    这份账整个记给该端口**最近**的那条记录(`counted[pk]`)。于是一个会议
///    socket 同时打 STUN(直连)与 TURN(隧道)时,它在两个组里各有一行,而字节
///    **全在其中一行**,另一行是 0。
///
/// **② 是 2026-08-24 之后才看得见的。** 在那之前活连接表按端口记、并存的流被
/// 压成一条,两行合成了一行,「一行有字节一行是 0」这个现象根本不存在;把并存
/// 的流拆开是对的(连接数与分组终于对了),但它顺带让这个一直存在的近似露了出来。
/// 一个显示 0 B 却明明有活连接的行,不说明白就会被读成「这条流是闲的」。
///
/// 底部这两句(连同「报告只覆盖最近 60 秒」)是**整份数据的性质**,不是某几行的
/// 性质 —— 与那句已经删掉的「订阅前的连接可能只出现在一个组里」不同,后者是行内
/// 注记该干的事,而它本身在并存流被拆开之后已经不成立了。
let appTrafficApproximateNote =
    "Byte counts are approximate: ports get reused, and an app listed in two sections "
    + "may show all its bytes on one side."

/// 右键能加规则这件事要有人告诉用户 —— 右键菜单是发现不了的。只在这一版 Guardian
/// 支持规则编辑时显示(窗口按 `ruleEditingAvailable` 决定),旧版一个字不提。
let appTrafficRuleHint = "Right-click an app to always send one of its destinations direct or through the tunnel."

/// 连续失败多少次之后,就不再把手上那份快照当作「此刻的事实」。
///
/// 3 次 × `appTrafficRefreshSeconds` ≈ 15 秒 —— 短到用户还记得自己刚做了什么,
/// 长到一次瞬时失败(Guardian 正忙、Core 刚重启)不会让界面闪一下。
let appTrafficStaleAfterFailures = 3

/// 连续失败到一定次数之后要盖在窗口上的那句话;还没到就返回 nil。
///
/// **窗口静默冻在上一份快照上是这个界面最坏的失效模式。** 那份快照读起来是
/// 「这些应用**此刻**正在走隧道」,而此刻保护可能已经被关掉了 —— 与 watch
/// 那条记过的失效模式同一形状:静默失效时界面停在最后一次收到的状态上,
/// 看起来完全正常。
///
/// 措辞刻意只说**观测到的事实**(拉不到了、显示的是上一份),对原因只给一句
/// 可能性而不断言 —— bx 在这条路上分不清「保护被关了」「Guardian 正忙」
/// 「Core 刚重启」,断言其中一个就是编一个自己没查过的答案。
func appTrafficStaleNotice(consecutiveFailures: Int) -> String? {
    guard consecutiveFailures >= appTrafficStaleAfterFailures else { return nil }
    return "Not updating — this is the last report bx could read. Protection may be off."
}
