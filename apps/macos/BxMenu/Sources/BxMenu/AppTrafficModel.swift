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

    private static func detail(for row: AppTrafficRow) -> String {
        let conns = "\(row.conns) connection\(row.conns == 1 ? "" : "s")"
        return "\(conns) · \(formatByteCount(row.bytesUp)) up · \(formatByteCount(row.bytesDown)) down"
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
