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

/// 「服务端应答了、但代际号没变」那一支的 floor 延迟。
///
/// **这是第二道防线,不是主要机制**——主要机制是 `watchIsAvailable` 那道能力
/// 门控。一台真正认得 `wait=` 的服务端应该已经挂住到最多服务端的 25 秒上限
/// (`guardianStatusWatchTimeout` 注释里的那个数)才回,所以这个 floor 正常
/// 情况下不会真的被等到。它兜的是「第一道门被绕过,或者对面明明声明了
/// `status_watch` 能力却仍然回一份没有代际号推进的应答」这种理论上不该发生、
/// 但不该让客户端付出满速空转代价的情形。
///
/// **它不是推测性的**——CLI 侧 `bx status --watch`(`internal/cli/statuswatch.go`
/// 的 `watchIdleDelay`)在真机上撞到过同一个坑:对一台不认 `wait=` 的旧
/// Guardian,GET /v1/status 秒回、响应里连 status_generation 字段都没有,
/// 解出来是零值,与起始 generation 0 恰好相等,「未变化」分支被命中,没有
/// floor 就会以满速一直打本机 unix socket(真机实测 CPU 常驻 26%~46%、
/// 吞吐上千次/秒)。菜单这一侧走的是同一份协议,却在此之前只有第一道门,
/// 没有第二道——同一份契约的两个消费方,防线不该不一致。
///
/// 数值与 CLI 侧的 `watchIdleDelay`(1 秒)同源,取一致。
let menuWatchIdleDelaySeconds: TimeInterval = 1

/// 连续第 n 次失败之后该等多久。n == 0(刚成功)不等。
///
/// **先判轮次再算乘法**:先乘后钳会在极大轮次上溢出。
func watchBackoffSeconds(consecutiveFailures: Int) -> TimeInterval {
    guard consecutiveFailures > 0 else { return 0 }
    if consecutiveFailures > 10 { return watchBackoffMaxSeconds }
    let delay = pow(2.0, Double(consecutiveFailures - 1))
    return min(delay, watchBackoffMaxSeconds)
}

/// 一个按需取数的 in-flight 守卫,该不该拦住**这一次**调用。
///
/// **两种失败的代价不对称,这是判据。** 重叠取数的代价是一次多余的本机 unix
/// socket 往返(亚秒级,已判定无害)。拦住一次显式动作的代价是「用户点了菜单
/// 项、窗口没出现、没有 alert,什么都没发生」——点了没反应。这两种代价不该用
/// 同一条规则去权衡。
///
/// 所以:**`explicit == true` 永不被拦**,不管有没有一次取数正在飞;只有
/// `explicit == false`(环境刷新那一路,不是用户直接点出来的)才会在已有一次
/// 在飞时被拦住——拦的目的仅仅是不让连着来的环境刷新叠起来,漏一次的代价只是
/// 晚一拍,不是「什么都没发生」。
///
/// 用在 `main.swift` 的三个窗口上(服务器、按应用看分流、规则):它们的 in-flight
/// 标志都被「显式打开」(点菜单项)与「环境刷新」(窗口已可见时跟着状态变化再拉
/// 一次)两条路共用。两条路共用同一个布尔标志、却只用一条 `guard !flag` 去判,
/// 曾经把这个不对称判反——环境刷新设的标志会把紧跟着来的显式打开也拦住,这就
/// 是那次回归的机制,不是「运气不好撞上」。规则窗口是 2026-09-11 补上环境刷新
/// 那一路时加入的,**所以这里不再是「唯一一个」** —— 它当初写那句话时是真的。
func shouldSuppressFetch(inFlight: Bool, explicit: Bool) -> Bool {
    inFlight && !explicit
}
