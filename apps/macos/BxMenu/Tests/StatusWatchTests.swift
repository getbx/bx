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

        exit(failures == 0 ? 0 : 1)
    }
}
