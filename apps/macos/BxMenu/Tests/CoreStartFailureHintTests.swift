import Foundation

@main
struct CoreStartFailureHintTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    /// 与 Go 侧 supervisor.StartFailureCodes() 一一对应。**这份清单由
    /// internal/cli 的一条双向守卫钉住与 Go 常量逐字相同** —— 少一个码
    /// 就是一段用户永远读不到的话,而两侧测试都不会红。
    static let codes = [
        "core_tunnel_unreachable",
        "core_tunnel_handshake_failed",
        "core_tunnel_unhealthy_undetermined_udp_transport",
        "core_tunnel_unhealthy_undetermined_local_dial",
        "core_tunnel_unhealthy_undetermined",
        "core_tun_open_failed",
        "core_hijack_failed",
        "core_provision_failed",
        "core_config_unusable",
        "core_other",
    ]

    static func main() {
        let facts = CoreStartFailureServers(
            currentName: "vps",
            currentHostPort: "195.133.192.92:443",
            others: ["tokyo (166.1.190.123)"])

        // 每一种结局在**渲染出来的整段话**上两两不同。判据刻意不是枚举值:
        // 把几个分支映射到同一句「隧道没起来」照样能让一条比枚举的测试全绿,
        // 而那正是这一支要消灭的东西。
        var rendered: [String: String] = [:]
        for code in codes {
            guard let text = coreStartFailureHint(code: code, servers: facts), !text.isEmpty else {
                expect(false, "\(code) 一个字都没说")
                continue
            }
            for (other, seen) in rendered where seen == text {
                expect(false, "\(code) 与 \(other) 渲染成同一段话 —— 它们的下一步不一样")
            }
            rendered[code] = text
        }
        expect(rendered.count == codes.count, "只渲染了 \(rendered.count) 种结局,want \(codes.count)")

        // 「连不上」与「连得上但没握上」的措辞必须**相反**。说反了就是把人
        // 派去修一台好机器(reality 全挂那次的教训)。
        let unreachable = coreStartFailureHint(code: "core_tunnel_unreachable", servers: facts) ?? ""
        let handshake = coreStartFailureHint(code: "core_tunnel_handshake_failed", servers: facts) ?? ""
        expect(handshake.contains("is answering"), "「连得上」没说那台机器在应答:\(handshake)")
        expect(handshake.contains("is alive"), "「连得上」没说那台机器活着:\(handshake)")
        expect(!unreachable.contains("is answering"), "「连不上」里出现了 is answering:\(unreachable)")
        expect(!handshake.contains("may be down"), "对一台正在应答的服务器说它可能挂了:\(handshake)")

        // **只说 bx 观测到什么,绝不断言那台服务器的状态。** 这台 Mac 自己
        // 没网时同样拨不通,一句「that server is down」会让用户去重启一台
        // 好好的 VPS。
        expect(unreachable.contains("bx cannot reach"),
               "「连不上」没把它说成一次尝试的事实:\(unreachable)")
        for forbidden in ["server is down", "the server is not responding", "that machine is down."] {
            expect(!unreachable.contains(forbidden),
                   "这句话断言了那台服务器的状态(\(forbidden)):\(unreachable)")
        }
        expect(unreachable.contains("this Mac's own network"),
               "没有把「也可能是本机自己没网」说出来:\(unreachable)")

        // 三个「没判出来」都必须说出「没判出来」,一个都不许被读成「服务器没事」。
        for code in codes where code.hasPrefix("core_tunnel_unhealthy_undetermined") {
            let text = coreStartFailureHint(code: code, servers: facts) ?? ""
            expect(text.contains("could not tell"), "\(code) 没说出「没能判断」:\(text)")
            expect(!text.contains("is answering"), "\(code) 断言了服务器在应答:\(text)")
        }

        // 本机拨号失败那一档指着 **bx 自己的直连器**,不指着 VPS。
        // 2026-08-13 那次事故的签名:SYN 根本没离开这台机器,去探 VPS 白费力气。
        let localDial = coreStartFailureHint(code: "core_tunnel_unhealthy_undetermined_local_dial", servers: facts) ?? ""
        expect(localDial.contains("-ifscope"), "没给出那条出路(route -n get -ifscope):\(localDial)")
        expect(!localDial.contains("nc -z 195.133.192.92"),
               "把用户派去探那台 VPS —— SYN 根本没出去,那次探测什么也说明不了:\(localDial)")
        // **不许一边说「与那台服务器无关」、一边叫用户换一台。** 这个码盖着两种
        // 毛病、出路相反:直连器坏了(换服务器帮不上忙)与这台机器解析不出那台
        // 服务器的主机名(换一台确实有用)。判据:那句「帮不上忙」必须挂在那次
        // 路由检查的结果上,而另一种毛病必须被说出来。
        expect(localDial.contains("another server"),
               "这一档没给「你还配了另一台」—— 下面这条测的矛盾不存在了,回来重判:\(localDial)")
        expect(localDial.contains("if it says \"not in table\""),
               "「switching servers will not help」成了一句无条件断言,而同一段话下面就叫用户换一台:\(localDial)")
        expect(localDial.contains("cannot resolve"),
               "没说出这一档里那种换一台确实有用的毛病(主机名解析不出来):\(localDial)")

        // **隧道那一族的每一句话,在 bx 知道地址时都必须点名那台服务器。**
        //
        // `local_dial` 从前是这一族里唯一一句连 host:port 都没有的话,而
        // 2026-08-13 那种机器上(scoped 表里没有默认路由)最容易落进它的恰恰是
        // 「VPS 真的挂了」那一次 —— 用户读到一句既不说哪台机器、又先派他去查
        // bx 自己路由的话。菜单明明拿着那个地址。
        var namedFamily = 0
        for code in codes where code.hasPrefix("core_tunnel") {
            namedFamily += 1
            let text = coreStartFailureHint(code: code, servers: facts) ?? ""
            expect(text.contains("195.133.192.92"),
                   "\(code) 那句话里没有那台服务器的地址 —— 菜单明明拿着它:\(text)")
        }
        expect(namedFamily >= 5, "只走到 \(namedFamily) 档隧道结局(want ≥5)—— 族的判据认不出现在的码了")

        // 「你还配了另一台」只在真有另一台时出现,而且绝不出现链接。
        let alone = CoreStartFailureServers(currentName: "vps", currentHostPort: "195.133.192.92:443")
        let soloText = coreStartFailureHint(code: "core_tunnel_unreachable", servers: alone) ?? ""
        expect(!soloText.contains("another server"), "只有一台却说「你还配了另一台」:\(soloText)")
        let pairText = coreStartFailureHint(code: "core_tunnel_unreachable", servers: facts) ?? ""
        expect(pairText.contains("tokyo") && pairText.contains("Servers"),
               "有另一台却没点名或没给出路:\(pairText)")
        // 换服务器对隧道之外那几种一点用都没有 —— 不许给那句话。
        let tunOpen = coreStartFailureHint(code: "core_tun_open_failed", servers: facts) ?? ""
        expect(!tunOpen.contains("another server"),
               "开 TUN 失败也建议换服务器 —— 换哪台都一样打不开:\(tunOpen)")

        for code in codes {
            let text = coreStartFailureHint(code: code, servers: facts) ?? ""
            for secret in ["vless://", "bx://", "hysteria2://"] {
                expect(!text.contains(secret), "\(code) 的那段话里出现了链接片段 \(secret)")
            }
        }

        // 认不出的码 / 无码 / 别族的码,一个字都不许编。
        expect(coreStartFailureHint(code: nil, servers: facts) == nil, "无码时编了话")
        expect(coreStartFailureHint(code: "", servers: facts) == nil, "空码时编了话")
        expect(coreStartFailureHint(code: "core_我是新来的", servers: facts) == nil, "认不出的码编了话")
        expect(coreStartFailureHint(code: "core_ownership_uncertain", servers: facts) == nil,
               "别族的码被这一族接走了 —— 它有自己的指引")
        expect(coreStartFailureHint(code: "guardian_busy", servers: facts) == nil, "无前缀的码被接走了")

        // 问不出地址时那句话仍然成立,但**不许编一个占位主机**:
        // 一句指着 <unknown>:0 的排查命令比不给更糟。
        let nameless = coreStartFailureHint(code: "core_tunnel_unreachable", servers: CoreStartFailureServers()) ?? ""
        expect(!nameless.isEmpty, "读不到清单就一个字都不说了")
        for forbidden in ["<", ":0", "unknown"] {
            expect(!nameless.contains(forbidden), "编了一个占位地址(\(forbidden)):\(nameless)")
        }

        // 清单折成事实:当前那台与其余那几台分得开;端口为 0 时**不写 :0**。
        let folded = coreStartFailureServers([
            CoreStartFailureServer(name: "vps", host: "195.133.192.92", port: 443, isCurrent: true),
            CoreStartFailureServer(name: "tokyo", host: "166.1.190.123", port: 8443, isCurrent: false),
        ])
        expect(folded.currentName == "vps" && folded.currentHostPort == "195.133.192.92:443",
               "当前那台折错了:\(folded)")
        expect(folded.others == ["tokyo (166.1.190.123)"], "另一台折错了:\(folded.others)")
        let noPort = coreStartFailureServers([
            CoreStartFailureServer(name: "vps", host: "195.133.192.92", port: 0, isCurrent: true),
        ])
        expect(noPort.currentHostPort == "195.133.192.92",
               "端口问不出来时写了一个 :0:\(noPort.currentHostPort)")

        // **每一种结局都要给出「完整原因在哪儿」。** 应答体只带一个码,而真正
        // 那句话(事故那次是 dial tcp <server>:443: i/o timeout)只在 root-only
        // 的 Core 日志里 —— 这条指引是两者之间唯一的桥。tunnel_unreachable 此前
        // 是唯一没有它的一种,而它恰恰就是事故那一种。
        for code in codes {
            let text = coreStartFailureHint(code: code, servers: facts) ?? ""
            expect(text.contains("sudo tail -50 /var/log/bx.log"),
                   "\(code) 没告诉用户完整原因在哪儿:\(text)")
        }

        // 用户可见的那几行里不许有 markdown 的 `**`:NSAlert 不渲染它,
        // 用户读到的是字面上的星号(三条曾经就这么发出去过)。
        for code in codes {
            for servers in [facts, CoreStartFailureServers()] {
                let text = coreStartFailureHint(code: code, servers: servers) ?? ""
                expect(!text.contains("**"), "\(code) 渲染出了 markdown 的 `**`:\(text)")
            }
        }

        // 端口解不出来时那条 nc 命令整条不给 —— 不许渲染出尾巴上空着的端口。
        let hostOnly = CoreStartFailureServers(currentName: "vps", currentHostPort: "195.133.192.92")
        for code in codes {
            let text = coreStartFailureHint(code: code, servers: hostOnly) ?? ""
            expect(!text.contains("nc -z 195.133.192.92 "),
                   "\(code) 在端口未知时渲染出了 `nc -z <主机> `(尾巴上一个空端口):\(text)")
        }
        // 反面:端口问得出来时那条命令仍然要给,否则「一律不给」也能满足上面。
        expect((coreStartFailureHint(code: "core_tunnel_unreachable", servers: facts) ?? "")
                .contains("nc -z 195.133.192.92 443"),
               "端口问得出来时反而不给 nc 命令了")

        // 一次成功的开关不许被这一族接走。
        expect(toggleFailureMessage(code: nil, transportDescription: nil, servers: facts) == nil,
               "没有码也没有传输错误时编了话")

        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
        print("core start failure hint tests passed")
    }
}
