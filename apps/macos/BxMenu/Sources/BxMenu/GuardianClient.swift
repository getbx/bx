import Darwin
import Foundation

private let guardianHeaderLimit = 32 * 1024
private let guardianBodyLimit = 1024 * 1024
private let guardianSocketPath = "/var/run/bx/guardian.sock"
private let guardianDefaultTimeout: TimeInterval = 5
// Go 侧 guardianMutationTimeout = 1 分钟。客户端必须比它长,否则拿到的是自己的
// 超时而不是服务端给的失败码(用户最需要的恰恰是那个码)。
private let guardianMutationTimeout: TimeInterval = 75
// Go 侧 updateCheckTimeout = 20 秒(这条查询要跟 GitHub 往返)。客户端必须比它长,
// 否则拿到的永远是自己的超时,而服务端那个上限一次都不会生效。
private let guardianUpdateCheckTimeout: TimeInterval = 30
private let guardianMaximumTimeout: TimeInterval = TimeInterval(Int32.max) / 1_000

enum GuardianEndpoint {
    case requestRecovery
    case currentRecovery
    case turnOn
    case turnOff
    case status
    case updateCheck

    var expectedStatus: Int {
        switch self {
        case .requestRecovery: return 202
        case .currentRecovery, .turnOn, .turnOff, .status, .updateCheck: return 200
        }
    }

    var timeout: TimeInterval {
        switch self {
        case .requestRecovery, .currentRecovery, .status: return guardianDefaultTimeout
        case .turnOn, .turnOff: return guardianMutationTimeout
        case .updateCheck: return guardianUpdateCheckTimeout
        }
    }
}

enum GuardianClientError: LocalizedError {
    case socket(Int32)
    case invalidResponse
    case responseTooLarge
    case contentType
    /// Guardian 回了一个不是我们要的状态码。`code` 是它在响应体里附的失败码
    /// (仅 500 会带),**可能合法缺席** —— 见 `guardianFailureCode(body:contentLength:)`。
    case status(Int, code: String?)

    var errorDescription: String? {
        switch self {
        case .socket(let code):
            return "Guardian connection failed (\(code))."
        case .invalidResponse:
            return "Guardian returned an invalid response."
        case .responseTooLarge:
            return "Guardian response exceeded its safety limit."
        case .contentType:
            return "Guardian returned a non-JSON response."
        case .status(let status, let code):
            guard let code, !code.isEmpty else {
                return "Guardian request failed (\(status))."
            }
            return "Guardian request failed (\(status), code=\(code))."
        }
    }
}

/// Guardian 失败响应体里菜单用得上的字段。
///
/// 契约在 Go 侧 `internal/guardian/localapi.go` 的 `failureResponseBody`
/// (`{"error":…,"code":…}`),`internal/guardian/client.go` 的
/// `guardianFailureBody` 是同一份镜像。四个 mutation handler
/// (mutation/update/migration/recoveryRequest)都只在 **500** 上写它。
private struct GuardianFailureBody: Decodable {
    let error: String?
    let code: String?
}

/// 从失败响应体里取出 Guardian 的失败码。
///
/// `code` 会**合法缺席**:`failureResponseBody` 只在这次调用真的走过
/// `needsAttention`(用递增代际号判定,不是值比较),或者错误本身是
/// `recovery_incomplete`/`guardian_busy` 这两个短路哨兵时才写它。其余失败
/// 宁可不带码也不肯回放一个陈旧的值 —— 错的码比没有码更糟,它把排查引向
/// 错误方向。所以这里也必须让「缺席」如实保持为 nil,绝不替它编一个。
func guardianFailureCode(body: Data, contentLength: Int) -> String? {
    guard body.count == contentLength else { return nil }
    guard let failure = try? JSONDecoder().decode(GuardianFailureBody.self, from: body) else { return nil }
    guard let code = failure.code, !code.isEmpty else { return nil }
    return code
}

/// 从一次抛出的错误里取出 Guardian 的失败码;不是「Guardian 明确回了状态码」
/// 这类错误(连不上、响应损坏等)一律 nil。
func guardianFailureCode(of error: Error) -> String? {
    guard case GuardianClientError.status(_, let code) = error else { return nil }
    return code
}

private struct GuardianHTTPHead {
    let status: Int
    let contentLength: Int
    let bodyOffset: Int
}

private struct GuardianDeadline {
    private let deadline: TimeInterval
    private let now: () -> TimeInterval

    init(timeout: TimeInterval, now: @escaping () -> TimeInterval) {
        self.now = now
        let boundedTimeout = timeout > 0 ? min(timeout, guardianMaximumTimeout) : 0
        deadline = now() + boundedTimeout
    }

    func remaining() throws -> TimeInterval {
        let remaining = deadline - now()
        guard remaining > 0 else {
            throw GuardianClientError.socket(ETIMEDOUT)
        }
        return remaining
    }

    func checkpoint() throws {
        _ = try remaining()
    }
}

struct GuardianClient {
    private let connectSocket: (GuardianDeadline) throws -> Int32
    private let overrideTimeout: TimeInterval?
    private let clock: () -> TimeInterval

    init() {
        overrideTimeout = nil
        clock = { ProcessInfo.processInfo.systemUptime }
        connectSocket = { deadline in
            try connectToGuardian(deadline: deadline)
        }
    }

    init(
        connectSocket: @escaping () throws -> Int32,
        ioTimeout: TimeInterval = guardianDefaultTimeout,
        clock: @escaping () -> TimeInterval = { ProcessInfo.processInfo.systemUptime }
    ) {
        self.connectSocket = { _ in try connectSocket() }
        self.overrideTimeout = ioTimeout
        self.clock = clock
    }

    func requestRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .requestRecovery, as: RecoverySnapshot.self)
    }

    func currentRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .currentRecovery, as: RecoverySnapshot.self)
    }

    func turnOn() throws -> GuardianStatus {
        try perform(endpoint: .turnOn, as: GuardianStatus.self)
    }

    func turnOff() throws -> GuardianStatus {
        try perform(endpoint: .turnOff, as: GuardianStatus.self)
    }

    func status() throws -> GuardianStatus {
        try perform(endpoint: .status, as: GuardianStatus.self)
    }

    /// 有没有可装的新版。**由 Guardian 代查**(它跑的是 `bx update --check --json`
    /// 那条完全相同的路径),菜单不再 spawn 那条命令。
    ///
    /// 查不动时服务端回 503 → 这里抛错 → 调用方落到「不知道」(不显示更新入口),
    /// 而不是被喂一个 `available:false` 的假「已是最新」。
    func updateCheck() throws -> UpdateCheck {
        try perform(endpoint: .updateCheck, as: UpdateCheck.self)
    }

    /// 单一出口:生产 `init()` 让每个端点用自己的 `timeout`(`overrideTimeout == nil`);
    /// 测试用 `init(connectSocket:ioTimeout:clock:)` 注入的值始终优先。
    /// `perform` 与测试都必须经它取超时,不许各自重算 `overrideTimeout ?? endpoint.timeout`
    /// ——否则两处会各自正确却整体对不上,而套件不会注意到。
    func effectiveTimeout(for endpoint: GuardianEndpoint) -> TimeInterval {
        overrideTimeout ?? endpoint.timeout
    }

    private func perform<T: Decodable>(endpoint: GuardianEndpoint, as type: T.Type) throws -> T {
        let deadline = GuardianDeadline(timeout: effectiveTimeout(for: endpoint), now: clock)
        let fd = try connectSocket(deadline)
        defer { close(fd) }
        try configureGuardianSocketTimeouts(fd, timeout: deadline.remaining())
        try setGuardianSocketNonBlocking(fd)
        var noSignal: Int32 = 1
        guard setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout.size(ofValue: noSignal))) == 0 else {
            throw GuardianClientError.socket(errno)
        }
        try writeGuardianRequest(guardianRequest(for: endpoint), to: fd, deadline: deadline)
        let response = try readGuardianHTTPResponse(from: fd, deadline: deadline)
        let decoded = try decodeGuardianHTTPResponse(response, expectedStatus: endpoint.expectedStatus, as: T.self)
        try deadline.checkpoint()
        return decoded
    }
}

func decodeGuardianHTTPResponse<T: Decodable>(_ response: Data, expectedStatus: Int, as type: T.Type) throws -> T {
    let head = try parseGuardianHTTPHead(response)
    let body = response[head.bodyOffset...]
    guard head.status == expectedStatus else {
        // 失败码必须在这里取:抛出之前不读响应体,整套 toggleFailureHint 就
        // 永远拿不到码(Guardian 的码只在 500 上,而 200 那条路上 Manager
        // 已经把 LastError 清掉了),用户只会看到 "Guardian request
        // failed (500)."。响应体损坏/长度对不上时取不到码 —— 那就没有码,
        // 但状态码本身仍要如实抛出,不能因为体读不动就变成 invalidResponse。
        throw GuardianClientError.status(
            head.status,
            code: guardianFailureCode(body: body, contentLength: head.contentLength)
        )
    }
    guard body.count == head.contentLength else {
        throw GuardianClientError.invalidResponse
    }
    do {
        return try JSONDecoder().decode(T.self, from: body)
    } catch {
        throw GuardianClientError.invalidResponse
    }
}

func decodeGuardianHTTPResponse(_ response: Data, expectedStatus: Int) throws -> RecoverySnapshot {
    try decodeGuardianHTTPResponse(response, expectedStatus: expectedStatus, as: RecoverySnapshot.self)
}

private func guardianRequest(for endpoint: GuardianEndpoint) -> Data {
    let method: String
    let path: String
    let body: Data?
    switch endpoint {
    case .requestRecovery:
        method = "POST"
        path = "/v1/recoveries"
        body = Data(#"{"reason":"manual"}"#.utf8)
    case .currentRecovery:
        method = "GET"
        path = "/v1/recoveries/current"
        body = nil
    case .turnOn:
        method = "POST"
        path = "/v1/up"
        body = Data("{}".utf8)
    case .turnOff:
        method = "POST"
        path = "/v1/down"
        body = Data("{}".utf8)
    case .status:
        method = "GET"
        path = "/v1/status"
        body = nil
    case .updateCheck:
        method = "GET"
        path = "/v1/update-check"
        body = nil
    }

    var requestText = "\(method) \(path) HTTP/1.1\r\nHost: local\r\nAccept: application/json\r\nConnection: close\r\n"
    if let body {
        requestText += "Content-Type: application/json\r\nContent-Length: \(body.count)\r\n"
    }
    requestText += "\r\n"
    var request = Data(requestText.utf8)
    if let body {
        request.append(body)
    }
    return request
}

private func connectToGuardian(deadline: GuardianDeadline) throws -> Int32 {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    guard fd >= 0 else {
        throw GuardianClientError.socket(errno)
    }
    var keepOpen = false
    defer {
        if !keepOpen {
            close(fd)
        }
    }
    var address = sockaddr_un()
    let path = Array(guardianSocketPath.utf8CString)
    guard path.count <= MemoryLayout.size(ofValue: address.sun_path) else {
        throw GuardianClientError.invalidResponse
    }
    address.sun_family = sa_family_t(AF_UNIX)
    withUnsafeMutablePointer(to: &address.sun_path) { destination in
        destination.withMemoryRebound(to: CChar.self, capacity: path.count) { bytes in
            for index in path.indices {
                bytes[index] = path[index]
            }
        }
    }
    let length = socklen_t(MemoryLayout<sa_family_t>.size + path.count)
    address.sun_len = UInt8(length)
    let originalFlags = fcntl(fd, F_GETFL)
    guard originalFlags >= 0, fcntl(fd, F_SETFL, originalFlags | O_NONBLOCK) == 0 else {
        throw GuardianClientError.socket(errno)
    }
    try deadline.checkpoint()
    let result = withUnsafePointer(to: &address) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            Darwin.connect(fd, $0, length)
        }
    }
    if result != 0 && errno != EINPROGRESS {
        throw GuardianClientError.socket(errno)
    }
    if result != 0 {
        try waitForGuardianConnect(fd, deadline: deadline)
    }
    try deadline.checkpoint()
    guard fcntl(fd, F_SETFL, originalFlags) == 0 else {
        throw GuardianClientError.socket(errno)
    }
    keepOpen = true
    return fd
}

private func waitForGuardianConnect(_ fd: Int32, deadline: GuardianDeadline) throws {
    var descriptor = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
    while true {
        let milliseconds = try guardianPollMilliseconds(deadline)
        let result = Darwin.poll(&descriptor, 1, milliseconds)
        if result < 0 && errno == EINTR {
            continue
        }
        guard result > 0 else {
            throw GuardianClientError.socket(result == 0 ? ETIMEDOUT : errno)
        }
        break
    }
    var socketError: Int32 = 0
    var length = socklen_t(MemoryLayout.size(ofValue: socketError))
    guard getsockopt(fd, SOL_SOCKET, SO_ERROR, &socketError, &length) == 0 else {
        throw GuardianClientError.socket(errno)
    }
    guard socketError == 0 else {
        throw GuardianClientError.socket(socketError)
    }
}

private func guardianPollMilliseconds(_ deadline: GuardianDeadline) throws -> Int32 {
    let remaining = try deadline.remaining()
    return Int32(max(1, ceil(remaining * 1_000)))
}

private func waitForGuardianIO(_ fd: Int32, events: Int16, deadline: GuardianDeadline) throws {
    var descriptor = pollfd(fd: fd, events: events, revents: 0)
    while true {
        let result = Darwin.poll(&descriptor, 1, try guardianPollMilliseconds(deadline))
        if result < 0 && errno == EINTR {
            continue
        }
        guard result > 0 else {
            throw GuardianClientError.socket(result == 0 ? ETIMEDOUT : errno)
        }
        try deadline.checkpoint()
        return
    }
}

private func configureGuardianSocketTimeouts(_ fd: Int32, timeout: TimeInterval) throws {
    let bounded = max(0.001, timeout)
    let seconds = Int(bounded)
    let microseconds = Int32((bounded - Double(seconds)) * 1_000_000)
    var value = timeval(tv_sec: seconds, tv_usec: microseconds)
    for option in [SO_RCVTIMEO, SO_SNDTIMEO] {
        guard setsockopt(fd, SOL_SOCKET, option, &value, socklen_t(MemoryLayout.size(ofValue: value))) == 0 else {
            throw GuardianClientError.socket(errno)
        }
    }
}

private func setGuardianSocketNonBlocking(_ fd: Int32) throws {
    let flags = fcntl(fd, F_GETFL)
    guard flags >= 0, fcntl(fd, F_SETFL, flags | O_NONBLOCK) == 0 else {
        throw GuardianClientError.socket(errno)
    }
}

private func writeGuardianRequest(_ request: Data, to fd: Int32, deadline: GuardianDeadline) throws {
    try request.withUnsafeBytes { rawBuffer in
        guard let base = rawBuffer.baseAddress else { return }
        var written = 0
        while written < rawBuffer.count {
            try waitForGuardianIO(fd, events: Int16(POLLOUT), deadline: deadline)
            try deadline.checkpoint()
            let count = Darwin.write(fd, base.advanced(by: written), rawBuffer.count - written)
            if count < 0 && errno == EINTR {
                continue
            }
            if count < 0 && (errno == EAGAIN || errno == EWOULDBLOCK) {
                continue
            }
            guard count > 0 else {
                throw GuardianClientError.socket(errno)
            }
            written += count
            try deadline.checkpoint()
        }
    }
}

private func readGuardianHTTPResponse(from fd: Int32, deadline: GuardianDeadline) throws -> Data {
    let maximum = guardianHeaderLimit + guardianBodyLimit
    var response = Data()
    var buffer = [UInt8](repeating: 0, count: 8192)
    while true {
        try waitForGuardianIO(fd, events: Int16(POLLIN), deadline: deadline)
        try deadline.checkpoint()
        let count = Darwin.read(fd, &buffer, buffer.count)
        if count == 0 {
            try deadline.checkpoint()
            return response
        }
        guard count > 0 else {
            if errno == EINTR {
                continue
            }
            if errno == EAGAIN || errno == EWOULDBLOCK {
                continue
            }
            throw GuardianClientError.socket(errno)
        }
        guard response.count + count <= maximum else {
            throw GuardianClientError.responseTooLarge
        }
        response.append(buffer, count: count)
        try deadline.checkpoint()
        if response.range(of: Data("\r\n\r\n".utf8)) == nil && response.count >= guardianHeaderLimit {
            throw GuardianClientError.responseTooLarge
        }
        if let expectedLength = try completeGuardianResponseLength(response) {
            guard response.count == expectedLength else {
                throw GuardianClientError.invalidResponse
            }
            try deadline.checkpoint()
            return response
        }
    }
}

private func completeGuardianResponseLength(_ response: Data) throws -> Int? {
    let separator = Data("\r\n\r\n".utf8)
    guard response.range(of: separator) != nil else {
        return nil
    }
    let head = try parseGuardianHTTPHead(response)
    let expected = head.bodyOffset + head.contentLength
    if response.count < expected {
        return nil
    }
    return expected
}

private func parseGuardianHTTPHead(_ response: Data) throws -> GuardianHTTPHead {
    let separator = Data("\r\n\r\n".utf8)
    guard let separatorRange = response.range(of: separator) else {
        if response.count >= guardianHeaderLimit {
            throw GuardianClientError.responseTooLarge
        }
        throw GuardianClientError.invalidResponse
    }
    guard separatorRange.upperBound <= guardianHeaderLimit else {
        throw GuardianClientError.responseTooLarge
    }
    guard let headerText = String(data: response[..<separatorRange.lowerBound], encoding: .utf8) else {
        throw GuardianClientError.invalidResponse
    }
    let lines = headerText.components(separatedBy: "\r\n")
    guard let statusLine = lines.first else {
        throw GuardianClientError.invalidResponse
    }
    let statusParts = statusLine.split(separator: " ", maxSplits: 2, omittingEmptySubsequences: false)
    guard statusParts.count >= 2,
          statusParts[0] == "HTTP/1.0" || statusParts[0] == "HTTP/1.1",
          statusParts[1].utf8.count == 3,
          statusParts[1].utf8.allSatisfy({ $0 >= 48 && $0 <= 57 }),
          let status = Int(statusParts[1]),
          (100...599).contains(status)
    else {
        throw GuardianClientError.invalidResponse
    }

    var contentType: String?
    var contentLength: Int?
    var sawTransferEncoding = false
    for line in lines.dropFirst() {
        guard let colon = line.firstIndex(of: ":"), colon != line.startIndex else {
            throw GuardianClientError.invalidResponse
        }
        let rawName = line[..<colon]
        guard rawName == rawName.trimmingCharacters(in: .whitespaces),
              rawName.utf8.allSatisfy(isGuardianHeaderTokenByte)
        else {
            throw GuardianClientError.invalidResponse
        }
        let name = rawName.lowercased()
        let value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
        switch name {
        case "content-type":
            guard contentType == nil else {
                throw GuardianClientError.invalidResponse
            }
            contentType = value
        case "content-length":
            guard !value.isEmpty,
                  value.utf8.allSatisfy({ $0 >= 48 && $0 <= 57 }),
                  let length = Int(value),
                  length <= guardianBodyLimit
            else {
                throw GuardianClientError.invalidResponse
            }
            if let existing = contentLength, existing != length {
                throw GuardianClientError.invalidResponse
            }
            contentLength = length
        case "transfer-encoding":
            if sawTransferEncoding {
                throw GuardianClientError.invalidResponse
            }
            sawTransferEncoding = true
        default:
            break
        }
    }
    guard !sawTransferEncoding else {
        throw GuardianClientError.invalidResponse
    }
    guard let rawContentType = contentType,
          rawContentType.split(separator: ";", maxSplits: 1).first?
            .trimmingCharacters(in: .whitespaces)
            .lowercased() == "application/json"
    else {
        throw GuardianClientError.contentType
    }
    guard let contentLength else {
        throw GuardianClientError.invalidResponse
    }
    return GuardianHTTPHead(
        status: status,
        contentLength: contentLength,
        bodyOffset: separatorRange.upperBound
    )
}

private func isGuardianHeaderTokenByte(_ byte: UInt8) -> Bool {
    switch byte {
    case 48...57, 65...90, 97...122:
        return true
    case 33, 35...39, 42, 43, 45, 46, 94, 95, 96, 124, 126:
        return true
    default:
        return false
    }
}
