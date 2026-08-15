import Foundation

@main
struct DeployModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    // 正常输入拼出的命令必须正好是 bx 自己那条 —— 拼错的后果是一条看起来对、
    // 跑起来错的命令,而用户只会看到 ssh 的一句莫名其妙的报错。
    static func testCommandLineShape() {
        let command = deployCommandLine(DeployTarget(host: "1.2.3.4", user: "root", name: "osaka"))
        expect(command == "'/usr/local/bin/bx' server deploy --name 'osaka' 'root@1.2.3.4'",
               "命令拼错了:\(command)")
    }

    // 名字留空 = 不自动加进清单,那时不许带 --name(带一个空的会被 CLI 拒绝)。
    static func testNoNameMeansNoFlag() {
        let command = deployCommandLine(DeployTarget(host: "vps-tokyo", user: "admin"))
        expect(!command.contains("--name"), "名字为空却带上了 --name:\(command)")
        expect(command.contains("'admin@vps-tokyo'"), "目标拼错了:\(command)")
    }

    // **每一个插值都必须单引号包起来。** 值来自文本框;不包的话一个引号就能改变
    // 这条命令的结构 —— 与 GuardianClient 用 JSONSerialization 而不是手拼 JSON
    // 同一条纪律。校验会挡住大部分,但拼命令这一步不许依赖上一步。
    static func testEveryInterpolationIsQuoted() {
        let command = deployCommandLine(DeployTarget(host: "h", user: "u", name: "a'b"))
        expect(command.contains(#"'a'\''b'"#), "单引号没有被正确转义:\(command)")
        // 转义之后不该留下任何一个能结束引号又开始新词的裸分号 / 反引号。
        for danger in ["; rm", "&&", "`"] {
            expect(!command.contains(danger), "命令里出现了 \(danger):\(command)")
        }
    }

    // 校验挡的是打字错误。**@ 要单独挡**:用户很容易把 root@1.2.3.4 整个粘进
    // 地址框,那会拼出 root@root@1.2.3.4。
    static func testValidationCatchesTypos() {
        expect(deployValidationError(DeployTarget(host: "", user: "root")) != nil, "空地址被放行")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4", user: "")) != nil, "空用户名被放行")
        expect(deployValidationError(DeployTarget(host: "root@1.2.3.4", user: "root")) != nil,
               "地址里带 @ 被放行 —— 会拼出 root@root@1.2.3.4")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4 ; rm -rf /", user: "root")) != nil,
               "带空格的地址被放行")
        expect(deployValidationError(DeployTarget(host: "1.2.3.4", user: "root")) == nil,
               "正常输入被拒绝")
        expect(deployValidationError(DeployTarget(host: "vps-tokyo", user: "ubuntu", name: "tokyo-1")) == nil,
               "正常输入(含别名与名字)被拒绝")
    }

    // 名字的规则必须与 Go 侧 config.ValidateServerName 一致。**这里放行而那边
    // 拒绝**,用户会看着一条成功的部署以一句配置错误收场。
    static func testNameRulesMatchTheConfig() {
        for bad in ["osaka jp", "osaka/jp", "osaka:1", "东京", String(repeating: "a", count: 65)] {
            expect(deployValidationError(DeployTarget(host: "h", user: "u", name: bad)) != nil,
                   "非法名字 \(bad.prefix(20)) 被放行")
        }
        for good in ["osaka", "osaka-1", "osaka_1", "vps.tokyo", "A1"] {
            expect(deployValidationError(DeployTarget(host: "h", user: "u", name: good)) == nil,
                   "合法名字 \(good) 被拒绝")
        }
    }

    // **凭据去哪儿了必须写在用户看得见的地方** —— 表单上一句,终端里再一句。
    // 一个会 ssh 到别人机器上装东西的动作,不该只在设计文档里解释过。
    static func testCredentialNoteIsVisibleInBothPlaces() {
        expect(deployCredentialNote.lowercased().contains("never handles"),
               "表单上没说清楚凭据不经过 bx:\(deployCredentialNote)")
        let script = deployScriptText(DeployTarget(host: "1.2.3.4", user: "root", name: "osaka"))
        expect(script.lowercased().contains("never sees it"), "终端里没说凭据的事:\(script)")
        expect(script.hasPrefix("#!/bin/sh"), "脚本没有 shebang:\(script)")
        // exec 掉,免得终端里多留一层 shell —— 用户 Ctrl-C 时要打断的是 ssh。
        expect(script.contains("exec '/usr/local/bin/bx' server deploy"), "脚本没有 exec 那条命令:\(script)")
    }

    static func main() {
        testCommandLineShape()
        testNoNameMeansNoFlag()
        testEveryInterpolationIsQuoted()
        testValidationCatchesTypos()
        testNameRulesMatchTheConfig()
        testCredentialNoteIsVisibleInBothPlaces()
        if failures == 0 {
            print("DeployModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
