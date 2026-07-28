import Darwin
import Foundation

@main
struct GuardianClientTests {
    static func main() throws {
        try run("fixed requests", testFixedRecoveryRequestsAndJSONDecode)
        try run("complete body without EOF", testCompleteContentLengthReturnsWithoutEOFAndSetsTimeouts)
        try run("partial body timeout", testPartialBodyTimesOutAndClosesClientFD)
        try run("progressive body respects total deadline", testProgressiveBodyRespectsTotalDeadline)
        try run("deadline expires after write poll", testDeadlineExpiresAfterWritePollReadiness)
        try run("deadline rejects complete response", testCompleteResponseAfterDeadlineIsRejected)
        try run("deadline accepts complete response", testCompleteResponseBeforeDeadlineIsAccepted)
        try run("extreme deadline stays bounded", testExtremeTimeoutDoesNotOverflow)
        try run("parser bounds", testResponseBoundsAndContentType)
        try run("strict HTTP framing", testStrictHTTPFraming)
    }

    private static func run(_ label: String, _ body: () throws -> Void) throws {
        do {
            try body()
        } catch {
            fputs("failed: \(label): \(error)\n", stderr)
            throw error
        }
    }

    private static func testFixedRecoveryRequestsAndJSONDecode() throws {
        let post = try fixtureClient(response: response(status: 202, body: recoveryJSON))
        let submitted = try GuardianClient(connectSocket: { post.clientSocket }).requestRecovery()
        expect(submitted.recoveryID == "recovery-1", "POST snapshot decoded")
        let postRequest = try readRequest(post.serverFD)
        expect(postRequest.hasPrefix("POST /v1/recoveries HTTP/1.1\r\n"), "fixed POST path")
        expect(postRequest.contains("Content-Type: application/json\r\n"), "POST JSON content type")
        expect(postRequest.hasSuffix("\r\n\r\n{\"reason\":\"manual\"}"), "fixed manual recovery body")

        let get = try fixtureClient(response: response(status: 200, body: recoveryJSON))
        let current = try GuardianClient(connectSocket: { get.clientSocket }).currentRecovery()
        expect(current.stage == "rebind_underlay", "GET snapshot decoded")
        let getRequest = try readRequest(get.serverFD)
        expect(getRequest.hasPrefix("GET /v1/recoveries/current HTTP/1.1\r\n"), "fixed GET path")
        expect(getRequest.hasSuffix("\r\n\r\n"), "GET has no request body")
    }

    private static func testResponseBoundsAndContentType() throws {
        expectThrows("non-JSON content type") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\n{}".utf8),
                expectedStatus: 200
            )
        }
        do {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 500 Error\r\nContent-Type: text/plain\r\nContent-Length: 2\r\n\r\n{}".utf8),
                expectedStatus: 202
            )
            expect(false, "error response requires JSON content type")
        } catch GuardianClientError.contentType {
            // Expected: status failures still have to satisfy the local JSON protocol.
        } catch {
            expect(false, "error response validated status before content type")
        }

        let oversizedHeader = "HTTP/1.1 200 OK\r\nX-Fill: " + String(repeating: "a", count: 32 * 1024) + "\r\n\r\n"
        expectThrows("oversized headers") {
            _ = try decodeGuardianHTTPResponse(Data(oversizedHeader.utf8), expectedStatus: 200)
        }
        let boundedHeaderPrefix = "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nX-Fill: "
        let delimiter = "\r\n\r\n"
        let boundaryFill = String(
            repeating: "a",
            count: 32 * 1024 - boundedHeaderPrefix.utf8.count - 2
        )
        expectThrows("header delimiter counts toward limit") {
            _ = try decodeGuardianHTTPResponse(
                Data((boundedHeaderPrefix + boundaryFill + delimiter + recoveryJSON).utf8),
                expectedStatus: 200
            )
        }

        let oversizedBody = Data(repeating: 0x7b, count: 1024 * 1024 + 1)
        var response = Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(oversizedBody.count)\r\n\r\n".utf8)
        response.append(oversizedBody)
        expectThrows("oversized body") {
            _ = try decodeGuardianHTTPResponse(response, expectedStatus: 200)
        }
    }

    private static func testStrictHTTPFraming() throws {
        expectThrows("conflicting duplicate Content-Length") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 1\r\nContent-Length: \(recoveryJSON.utf8.count)\r\n\r\n\(recoveryJSON)".utf8),
                expectedStatus: 200
            )
        }
        let duplicateMatchingLength = try decodeGuardianHTTPResponse(
            Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(recoveryJSON.utf8.count)\r\nContent-Length: \(recoveryJSON.utf8.count)\r\n\r\n\(recoveryJSON)".utf8),
            expectedStatus: 200
        )
        expect(duplicateMatchingLength.recoveryID == "recovery-1", "matching duplicate Content-Length accepted")

        expectThrows("duplicate Content-Type") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Type: application/json\r\nContent-Length: \(recoveryJSON.utf8.count)\r\n\r\n\(recoveryJSON)".utf8),
                expectedStatus: 200
            )
        }
        expectThrows("duplicate Transfer-Encoding") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\nTransfer-Encoding: identity\r\nContent-Length: 2\r\n\r\n{}".utf8),
                expectedStatus: 200
            )
        }
        expectThrows("malformed HTTP version") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.x 200 OK\r\nContent-Type: application/json\r\nContent-Length: \(recoveryJSON.utf8.count)\r\n\r\n\(recoveryJSON)".utf8),
                expectedStatus: 200
            )
        }
        expectThrows("malformed short status code") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 20 OK\r\nContent-Type: application/json\r\nContent-Length: \(recoveryJSON.utf8.count)\r\n\r\n\(recoveryJSON)".utf8),
                expectedStatus: 20
            )
        }
        expectThrows("missing Content-Length") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n\(recoveryJSON)".utf8),
                expectedStatus: 200
            )
        }
        expectThrows("short body") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 3\r\n\r\n{}".utf8),
                expectedStatus: 200
            )
        }
        expectThrows("extra bytes after body") {
            _ = try decodeGuardianHTTPResponse(
                Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 2\r\n\r\n{}x".utf8),
                expectedStatus: 200
            )
        }
    }

    private static func testCompleteContentLengthReturnsWithoutEOFAndSetsTimeouts() throws {
        var sockets = [Int32](repeating: -1, count: 2)
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &sockets) == 0 else {
            throw POSIXError(.ENOTCONN)
        }
        var inspectionFD: Int32 = -1
        let client = GuardianClient(
            connectSocket: {
                inspectionFD = dup(sockets[0])
                return sockets[0]
            },
            ioTimeout: 0.05
        )
        try writeAll(response(status: 202, body: recoveryJSON), to: sockets[1])

        let snapshot: RecoverySnapshot
        do {
            snapshot = try client.requestRecovery()
        } catch {
            fputs("failed: complete response client call: \(error)\n", stderr)
            throw error
        }
        expect(snapshot.recoveryID == "recovery-1", "complete Content-Length returns without EOF")
        expectSocketHasFiniteTimeout(inspectionFD, option: SO_RCVTIMEO, label: "read timeout")
        expectSocketHasFiniteTimeout(inspectionFD, option: SO_SNDTIMEO, label: "write timeout")
        close(inspectionFD)
        let request: String
        do {
            request = try readRequestToEOF(sockets[1])
        } catch {
            fputs("failed: complete response peer close check: \(error)\n", stderr)
            throw error
        }
        expect(request.hasPrefix("POST /v1/recoveries HTTP/1.1\r\n"), "complete response closes client FD")
    }

    private static func testPartialBodyTimesOutAndClosesClientFD() throws {
        var sockets = [Int32](repeating: -1, count: 2)
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &sockets) == 0 else {
            throw POSIXError(.ENOTCONN)
        }
        let fullResponse = response(status: 202, body: recoveryJSON)
        try writeAll(fullResponse.dropLast(8), to: sockets[1])
        let client = GuardianClient(connectSocket: { sockets[0] }, ioTimeout: 0.05)

        let started = Date()
        expectThrows("partial Content-Length times out") {
            _ = try client.requestRecovery()
        }
        expect(Date().timeIntervalSince(started) < 1, "partial response timeout is bounded")
        let request = try readRequestToEOF(sockets[1])
        expect(request.hasPrefix("POST /v1/recoveries HTTP/1.1\r\n"), "timeout closes client FD")
    }

    private static func testProgressiveBodyRespectsTotalDeadline() throws {
        var sockets = [Int32](repeating: -1, count: 2)
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &sockets) == 0 else {
            throw POSIXError(.ENOTCONN)
        }
        defer { close(sockets[1]) }

        var noSignal: Int32 = 1
        guard setsockopt(
            sockets[1],
            SOL_SOCKET,
            SO_NOSIGPIPE,
            &noSignal,
            socklen_t(MemoryLayout.size(ofValue: noSignal))
        ) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }

        let response = response(status: 202, body: recoveryJSON)
        try writeAll(response.prefix(1), to: sockets[1])
        let finishedWriting = DispatchSemaphore(value: 0)
        Thread.detachNewThread {
            defer { finishedWriting.signal() }
            for byte in response.dropFirst() {
                usleep(20_000)
                var chunk = byte
                if Darwin.write(sockets[1], &chunk, 1) <= 0 {
                    return
                }
            }
        }

        let client = GuardianClient(connectSocket: { sockets[0] }, ioTimeout: 0.08)
        let started = Date()
        expectSocketTimeout("progressive response uses one total deadline") {
            _ = try client.requestRecovery()
        }
        expect(Date().timeIntervalSince(started) < 0.5, "progressive response cannot renew deadline")
        let request = try readRequestToEOF(sockets[1])
        expect(request.hasPrefix("POST /v1/recoveries HTTP/1.1\r\n"), "progressive timeout closes client FD")
        expect(finishedWriting.wait(timeout: .now() + 1) == .success, "progressive writer stopped after client close")
    }

    private static func testDeadlineExpiresAfterWritePollReadiness() throws {
        var sockets = [Int32](repeating: -1, count: 2)
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &sockets) == 0 else {
            throw POSIXError(.ENOTCONN)
        }
        defer { close(sockets[1]) }

        let clock = TestClock(samples: [0, 0, 0, 0.05])
        let client = GuardianClient(
            connectSocket: { sockets[0] },
            ioTimeout: 0.05,
            clock: clock.now
        )
        expectSocketTimeout("deadline expires after write poll readiness") {
            _ = try client.requestRecovery()
        }
        expect(try readRequestToEOF(sockets[1]).isEmpty, "expired write is never attempted")
    }

    private static func testCompleteResponseAfterDeadlineIsRejected() throws {
        let fixture = try fixtureClient(response: response(status: 202, body: recoveryJSON))
        let clock = TestClock(samples: Array(repeating: 0, count: 10) + [0.05])
        let client = GuardianClient(
            connectSocket: { fixture.clientSocket },
            ioTimeout: 0.05,
            clock: clock.now
        )
        expectSocketTimeout("complete response is rejected after deadline") {
            _ = try client.requestRecovery()
        }
        expect((try readRequest(fixture.serverFD)).hasPrefix("POST /v1/recoveries HTTP/1.1\r\n"), "expired response still closes client FD")
    }

    private static func testCompleteResponseBeforeDeadlineIsAccepted() throws {
        let fixture = try fixtureClient(response: response(status: 202, body: recoveryJSON))
        let clock = TestClock(samples: Array(repeating: 0, count: 11))
        let client = GuardianClient(
            connectSocket: { fixture.clientSocket },
            ioTimeout: 0.05,
            clock: clock.now
        )
        let snapshot = try client.requestRecovery()
        expect(snapshot.recoveryID == "recovery-1", "complete response before deadline succeeds")
        _ = try readRequest(fixture.serverFD)
    }

    private static func testExtremeTimeoutDoesNotOverflow() throws {
        let fixture = try fixtureClient(response: response(status: 202, body: recoveryJSON))
        let clock = TestClock(samples: Array(repeating: 0, count: 11))
        let client = GuardianClient(
            connectSocket: { fixture.clientSocket },
            ioTimeout: .greatestFiniteMagnitude,
            clock: clock.now
        )
        expect(try client.requestRecovery().recoveryID == "recovery-1", "extreme timeout does not overflow socket timeout")
        _ = try readRequest(fixture.serverFD)
    }

    private static func fixtureClient(response: Data) throws -> (clientSocket: Int32, serverFD: Int32) {
        var sockets = [Int32](repeating: -1, count: 2)
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &sockets) == 0 else {
            throw POSIXError(.ENOTCONN)
        }
        try writeAll(response, to: sockets[1])
        guard shutdown(sockets[1], SHUT_WR) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        return (sockets[0], sockets[1])
    }

    private static func readRequest(_ fd: Int32) throws -> String {
        defer { close(fd) }
        var bytes = [UInt8](repeating: 0, count: 4096)
        let count = read(fd, &bytes, bytes.count)
        guard count >= 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        return String(decoding: bytes.prefix(count), as: UTF8.self)
    }

    private static func readRequestToEOF(_ fd: Int32) throws -> String {
        defer { close(fd) }
        let flags = fcntl(fd, F_GETFL)
        guard flags >= 0, fcntl(fd, F_SETFL, flags | O_NONBLOCK) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        var request = Data()
        var bytes = [UInt8](repeating: 0, count: 4096)
        while true {
            let count = read(fd, &bytes, bytes.count)
            if count == 0 {
                return String(decoding: request, as: UTF8.self)
            }
            guard count > 0 else {
                if errno == EAGAIN || errno == EWOULDBLOCK {
                    throw POSIXError(.ETIMEDOUT)
                }
                throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
            }
            request.append(bytes, count: count)
        }
    }

    private static func expectSocketHasFiniteTimeout(_ fd: Int32, option: Int32, label: String) {
        var timeout = timeval()
        var length = socklen_t(MemoryLayout.size(ofValue: timeout))
        let result = getsockopt(fd, SOL_SOCKET, option, &timeout, &length)
        expect(result == 0, "\(label) readable")
        expect(timeout.tv_sec > 0 || timeout.tv_usec > 0, "\(label) is finite")
    }

    private static func writeAll(_ data: Data, to fd: Int32) throws {
        try data.withUnsafeBytes { rawBuffer in
            guard let base = rawBuffer.baseAddress else { return }
            var written = 0
            while written < rawBuffer.count {
                let count = Darwin.write(fd, base.advanced(by: written), rawBuffer.count - written)
                guard count > 0 else {
                    throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
                }
                written += count
            }
        }
    }

    private static func response(status: Int, body: String) -> Data {
        Data(
            """
            HTTP/1.1 \(status) Result\r
            Content-Type: application/json\r
            Content-Length: \(body.utf8.count)\r
            Connection: close\r
            \r
            \(body)
            """.utf8
        )
    }

    private static let recoveryJSON = """
    {"recovery_id":"recovery-1","state":"running","stage":"rebind_underlay","reason":"manual","attempt":1,"started_at":"2026-07-27T12:00:00Z","updated_at":"2026-07-27T12:00:01Z"}
    """

    private static func expectThrows(_ label: String, _ body: () throws -> Void) {
        do {
            try body()
            expect(false, label)
        } catch {
            return
        }
    }

    private static func expectSocketTimeout(_ label: String, _ body: () throws -> Void) {
        do {
            try body()
            expect(false, label)
        } catch GuardianClientError.socket(let code) {
            expect(code == ETIMEDOUT, label)
        } catch {
            expect(false, "\(label): unexpected \(error)")
        }
    }

    private final class TestClock {
        private var samples: [TimeInterval]
        private var index = 0

        init(samples: [TimeInterval]) {
            self.samples = samples
        }

        func now() -> TimeInterval {
            let sample = samples[min(index, samples.count - 1)]
            index += 1
            return sample
        }
    }

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }
}
