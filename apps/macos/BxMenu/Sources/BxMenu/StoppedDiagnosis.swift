import Foundation

/// 一次 `bx doctor --json --skip-probe` 里,菜单用得上的三条检查。
///
/// 只带 status(与 socket 的 detail):判定不该依赖 doctor 报告的其余结构,
/// 那样它就没法被单测覆盖 —— 而它守的正是一条只有单测才盯得住的不变量。
struct StoppedEvidence: Equatable {
    /// `service_installed`:Guardian 的 launchd unit 装没装。
    let serviceInstalled: String?
    /// `service_active`:launchd 认为那个 job 装载着没有。
    let serviceActive: String?
    /// `status_socket`:**Core** 的控制 socket 拨不拨得通(不是 Guardian 的)。
    let coreSocket: String?
    let coreSocketDetail: String?

    init(
        serviceInstalled: String? = nil,
        serviceActive: String? = nil,
        coreSocket: String? = nil,
        coreSocketDetail: String? = nil
    ) {
        self.serviceInstalled = serviceInstalled
        self.serviceActive = serviceActive
        self.coreSocket = coreSocket
        self.coreSocketDetail = coreSocketDetail
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

/// Guardian 的 socket 拨不通之后,拿 doctor 的观测判菜单该显示什么。
///
/// **顺序是这条判定的全部内容:任何「没在跑」的结论都必须先证明 Core 的控制
/// socket 不应答。** Guardian 不在不等于 Core 不在 —— `launchctl bootout` 的
/// SIGTERM 不可靠地投给 Core(强制拆除因此要另经 `/v0/shutdown` 关它),所以
/// 「Guardian job 没装载而 Core 还在转发流量」是真实可达的状态。
///
/// 那个状态下报 `.serviceStopped` 会画出灰盾 + "Not running" + "Start Protection",
/// 而 quitPlan 对 `.offServiceStopped` 与 `.setupNeeded` 都判 terminateImmediately
/// —— 用户点 Quit,菜单消失,什么都没关。**保护在跑但没有任何指示灯**,正是
/// `OffOrigin` 当初分两支要挡住的那个结局。
func stoppedDiagnosis(_ evidence: StoppedEvidence) -> StoppedDiagnosis {
    if evidence.coreSocket == "ok" {
        return .warning(guardianUnreachableCoreAliveMessage)
    }
    if evidence.serviceInstalled == "fail" {
        return .setupNeeded
    }
    // `.off(.serviceStopped)` 要两条**新鲜的否定观测**叠在一起:launchd 说 job 没
    // 装载,**且** Core 的控制 socket 已被证实不应答。少了后者它就退化成只信
    // launchd 对 Guardian 的看法,而那恰恰证明不了 Core 的死活。
    // `coreSocket == nil`(doctor 没给这条检查)是「问不出来」,不是「证明了没有」。
    if let socket = evidence.coreSocket, socket != "ok", evidence.serviceActive != "ok" {
        return .serviceStopped
    }
    if let socket = evidence.coreSocket, socket != "ok" {
        return .warning(evidence.coreSocketDetail ?? "Status socket unavailable")
    }
    return .warning("Needs attention")
}
