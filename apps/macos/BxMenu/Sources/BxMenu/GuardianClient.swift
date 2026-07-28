import Darwin
import Foundation

private let guardianHeaderLimit = 32 * 1024
private let guardianBodyLimit = 1024 * 1024
private let guardianSocketPath = "/var/run/bx-guard.sock"
private let guardianDefaultTimeout: TimeInterval = 5
private let guardianMaximumTimeout: TimeInterval = TimeInterval(Int32.max) / 1_000

private enum GuardianEndpoint {
    case requestRecovery
    case currentRecovery

    var expectedStatus: Int {
        switch self {
        case .requestRecovery:
            return 202
        case .currentRecovery:
            return 200
        }
    }
}

enum GuardianClientError: LocalizedError {
    case socket(Int32)
    case invalidResponse
    case responseTooLarge
    case contentType
    case status(Int)

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
        case .status(let code):
            return "Guardian request failed (\(code))."
        }
    }
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
    private let ioTimeout: TimeInterval
    private let clock: () -> TimeInterval

    init() {
        ioTimeout = guardianDefaultTimeout
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
        self.ioTimeout = ioTimeout
        self.clock = clock
    }

    func requestRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .requestRecovery)
    }

    func currentRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .currentRecovery)
    }

    private func perform(endpoint: GuardianEndpoint) throws -> RecoverySnapshot {
        let deadline = GuardianDeadline(timeout: ioTimeout, now: clock)
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
        return try decodeGuardianHTTPResponse(response, expectedStatus: endpoint.expectedStatus)
    }
}

func decodeGuardianHTTPResponse(_ response: Data, expectedStatus: Int) throws -> RecoverySnapshot {
    let head = try parseGuardianHTTPHead(response)
    guard head.status == expectedStatus else {
        throw GuardianClientError.status(head.status)
    }
    let body = response[head.bodyOffset...]
    guard body.count == head.contentLength else {
        throw GuardianClientError.invalidResponse
    }
    do {
        return try JSONDecoder().decode(RecoverySnapshot.self, from: body)
    } catch {
        throw GuardianClientError.invalidResponse
    }
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
