import Foundation

/// Guardian 拨不通之后,菜单**自己直接观测**到的几件事。
///
/// 这些字段此前来自 `bx doctor --json --skip-probe` 的三条检查。**doctor 的那次
/// spawn 已经删掉**,而且它的去处不是一个 Guardian 端点:能走到这条判定的**前提**
/// 就是 Guardian 不应答,同一个 socket 上的端点也答不了。CLI 当初做的也只是替我们
/// stat 一个文件、拨一个 socket——那是菜单自己就能做的观测,经由一个可能是旧版的
/// 二进制转述一遍只是多了一层会说谎的中间人(而它正是这轮架构诊断要拆掉的那层)。
///
/// 每一项都是三态(`nil` = 问不出来)。**「问不出来」绝不能被压成「答案是否定的」**
/// ——`.off(.serviceStopped)` 的全部安全性建立在两条**新鲜的否定观测**上,少一条
/// 就退化成猜。
struct StoppedEvidence: Equatable {
    /// Guardian 的 launchd plist 在不在盘上。`false` = 从没 setup 过。
    let serviceInstalled: Bool?
    /// Guardian 的 socket 上**有没有人在监听**。
    ///
    /// `false` 只在内核明确这么说时才给(ENOENT / ECONNREFUSED):没有那个文件,
    /// 或者有文件但没人 accept。超时、权限错误等一律 `nil` —— 一个挂住的 Guardian
    /// 与一个不存在的 Guardian 在「拨不通」这件事上长得一样,而两者的正确显示
    /// 完全相反。这一项取代了旧证据里的 `service_active`(launchctl 对 job 装载
    /// 状态的看法);**它比那个更贴题**:我们要问的从来不是「job 装载了吗」,
    /// 而是「它还在服务吗」。
    let guardianListening: Bool?
    /// **Core** 的控制 socket 应不应答(不是 Guardian 的)。
    let coreSocketAnswering: Bool?
    /// Core socket 拨号失败时的人话,原样透给用户。
    let coreSocketDetail: String?
    /// Guardian 拨号失败时的人话。判定拿不出更具体的结论时才用它。
    let guardianDetail: String?

    init(
        serviceInstalled: Bool? = nil,
        guardianListening: Bool? = nil,
        coreSocketAnswering: Bool? = nil,
        coreSocketDetail: String? = nil,
        guardianDetail: String? = nil
    ) {
        self.serviceInstalled = serviceInstalled
        self.guardianListening = guardianListening
        self.coreSocketAnswering = coreSocketAnswering
        self.coreSocketDetail = coreSocketDetail
        self.guardianDetail = guardianDetail
    }
}

enum StoppedDiagnosis: Equatable {
    /// 没配置过 → `.setupNeeded`
    case setupNeeded
    /// 确实什么都没在跑 → `.off(.serviceStopped)`
    case serviceStopped
    /// 其余一律告警,附带要显示的那句话。
    case warning(String)
}

/// Guardian 不应答、而 Core 还在应答时的措辞。
///
/// **绝不能写成任何暗示「已经停了」的话。** 这一行会同时出现在菜单正文的 Status
/// 行和指示灯 tooltip 上,而那一刻整机流量很可能仍然经由 TUN 被保护着。
let guardianUnreachableCoreAliveMessage = "Guardian not responding; protection may still be on"

/// Guardian 的 socket 拨不通之后,拿直接观测判菜单该显示什么。
///
/// **顺序是这条判定的全部内容:任何「没在跑」的结论都必须先证明 Core 的控制
/// socket 不应答。** Guardian 不在不等于 Core 不在 —— `launchctl bootout` 的
/// SIGTERM 不可靠地投给 Core(强制拆除因此要另经 `/v0/shutdown` 关它),所以
/// 「Guardian 没在跑而 Core 还在转发流量」是真实可达的状态。
///
/// 那个状态下报 `.serviceStopped` 会画出灰盾 + "Not running" + "Start Protection",
/// 而 quitPlan 对 `.offServiceStopped` 与 `.setupNeeded` 都判 terminateImmediately
/// —— 用户点 Quit,菜单消失,什么都没关。**保护在跑但没有任何指示灯**,正是
/// `OffOrigin` 当初分两支要挡住的那个结局。
func stoppedDiagnosis(_ evidence: StoppedEvidence) -> StoppedDiagnosis {
    if evidence.coreSocketAnswering == true {
        return .warning(guardianUnreachableCoreAliveMessage)
    }
    if evidence.serviceInstalled == false {
        return .setupNeeded
    }
    // `.off(.serviceStopped)` 要两条**新鲜的否定观测**叠在一起:Core 的控制 socket
    // 已被证实不应答,**且** Guardian 的 socket 上已被证实没人监听。
    //
    // 两个 `== false` 都是刻意的,不是 `!= true` 的笔误:`nil` 是「问不出来」,
    // 不是「证明了没有」。尤其 `guardianListening == nil` 覆盖的正是「Guardian 还
    // 在、只是挂住了」——那时屏障可能仍在生效,说它「没在跑」既不准也危险。
    if evidence.coreSocketAnswering == false, evidence.guardianListening == false {
        return .serviceStopped
    }
    if evidence.coreSocketAnswering == false {
        return .warning(evidence.coreSocketDetail ?? "Status socket unavailable")
    }
    return .warning(evidence.guardianDetail ?? "Needs attention")
}

/// 把一次 unix socket 连接的结果翻译成三态观测。
///
/// `nil` errno = 连上了。`ENOENT`/`ECONNREFUSED` 是内核明确的「没人在那儿」;
/// 其余(超时、EACCES、EAGAIN…)一律 `nil` —— 这些情形下 socket 那头**可能**
/// 好端端地活着,把它们读成「不在」正是本文件反复在挡的那种谎。
func socketObservation(connectErrno: Int32?) -> Bool? {
    guard let connectErrno else { return true }
    if connectErrno == ENOENT || connectErrno == ECONNREFUSED {
        return false
    }
    return nil
}
