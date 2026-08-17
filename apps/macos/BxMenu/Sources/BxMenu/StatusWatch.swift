import Foundation

/// Guardian 侧 watchMaxHold = 25 秒。**客户端必须比它长**,否则拿到的永远是
/// 自己的超时,而服务端那个上限一次都不会生效 —— 与 switchServer/probeServers
/// 注释里记下的是同一个坑。
///
/// **这一对数字是跨语言手抄的,没有守卫能同时钉住两边。** Go 侧那个 25 秒
/// (`internal/guardian` 里挂住的上限)未导出,Swift 侧拿不到,只能靠人读两份
/// 源码时都留意到。这里只能钉住 Swift 这一侧「比 25 大」,证明不了 Go 那边
/// 真的是 25——它变了这条测试也不会红。
let guardianStatusWatchTimeout: TimeInterval = 40

/// 重连退避上限。**上限不是礼貌,是正确性**:没有上限的指数退避在极大轮次上
/// 会溢出,而溢出之后的值意味着满速重连。
let watchBackoffMaxSeconds: TimeInterval = 30

/// 兜底轮询间隔。
///
/// **它与 watch 的健康判断无关,而且刻意如此。** watch 有一类失效是静默的
/// (连接半开、循环自己死掉),此时没有任何东西会报错,菜单就停在最后一次收到
/// 的状态上而看起来完全正常。一个会被 watch 自己的健康判断影响的兜底,
/// 在那个判断错的时候恰好也是坏的 —— 所以它是个常量,永远在跑。
///
/// 「指示灯不再指示」是这个组件最坏的失效模式(这个项目为此删掉过 Quit Menu)。
let menuWatchBackstopSeconds: TimeInterval = 60

/// watchIsAvailable 判断这一版 Guardian 认不认 `/v1/status?wait=`。
///
/// **nil 与 [] 是两件事**:nil 是「这一版压根没声明过能力」(旧 Guardian,
/// 键缺席),[] 是「声明了、一个都没有」。两者都不走 watch,但不是同一件事,
/// 而 GuardianStatus.capabilities 刻意保留了这个区分。
///
/// **绝不「试着拨一下看看」**:旧 Guardian 会忽略未知 query 参数、回一份普通
/// 应答,而那与「立刻返回因为状态变了」在客户端看来一模一样 —— 于是 watch
/// 循环会退化成一个满速轮询。
func watchIsAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("status_watch")
}

/// 连续第 n 次失败之后该等多久。n == 0(刚成功)不等。
///
/// **先判轮次再算乘法**:先乘后钳会在极大轮次上溢出。
func watchBackoffSeconds(consecutiveFailures: Int) -> TimeInterval {
    guard consecutiveFailures > 0 else { return 0 }
    if consecutiveFailures > 10 { return watchBackoffMaxSeconds }
    let delay = pow(2.0, Double(consecutiveFailures - 1))
    return min(delay, watchBackoffMaxSeconds)
}
