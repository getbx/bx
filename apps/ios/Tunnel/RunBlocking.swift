import Foundation

// libbox calls the platform interface synchronously from Go threads; NetworkExtension's
// APIs are async. Same bridge the official sing-box Apple client uses.
func runBlocking<T>(_ block: @escaping () async throws -> T) throws -> T {
    let semaphore = DispatchSemaphore(value: 0)
    let box = ResultBox<T>()
    Task.detached(priority: .userInitiated) {
        do {
            box.result = .success(try await block())
        } catch {
            box.result = .failure(error)
        }
        semaphore.signal()
    }
    semaphore.wait()
    return try box.result.get()
}

private final class ResultBox<T>: @unchecked Sendable {
    var result: Result<T, Error>!
}
