import Foundation

struct RuntimeRelease: Decodable, Equatable {
    let version: String
    private enum CodingKeys: String, CodingKey { case version }
}

func decodeRuntimeVersion(_ data: Data) -> String? {
    (try? JSONDecoder().decode(RuntimeRelease.self, from: data))?.version
}

func unifiedRuntimeVersion(root: String = "/Library/Application Support/bx/runtime") -> String? {
    let path = root + "/current/release.json"
    guard let data = FileManager.default.contents(atPath: path) else { return nil }
    return decodeRuntimeVersion(data)
}

func installActionTitle(runtimeInstalled: Bool, cliUsable: Bool) -> String? {
    if runtimeInstalled && cliUsable { return nil }
    if !runtimeInstalled && cliUsable { return nil } // legacy 布局:沿用既有指引,不递归安装
    return "Install bx…"
}

func menuUpdateActionTitle(check: UpdateCheck?) -> String? {
    updateActionTitle(for: check)
}

func updatingBanner(phase: String?) -> String? {
    guard let phase else { return nil }
    switch phase {
    case "prepared", "barrier_active", "activating", "rolling_back":
        return "Updating bx…"
    default:
        return nil
    }
}

let turnOffActionTitle = "Turn Off bx"
