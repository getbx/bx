import Foundation

/// 维护挂起在菜单里的呈现。
///
/// **fixture 全部用 Guardian 真的会发出来的字节**:`expires_at` 是 Go 的
/// `time.Time` 走 RFC3339Nano 印出来的,带小数秒、带数字时区偏移
/// (`2026-08-10T08:21:33.056964-07:00`,本机跑 json.Marshal 实测),过期时刻最多
/// 比此刻晚 15 分钟(`guardian.MaintenanceHoldDuration`,LoadMaintenanceHold 会拒绝
/// 更远的)。用一个 2126 年的过期时刻或不带小数秒的时间戳去测,测的是一份生产
/// 永远不会产生的输入 —— 那正是这个计划里已经栽过一次的坑。
@main
struct MaintenancePresentationTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    /// 测试自己的时间解析,**刻意不复用被测的 maintenanceHoldExpiry**:拿被测函数
    /// 去造 fixture,它坏掉时 fixture 会跟着一起坏,断言便测不出任何东西。
    static func at(_ raw: String) -> Date {
        let fractional = ISO8601DateFormatter()
        fractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = fractional.date(from: raw) { return date }
        guard let date = ISO8601DateFormatter().date(from: raw) else {
            FileHandle.standardError.write(Data("FAIL: 测试自己的 fixture 时间解析不了: \(raw)\n".utf8))
            exit(1)
        }
        return date
    }

    static func decode(_ json: String) -> GuardianStatus {
        try! JSONDecoder().decode(GuardianStatus.self, from: Data(json.utf8))
    }

    /// 挂起在生效 → 菜单必须说出来,否则它渲染成一个普通的「已关闭」外加一个
    /// 可点的 Start Protection,用户完全不知道升级正在进行。
    ///
    /// 供养这条的生产改动:`maintenanceRow` 的函数体(删掉 return 即红)、
    /// `maintenanceHoldExpiry` 里带小数秒的那个 formatter(删掉即红 —— 而线上
    /// 每一张挂起的 expires_at 都带小数秒,所以那才是真实后果)。
    static func runArmedHoldShowsRow() {
        let now = at("2026-08-10T08:21:33-07:00")
        let status = decode("""
        {"desired":"on","phase":"idle","protection_state":"off",
         "capabilities":["diagnostics_archive","reconcile_report","maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade","expires_at":"2026-08-10T08:36:03.056964-07:00"}}
        """)
        let row = maintenanceRow(status: status, now: now)
        expect(row != nil, "挂起在生效时必须有一行(线上的 expires_at 带小数秒,默认 ISO8601 formatter 解不动)")
        expect(row?.label == "Maintenance", "行标签,实际 \(String(describing: row?.label))")
        expect(row?.value.contains("upgrade") == true,
               "行里要留住日志里 grep 得到的那个标识符: \(String(describing: row?.value))")
        expect(row?.value.contains("updating bx") == true,
               "行里也要有一句人话: \(String(describing: row?.value))")
        expect(row?.value.contains("up to 14 min") == true,
               "剩余 14 分 30 秒应报 14 分,实际 \(String(describing: row?.value))")
        expect(row?.mark == .unknown, "挂起不是异常,不许计进 anomalyCount 让图标裂开")
    }

    /// 不带小数秒的时间戳同样要认(纳秒恰好为零时 Go 就这么印)。
    /// 供养:`maintenanceHoldExpiry` 末尾那个不带小数秒的兜底 formatter —— 删掉即红。
    static func runWholeSecondTimestampParses() {
        let now = at("2026-08-10T08:21:33-07:00")
        let status = decode("""
        {"desired":"on","phase":"idle","protection_state":"off",
         "capabilities":["maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade","expires_at":"2026-08-10T15:36:03Z"}}
        """)
        expect(maintenanceRow(status: status, now: now) != nil,
               "不带小数秒的 expires_at 也必须解得动")
    }

    /// 过期的挂起不再说话 —— 半边输入空间的断言等于没断言。
    /// 供养:`maintenanceRow` 里 `expires > now` 的比较。改成 `true` 即红。
    static func runExpiredHoldShowsNoRow() {
        let status = decode("""
        {"desired":"on","phase":"idle","protection_state":"off",
         "capabilities":["maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade","expires_at":"2026-08-10T08:36:03.056964-07:00"}}
        """)
        expect(maintenanceRow(status: status, now: at("2026-08-10T08:36:04-07:00")) == nil,
               "过期一秒的挂起就不该再显示 —— 菜单最长会拿着 30 秒前的状态")
    }

    /// 旧 Guardian(没声明能力、也没有这个键)**不是**「没有挂起」——它是「不知道」。
    /// 但也不能凭空造一行,故:不声明能力时不显示。
    /// 供养:`declaresMaintenanceHold(status.capabilities)` 那道门。删掉即红。
    static func runOldGuardianShowsNoRow() {
        let status = decode("""
        {"desired":"on","phase":"idle","protection_state":"off",
         "maintenance_hold":{"reason":"upgrade","expires_at":"2026-08-10T08:36:03.056964-07:00"}}
        """)
        expect(status.capabilities == nil, "旧 Guardian 连 capabilities 键都没有")
        expect(maintenanceRow(status: status, now: at("2026-08-10T08:21:33-07:00")) == nil,
               "没有能力声明时不许编一行出来")
        expect(declaresMaintenanceHold(nil) == false, "nil 能力集不得被当成「支持」")
        expect(declaresMaintenanceHold([]) == false, "空能力集不含挂起")
        expect(declaresMaintenanceHold(["reconcile_report"]) == false, "别的能力不算数")
    }

    /// 声明了能力、键缺席 = 「此刻确实没有挂起」。同样什么都不说,但这是另一半
    /// 输入空间:上面那条只证明了「没声明能力时安静」。
    /// 供养:`let hold = status.maintenanceHold` 那道 guard。
    static func runDeclaredButNoHoldShowsNoRow() {
        let status = decode("""
        {"desired":"on","phase":"idle","protection_state":"protected",
         "capabilities":["maintenance_hold"]}
        """)
        expect(status.maintenanceHold == nil, "键缺席应解成 nil")
        expect(maintenanceRow(status: status, now: at("2026-08-10T08:21:33-07:00")) == nil,
               "没有挂起时不显示")
        expect(maintenanceRow(status: nil, now: Date()) == nil, "压根没有状态时也不许崩、不许显示")
    }

    /// 未知键与缺席键都不许把整份状态解坏(菜单失明比显示不全严重得多)。
    /// 供养:`GuardianStatus.init(from:)` 里的 `decodeIfPresent` + `MaintenanceHold`
    /// 全可选字段。任一改成 `decode` ⇒ 抛错 ⇒ `decode()` 里的 `try!` 当场崩,红。
    static func runUnknownFieldsStillDecode() {
        let status = decode("""
        {"desired":"on","phase":"idle","protection_state":"off","brand_new_key":123,
         "capabilities":["maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade"}}
        """)
        expect(status.maintenanceHold?.reason == "upgrade", "挂起没解出来")
        expect(status.maintenanceHold?.expiresAt == nil, "缺席的 expires_at 应为 nil,不是解码失败")
        expect(maintenanceRow(status: status, now: Date()) == nil,
               "没有到期时刻就无从判断它还算不算数,不显示")

        // 时间戳整个解不动时也只是不显示,绝不能让整份状态解码失败。
        let garbled = decode("""
        {"desired":"on","phase":"idle","protection_state":"off",
         "capabilities":["maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade","expires_at":"not a timestamp"}}
        """)
        expect(garbled.maintenanceHold?.expiresAt == "not a timestamp", "坏时间戳应原样解成字符串")
        expect(maintenanceRow(status: garbled, now: Date()) == nil, "解不动的时间戳 ⇒ 不显示")
    }

    /// 人话 + 标识符两个都要;未知来由只印标识符,不编人话。
    /// 供养:`maintenanceHoldReasonLabel` 的映射表与它的 `guard let label else`。
    static func runReasonLabelKeepsIdentifier() {
        let upgrade = maintenanceHoldReasonLabel("upgrade")
        expect(upgrade.contains("upgrade") && upgrade.contains("updating bx"),
               "已知来由要同时给出人话与标识符,实际 \(upgrade)")
        let legacy = maintenanceHoldReasonLabel("legacy_upgrade")
        expect(legacy.contains("legacy_upgrade") && legacy.contains("older upgrade record"),
               "legacy_upgrade 的人话要说清它从旧记录迁移而来,实际 \(legacy)")
        expect(maintenanceHoldReasonLabel("some_future_reason") == "some_future_reason",
               "未知来由只印标识符,编一句人话比不编更糟,实际 \(maintenanceHoldReasonLabel("some_future_reason"))")
        expect(maintenanceHoldReasonLabel(nil) == "maintenance", "没有来由时只说「维护」")
        expect(maintenanceHoldReasonLabel("") == "maintenance", "空来由等同没有来由")
    }

    /// 剩余时间不足一分钟时报 1,不报 0 —— 「up to 0 min」与同一行断言的
    /// 「还在生效」自相矛盾。供养:`max(1, …)`。改成 `Int(…)` 即红。
    static func runRemainingMinutesNeverCollapseToZero() {
        let now = at("2026-08-10T08:21:33-07:00")
        let status = decode("""
        {"desired":"on","phase":"idle","protection_state":"off",
         "capabilities":["maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade","expires_at":"2026-08-10T08:21:53.056964-07:00"}}
        """)
        let row = maintenanceRow(status: status, now: now)
        expect(row?.value.contains("up to 1 min") == true,
               "还剩 20 秒应报 1 分钟,实际 \(String(describing: row?.value))")
    }

    /// 表头必须分得开「维护挂起」与「用户自己关的」。挂起期间 protection_state
    /// 就是 off,菜单此前与用户主动关闭逐字相同。
    /// 供养:`offSubtitle` 的判据。恒返回 "Off" 即红。
    static func runOffSubtitleSaysPausedOnlyDuringHold() {
        let now = at("2026-08-10T08:21:33-07:00")
        let held = decode("""
        {"desired":"on","phase":"idle","protection_state":"off",
         "capabilities":["maintenance_hold"],
         "maintenance_hold":{"reason":"upgrade","expires_at":"2026-08-10T08:36:03.056964-07:00"}}
        """)
        expect(offSubtitle(status: held, now: now) == "Paused",
               "挂起期间表头不能还写着 Off,实际 \(offSubtitle(status: held, now: now))")
        expect(offSubtitle(status: held, now: at("2026-08-10T08:36:04-07:00")) == "Off",
               "挂起过期之后回到 Off")
        let userOff = decode("""
        {"desired":"off","phase":"idle","protection_state":"off",
         "capabilities":["maintenance_hold"]}
        """)
        expect(offSubtitle(status: userOff, now: now) == "Off",
               "用户自己关的仍然是 Off —— 否则这个区分等于没做")
        expect(offSubtitle(status: nil, now: now) == "Off",
               "没有状态时不许说 Paused")
    }

    static func main() {
        runArmedHoldShowsRow()
        runWholeSecondTimestampParses()
        runExpiredHoldShowsNoRow()
        runOldGuardianShowsNoRow()
        runDeclaredButNoHoldShowsNoRow()
        runUnknownFieldsStillDecode()
        runReasonLabelKeepsIdentifier()
        runRemainingMinutesNeverCollapseToZero()
        runOffSubtitleSaysPausedOnlyDuringHold()
        if failures > 0 { exit(1) }
        print("maintenance presentation tests passed")
    }
}
