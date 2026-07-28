import Darwin
import Foundation

private let guardianHeaderLimit = 32 * 1024
private let guardianBodyLimit = 1024 * 1024
private let guardianSocketPath = "/var/run/bx-guard.sock"

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

struct GuardianClient {
    private let connectSocket: () throws -> Int32

    init() {
        connectSocket = connectToGuardian
    }

    init(connectSocket: @escaping () throws -> Int32) {
        self.connectSocket = connectSocket
    }

    func requestRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .requestRecovery)
    }

    func currentRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .currentRecovery)
    }

    private func perform(endpoint: GuardianEndpoint) throws -> RecoverySnapshot {
        let fd = try connectSocket()
        defer { close(fd) }
        var noSignal: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout.size(ofValue: noSignal)))
        try writeGuardianRequest(guardianRequest(for: endpoint), to: fd)
        let response = try readGuardianHTTPResponse(from: fd)
        return try decodeGuardianHTTPResponse(response, expectedStatus: endpoint.expectedStatus)
    }
}

func decodeGuardianHTTPResponse(_ response: Data, expectedStatus: Int) throws -> RecoverySnapshot {
    let separator = Data("\r\n\r\n".utf8)
    guard let separatorRange = response.range(of: separator) else {
        if response.count > guardianHeaderLimit {
            throw GuardianClientError.responseTooLarge
        }
        throw GuardianClientError.invalidResponse
    }
    guard separatorRange.upperBound <= guardianHeaderLimit else {
        throw GuardianClientError.responseTooLarge
    }

    let headerData = response[..<separatorRange.lowerBound]
    guard let headerText = String(data: headerData, encoding: .utf8) else {
        throw GuardianClientError.invalidResponse
    }
    let lines = headerText.components(separatedBy: "\r\n")
    guard let statusLine = lines.first else {
        throw GuardianClientError.invalidResponse
    }
    let statusParts = statusLine.split(separator: " ", maxSplits: 2)
    guard statusParts.count >= 2, statusParts[0].hasPrefix("HTTP/1."), let status = Int(statusParts[1]) else {
        throw GuardianClientError.invalidResponse
    }
    var headers: [String: String] = [:]
    for line in lines.dropFirst() {
        guard let colon = line.firstIndex(of: ":") else {
            throw GuardianClientError.invalidResponse
        }
        let name = line[..<colon].trimmingCharacters(in: .whitespaces).lowercased()
        let value = line[line.index(after: colon)...].trimmingCharacters(in: .whitespaces)
        headers[name] = value
    }
    guard let contentType = headers["content-type"]?
        .split(separator: ";", maxSplits: 1)
        .first?
        .trimmingCharacters(in: .whitespaces)
        .lowercased(),
        contentType == "application/json"
    else {
        throw GuardianClientError.contentType
    }
    guard status == expectedStatus else {
        throw GuardianClientError.status(status)
    }
    if headers["transfer-encoding"] != nil {
        throw GuardianClientError.invalidResponse
    }

    let body = response[separatorRange.upperBound...]
    guard body.count <= guardianBodyLimit else {
        throw GuardianClientError.responseTooLarge
    }
    if let rawLength = headers["content-length"] {
        guard let length = Int(rawLength), length >= 0, length <= guardianBodyLimit, length == body.count else {
            throw GuardianClientError.invalidResponse
        }
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

private func connectToGuardian() throws -> Int32 {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    guard fd >= 0 else {
        throw GuardianClientError.socket(errno)
    }
    var address = sockaddr_un()
    let path = Array(guardianSocketPath.utf8CString)
    guard path.count <= MemoryLayout.size(ofValue: address.sun_path) else {
        close(fd)
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
    let result = withUnsafePointer(to: &address) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            Darwin.connect(fd, $0, length)
        }
    }
    guard result == 0 else {
        let code = errno
        close(fd)
        throw GuardianClientError.socket(code)
    }
    return fd
}

private func writeGuardianRequest(_ request: Data, to fd: Int32) throws {
    try request.withUnsafeBytes { rawBuffer in
        guard let base = rawBuffer.baseAddress else { return }
        var written = 0
        while written < rawBuffer.count {
            let count = Darwin.write(fd, base.advanced(by: written), rawBuffer.count - written)
            guard count > 0 else {
                throw GuardianClientError.socket(errno)
            }
            written += count
        }
    }
}

private func readGuardianHTTPResponse(from fd: Int32) throws -> Data {
    let maximum = guardianHeaderLimit + guardianBodyLimit
    var response = Data()
    var buffer = [UInt8](repeating: 0, count: 8192)
    while true {
        let count = Darwin.read(fd, &buffer, buffer.count)
        if count == 0 {
            return response
        }
        guard count > 0 else {
            if errno == EINTR {
                continue
            }
            throw GuardianClientError.socket(errno)
        }
        guard response.count + count <= maximum else {
            throw GuardianClientError.responseTooLarge
        }
        response.append(buffer, count: count)
        if response.range(of: Data("\r\n\r\n".utf8)) == nil && response.count > guardianHeaderLimit {
            throw GuardianClientError.responseTooLarge
        }
    }
}
