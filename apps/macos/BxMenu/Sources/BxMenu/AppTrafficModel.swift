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

    enum CodingKeys: String, CodingKey {
        case app, conns
        case bytesUp = "bytes_up"
        case bytesDown = "bytes_down"
        case rules
    }

    /// **必须手写。** 服务端对 `rules` 用 omitempty——一条规则都没有命中是
    /// 完全正常的状态,这个键会整个缺席。合成的 Decodable 不认默认值,会让
    /// 这种正常状态直接解码失败(RulesModel 那次就是这么被抓到的)。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        app = try c.decode(String.self, forKey: .app)
        conns = try c.decode(Int.self, forKey: .conns)
        bytesUp = try c.decode(Int64.self, forKey: .bytesUp)
        bytesDown = try c.decode(Int64.self, forKey: .bytesDown)
        rules = try c.decodeIfPresent([String].self, forKey: .rules) ?? []
    }

    init(app: String, conns: Int, bytesUp: Int64, bytesDown: Int64, rules: [String] = []) {
        self.app = app
        self.conns = conns
        self.bytesUp = bytesUp
        self.bytesDown = bytesDown
        self.rules = rules
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
        case entry(app: String, detail: String)
        case notice(String)
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
    func rows() -> [Row] {
        guard subscribed else {
            return [.notice("Not collecting app traffic right now.")]
        }
        guard error.isEmpty else {
            return [.notice("Couldn't read app traffic: \(error)")]
        }
        let byPath = Dictionary(
            report.groups.map { ($0.path, $0) },
            uniquingKeysWith: { first, _ in first }
        )
        var out: [Row] = []
        for path in AppTrafficReport.order {
            guard let group = byPath[path], !group.rows.isEmpty else { continue }
            out.append(.sectionHeader(AppTrafficReport.sectionTitle(for: path)))
            for row in group.rows {
                out.append(.entry(
                    app: row.app.isEmpty ? "Unknown app" : row.app,
                    detail: AppTrafficReport.detail(for: row)
                ))
            }
        }
        guard !out.isEmpty else {
            return [.notice("No app traffic seen yet.")]
        }
        return out
    }

    /// 一条应用记录的副标题。
    ///
    /// **`rules` 有话说时才占地方。** 服务端只在**用户自己写的规则**命中时填它
    /// (命中内建 china 列表时给空串,见 `appattr.ConnRecord.Rule`),所以它天然
    /// 稀疏,而且恰好是唯一可行动的那一半:用户能去改的只有自己写的那几行。
    /// 无差别地给每一行都加一句会让它变成墙纸(与项目所有者否掉「Direct rules:
    /// N unreachable」常驻红字同一条判断);反过来,把它整个丢掉就是把「你的
    /// Steam 之所以直连,是因为你自己写的 *.steamstatic.com」这条唯一的归因扔掉。
    ///
    /// 措辞点名「your rule」:内建列表的判定用户改不了,说成「rule」会让他去
    /// 找一条配置文件里根本不存在的行。
    private static func detail(for row: AppTrafficRow) -> String {
        let conns = "\(row.conns) connection\(row.conns == 1 ? "" : "s")"
        let base = "\(conns) · \(formatByteCount(row.bytesUp)) up · \(formatByteCount(row.bytesDown)) down"
        guard !row.rules.isEmpty else { return base }
        return "\(base) · your rule: \(row.rules.joined(separator: ", "))"
    }
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
/// 缓冲清空 —— 「没人看时开销精确为零」就是这么成立的,不需要一条退订路径
/// (菜单被强杀时也没人来退订,而 TTL 一视同仁)。
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
let appTrafficApproximateNote = "Byte counts are approximate (ports get reused)."

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
