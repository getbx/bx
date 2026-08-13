import Foundation

@main
struct FirstRunTests {
    static func main() {
        // **没装 → 每次都问。** 这个状态只可能来自用户手动双击(登录项只有装好后才写),
        // 那一刻他正等着被告知下一步。
        expect(firstRunAction(state: .notInstalled, alreadyOfferedSetup: false) == .offerInstall,
               "没装时应引导安装")
        expect(firstRunAction(state: .notInstalled, alreadyOfferedSetup: true) == .offerInstall,
               "没装时即使问过也要问 —— 他刚刚又双击了一次")
        expect(firstRunAction(state: .missing, alreadyOfferedSetup: true) == .offerInstall,
               "组件缺失同样引导安装")

        // **装了没配 → 只问一次。** 这个状态下 app 是登录项,每次开机都起来;
        // 每次登录弹一个框是骚扰,而菜单里那一项一直都在,拒绝不会失去入口。
        expect(firstRunAction(state: .setupNeeded, alreadyOfferedSetup: false) == .offerSetup,
               "装好没配时应引导设置")
        expect(firstRunAction(state: .setupNeeded, alreadyOfferedSetup: true) == .none,
               "已经问过就不要每次登录都问 —— 那是骚扰")

        // 已经在用了就闭嘴:主动开口只会打断他。
        for state in [MenuStateKind.connected, .warning, .updateNeeded,
                      .offGuardianResponding, .offServiceStopped] {
            expect(firstRunAction(state: state, alreadyOfferedSetup: false) == .none,
                   "已经在用时不该主动弹框,状态 \(state)")
        }

        if failures > 0 {
            FileHandle.standardError.write("FirstRunTests: \(failures) failure(s)\n".data(using: .utf8)!)
            exit(1)
        }
        print("FirstRunTests passed")
    }

    private static var failures = 0

    private static func expect(_ condition: Bool, _ label: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write("failed: \(label)\n".data(using: .utf8)!)
        }
    }
}
