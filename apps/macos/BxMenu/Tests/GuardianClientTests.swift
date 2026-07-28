import Darwin
import Foundation

@main
struct GuardianClientTests {
    static func main() throws {
        try testFixedRecoveryRequestsAndJSONDecode()
        try testResponseBoundsAndContentType()
    }

    private static func testFixedRecoveryRequestsAndJSONDecode() throws {
        let post = try fixtureClient(response: response(status: 202, body: recoveryJSON))
        let submitted = try post.client.requestRecovery()
        expect(submitted.recoveryID == "recovery-1", "POST snapshot decoded")
        let postRequest = try readRequest(post.serverFD)
        expect(postRequest.hasPrefix("POST /v1/recoveries HTTP/1.1\r\n"), "fixed POST path")
        expect(postRequest.contains("Content-Type: application/json\r\n"), "POST JSON content type")
        expect(postRequest.hasSuffix("\r\n\r\n{\"reason\":\"manual\"}"), "fixed manual recovery body")

        let get = try fixtureClient(response: response(status: 200, body: recoveryJSON))
        let current = try get.client.currentRecovery()
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

    private static func fixtureClient(response: Data) throws -> (client: GuardianClient, serverFD: Int32) {
        var sockets = [Int32](repeating: -1, count: 2)
        guard socketpair(AF_UNIX, SOCK_STREAM, 0, &sockets) == 0 else {
            throw POSIXError(.ENOTCONN)
        }
        try writeAll(response, to: sockets[1])
        guard shutdown(sockets[1], SHUT_WR) == 0 else {
            throw POSIXError(POSIXErrorCode(rawValue: errno) ?? .EIO)
        }
        return (GuardianClient(connectSocket: { sockets[0] }), sockets[1])
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

    private static func expect(_ condition: Bool, _ label: String) {
        guard condition else {
            fputs("failed: \(label)\n", stderr)
            exit(1)
        }
    }
}
