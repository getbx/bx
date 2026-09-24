import Foundation

struct UpdateCheck: Decodable, Equatable {
    let current: String
    let latest: String
    let available: Bool
    let verified: Bool
}

func updateActionTitle(for check: UpdateCheck?) -> String? {
    guard let check, check.available, check.verified else { return nil }
    return "Update bx…"
}

/// 一次更新检查的结果如何并入既有答案。
///
/// **查不动就保留上一次的已知答案。** 此前是无条件覆盖:一次抖动(Guardian 刚重启、
/// 网络断了几秒)就把「有新版可装」抹成 nil,Update 入口凭空消失,而下一次检查要等
/// 到 24 小时后 —— 用户看到的是一个安静地少了一项的菜单,没有任何症状可循。
///
/// 反过来的代价很小:装完之后 `updateBx` 会立刻再查一次并覆盖掉;真查不动时多留着
/// 一个 Update 入口,点下去跑的也只是 `bx update`(已是最新时它自己就什么都不做)。
func mergedUpdateCheck(previous: UpdateCheck?, fetched: UpdateCheck?) -> UpdateCheck? {
    fetched ?? previous
}

let quitBxActionTitle = "Quit bx…"
let quitBxConfirmMessage = "bx will stop protecting system traffic, restore managed DNS settings, and close this menu. To start bx again, open Bx.app from Applications."

struct UpdateResultJSON: Decodable, Equatable {
    let fromVersion: String
    let toVersion: String
    let phase: String
    let coreActivated: Bool
    let rolledBack: Bool
    let protectionState: String
    /// 新版 Core 起不来时它自报的启动失败码(A3)。**缺席 = 没说**(旧 Guardian,或 Core
    /// 什么都没写)—— 那时那句话退回原样,不编原因。
    let coreStartFailure: String?
    enum CodingKeys: String, CodingKey {
        case fromVersion = "from_version"
        case toVersion = "to_version"
        case phase
        case coreActivated = "core_activated"
        case rolledBack = "rolled_back"
        case protectionState = "protection_state"
        case coreStartFailure = "core_start_failure"
    }
}

enum UpdateOutcome: Equatable {
    case succeeded(to: String)
    case rolledBack(from: String, reason: String?)
    case failed
}

func parseUpdateOutcome(_ logData: Data) -> UpdateOutcome {
    guard let text = String(data: logData, encoding: .utf8) else { return .failed }
    let lines = text.split(separator: "\n", omittingEmptySubsequences: true)
    for line in lines.reversed() {
        guard let result = try? JSONDecoder().decode(UpdateResultJSON.self, from: Data(line.utf8)) else {
            continue
        }
        if result.rolledBack {
            return .rolledBack(from: result.fromVersion, reason: result.coreStartFailure)
        }
        if result.phase == "committed" {
            return .succeeded(to: result.toVersion)
        }
        return .failed
    }
    return .failed
}

let updateConfirmTitle = "Update bx?"
let updateConfirmMessage = "Internet access may pause briefly. bx will reconnect automatically."
let updateSucceededMessage = "bx is up to date"
let updateRolledBackMessage = "Update couldn't be completed. Previous version restored."

/// 回滚那句话按原因分三种(A3)。**隧道那一族**(与 Go 的 supervisor.IsTunnelStartFailureCode
/// 同一组码,由 Go 侧守卫钉住)指向服务器或路径、**不是升级** —— 否则「VPS 刚好不通」
/// 会被读成「这个版本有问题」;别的原因才说「新版本在这台 Mac 上起不来」;没说就原样。
func updateRolledBackMessage(reason: String?) -> String {
    guard let reason, !reason.isEmpty else { return updateRolledBackMessage }
    if isTunnelStartFailure(reason) {
        return "Update couldn't be completed: during the switch the new version could not bring up the tunnel. "
            + "That points at your server or the path to it, not the update itself. "
            + "Previous version restored — try updating again later."
    }
    return "Update couldn't be completed: the new version could not start on this Mac. Previous version restored."
}

/// 「隧道没起来」那一族。**码与 Go 那一份逐字相同**(TestMenuTunnelStartFailureCodesMatchGo 钉住)。
func isTunnelStartFailure(_ code: String) -> Bool {
    code == "tunnel_unreachable" || code == "tunnel_handshake_failed"
        || code.hasPrefix("tunnel_unhealthy_undetermined")
}

/// 版本那一行显示什么。**有新版时这一行自己就把话说完**,颜色只做强化。
///
/// 为什么不是「用不同颜色的字」了事:菜单项被鼠标划过时会**反色** —— 一个只靠颜色
/// 的提示,恰好在用户把指针移过去要点它的那一刻消失。而这个项目在图标那期已经定过
/// 同一条原则:**状态编码在形状/文字里,不在颜色里**(色觉障碍、小尺寸、系统反色
/// 三种情况下颜色都不可靠)。所以文字承担信号,颜色承担吸引注意。
func versionRowTitle(current: String, check: UpdateCheck?) -> String {
    guard let check, check.available, check.verified,
          !check.latest.isEmpty, check.latest != current else {
        return "Version: \(current)"
    }
    return "Version: \(current) → \(check.latest) available"
}

/// 版本那一行是不是同时充当「去更新」的入口。
///
/// 合并进来是为了**每个状态只留一个更新入口**:此前页脚另有一条 "Update bx…" 动作,
/// 而版本号就在它上面几行 —— 两处说同一件事。
func versionRowOffersUpdate(check: UpdateCheck?) -> Bool {
    guard let check else { return false }
    return check.available && check.verified
}

/// 从更新日志里取出**能告诉用户发生了什么**的那一行。
///
/// 真机(2026-08-14):菜单跑完 `bx update --json`、**读到了日志、解析了它**,
/// 然后只报一句 "bx could not complete the update. Run Doctor for details." ——
/// 而日志里就写着 `download stalled: no data received…`。**原因在它手里,被扔掉了。**
///
/// 取最后一条非空、且不是进度行的内容:更新器把进度打成 `⏳ …`,而失败原因
/// 总是在最后。取不到就返回 nil —— 编一句比不说更糟。
func updateFailureDetail(_ logData: Data) -> String? {
    guard let text = String(data: logData, encoding: .utf8) else { return nil }
    for raw in text.split(separator: "\n").reversed() {
        var line = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if line.isEmpty || line.hasPrefix("⏳") {
            continue
        }
        // 更新器的错误行带 Go 的 log 前缀(`2026/08/14 06:30:31 …`),
        // 那对用户没有意义。
        line = strippedLogTimestamp(line)
        if line.isEmpty {
            continue
        }
        // 一整行 JSON 不是给人读的(`--json` 成功时打的就是它)。
        if line.hasPrefix("{") {
            continue
        }
        return line
    }
    return nil
}

/// 去掉 `2026/08/14 06:30:31 ` 这样的 Go log 前缀。
func strippedLogTimestamp(_ line: String) -> String {
    let parts = line.split(separator: " ", maxSplits: 2, omittingEmptySubsequences: false)
    guard parts.count == 3,
          parts[0].count == 10, parts[0].filter({ $0 == "/" }).count == 2,
          parts[1].count == 8, parts[1].filter({ $0 == ":" }).count == 2
    else {
        return line
    }
    return String(parts[2]).trimmingCharacters(in: .whitespaces)
}

/// CLI 每打一行下载进度用的前缀。**它是一条跨语言契约** —— 产地在
/// `internal/cli/download_progress.go` 的 formatDownloadProgress,这里手抄了一份。
/// 抄漂的后果是静默的:菜单永远找不到进度行,于是永远退回那句只有秒数的话,
/// 而两侧测试都绿。由 Go 侧 TestMenuReadsTheSameDownloadProgressMarkerTheCLIWrites 双向钉住。
let downloadProgressMarker = "⏳ downloaded "

/// 从升级日志里取**最后一行**下载进度,返回前缀之后那一段(`12.0 MB of 38.9 MB (31%)`)。
///
/// 取最后一行而不是第一行:进度是一行行追加的,第一行永远是 0%。
func lastDownloadProgressLine(_ log: String?) -> String? {
    guard let log else { return nil }
    var found: String?
    for rawLine in log.split(whereSeparator: { $0 == "\n" || $0 == "\r" }) {
        let line = rawLine.trimmingCharacters(in: .whitespaces)
        guard line.hasPrefix(downloadProgressMarker) else { continue }
        let payload = String(line.dropFirst(downloadProgressMarker.count)).trimmingCharacters(in: .whitespaces)
        if !payload.isEmpty { found = payload }
    }
    return found
}

/// 升级那一行该说什么。
///
/// **两段的性质完全不同,所以必须分开说**:下载要十几分钟、保护一动不动、走开
/// 完全没事;换文件只有几秒,但屏障装着、网络会停一下。合成一句
/// `Downloading and installing… 199s`,等于用同一句话同时表示「随便等」和「别碰」。
///
/// `installing` 三态,而**第三态是承重的**:Guardian 没答话、或这一版不发 phase 时
/// 是 nil,那时**不许挑一段说** —— 猜错哪一边都是一句我们无权说的话,退回原来那句
/// 合并文案(它没有变得更好,但它没有撒谎)。
///
/// 进度优先于秒数:一个时钟在下载死掉之后照样在涨,它答不了「是不是卡住了」;
/// 一个朝着已知终点走的字节数自己就证明自己活着。
func updateStageText(installing: Bool?, progress: String?, elapsedSeconds: Int) -> String {
    let progressText = (progress?.isEmpty == false) ? progress : nil
    switch installing {
    case .some(true):
        return "Installing… the network pauses for a few seconds (\(elapsedSeconds)s)"
    case .some(false):
        if let progressText { return "Downloading — \(progressText)" }
        return "Downloading… \(elapsedSeconds)s"
    case .none:
        if let progressText { return "Downloading and installing — \(progressText)" }
        return "Downloading and installing… \(elapsedSeconds)s"
    }
}
