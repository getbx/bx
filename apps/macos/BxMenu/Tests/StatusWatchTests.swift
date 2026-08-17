import Foundation

@main
struct StatusWatchTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // **能力缺席 ≠ 能力为空。** nil 是「这一版 Guardian 压根没声明过能力」
        // (旧版,键缺席),[] 是「声明了、一个都没有」。两者都不能走 watch,
        // 但它们不是同一件事,而 GuardianStatus.capabilities 刻意保留了这个区分。
        expect(!watchIsAvailable(capabilities: nil),
               "能力缺席时不该走 watch —— 旧 Guardian 会忽略未知 query 参数回一份普通应答,而那与「立刻返回因为变了」无法区分,watch 循环会退化成满速轮询")
        expect(!watchIsAvailable(capabilities: []), "能力为空时不该走 watch")
        expect(!watchIsAvailable(capabilities: ["rules", "servers"]), "没有 status_watch 时不该走 watch")
        expect(watchIsAvailable(capabilities: ["rules", "status_watch"]), "声明了 status_watch 就该走 watch")

        // 退避:增长 + 有上限。上限不是礼貌 —— 没有上限的话极大轮次会溢出,
        // 而溢出之后的值意味着满速重连。
        expect(watchBackoffSeconds(consecutiveFailures: 0) == 0, "刚成功不该等")
        let first = watchBackoffSeconds(consecutiveFailures: 1)
        let second = watchBackoffSeconds(consecutiveFailures: 2)
        expect(first > 0 && second > first, "退避没有增长:\(first) → \(second)")
        expect(watchBackoffSeconds(consecutiveFailures: 1000) <= watchBackoffMaxSeconds,
               "第 1000 次退避是 \(watchBackoffSeconds(consecutiveFailures: 1000)),超过上限")
        expect(watchBackoffSeconds(consecutiveFailures: 1000) > 0,
               "第 1000 次退避不是正数 —— 已经溢出了,而那意味着满速重连")

        // **客户端超时必须大于服务端挂住上限(25 秒)。**
        // 小于它的话客户端拿到的永远是自己的超时,服务端那个上限一次都不生效
        // —— switchServer/probeServers 的注释里已经踩过这个坑。
        expect(guardianStatusWatchTimeout > 25,
               "watch 的客户端超时是 \(guardianStatusWatchTimeout) 秒,不大于服务端的 25 秒上限")

        // 兜底轮询与 watch 的健康判断**无关**:一个会被 watch 自己的健康判断
        // 影响的兜底,在那个判断错的时候恰好也是坏的。
        expect(menuWatchBackstopSeconds >= 30,
               "兜底轮询 \(menuWatchBackstopSeconds) 秒太密 —— 它只是「watch 哑了」的保险,不是取数据的手段")

        // 第二道防线:「代际号没变」那一支的 floor 延迟必须是正数,否则一台
        // 声明了能力却仍然秒回的服务端会把这个循环烧到满速——与 CLI 侧
        // watchIdleDelay 同一个角色(见 StatusWatch.swift 里
        // menuWatchIdleDelaySeconds 的注释)。
        expect(menuWatchIdleDelaySeconds > 0,
               "menuWatchIdleDelaySeconds 必须是正数,否则「代际号没变」那一支会满速空转")

        // shouldSuppressFetch:两种失败的代价不对称(重叠取数 vs. 显式动作
        // 点了没反应),表驱动测四种组合,钉住「explicit 永不被拦」这条不对称
        // ——这条判据曾经被裸的 `guard !flag` 判反,让环境刷新设的标志把紧跟着
        // 来的显式打开也拦住(main.swift 的 fetchServersOnDemand)。
        let suppressCases: [(inFlight: Bool, explicit: Bool, expected: Bool, why: String)] = [
            (false, false, false, "没有取数在飞时,环境刷新不该被拦"),
            (false, true, false, "没有取数在飞时,显式动作当然不该被拦"),
            (true, false, true, "有一次在飞时,环境刷新应当被拦——防的是连着来的刷新叠起来"),
            (true, true, false, "有一次在飞时,显式动作仍然不许被拦——拦住的后果是点了没反应"),
        ]
        for testCase in suppressCases {
            let got = shouldSuppressFetch(inFlight: testCase.inFlight, explicit: testCase.explicit)
            expect(got == testCase.expected,
                   "shouldSuppressFetch(inFlight: \(testCase.inFlight), explicit: \(testCase.explicit)) "
                       + "= \(got),期望 \(testCase.expected) —— \(testCase.why)")
        }

        // **本仓库另外 20 个 Swift 测试套件全部以 `X passed` 收尾**,唯独这一条
        // 此前没有——那是个具体的漏洞,不是风格差异:`test-macos-menu.sh`
        // 提前 `exit 0` 时退出码仍是 0,只有这行收尾横幅能证明「这个套件真的
        // 跑到了最后一行」而不是「这个套件的调用整段消失了」。
        if failures == 0 { print("StatusWatchTests passed") } else { exit(1) }
    }
}
