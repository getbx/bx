import Foundation

/// GET /v1/logs 的应答。**判据都在这个文件里**(AppKit 那半编不进套件)。
struct LogTail: Decodable, Equatable {
    let name: String
    let path: String
    /// 空数组 = 这份日志是空的;读不到时服务端给空数组 + unavailable。
    let lines: [String]
    /// 非空 = 没读到;原因不在这里(它在 Guardian 自己的日志里),这里只说没读到。
    let unavailable: String

    enum CodingKeys: String, CodingKey { case name, path, lines, unavailable }

    /// **必须手写。** Swift 合成的解码器不用属性默认值,而 `unavailable` 是 omitempty。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        path = try c.decodeIfPresent(String.self, forKey: .path) ?? ""
        lines = try c.decodeIfPresent([String].self, forKey: .lines) ?? []
        unavailable = try c.decodeIfPresent(String.self, forKey: .unavailable) ?? ""
    }

    init(name: String, path: String, lines: [String], unavailable: String = "") {
        self.name = name
        self.path = path
        self.lines = lines
        self.unavailable = unavailable
    }
}

struct LogsReport: Decodable, Equatable {
    let logs: [LogTail]

    enum CodingKeys: String, CodingKey { case logs }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        logs = try c.decodeIfPresent([LogTail].self, forKey: .logs) ?? []
    }

    init(logs: [LogTail]) { self.logs = logs }
}

/// 缺省向 Guardian 要多少行(服务端上限 2000)。
let logsDefaultLineCount = 200

/// 这一版 Guardian 支不支持 /v1/logs。**能力键缺席 = 旧版**,不是「不支持」的
/// 同义反复:少了这个判断菜单会对着一个每次点都 404 的按钮。
func logsAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("logs")
}

/// 日志里含失败码的行下标。Guardian 日志的形状是 `guardian_xxx … code=… err=…`,
/// 按子串找就够;没有码(或没命中)是**空集**,不是「全部」—— 高亮全部等于没高亮。
func logLinesMatching(_ lines: [String], code: String?) -> Set<Int> {
    guard let code, !code.isEmpty else { return [] }
    var out = Set<Int>()
    for (index, line) in lines.enumerated() where line.contains(code) {
        out.insert(index)
    }
    return out
}
