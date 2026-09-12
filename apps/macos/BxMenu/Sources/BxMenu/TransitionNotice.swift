import Foundation

/// 状态转换通知的**判据**,纯函数 + 一个小状态机;投递(UNUserNotificationCenter)
/// 在 main.swift,这里一行 AppKit 都没有,好让它进 scripts/test-macos-menu.sh。
///
/// **为什么是通知、为什么只在转换上**:kill-switch 拦下流量时用户看到的只是网页
/// 转圈,唯一信号是菜单栏图标轮廓变了 —— 而那个图标 18pt、在角落里。项目所有者
/// 否掉的是**常驻**红字(常态会变墙纸);一条只在「受保护 → 阻断并持续 30 秒」
/// 那一刻发、恢复时收掉的通知是**事件**,与那条判断不冲突。
///
/// **不响的那几种,每一种都有理由**(测试逐条钉住):
/// - 30 秒内就恢复的抖动 —— 睡醒后 Wi-Fi 起落几秒就好,响了就是噪声;
/// - 用户自己关掉 / 打开 —— 那是他的意图不是事件;
/// - 从 off 打开后直接失败 —— 他正站在旁边,菜单进度条已经在说;
/// - 菜单启动时机器就已经是坏的 —— 上一段故事菜单没看见,不替它讲;
/// - starting / recovering 这类过渡态 —— 既不算变好也不算变坏。

/// 一次观测折成的信号。
enum ProtectionSignal: Equatable {
    case protectedHealthy
    /// protection_state 说 protected,但隧道不健康 —— 此刻 kill-switch 在拦。
    case protectedTunnelDown
    case blocked
    case attention
    case off
    /// starting / recovering / 认不出的状态。
    case transient

    var isDegraded: Bool {
        switch self {
        case .protectedTunnelDown, .blocked, .attention: return true
        case .protectedHealthy, .off, .transient: return false
        }
    }
}

/// `tunnelHealthy == nil` 是「Guardian 没说」,**不是**「不健康」—— 与
/// StatusReport 那条纪律同源:键缺席读成 false 会凭空造出一句 "tunnel down"。
///
/// **而它同样不是「健康」。** 这里曾经写着 `tunnelHealthy == false ? … : .protectedHealthy`
/// —— 只防住了一头,nil 从另一头滑进了那个**肯定的好答案**。调用方给的是
/// `report.core?.tunnelHealthy`,它把两种「没说」摊平成同一个 nil:`core` 整个
/// 缺席(旧 Guardian,或没接 CoreRuntime provider —— 升级窗口里的常态),以及
/// `tunnel_healthy` 这个键缺席。后果不是显示错一行:这个函数唯一的消费者是通知,
/// 而 `.protectedHealthy` 会**结束一段故障并弹一条「已恢复」**,根据是一个没人
/// 发过的字段;同一份输入 menuProtectionVerdict 给的却是 attention,于是通知与
/// 菜单栏图标对同一个瞬间各说各话。
///
/// 归到 `.transient` 而不是 `.attention`:那一档的语义正是「既不算变好也不算
/// 变坏」,状态机对它一个字不说、也不改写上一次的稳态 —— 这才是「没问出来」
/// 该有的处置。判成 attention 则是拿一个缺失的键去断言机器坏了,与原来的错误
/// 只是方向相反(而 `.attention` 那句文案会说「保护没能自己恢复」,同样是一句
/// 我们无权说的话)。图标那半照旧由 menuProtectionVerdict 显示 attention:
/// 常驻指示灯说「问不出来」,事件通知保持沉默,两者不矛盾。
///
/// `reachable == false` 不走这条路:Go 侧那时把 `tunnel_healthy` 发成零值
/// `false`(无 omitempty),于是落在下面 `.some(false)` 那一支。
func protectionSignal(protectionState: String, tunnelHealthy: Bool?) -> ProtectionSignal {
    switch protectionState {
    case "protected":
        switch tunnelHealthy {
        case .some(true): return .protectedHealthy
        case .some(false): return .protectedTunnelDown
        case .none: return .transient
        }
    case "blocked":
        return .blocked
    case "needs_attention":
        return .attention
    case "off":
        return .off
    default:
        return .transient
    }
}

/// 变坏要持续这么久才响。短于它的抖动本就不该被看见。
let transitionNoticeHoldSeconds: TimeInterval = 30

enum TransitionNoticeKind: Equatable {
    case degraded
    case recovered
}

struct TransitionNotice: Equatable {
    let kind: TransitionNoticeKind
    let title: String
    let body: String
}

/// 三种坏法各说各的,恢复一句。措辞只陈述观测到的事实,原因只给可能性 ——
/// 菜单分不清「服务器挂了」与「酒店门户」,断言其中一个就是编答案。
func transitionNoticeText(kind: TransitionNoticeKind, signal: ProtectionSignal) -> (title: String, body: String) {
    switch kind {
    case .recovered:
        return ("bx is protected again", "The tunnel is back. Traffic is flowing through bx.")
    case .degraded:
        switch signal {
        case .attention:
            return ("bx needs attention",
                    "Protection could not be restored on its own. Open the bx menu for details.")
        case .protectedTunnelDown:
            return ("bx: tunnel is down",
                    "The tunnel has been down for a while. bx is blocking traffic so nothing leaks (kill-switch).")
        default:
            return ("bx: traffic blocked",
                    "Protection is on but the tunnel is unavailable. bx is blocking traffic so nothing leaks (kill-switch).")
        }
    }
}

/// 状态机。**值类型、无时钟**:时间由调用方传进来,判据才测得准。
struct TransitionNoticeTracker: Equatable {
    /// 上一次看到的**稳态**(不含 transient)。nil = 还没观测过。
    private var lastStable: ProtectionSignal?
    /// 当前这段故障从什么时候开始;nil = 没在故障里。
    private var degradedSince: Date?
    /// 当前这段故障已经通知过。
    private var notified = false

    init() {}

    mutating func observe(_ signal: ProtectionSignal, at now: Date) -> TransitionNotice? {
        defer {
            if signal != .transient { lastStable = signal }
        }
        // 过渡态不改变任何判断:既不开始一段故障,也不结束一段。
        if signal == .transient { return nil }

        if signal.isDegraded {
            if degradedSince == nil {
                // 只有从「受保护且健康」掉下来的才算一段故障。菜单刚启动就看到坏
                // (lastStable == nil)、或从 off 打开后直接坏,都不算 —— 前者的
                // 故事菜单没看见,后者用户正站在旁边。
                guard lastStable == .protectedHealthy else { return nil }
                degradedSince = now
                notified = false
                return nil
            }
            guard !notified, let since = degradedSince,
                  now.timeIntervalSince(since) >= transitionNoticeHoldSeconds else { return nil }
            notified = true
            let text = transitionNoticeText(kind: .degraded, signal: signal)
            return TransitionNotice(kind: .degraded, title: text.title, body: text.body)
        }

        // 不在故障里:protectedHealthy / off。
        let wasNotified = notified
        degradedSince = nil
        notified = false
        if signal == .protectedHealthy, wasNotified {
            let text = transitionNoticeText(kind: .recovered, signal: signal)
            return TransitionNotice(kind: .recovered, title: text.title, body: text.body)
        }
        // off:用户关掉的,故障段静默结束,不发「已恢复」。
        return nil
    }
}
