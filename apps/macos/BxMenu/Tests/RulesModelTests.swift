import Foundation

@main
struct RulesModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func testRuleRowsPutFailingRulesFirst() {
    let list = RuleList(direct: ["*.icloud.com", "*.steamstatic.com"], proxy: ["*.blocked.com"])
    let failing = [FailingRule(kind: .direct, rule: "*.steamstatic.com", attempts: 8113, failures: 8113)]
    let rows = ruleRows(from: list, failing: failing)

    // 用户打开这个界面十有八九是因为有东西坏了 —— 坏的那条不许排在第三行。
    expect(rows.first?.pattern == "*.steamstatic.com",
           "失败的规则没排在最前:\(rows.map(\.pattern))")
    expect(rows.first?.detail?.contains("8113") == true,
           "没说清失败了多少条:\(String(describing: rows.first?.detail))")
    expect(rows.first?.detail?.contains("100%") == true,
           "没给出比例 —— 8113 条失败在 8113 条里和在 100 万条里是两回事")

    // **健康的规则一个字都不多说。** 每行都挂一句解释会把要紧的那行淹掉。
    let healthy = rows.first { $0.pattern == "*.icloud.com" }
    expect(healthy?.detail == nil, "健康规则也在说话:\(String(describing: healthy?.detail))")
}

    static func testRuleRowsMatchFailuresByKindNotJustName() {
    // 同名规则可以同时出现在 direct 与 proxy 里(语义完全相反)。
    // 只按名字对齐会把失败标到错误的那一行上。
    let list = RuleList(direct: ["*.a.com"], proxy: ["*.a.com"])
    let rows = ruleRows(from: list, failing: [
        FailingRule(kind: .proxy, rule: "*.a.com", attempts: 100, failures: 90),
    ])
    let direct = rows.first { $0.kind == .direct }
    let proxy = rows.first { $0.kind == .proxy }
    expect(direct?.failure == nil, "失败标到了错误的那一行(direct)")
    expect(proxy?.failure != nil, "proxy 那一行没标上失败")
}

    static func testRuleRowsAreCaseInsensitiveWhenMatching() {
    // 配置里写的大小写与 Core 上报的可能不同(Core 归一化过)。
    let rows = ruleRows(from: RuleList(direct: ["*.SteamStatic.com"]), failing: [
        FailingRule(kind: .direct, rule: "*.steamstatic.com", attempts: 10, failures: 10),
    ])
    expect(rows.first?.failure != nil, "大小写不同就对不上了")
}

    static func testValidateRulePatternRejectsWhatWouldBreakTheConfig() {
    for bad in ["", "   ", "has space.com", "'*.quoted.com'", "*.", "nodot", "a..b.com", ".lead.com", "trail.com.", "*.uni码.com"] {
        expect(validateRulePattern(bad) != nil, "接受了非法写法 \(bad)")
    }
    for good in ["*.qq.com", "gsa.apple.com", "a-b.example.co.uk", "*.STEAMSTATIC.com"] {
        expect(validateRulePattern(good) == nil, "拒绝了合法写法 \(good):\(validateRulePattern(good) ?? "")")
    }
    expect(normalizedRulePattern("  *.QQ.com  ") == "*.qq.com", "归一化不对")
}

    static func testRulesEditingHiddenOnOlderGuardian() {
    // **键缺席 = 旧版 Guardian**,不是「支持但为空」。少了这个判断,
    // 用户会对着一个每次点都 404 的按钮,而 404 在界面上表达不出来。
    expect(rulesEditingAvailable(capabilities: nil) == false, "没有能力清单时不该显示入口")
    expect(rulesEditingAvailable(capabilities: []) == false, "空能力清单时不该显示入口")
    expect(rulesEditingAvailable(capabilities: ["maintenance_hold"]) == false, "别的能力不该开这个入口")
    expect(rulesEditingAvailable(capabilities: ["rules"]) == true, "声明了 rules 却不显示入口")
}

    /// **一条 proxy 规则都没有是完全正常的配置。** 服务端对空列表用 omitempty,
    /// 而 Swift 合成的解码器不使用属性默认值 —— 缺键直接抛错,整个界面打不开。
    /// 这条测试写完当场就把它抓出来了。
    static func testMissingListsDecodeAsEmptyNotAsError() {
        let json = Data(#"{"direct":["*.qq.com"],"requires_restart":true}"#.utf8)
        guard let list = try? JSONDecoder().decode(RuleList.self, from: json) else {
            expect(false, "只有 direct 的应答解不出来 —— 没有 proxy 规则是常态,不是错误")
            return
        }
        expect(list.proxy.isEmpty, "缺席的 proxy 没变成空数组")
        expect(list.direct == ["*.qq.com"], "direct 解错了:\(list.direct)")
        // 两个都缺也一样(全新安装就是这个状态)。
        let bare = try? JSONDecoder().decode(RuleList.self, from: Data("{}".utf8))
        expect(bare?.direct.isEmpty == true && bare?.proxy.isEmpty == true, "空应答解不出来")
    }

    /// **归因要真的从 /v1/status 送到界面。** 判据写对而某一跳没接上,
    /// 是这个仓库全部事故的形状 —— 这条走真实的应答形状,一路解到 FailingRule。
    static func testFailingRulesArriveFromTheRealStatusShape() {
        let json = """
        {"desired":"on","phase":"idle","protection_state":"protected","core":{"reachable":true,"tunnel_healthy":true,
         "failing_rules":[{"kind":"direct","rule":"*.steamstatic.com","attempts":8113,"failures":8113}]}}
        """
        guard let status = try? JSONDecoder().decode(GuardianStatus.self, from: Data(json.utf8)) else {
            expect(false, "真实形状的 /v1/status 解不出来")
            return
        }
        let failing = status.core?.failingRules ?? []
        expect(failing.count == 1 && failing.first?.rule == "*.steamstatic.com",
               "归因没送到界面:\(failing)")
        expect(failing.first?.kind == .direct, "kind 没解出来")

        // **旧 Core 没有这个字段,菜单必须照常工作** —— 缺席是空,不是解码失败。
        let old = try? JSONDecoder().decode(
            GuardianStatus.self,
            from: Data(#"{"desired":"on","phase":"idle","protection_state":"protected","core":{"reachable":true}}"#.utf8))
        expect(old?.core?.failingRules.isEmpty == true, "旧 Core 的应答把菜单弄挂了")
    }

    static func testRequiresRestartAbsenceIsNotFalse() {
    // 服务端恒为 true;键缺席意味着「这一版没说」,与「不需要重启」是两回事。
    let quiet = try! JSONDecoder().decode(RuleList.self, from: Data(#"{"direct":["a.com"]}"#.utf8))
    expect(quiet.requiresRestart == nil, "键缺席被读成了 false")
    let spoken = try! JSONDecoder().decode(RuleList.self, from: Data(#"{"requires_restart":true}"#.utf8))
    expect(spoken.requiresRestart == true, "requires_restart 没解出来")
}

    static func main() {
        testRuleRowsPutFailingRulesFirst()
        testRuleRowsMatchFailuresByKindNotJustName()
        testRuleRowsAreCaseInsensitiveWhenMatching()
        testValidateRulePatternRejectsWhatWouldBreakTheConfig()
        testRulesEditingHiddenOnOlderGuardian()
        testFailingRulesArriveFromTheRealStatusShape()
        testMissingListsDecodeAsEmptyNotAsError()
        testRequiresRestartAbsenceIsNotFalse()
        // 通过横幅是「这个套件真的跑过」的唯一证据 —— 退出码只证明「没失败」,
        // 而一个根本没被脚本登记的套件退出码也是 0(本仓库实测栽过)。
        if failures == 0 {
            print("RulesModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
