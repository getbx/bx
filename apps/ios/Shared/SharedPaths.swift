import Foundation

// Compiled into both the app and the Packet Tunnel extension: where they meet is the app
// group container, and both must agree on every name in it.
enum SharedPaths {
    static let appGroup = "group.com.getbx.bx"
    static let startConfigName = "start-config.json"
    static let brokenMarker = "BX-BROKEN-CONFIG"

    struct Paths {
        let base: URL
        let working: URL
        let temp: URL
    }

    static var container: URL? {
        FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup)
    }

    /// The app writes what the extension runs on an on-demand start (which carries no options):
    /// the config and its rule-set files, in the shared working directory. Readable after the
    /// first unlock, so a reconnect after a reboot still finds it; the link itself is not here
    /// (it is in the app's Keychain), only the config derived from it.
    static func writeStartConfig(_ config: String, ruleSets: [String: String]) throws {
        let working = try make().working
        let protection: [FileAttributeKey: Any] = [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication]
        for (name, body) in ruleSets {
            let url = working.appendingPathComponent(name)
            try Data(body.utf8).write(to: url, options: .atomic)
            try FileManager.default.setAttributes(protection, ofItemAtPath: url.path)
        }
        let url = working.appendingPathComponent(startConfigName)
        try Data(config.utf8).write(to: url, options: .atomic)
        try FileManager.default.setAttributes(protection, ofItemAtPath: url.path)
    }

    static func removeStartConfig() {
        guard let working = try? make().working else { return }
        try? FileManager.default.removeItem(at: working.appendingPathComponent(startConfigName))
    }

    static func make() throws -> Paths {
        guard let base = FileManager.default.containerURL(forSecurityApplicationGroupIdentifier: appGroup) else {
            throw SharedPathsError("app group \(appGroup) is not available")
        }
        let working = base.appendingPathComponent("Working", isDirectory: true)
        let temp = base.appendingPathComponent("Temp", isDirectory: true)
        for dir in [working, temp] {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        }
        return Paths(base: base, working: working, temp: temp)
    }

    // The config refers to rule-set files by bare name and has no log file; both are
    // anchored to the shared working directory here, where the app put the files and
    // reads the log back.
    static func anchor(_ raw: String, workingDirectory: URL) throws -> String {
        guard var doc = try JSONSerialization.jsonObject(with: Data(raw.utf8)) as? [String: Any] else {
            throw SharedPathsError("configContent is not a JSON object")
        }
        var log = doc["log"] as? [String: Any] ?? [:]
        log["output"] = workingDirectory.appendingPathComponent("box.log").path
        log["timestamp"] = true
        doc["log"] = log
        if var route = doc["route"] as? [String: Any], let sets = route["rule_set"] as? [[String: Any]] {
            route["rule_set"] = sets.map { set in
                var set = set
                if let path = set["path"] as? String, !path.hasPrefix("/") {
                    set["path"] = workingDirectory.appendingPathComponent(path).path
                }
                return set
            }
            doc["route"] = route
        }
        let data = try JSONSerialization.data(withJSONObject: doc)
        return String(decoding: data, as: UTF8.self)
    }
}

struct SharedPathsError: LocalizedError {
    let message: String
    init(_ message: String) { self.message = message }
    var errorDescription: String? { message }
}
