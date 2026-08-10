import Foundation

/// 与 Go 侧 `guardian.CapabilityMaintenanceHold` 逐字对应
/// (TestMacMenuMaintenanceHoldVocabularyMatchesGuardian 跨语言钉住)。
let maintenanceHoldCapability = "maintenance_hold"

/// **nil 不是「没有挂起」,是「这一版 Guardian 没有挂起这个概念」。**
/// 二者不分开,升级窗口里的旧 Guardian 会被读成「确定没有维护在进行」——
/// 与 declaresDiagnosticsArchive 同一条纪律,理由见 GuardianStatus.capabilities。
///
/// 声明了却不含,与压根没声明,在这里落到同一个结论(不显示挂起),但**理由不同**:
/// 前者是「这一版有这个概念、此刻确实没有挂起」,后者是「不知道」。两者的显示都
/// 只能是「什么都不说」——凭空造一行「可能正在维护」对每一台旧机器恒真,正是
/// writeClientMaintenanceHold 拒绝的那种噪声。
func declaresMaintenanceHold(_ capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains(maintenanceHoldCapability)
}

/// 解析 Guardian 发布的 `expires_at`。
///
/// **两个 formatter 缺一不可,而且默认那个单独用必然失效。** Go 的 `time.Time`
/// 走 RFC3339Nano:挂起的过期时刻是 `time.Now().Add(15m)`,纳秒几乎不可能恰好为零,
/// 于是线上真实的字节长这样 —— `2026-08-10T08:21:33.056964-07:00`。而
/// `ISO8601DateFormatter()` 的默认 formatOptions **不含** `.withFractionalSeconds`,
/// 对这个字符串返回 nil(本机实测);反过来打开小数秒之后,不带小数的
/// `…T00:00:00Z` 又解不动(同样实测)。只用其中一个,结果不是崩溃而是**这一行
/// 永远不出现** —— 一次安静的失败,正是本仓库反复栽的那一类。
func maintenanceHoldExpiry(_ raw: String) -> Date? {
    let fractional = ISO8601DateFormatter()
    fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let date = fractional.date(from: raw) {
        return date
    }
    return ISO8601DateFormatter().date(from: raw)
}

/// 把稳定标识符翻成一句人话,**并保留标识符本身**。
///
/// 与 Go 侧 `maintenanceHoldReasonLabel`(internal/cli/cli.go)同一条纪律:标识符是
/// Guardian 日志里 grep 得到的那个词(`reason=legacy_upgrade`),只印人话会让用户
/// 没法把菜单这一行与日志对上;只印标识符则是把内部词汇丢给用户。**未知来由如实
/// 只印标识符** —— 编一句人话出来比不编更糟。
func maintenanceHoldReasonLabel(_ reason: String?) -> String {
    guard let reason, !reason.isEmpty else {
        // 连来由都没有:说「维护」是它字面上的意思,不多说一个字。
        return "maintenance"
    }
    let labels = [
        "upgrade": "updating bx",
        "legacy_upgrade": "updating bx, migrated from an older upgrade record",
    ]
    guard let label = labels[reason] else { return reason }
    return "\(label) (\(reason))"
}

/// 维护挂起那一行。没有挂起、挂起过期、或这一版根本不认识挂起 ⇒ 不显示。
///
/// mark 用 `.unknown` 而不是 `.bad`:挂起不是异常,而 anomalyCount 只数 `.bad`
/// 并且驱动图标裂不裂 —— 一次正常的升级不该让图标裂开。
///
/// **过期在渲染这一刻重判,不是信 Guardian 那一次 RPC。** Guardian 只发布还武装着
/// 的挂起,但菜单最长可能拿着 30 秒前的那份状态(见 MenuCadence),盘上早过期了
/// 还在屏幕上宣告「保护被有意压制」就是一句谎。
func maintenanceRow(status: GuardianStatus?, now: Date) -> MenuRow? {
    guard let status, declaresMaintenanceHold(status.capabilities),
          let hold = status.maintenanceHold,
          let raw = hold.expiresAt,
          let expires = maintenanceHoldExpiry(raw),
          expires > now
    else { return nil }
    // 下限钳到 1:剩余时间是渲染那一刻现算的,不足一分钟时 Int() 会截成 0,
    // 而「up to 0 min」既没用又和这一行同时断言的「还在生效」自相矛盾。
    // 负数到不了这里(上面 expires > now 已经挡住),这与 Go 侧
    // maintenanceHoldRemaining 把剩余时间钳到零是同一条纪律。
    let minutes = max(1, Int(expires.timeIntervalSince(now) / 60))
    return MenuRow(label: "Maintenance",
                   value: "Paused for \(maintenanceHoldReasonLabel(hold.reason)) — up to \(minutes) min",
                   mark: .unknown)
}

/// 保护关着时表头的副标题。
///
/// **「维护挂起」与「用户自己关的」在菜单上必须长得不一样。** 挂起期间
/// `protection_state` 就是 `off`,于是这一屏此前与用户主动关掉保护逐字相同:
/// 一个大写的 Off 加一句 Not running —— 非技术用户唯一看得见的那块界面,把
/// 「bx 正在自我升级」说成了「你把它关了」。
///
/// 判据直接复用 maintenanceRow(同一个能力门、同一次过期判定):两处各写一遍
/// 条件,迟早出现「表头说 Paused、下面一行挂起却不显示」这种自相矛盾。
func offSubtitle(status: GuardianStatus?, now: Date) -> String {
    maintenanceRow(status: status, now: now) == nil ? "Off" : "Paused"
}
