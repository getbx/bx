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

    /// **归因按组汇总,而不是逐条列域名。** 十行域名对普通用户没有意义,
    /// 「Steam 相关的走不通」有 —— 这是这一版整个改动的理由。
    static func testGroupRowsAggregateFailuresAndPutBrokenGroupsFirst() {
        let list = RuleList(groups: [
            RuleGroup(name: "apple", title: "Apple 服务", summary: "Apple 系统服务",
                      state: .on, installed: 6, total: 6, domains: ["*.icloud.com", "*.apple.com"]),
            RuleGroup(name: "gaming", title: "Steam / 游戏", state: .on, installed: 5, total: 5,
                      domains: ["*.steamstatic.com", "*.steamcontent.com"]),
        ])
        let rows = ruleGroupRows(from: list, failing: [
            FailingRule(kind: .direct, rule: "*.steamstatic.com", attempts: 800, failures: 800),
            FailingRule(kind: .direct, rule: "*.steamcontent.com", attempts: 200, failures: 200),
        ])
        expect(rows.first?.group.name == "gaming", "坏掉的组没排最前:\(rows.map(\.group.name))")
        expect(rows.first?.failing == 2, "组内失败规则数不对:\(String(describing: rows.first?.failing))")
        expect(rows.first?.trailing?.contains("1000") == true,
               "失败总数没汇总到组上:\(String(describing: rows.first?.trailing))")
        // 好的那一组**一个字都不说**。
        let apple = rows.first { $0.group.name == "apple" }
        expect(apple?.failing == 0, "好的组被标成失败")
        expect(apple?.trailing == nil, "好的组还在说话:\(apple?.trailing ?? "")")
    }

    /// **半装的组要看得出来是半装的。** 画成「开」用户会以为那几条在生效。
    static func testPartialGroupIsVisiblyPartial() {
        let list = RuleList(groups: [
            RuleGroup(name: "apple", title: "Apple 服务", state: .partial, installed: 2, total: 6),
        ])
        let row = ruleGroupRows(from: list, failing: [])[0]
        expect(row.isMixed, "半装的组没被标成 mixed")
        expect(!row.isOn, "半装的组被当成全开")
        expect(row.trailing?.contains("2") == true && row.trailing?.contains("6") == true,
               "副标题没说清装了几条:\(row.trailing ?? "")")
    }

    /// 认不出的 state 落到 partial —— **三态里唯一不撒谎的那个**。
    /// 说「装了一部分」在任何情况下都不构成一句关于生效与否的断言。
    static func testUnknownGroupStateFallsBackToPartial() {
        let json = Data(#"{"groups":[{"name":"x","title":"X","state":"brand-new-state"}]}"#.utf8)
        guard let list = try? JSONDecoder().decode(RuleList.self, from: json) else {
            expect(false, "没见过的 state 让整份应答解不出来了 —— 旧菜单会因此瞎掉")
            return
        }
        expect(list.groups.first?.state == .partial, "没见过的 state 没落到 partial")
    }

    /// 失败只算**这一组名下**的域名:同一次泄漏不该在三个组里各报一遍。
    static func testFailuresAreNotCountedAgainstUnrelatedGroups() {
        let list = RuleList(groups: [
            RuleGroup(name: "apple", title: "A", state: .on, domains: ["*.icloud.com"]),
            RuleGroup(name: "gaming", title: "G", state: .on, domains: ["*.steamstatic.com"]),
        ])
        let rows = ruleGroupRows(from: list, failing: [
            FailingRule(kind: .direct, rule: "*.steamstatic.com", attempts: 10, failures: 10),
        ])
        let apple = rows.first { $0.group.name == "apple" }
        expect(apple?.failing == 0, "别的组的失败算到了 apple 头上")
    }

    static func testRequiresRestartAbsenceIsNotFalse() {
    // 服务端恒为 true;键缺席意味着「这一版没说」,与「不需要重启」是两回事。
    let quiet = try! JSONDecoder().decode(RuleList.self, from: Data(#"{"direct":["a.com"]}"#.utf8))
    expect(quiet.requiresRestart == nil, "键缺席被读成了 false")
    let spoken = try! JSONDecoder().decode(RuleList.self, from: Data(#"{"requires_restart":true}"#.utf8))
    expect(spoken.requiresRestart == true, "requires_restart 没解出来")
}

    /// **标签式:名字要短,而且不解释。**
    ///
    /// 项目所有者的原话:「tag 类似于 github 里面那种标签感觉即可。然后解释也略多,
    /// 不说人话。宁愿不要解释。」上一版每一组都挂着我写给开发者看的整段说明
    /// (「只含纯字节:游戏文件、客户端更新…商店与社区页面刻意不在其中」),
    /// 那是设计笔记,不是界面文案。
    ///
    /// 现在:**没问题的组一个字都不说**;有话说时只说数字。
    static func testHealthyGroupSaysNothing() {
        let rows = ruleGroupRows(from: RuleList(groups: [
            RuleGroup(name: "apple", title: "Apple",
                      summary: "Apple 系统服务、Game Center、Arcade、iCloud 同步可用性",
                      state: .on, installed: 6, total: 6),
        ]), failing: [])
        expect(rows[0].trailing == nil,
               "健康的组还在解释自己:\(rows[0].trailing ?? "")")
    }

    /// 半装的组只报数字,不解释「半装」是什么意思。
    static func testPartialGroupShowsOnlyTheCount() {
        let rows = ruleGroupRows(from: RuleList(groups: [
            RuleGroup(name: "apple", title: "Apple", state: .partial, installed: 2, total: 6),
        ]), failing: [])
        guard let detail = rows[0].trailing else {
            expect(false, "半装的组什么都没说")
            return
        }
        expect(detail.contains("2") && detail.contains("6"), "没报数字:\(detail)")
        expect(detail.count <= 12, "半装那行太长了(\(detail.count) 字符):\(detail)")
    }

    /// 出问题的组只说「多少条失败」—— 那是唯一要用户行动的信息。
    static func testFailingGroupSaysOnlyWhatMatters() {
        let rows = ruleGroupRows(from: RuleList(groups: [
            RuleGroup(name: "gaming", title: "Steam", state: .on, installed: 4, total: 4,
                      domains: ["*.steamcontent.com"]),
        ]), failing: [
            FailingRule(kind: .direct, rule: "*.steamcontent.com", attempts: 900, failures: 900),
        ])
        guard let detail = rows[0].trailing else {
            expect(false, "坏掉的组什么都没说")
            return
        }
        expect(detail.contains("900"), "没说失败了多少条:\(detail)")
        expect(detail.count <= 24, "失败那行太长了(\(detail.count) 字符):\(detail)")
    }

    /// **标题必须短到能当标签用。** 长标题会把这个窗口变回一份说明书。
    static func testGroupTitlesAreShortEnoughToBeTags() {
        for group in ["apple", "gaming", "china-cdn"] {
            let title = presetTitleForTest(group)
            expect(!title.isEmpty, "\(group) 没有标题")
            expect(title.count <= 12, "\(group) 的标题当不了标签(\(title.count) 字符):\(title)")
            expect(!title.contains("("), "\(group) 的标题里塞了括号说明:\(title)")
        }
    }

    /// **换配置的确认框显示事实,不讲道理。**
    ///
    /// 项目所有者的立场:用户可以有很多躺着的节点,但换出口是件要看清楚的事。
    /// 而真正的防线是**让他看见出口要变**,不是让他多点两下或者读一段告诫。
    static func testReplaceMessageShowsTheExitChangeNotALecture() {
        let text = replaceConfigurationMessage(currentServer: "166.1.190.123", pastedFrom: .clipboard)
        expect(text.contains("166.1.190.123"), "没说现在的出口是哪:\(text)")
        expect(text.lowercased().contains("clipboard"), "没说链接是从剪贴板来的:\(text)")
        // 不许出现空泛的风险告诫 —— 那种句子只会被点穿。
        for lecture in ["risk", "dangerous", "be careful", "are you sure"] {
            expect(!text.lowercased().contains(lecture), "混进了空泛的告诫(\(lecture)):\(text)")
        }
    }

    /// 认不出当前出口时**只说新的**,不编一个「未知」当对照。
    static func testReplaceMessageOmitsTheOldServerWhenUnknown() {
        let text = replaceConfigurationMessage(currentServer: nil, pastedFrom: .typed)
        expect(!text.lowercased().contains("unknown"), "编了一个「未知」出口:\(text)")
        expect(!text.lowercased().contains("clipboard"), "没从剪贴板来却说了剪贴板:\(text)")
        expect(text.contains("reconnect"), "没说要重连才生效:\(text)")
    }

    /// 剪贴板预填:像链接才吃,多行不吃。
    static func testClipboardCandidateOnlyTakesASingleLinkLine() {
        expect(clipboardCandidateLink("  bx://abc  ") == "bx://abc", "没吃掉合法链接周围的空白")
        expect(clipboardCandidateLink("vless://u@1.2.3.4:443") != nil, "裸链接也该认")
        // **样例必须以链接开头**,否则前缀检查会顺带把它挡掉,这条断言就测不到
        // 「多行」那道闸门(变异验证当场发现:把多行检查改成恒真,仍然全绿)。
        // 而且链接在前、后面跟着一句话,正是从聊天窗口整段复制的真实形状。
        expect(clipboardCandidateLink("bx://abc\n这是我给你的链接,记得装") == nil,
               "多行文本被当成了链接 —— 那多半是聊天记录")
        expect(clipboardCandidateLink("这是我给你的链接\nbx://abc") == nil,
               "链接不在开头时也不该吃")
        expect(clipboardCandidateLink("随便一句话") == nil, "把普通文本当成了链接")
        expect(clipboardCandidateLink(nil) == nil, "空剪贴板")
        expect(clipboardCandidateLink(String(repeating: "bx://", count: 4000)) == nil, "超长文本没挡住")
    }

    static func main() {
        testReplaceMessageShowsTheExitChangeNotALecture()
        testReplaceMessageOmitsTheOldServerWhenUnknown()
        testClipboardCandidateOnlyTakesASingleLinkLine()
        testHealthyGroupSaysNothing()
        testPartialGroupShowsOnlyTheCount()
        testFailingGroupSaysOnlyWhatMatters()
        testGroupTitlesAreShortEnoughToBeTags()
        testRuleRowsPutFailingRulesFirst()
        testRuleRowsMatchFailuresByKindNotJustName()
        testRuleRowsAreCaseInsensitiveWhenMatching()
        testValidateRulePatternRejectsWhatWouldBreakTheConfig()
        testRulesEditingHiddenOnOlderGuardian()
        testGroupRowsAggregateFailuresAndPutBrokenGroupsFirst()
        testPartialGroupIsVisiblyPartial()
        testUnknownGroupStateFallsBackToPartial()
        testFailuresAreNotCountedAgainstUnrelatedGroups()
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

/// presetTitleForTest 是 Go 侧 internal/preset 那份标题的镜像,供 Swift 套件断言
/// 「短到能当标签」。**Go 侧另有一条同样的守卫** —— 两边都钉,是因为标题是
/// 界面文案,而界面在这两个语言里各有一半。
func presetTitleForTest(_ name: String) -> String {
    switch name {
    case "apple": return "Apple"
    case "gaming": return "Steam"
    case "china-cdn": return "China CDN"
    default: return ""
    }
}
