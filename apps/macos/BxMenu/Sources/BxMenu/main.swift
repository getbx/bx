import AppKit
import Darwin
import Foundation

struct DoctorReport: Decodable {
    let checks: [DoctorCheck]
}

struct DoctorCheck: Decodable {
    let name: String
    let status: String
    let detail: String?
    let hint: String?
}

struct CommandResult {
    let code: Int32
    let stdout: String
    let stderr: String
}

enum BxState {
    case connected(BxReport, version: String, dns: String?)
    case warning(String, version: String?)
    case updateNeeded(String, version: String?)
    case setupNeeded(String)
    case missing(String)
    case notInstalled(bundleVersion: String?)
    case off
}

final class BxMenuApp: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private let bxPath = "/usr/local/bin/bx"
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let guardianClient = GuardianClient()
    private var timer: Timer?
    private var updateTimer: Timer?
    /// 非 nil 表示有一个开关动作正在进行中。菜单据此显示进度而不是常规状态。
    private var toggleInFlight: (action: ToggleAction, startedAt: Date)?
    private var toggleTicker: Timer?
    /// 上一次开关失败留下的指引,下次动作开始时清掉。
    private var toggleFailureText: String?
    /// 非 nil 表示用户已经确认 Quit,但当时有另一个动作在跑,只能排队——
    /// 等那个动作的 completion 里落定后再执行(参见 `quitDisposition`)。
    private var pendingQuit: QuitDisposition?
    private var state: BxState = .off
    private var updateCheck: UpdateCheck?
    /// 这两个是恢复状态的全部载体。**写入即 bump 代际号**——用 didSet 而不是逐个
    /// 改写者,是因为漏掉任何一个写者都不会有编译错误,只会在真机上偶发一次假红。
    private var recoverySnapshot: RecoverySnapshot? {
        didSet { recoveryGeneration.bump() }
    }
    private var reconnectInFlight = false {
        didSet { recoveryGeneration.bump() }
    }
    private var recoveryGeneration = RecoveryGeneration()
    private var repairVersions: (bundle: String?, runtime: String?, core: String?)?
    /// 图标呼吸的驱动。只动 alpha,见 `applyBreathing`。
    private var breathTimer: Timer?
    private var breathPhase: Double = 0
    /// 一次刷新未回时挡掉下一次(丢弃,不排队)。规则在 `RefreshGate`,这里只照做。
    private var refreshGate = RefreshGate()

    func applicationDidFinishLaunching(_ notification: Notification) {
        enforceSingleInstance()
        ensureLoginItemIfCanonical()
        configureMenu()
        // 启动那一次不可能撞上在途刷新,补跑与否无意义;标 false 以免被读成用户动作。
        refresh(userInitiated: false)
        refreshUpdateCheck()
        rescheduleRefreshTimer(menuOpen: false)
        updateTimer = commonModeTimer(every: 24 * 60 * 60, tolerance: 60) { [weak self] in
            self?.refreshUpdateCheck()
        }
    }

    private func enforceSingleInstance() {
        guard let bundleID = Bundle.main.bundleIdentifier else { return }
        let selfPID = NSRunningApplication.current.processIdentifier
        let peers = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
            .filter { $0.processIdentifier != selfPID }
        guard let peer = peers.first else { return }
        switch resolveInstanceConflict(selfPath: Bundle.main.bundleURL.path,
                                       peerPath: peer.bundleURL?.path,
                                       canonicalPath: "/Applications/Bx.app") {
        case .keepSelf(terminatePeer: true):
            peer.terminate()
        case .keepSelf(terminatePeer: false):
            break
        case .yieldToPeer:
            peer.activate(options: [])
            NSApp.terminate(nil)
        }
    }

    private func ensureLoginItemIfCanonical() {
        let canonical = "/Applications/Bx.app"
        guard Bundle.main.bundleURL.path == canonical else { return }
        let agentDir = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/LaunchAgents")
        let agentURL = agentDir.appendingPathComponent("com.getbx.bx.menu.plist")
        let logDir = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Logs/bx").path
        let desired = menuLaunchAgentPlist(executablePath: canonical + "/Contents/MacOS/BxMenu",
                                           logDirectory: logDir)
        if (try? String(contentsOf: agentURL, encoding: .utf8)) == desired { return }
        try? FileManager.default.createDirectory(at: agentDir, withIntermediateDirectories: true)
        try? desired.write(to: agentURL, atomically: true, encoding: .utf8)
    }

    private func configureMenu() {
        statusItem.button?.target = self
        statusItem.button?.action = #selector(openMenu)
        let menu = NSMenu()
        // delegate 只在这里设一次,菜单对象此后**永不更换**(rebuildMenu 就地重填)。
        // 换对象就等于换掉 delegate:menuWillOpen/menuDidClose 不再触发,轮询
        // 永久停在关闭档;而且 AppKit 在用户点击那一刻就捕获了当时的菜单对象,
        // 换上去的新菜单要到下一次打开才看得见。
        menu.delegate = self
        statusItem.menu = menu
    }

    @objc private func openMenu() {
        refresh(userInitiated: true)
    }

    /// 建一个挂在 `.common` 模式的重复定时器。
    ///
    /// **本文件里不许再出现 `Timer.scheduledTimer`**:它只进 `.default`,而菜单展开
    /// 期间主 runloop 处于 `NSEventTrackingRunLoopMode` —— 实测一次都不触发。冻住的
    /// 恰恰是菜单开着时唯一在动的两样东西:图标呼吸(图标就在展开的菜单正上方),
    /// 以及「Connecting / Disconnecting — N 秒」那个计数器 —— 而后者是阶段①的全部
    /// 交付物,它存在的理由就是 2026-08-04 那次 `bx down` 卡了 71 分钟、菜单是死的。
    /// 一个冻住的计数器正是我们要消灭的那个症状本身。
    private func commonModeTimer(
        every interval: TimeInterval,
        tolerance: TimeInterval = 0,
        _ body: @escaping () -> Void
    ) -> Timer {
        let timer = Timer(timeInterval: interval, repeats: true) { _ in body() }
        timer.tolerance = tolerance
        RunLoop.main.add(timer, forMode: .common)
        return timer
    }

    /// 刷新按菜单开合调频:有人在看就勤一点,没人看就别每 5 秒 spawn 四个进程。
    ///
    /// **必须挂进 `.common` 模式**:菜单展开期间主 runloop 处于
    /// `NSEventTrackingRunLoopMode`,`Timer.scheduledTimer` 只进 `.default`,
    /// 于是打开档那 2 秒一次在菜单开着的时候一次都不会触发(实测 0 次 vs
    /// `.default` 下同样一秒 10 次)——正是它唯一该干活的时候。
    private func rescheduleRefreshTimer(menuOpen: Bool) {
        timer?.invalidate()
        timer = commonModeTimer(every: menuPollInterval(menuOpen: menuOpen)) { [weak self] in
            self?.refresh(userInitiated: false)
        }
    }

    /// 菜单显示前重填。**只用缓存状态,不 spawn 任何子进程** —— 这条路径在
    /// 用户点击与菜单出现之间,跑一次 status --json(封顶 5 秒)就是肉眼可见的卡顿。
    func menuNeedsUpdate(_ menu: NSMenu) {
        rebuildMenu()
    }

    func menuWillOpen(_ menu: NSMenu) {
        rescheduleRefreshTimer(menuOpen: true)
        // 异步:数据回来后就地更新已经展开的这个菜单。
        //
        // 标 userInitiated 是**有意的判断**,尽管打开菜单不是一次变更的结果:若这次
        // 撞上在途刷新而不补跑,用户看到的是那次刷新**在他打开之前**采样的数据
        // (落定时已陈旧最多 5 秒),而菜单开着时的 2 秒拍每次撞上在途刷新也照样被丢
        // 且不补 —— 两条本该兜底的路径都不保证「打开之后采过一次」。补跑正好保证
        // 这一次,且每次打开最多补一次,不会像定时器那样接成满占空比。
        refresh(userInitiated: true)
    }

    func menuDidClose(_ menu: NSMenu) {
        rescheduleRefreshTimer(menuOpen: false)
    }

    /// 发起一次刷新。**子进程全在后台线程跑**,主线程一秒都不阻塞。
    ///
    /// 一次刷新要 spawn 四个 bx 子进程,其中 `status --json` 在 macOS 上跑完整观测、
    /// 整轮封顶 5 秒。放主线程就是菜单冻住;而它又必须能在菜单开着的时候跑完并
    /// 就地更新,所以结果统一回主线程由 `applyRefresh` 一次落定。
    /// `userInitiated` = 这一次是用户动作(点开菜单、setup、开关、更新)的直接后果。
    /// 只有这一类在被丢弃后会补跑;定时器那一拍丢了就丢了(见 RefreshGate.begin)。
    ///
    /// **刻意不给默认值。** 漏传的代价是静默的:用户刚开完/关完保护,那次刷新若正好
    /// 撞上在途的一次就被丢掉且不补,菜单要到下一个自然拍才纠正(关闭档最长 30 秒)——
    /// 没有任何报错。给了默认值,新加的调用点会默默落进「不补跑」那一档;不给,
    /// 编译器强制每个调用点当场表态。裸 `refresh()` 另有 Go 守卫兜(CI 不编 Swift)。
    private func refresh(userInitiated: Bool) {
        // 上一次还没回来就丢掉这一次:排队只会堆出一串拿到时已作废的刷新。
        guard refreshGate.begin(userInitiated: userInitiated) else { return }
        let inFlight = reconnectInFlight
        let snapshot = recoverySnapshot
        let generation = recoveryGeneration.value
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let outcome = self.loadState(reconnectInFlight: inFlight, snapshot: snapshot)
            DispatchQueue.main.async { [weak self] in
                self?.applyRefresh(outcome, capturedGeneration: generation)
            }
        }
    }

    /// 把后台收集到的结果落定。只在主线程调用。
    ///
    /// 快照那半边**只在代际号没变时**才写回:采集期间若有人动过恢复状态,主线程上
    /// 那个写者知道的比这次采集新,盖回去就是复活一个已经结束的恢复(见 RecoveryGeneration)。
    /// state/repairVersions 不受影响 —— 它们只由报告本身决定。
    private func applyRefresh(_ outcome: RefreshOutcome, capturedGeneration: Int) {
        state = outcome.state
        if recoveryGeneration.acceptsWriteBack(captured: capturedGeneration) {
            recoverySnapshot = outcome.recoverySnapshot
        }
        repairVersions = outcome.repairVersions
        updateIcon()
        rebuildMenu()
        if let snapshot = recoverySnapshot,
           recoveryPresentation(for: snapshot).isRunning,
           !reconnectInFlight {
            observeRecovery(startingWith: snapshot)
        }
        // 被丢掉的那次里可能就有用户刚做完动作后发起的刷新;补跑一次,别让菜单
        // 把用户自己那一下的结果报错到下一拍。补跑是一次性的,不是队列。
        if refreshGate.end() {
            refresh(userInitiated: true)
        }
    }

    /// 一次刷新的产物。`loadState` 跑在后台线程,故它**不再直接改 self**:
    /// 输入由参数带进来,输出一次性带回主线程落定,否则就是数据竞争
    /// (`RecoverySnapshot` 是个十来个 String 的结构体,撕裂读不是理论问题)。
    private struct RefreshOutcome {
        let state: BxState
        let recoverySnapshot: RecoverySnapshot?
        let repairVersions: (bundle: String?, runtime: String?, core: String?)?
    }

    /// 收集一次状态。**跑在后台线程**(它 spawn 四个 bx 子进程),因此不碰 self 的
    /// 可变状态:恢复快照与 repairVersions 作为局部量进出,由调用方在主线程落定。
    ///
    /// 判定体原样保在嵌套的 `resolve()` 里:那些 `recoverySnapshot = …` 紧跟
    /// `return .warning(…)` 的写法是 TestMacMenuWarningsDropGreenRecoverySnapshot
    /// 逐行审的对象,改写它等于把那条守卫连同它守的东西一起弄没。
    private func loadState(reconnectInFlight: Bool, snapshot: RecoverySnapshot?) -> RefreshOutcome {
        var recoverySnapshot = snapshot
        var repairVersions: (bundle: String?, runtime: String?, core: String?)?
        func resolve() -> BxState {
            let runtimeInstalled = unifiedRuntimeVersion() != nil
            let cliUsable = FileManager.default.isExecutableFile(atPath: bxPath) && runBx(["--version"]).code == 0
            if installActionTitle(runtimeInstalled: runtimeInstalled, cliUsable: cliUsable) != nil {
                return .notInstalled(bundleVersion: bundleReleaseVersion())
            }
            guard FileManager.default.isExecutableFile(atPath: bxPath) else {
                return .missing("Install bx at /usr/local/bin/bx")
            }
            let version = loadVersion()
            if !cliSupportsDiagnosticsArchive() {
                return .updateNeeded("Update bx CLI", version: version)
            }
            let status = runBx(["status", "--json"])
            guard status.code == 0 else {
                return diagnoseStopped(version: version, fallback: status.stderr)
            }
            let data = Data(status.stdout.utf8)
            guard let report = try? JSONDecoder().decode(BxReport.self, from: data) else {
                // 必须先清恢复快照:updateIcon 让快照覆盖状态图标,留着一个绿快照
                // 会画出「状态是 warning、盾牌却是绿的」。
                recoverySnapshot = nil
                return .warning("Status unreadable", version: version)
            }
            if let banner = updatingBanner(phase: report.phase) {
                recoverySnapshot = nil
                return .warning(banner, version: version)
            }
            let bundleVersion = bundleReleaseVersion()
            let runtimeVersion = unifiedRuntimeVersion()
            if repairActionNeeded(
                bundleVersion: bundleVersion,
                runtimeVersion: runtimeVersion,
                coreVersion: report.coreVersion,
                phase: report.phase
            ) {
                recoverySnapshot = nil
                repairVersions = (bundle: bundleVersion, runtime: runtimeVersion, core: report.coreVersion)
                return .warning("Repair Required", version: version)
            }
            repairVersions = nil
            let verdict = menuProtectionVerdict(report)
            switch verdict {
            case .off:
                // 用户主动关掉了保护。必须先于隧道判定返回——Core 已退出,隧道当然
                // 不健康,若先看 tunnelHealthy 就会把「自己关的」报成「隧道坏了」,
                // 而 .warning 分支不提供 Start Protection,用户就没法从菜单开回来
                // (真机 2026-08-06:只能回去敲 sudo bx up)。
                recoverySnapshot = nil
                return .off
            case .attention(let reason):
                // Guardian 明确报告的异常先于被动恢复快照判定,与既有行为一致:
                // 这两种状态下不保留恢复快照。
                if report.protectionState == "needs_attention" || report.protectionState == "blocked" {
                    recoverySnapshot = nil
                    return .warning(reason, version: version)
                }
            case .healthy:
                break
            }
            if !reconnectInFlight {
                recoverySnapshot = passiveStatusRecovery(
                    protectionState: report.protectionState,
                    recovery: report.recovery
                )
            }
            if case .attention(let reason) = verdict {
                recoverySnapshot = recoverySnapshotSurvivingWarning(recoverySnapshot)
                return .warning(reason, version: version)
            }
            let dns = dnsPresentation(
                state: report.dnsState,
                managed: report.dnsManaged ?? false,
                service: report.dnsService
            )
            guard dns.allowsProtected else {
                recoverySnapshot = recoverySnapshotSurvivingWarning(recoverySnapshot)
                return .warning(dns.menuWarning ?? "DNS status unavailable", version: version)
            }
            return .connected(report, version: version ?? "unknown", dns: dns.label)
        }
        return RefreshOutcome(
            state: resolve(),
            recoverySnapshot: recoverySnapshot,
            repairVersions: repairVersions
        )
    }

    private func updateIcon() {
        guard let button = statusItem.button else { return }
        let iconState = menuIconStateNow()
        // 用户在系统里要求过「减弱动态效果」就别动:菜单栏常驻视野边缘,是最不该
        // 无视这个设置的地方。四态此时全靠形态区分(MenuIconTests 钉死)。
        let style = menuIconStyle(
            state: iconState,
            reduceMotion: NSWorkspace.shared.accessibilityDisplayShouldReduceMotion
        )
        let systemTint = menuIconUsesSystemTint(state: iconState)
        // template 只吃 alpha 通道,颜色随便;给不透明黑是为了让蒙版是实的。
        button.image = compactStatusImage(for: style,
                                          tint: systemTint ? .black : iconTint(iconState),
                                          template: systemTint)
        button.imagePosition = .imageOnly
        button.title = ""
        button.toolTip = tooltipText()
        applyBreathing(style.motion)
    }

    /// 把菜单的八个状态收敛到图标的四态。
    ///
    /// 恢复浮层沿用它自己的判定(它比 `state` 知道得更多):正在跑 = 过渡态,
    /// 失败 = 需要注意。**不能一律当过渡态** —— 那会把一次失败的恢复画成
    /// 「正在忙」,而正在忙的图标是实心盾,与「保护中」只差快慢。
    private func menuIconStateNow() -> MenuIconState {
        if toggleInFlight != nil { return .transitioning }
        if let snapshot = recoverySnapshot {
            switch recoveryPresentation(for: snapshot).indicator {
            case .yellow:
                return .transitioning
            case .red:
                return .attention
            case .green, .gray:
                break   // 快照不再有话说,落回按 state 判定
            }
        }
        switch state {
        case .connected:
            return menuRowsNow().anomalyCount > 0 ? .attention : .protected
        case .warning, .updateNeeded:
            return .attention
        case .off, .setupNeeded, .missing, .notInstalled:
            return .off
        }
    }

    /// 颜色只作可选加强:去掉它四态仍靠形态可分(见 MenuIconTests)。
    ///
    /// 无色的两态不走这里(它们交给系统 template 上色,见 menuIconUsesSystemTint)——
    /// 自绘的 secondaryLabelColor 在深色菜单栏上会消失。
    private func iconTint(_ state: MenuIconState) -> NSColor {
        switch state {
        case .protected: return .systemGreen
        case .attention: return .systemYellow
        case .transitioning, .off: return .black
        }
    }

    /// 盾形轮廓。数据是 16×16、y 向下,这里翻 y 并整体 +1 居中进 18×18 的图标框。
    private func shieldPoint(_ point: (x: Double, y: Double)) -> NSPoint {
        NSPoint(x: point.x + 1, y: 17 - point.y)
    }

    private func shieldPath() -> NSBezierPath {
        let path = NSBezierPath()
        for (index, point) in shieldOutlinePoints.enumerated() {
            let p = shieldPoint(point)
            if index == 0 { path.move(to: p) } else { path.line(to: p) }
        }
        path.close()
        return path
    }

    /// 裂开的盾:同一条锯齿裂缝把盾切成两半,两半再各自错开一点——
    /// 读起来是「已经滑开了」,不是「画了一条线」。
    ///
    /// 每一半 = 沿裂缝从顶点走到底尖,再沿自己那侧的外缘走回顶点。顺序不能乱:
    /// 绕错了会自交成蝴蝶结,非零环绕规则下填出来是两个三角形,不是半个盾。
    private func crackedShieldPaths() -> (left: NSBezierPath, right: NSBezierPath) {
        func half(sideBackToApex: [(x: Double, y: Double)]) -> NSBezierPath {
            let path = NSBezierPath()
            for (index, point) in (shieldCrackPoints + sideBackToApex).enumerated() {
                let p = shieldPoint(point)
                if index == 0 { path.move(to: p) } else { path.line(to: p) }
            }
            path.close()
            return path
        }
        // 轮廓点顺序:顶点 → 右上 → 右下弧 → 底尖 → 左下弧 → 左上
        let leftSide = Array(shieldOutlinePoints[5...])                     // 底尖 → 左上
        let rightSide = Array(shieldOutlinePoints[1...3].reversed())        // 底尖 → 右上
        return (half(sideBackToApex: leftSide), half(sideBackToApex: rightSide))
    }

    private func compactStatusImage(for style: MenuIconStyle, tint: NSColor, template: Bool) -> NSImage {
        let image = NSImage(size: NSSize(width: 18, height: 18))
        image.lockFocus()
        defer { image.unlockFocus() }
        tint.setFill()
        tint.setStroke()
        switch style.form {
        case .filled:
            shieldPath().fill()
        case .hollow:
            let path = shieldPath()
            path.lineWidth = 1.35
            path.stroke()
        case .dashed:
            // 虚线 = 轮廓还没合上。与实心/空心/裂开三者在灰度、静态下都可分。
            let path = shieldPath()
            path.lineWidth = 1.6
            path.setLineDash([2.4, 1.9], count: 2, phase: 0)
            path.stroke()
        case .cracked:
            let halves = crackedShieldPaths()
            halves.left.transform(using: AffineTransform(translationByX: -0.65, byY: 0.15))
            halves.right.transform(using: AffineTransform(translationByX: 0.65, byY: -0.3))
            halves.left.fill()
            halves.right.fill()
        }
        image.isTemplate = template
        return image
    }

    /// 呼吸靠周期性调 button.alphaValue 实现。
    /// 不用 CABasicAnimation:状态项按钮的图层由 AppKit 托管,直接动 alpha
    /// 简单且在图标被替换时不会残留动画。
    private func applyBreathing(_ motion: MenuIconMotion) {
        breathTimer?.invalidate()
        breathTimer = nil
        guard let button = statusItem.button else { return }
        let period: Double
        let floorAlpha: Double
        switch motion {
        case .still:
            button.alphaValue = 1
            return
        case .breathe(let p):
            period = p
            floorAlpha = menuIconIdleFloorAlpha
        case .pulse(let p):
            period = p
            floorAlpha = menuIconBusyFloorAlpha
        }
        // 10Hz 足够:4 秒周期下每帧 alpha 最多变 0.043,看不出台阶。tolerance 让
        // 系统把这些唤醒合并到别的定时器上 —— 常驻进程,省电是白拿的。
        let step = 0.1
        breathTimer = commonModeTimer(every: step, tolerance: 0.02) { [weak self] in
            guard let self, let button = self.statusItem.button else { return }
            self.breathPhase += step
            let t = (self.breathPhase.truncatingRemainder(dividingBy: period)) / period
            // 余弦让两端停留久一点,读起来像呼吸而不是闪
            let eased = (1 - cos(t * 2 * Double.pi)) / 2
            button.alphaValue = floorAlpha + (1 - floorAlpha) * (1 - eased)
        }
    }

    private func tooltipText() -> String {
        if let snapshot = recoverySnapshot {
            let presentation = recoveryPresentation(for: snapshot)
            if let reason = presentation.shortReason {
                return "bx: \(presentation.title), \(reason)"
            }
            return "bx: \(presentation.title)"
        }
        switch state {
        case .connected(let report, _, _):
            return "bx: Protected, \(report.latencyMS) ms"
        case .warning(let message, _):
            return "bx: \(message)"
        case .updateNeeded:
            return "bx: Update Required"
        case .setupNeeded:
            return "bx: Setup Required"
        case .missing:
            return "bx: Not Installed"
        case .notInstalled:
            return "bx: Not Installed"
        case .off:
            return "bx: Off"
        }
    }

    /// 就地重填菜单项。**不建新 NSMenu**(见 configureMenu),这样重填会落在用户
    /// 正看着的那个菜单上,而不是下一次打开才生效的另一个对象上。只读缓存状态,
    /// 不 spawn 子进程,因此放在菜单显示前的路径上也是安全的。
    private func rebuildMenu() {
        guard let menu = statusItem.menu else { return }
        menu.removeAllItems()
        if let inFlight = toggleInFlight {
            let elapsed = Int(Date().timeIntervalSince(inFlight.startedAt))
            menu.addHeader("bx", subtitle: inFlight.action == .turnOn ? "Connecting" : "Disconnecting")
            menu.addInfo("Status", toggleProgressText(action: inFlight.action, elapsedSeconds: elapsed))
            if pendingQuit != nil {
                // Quit 已经排队:这是最重要的一句话,用户点了确认框,必须能
                // 看到"收到了",而不是一个看起来什么都没变的菜单。
                menu.addPlainText(quitQueuedStatusText())
            } else if let hint = toggleSlowHint(elapsedSeconds: elapsed) {
                menu.addPlainText(hint)
                menu.addAction("查看 Guardian 日志", symbol: "doc.text", target: self, action: #selector(openLogs))
            }
            menu.addItem(.separator())
            menu.addAction(quitBxActionTitle, symbol: "power", target: self, action: #selector(quitBx))
            return
        }
        if let snapshot = recoverySnapshot {
            let presentation = recoveryPresentation(for: snapshot)
            menu.addHeader("bx", subtitle: presentation.title)
            menu.addInfo("Status", presentation.shortReason ?? presentation.title)
            if !snapshot.recoveryID.isEmpty {
                menu.addInfo("Recovery", snapshot.recoveryID)
            }
            menu.addItem(.separator())
            if presentation.isRunning {
                menu.addAction(
                    "Troubleshoot: Reconnect",
                    symbol: "arrow.clockwise",
                    target: self,
                    action: #selector(reconnectBx),
                    enabled: false
                )
            } else if snapshot.state == "failed" {
                menu.addAction("Details", symbol: "info.circle", target: self, action: #selector(showRecoveryDetails))
                menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
            } else {
                menu.addAction("Troubleshoot: Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
            }
            return
        }
        switch state {
        case .connected(_, let version, _):
            menu.addHeader("bx", subtitle: "Connected")
            menu.addInfo("Status", "Protected")
            menu.addInfo("Network changes", "Automatically recovers safely after network changes")
            for row in menuRowsNow().rows {
                let suffix: String
                switch row.mark {
                case .ok: suffix = ""
                case .bad: suffix = "  ✗"
                case .unknown: suffix = ""
                }
                menu.addInfo(row.label, row.value + suffix)
            }
            menu.addInfo("Version", version)
        case .warning(let message, let version):
            menu.addHeader("bx", subtitle: "Needs Attention")
            menu.addInfo("Status", message)
            if message == "Repair Required", let versions = repairVersions {
                if let bundle = versions.bundle {
                    menu.addInfo("App", bundle)
                }
                if let runtime = versions.runtime {
                    menu.addInfo("Runtime", runtime)
                }
                if let core = versions.core {
                    menu.addInfo("Core", core)
                }
            } else if let version {
                menu.addInfo("Version", version)
            }
        case .updateNeeded(let message, let version):
            menu.addHeader("bx", subtitle: "Update Required")
            menu.addInfo("Status", message)
            if let version {
                menu.addInfo("Version", version)
            }
        case .setupNeeded(let message):
            menu.addHeader("bx", subtitle: "Setup Required")
            menu.addInfo("Status", message)
        case .missing(let message):
            menu.addHeader("bx", subtitle: "Not Installed")
            menu.addInfo("Status", message)
        case .notInstalled(let bundleVersion):
            menu.addHeader("bx", subtitle: "Not Installed")
            if let bundleVersion {
                menu.addInfo("Version", bundleVersion)
            }
        case .off:
            menu.addHeader("bx", subtitle: "Off")
            menu.addInfo("Status", "Not running")
        }
        if let failure = toggleFailureText {
            menu.addItem(.separator())
            menu.addInfo("上次操作失败", failure)
        }
        menu.addItem(.separator())
        if let title = menuUpdateActionTitle(check: updateCheck) {
            menu.addAction(title, symbol: "arrow.down.circle", target: self, action: #selector(updateBx))
            menu.addItem(.separator())
        }
        // 建设性主动作排在诊断入口之前:处在 off / 未配置 / 未安装 时,用户唯一
        // 想点的就是它,把它压在 View Logs 与 Run Doctor 下面是本末倒置。
        // 破坏性动作(Turn Off / Quit)反过来仍留在菜单底部——那是 macOS 惯例,
        // 也避免误点,所以 .connected/.warning 的顺序不动。
        switch state {
        case .off:
            menu.addAction("Start Protection", symbol: "play.fill", target: self, action: #selector(startBx))
        case .setupNeeded:
            menu.addAction("Set Up bx...", symbol: "link", target: self, action: #selector(setUpBx))
        case .notInstalled:
            menu.addAction("Install bx…", symbol: "arrow.down.circle", target: self, action: #selector(installBx))
        case .missing, .updateNeeded:
            menu.addAction("Open Install Guide", symbol: "book", target: self, action: #selector(openInstallGuide))
        case .connected, .warning:
            break
        }
        switch state {
        case .setupNeeded:
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        case .missing, .updateNeeded, .notInstalled:
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
        case .off:
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        case .connected, .warning:
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        }
        switch state {
        case .connected, .warning:
            // 只有这两个状态在下面还会加动作,分隔符才有东西可分隔。
            menu.addItem(.separator())
        default:
            break
        }
        switch state {
        case .connected:
            menu.addAction("Troubleshoot: Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
            menu.addAction(turnOffActionTitle, symbol: "pause.circle", target: self, action: #selector(turnOffBx))
        case .warning("Repair Required", _):
            menu.addAction(repairActionTitle, symbol: "wrench.and.screwdriver", target: self, action: #selector(repairBx))
            menu.addAction("Troubleshoot: Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
            menu.addAction(turnOffActionTitle, symbol: "pause.circle", target: self, action: #selector(turnOffBx))
        case .warning:
            menu.addAction("Troubleshoot: Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
            menu.addAction(turnOffActionTitle, symbol: "pause.circle", target: self, action: #selector(turnOffBx))
        case .off, .updateNeeded, .setupNeeded, .missing, .notInstalled:
            // 这些状态的主动作已经排在诊断入口之前了,这里不再重复。
            break
        }
        // 退出入口无条件加一次。**不要挪回上面任何一个 case**:此前它只在
        // .connected/.warning 里,于是 .off/.setupNeeded/.missing/.notInstalled/
        // .updateNeeded 下菜单没有任何退出入口(TestMacMenuQuitActionPresentInEveryState)。
        menu.addItem(.separator())
        menu.addAction(quitBxActionTitle, symbol: "power", target: self, action: #selector(quitBx))
    }

    /// 把带 payload 的 `BxState` 收成可测的 `MenuStateKind`。**只是映射,没有判定**
    /// —— 判定住在 ToggleController.swift(quitPlan),那里能被单测覆盖。
    /// 新增 BxState case 时这个 switch 会编译失败,漏映射跑不掉。
    private func menuStateKind() -> MenuStateKind {
        switch state {
        case .connected: return .connected
        case .warning: return .warning
        case .updateNeeded: return .updateNeeded
        case .setupNeeded: return .setupNeeded
        case .missing: return .missing
        case .notInstalled: return .notInstalled
        case .off: return .off
        }
    }

    private func menuRowsNow() -> MenuRowSet {
        switch state {
        case .connected(let report, _, let dns):
            return menuRows(report: report, dns: dns)
        default:
            return menuRows(report: nil, dns: nil)
        }
    }

    @objc private func openLogs() {
        let url = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library")
            .appendingPathComponent("Logs")
            .appendingPathComponent("bx")
        do {
            try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
            NSWorkspace.shared.open(url)
        } catch {
            showMessage("Logs Unavailable", error.localizedDescription)
        }
    }

    @objc private func runDoctor() {
        openTerminal("diag=\"$HOME/Library/Logs/bx/diagnostics\"; mkdir -p \"$diag\"; sudo env BX_LOG_ARCHIVE_DIR=\"$diag\" '\(bxPath)' doctor; latest=$(find \"$diag\" -maxdepth 1 -type d -name 'bx-logs-*' | sort | tail -1); if [ -n \"$latest\" ]; then group=$(id -gn); sudo chown -R \"$USER:$group\" \"$latest\" 2>/dev/null || true; open \"$latest\"; fi; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func startBx() {
        guard confirmStartProtection() else { return }
        performToggle(.turnOn)
    }

    @objc private func setUpBx() {
        guard let link = promptForClientLink() else { return }
        let command = "'\(bxPath)' setup \(shellSingleQuoted(link))"
        guard runPrivileged(command) else {
            showFailure("Setup Failed", "bx was not configured.")
            refresh(userInitiated: true)
            return
        }
        if confirmStartProtection(title: "bx is set up", cancelTitle: "Later") {
            if !runPrivileged("'\(bxPath)' up") {
                showFailure("Start Failed", "bx is configured, but did not start.")
            }
        }
        refresh(userInitiated: true)
    }

    @objc private func installBx() {
        runEmbeddedInstaller(
            confirmTitle: "Install bx?",
            confirmMessage: "bx will install its command line tool and background protection service. macOS will ask for administrator authorization. Protection is not started until you set up and turn it on.",
            confirmButton: "Install"
        )
    }

    @objc private func repairBx() {
        runEmbeddedInstaller(
            confirmTitle: "Repair bx?",
            confirmMessage: "bx will reinstall its components from this app. Your connection settings are kept.",
            confirmButton: "Repair"
        )
    }

    private func runEmbeddedInstaller(confirmTitle: String, confirmMessage: String, confirmButton: String) {
        let alert = NSAlert()
        alert.messageText = confirmTitle
        alert.informativeText = confirmMessage
        alert.addButton(withTitle: confirmButton)
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        let bundlePath = Bundle.main.bundleURL.path
        let installer = bundlePath + "/Contents/Resources/bx-cli"
        guard FileManager.default.isExecutableFile(atPath: installer) else {
            showFailure("Install Failed", "This copy of Bx.app has no embedded installer. Download the full bx-macos package.")
            return
        }
        let command = "\(shellSingleQuoted(installer)) app-install --app-source \(shellSingleQuoted(bundlePath))"
        if runPrivileged(command) {
            if bundlePath != "/Applications/Bx.app" {
                NSWorkspace.shared.open(URL(fileURLWithPath: "/Applications/Bx.app"))
                NSApp.terminate(nil)
            } else {
                refresh(userInitiated: true)
            }
        } else {
            showFailure("Install Failed", "bx could not complete the installation.")
        }
    }

    @objc private func openInstallGuide() {
        let alert = NSAlert()
        alert.messageText = "Install bx"
        alert.informativeText = "Install the macOS bx package again, or update the CLI at /usr/local/bin/bx, then restart the menu bar app."
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    @objc private func reconnectBx() {
        guard !reconnectInFlight else { return }
        reconnectInFlight = true
        let pending = localRecoverySnapshot(state: "accepted", stage: "queued")
        recoverySnapshot = pending
        updateIcon()
        rebuildMenu()

        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            do {
                let submitted = try self.guardianClient.requestRecovery()
                DispatchQueue.main.async { [weak self] in
                    self?.publishRecovery(submitted)
                }
                self.pollRecovery(startingWith: submitted, allowsTerminalSuccess: true)
            } catch {
                let transition = recoveryFailureTransition(
                    from: pending,
                    errorCode: self.recoveryErrorCode(error)
                )
                DispatchQueue.main.async { [weak self] in
                    guard let self else { return }
                    self.reconnectInFlight = transition.reconnectInFlight
                    self.publishRecovery(transition.snapshot)
                }
            }
        }
    }

    private func observeRecovery(startingWith snapshot: RecoverySnapshot) {
        reconnectInFlight = true
        DispatchQueue.global(qos: .utility).async { [weak self] in
            self?.pollRecovery(startingWith: snapshot, allowsTerminalSuccess: false)
        }
    }

    private func pollRecovery(startingWith submitted: RecoverySnapshot, allowsTerminalSuccess: Bool) {
        var snapshot = submitted
        var delay = 0.25
        while recoveryPresentation(for: snapshot).isRunning {
            Thread.sleep(forTimeInterval: delay)
            delay = 0.5
            do {
                let current = try guardianClient.currentRecovery()
                if let transition = recoveryObservationFailure(submitted: submitted, observed: current) {
                    DispatchQueue.main.async { [weak self] in
                        guard let self else { return }
                        self.reconnectInFlight = transition.reconnectInFlight
                        self.publishRecovery(
                            transition.snapshot,
                            allowsTerminalSuccess: allowsTerminalSuccess
                        )
                    }
                    return
                }
                snapshot = current
                DispatchQueue.main.async { [weak self] in
                    self?.publishRecovery(current, allowsTerminalSuccess: allowsTerminalSuccess)
                }
            } catch {
                let transition = recoveryFailureTransition(
                    from: snapshot,
                    errorCode: recoveryErrorCode(error)
                )
                DispatchQueue.main.async { [weak self] in
                    guard let self else { return }
                    self.reconnectInFlight = transition.reconnectInFlight
                    self.publishRecovery(
                        transition.snapshot,
                        allowsTerminalSuccess: allowsTerminalSuccess
                    )
                }
                return
            }
        }
        DispatchQueue.main.async { [weak self] in
            guard let self else { return }
            self.reconnectInFlight = false
            self.publishRecovery(snapshot, allowsTerminalSuccess: allowsTerminalSuccess)
            if snapshot.state == "succeeded" && allowsTerminalSuccess {
                DispatchQueue.main.asyncAfter(deadline: .now() + 2) { [weak self] in
                    guard let self, self.recoverySnapshot?.recoveryID == snapshot.recoveryID else { return }
                    self.recoverySnapshot = nil
                    self.refresh(userInitiated: true)
                }
            }
        }
    }

    private func publishRecovery(_ snapshot: RecoverySnapshot, allowsTerminalSuccess: Bool = true) {
        if allowsTerminalSuccess {
            recoverySnapshot = recoverySnapshotForDisplay(snapshot, allowsTerminalSuccess: true)
        } else {
            recoverySnapshot = passiveStatusRecovery(
                protectionState: passiveProtectionState,
                recovery: snapshot
            )
        }
        updateIcon()
        rebuildMenu()
    }

    private var passiveProtectionState: String? {
        switch state {
        case .warning("Blocked", _):
            return "blocked"
        case .warning("Repair Required", _):
            return "needs_attention"
        default:
            return nil
        }
    }

    @objc private func showRecoveryDetails() {
        guard let snapshot = recoverySnapshot else { return }
        let presentation = recoveryPresentation(for: snapshot)
        let alert = NSAlert()
        alert.messageText = presentation.title
        alert.informativeText = [
            presentation.shortReason,
            snapshot.recoveryID.isEmpty ? nil : "Recovery: \(snapshot.recoveryID)",
            "Stage: \(snapshot.stage)",
        ].compactMap { $0 }.joined(separator: "\n")
        alert.addButton(withTitle: "Run Doctor")
        alert.addButton(withTitle: "OK")
        if alert.runModal() == .alertFirstButtonReturn {
            runDoctor()
        }
    }

    private func localRecoverySnapshot(
        state: String,
        stage: String,
        errorCode: String? = nil
    ) -> RecoverySnapshot {
        let timestamp = ISO8601DateFormatter().string(from: Date())
        return RecoverySnapshot(
            recoveryID: "",
            state: state,
            stage: stage,
            reason: "manual",
            generation: nil,
            lastErrorCode: errorCode,
            detail: nil,
            attempt: 1,
            startedAt: timestamp,
            updatedAt: timestamp
        )
    }

    private func recoveryErrorCode(_ error: Error) -> String {
        if case GuardianClientError.socket = error {
            return "recovery_unavailable"
        }
        return "recovery_failed"
    }

    @objc private func updateBx() {
        guard menuUpdateActionTitle(check: updateCheck) != nil else { return }
        let alert = NSAlert()
        alert.messageText = updateConfirmTitle
        alert.informativeText = updateConfirmMessage
        alert.addButton(withTitle: "Update")
        alert.addButton(withTitle: "Not Now")
        guard alert.runModal() == .alertFirstButtonReturn else { return }

        let logDir = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library/Logs/bx")
        let logPath = logDir.appendingPathComponent("menu-update.log").path
        do {
            try FileManager.default.createDirectory(at: logDir, withIntermediateDirectories: true)
            FileManager.default.createFile(atPath: logPath, contents: nil)
        } catch {
            showFailure("Update Failed", "bx could not prepare its update log.")
            return
        }
        let command = "'\(bxPath)' update --json > \(shellSingleQuoted(logPath)) 2>&1"
        _ = runPrivileged(command)
        // bx exits non-zero on rollback but still prints valid JSON; always inspect the log.
        guard let logData = FileManager.default.contents(atPath: logPath) else {
            showFailure("Update Failed", "bx could not complete the update. Run Doctor for details.")
            return
        }
        switch parseUpdateOutcome(logData) {
        case .succeeded:
            showMessage("Update Complete", updateSucceededMessage)
            refresh(userInitiated: true)
            refreshUpdateCheck()
        case .rolledBack:
            showMessage("Update Rolled Back", updateRolledBackMessage)
        case .failed:
            showFailure("Update Failed", "bx could not complete the update. Run Doctor for details.")
        }
    }

    @objc private func quitBx() {
        let alert = NSAlert()
        alert.messageText = "Quit bx?"
        alert.informativeText = quitBxConfirmMessage
        alert.addButton(withTitle: "Quit bx")
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        // 没有东西可关就直接退出:在 .notInstalled/.missing/.setupNeeded 下走
        // turnOff 是一次注定失败的 socket 调用,而失败之后按阶段①的裁决又不退出
        // —— 用户被困在一个关不掉的菜单里,却根本没有保护需要被守着。判定在
        // quitPlan(纯函数,有单测),这里只照做。
        if quitPlan(state: menuStateKind(), inFlight: toggleInFlight?.action) == .terminateImmediately {
            NSApp.terminate(nil)
            return
        }
        let disposition = quitDisposition(inFlight: toggleInFlight?.action)
        switch disposition {
        case .turnOffNow:
            performToggle(.turnOff) { [weak self] turnedOff in
                self?.finishQuit(turnedOff: turnedOff)
            }
        case .waitThenQuit, .waitThenTurnOffThenQuit:
            // 已经有一个动作在跑,performToggle 的 re-entrancy guard 会让第二次
            // 调用静默返回——排队,让那个动作的 completion 负责收尾退出。
            pendingQuit = disposition
            rebuildMenu()
        }
    }

    @objc private func turnOffBx() {
        let alert = NSAlert()
        alert.messageText = "Turn off bx?"
        alert.informativeText = "bx will stop protecting system traffic and restore managed DNS settings. The menu stays open."
        alert.addButton(withTitle: "Turn Off")
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        performToggle(.turnOff)
    }

    private func runPrivileged(_ command: String) -> Bool {
        let script = "do shell script \(shellQuoted(command)) with administrator privileges"
        return runAppleScript(script)
    }

    /// 在非主线程执行一段特权 AppleScript。
    ///
    /// 不用 `runPrivileged`/NSAppleScript:那是同步的、且 NSAppleScript 按 Apple
    /// 的说法不是线程安全的,而这条路径只在后台队列上跑(它会阻塞到用户输完密码
    /// 为止,放在主线程就是 2026-08-04 那种「菜单冻住」的复刻)。改 spawn
    /// `/usr/bin/osascript` 子进程——独立进程,天然线程安全,授权框由系统
    /// SecurityAgent 弹。
    private func runPrivilegedScriptOffMainThread(_ script: String) -> Bool {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/osascript")
        process.arguments = ["-e", script]
        process.standardOutput = FileHandle.nullDevice
        process.standardError = FileHandle.nullDevice
        do {
            try process.run()
            process.waitUntilExit()
        } catch {
            return false
        }
        return process.terminationStatus == 0
    }

    /// Quit 的收尾。关不掉就**不退出** —— 见 quitTerminatesAfterTurnOff。
    private func finishQuit(turnedOff: Bool) {
        guard quitTerminatesAfterTurnOff(turnedOff: turnedOff) else {
            pendingQuit = nil
            toggleFailureText = quitBlockedByFailedTurnOffMessage()
            refresh(userInitiated: true)
            showFailure("bx Is Still Running", quitBlockedByFailedTurnOffMessage())
            return
        }
        NSApp.terminate(nil)
    }

    /// 开/关保护。经 Guardian socket 发起,全程不阻塞主线程。
    ///
    /// 不再走 AppleScript `with administrator privileges`:那条路既要每次输密码,
    /// 又是同步的 —— 2026-08-04 事故里 bx down 卡了 71 分钟,菜单跟着冻了 71 分钟。
    private func performToggle(_ action: ToggleAction, completion: ((Bool) -> Void)? = nil) {
        guard toggleInFlight == nil else { return }
        toggleFailureText = nil
        toggleInFlight = (action, Date())
        startToggleTicker()
        rebuildMenu()
        updateIcon()

        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            var succeeded = false
            var failureCode: String?
            var transportError: String?
            do {
                let status = action == .turnOn ? try self.guardianClient.turnOn() : try self.guardianClient.turnOff()
                failureCode = status.lastError
                succeeded = true
            } catch {
                // Guardian 把失败码写在 500 响应体里,GuardianClient 已经在抛出
                // 之前把它读出来了 —— 这是 toggleFailureHint 唯一的活水源:200
                // 那条路上 Manager 早把 LastError 清了。
                failureCode = guardianFailureCode(of: error)
                transportError = error.localizedDescription
            }
            // 逃生路径:socket 关不掉就回落到特权 CLI `bx down`,它拥有
            // forcedMacOSTeardown(Guardian 不可达或拒绝关闭时强制拆除)。
            // 同步执行,故必须留在这条后台队列上,绝不能回主线程再跑。
            var escape = ToggleEscapeOutcome.notAttempted
            if toggleEscape(action: action, socketSucceeded: succeeded) == .privilegedCLIDown {
                escape = self.runPrivilegedScriptOffMainThread(
                    privilegedTurnOffScript(bxPath: self.bxPath)
                ) ? .succeeded : .failed
                if escape == .succeeded {
                    succeeded = true
                }
            }
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                self.toggleInFlight = nil
                self.stopToggleTicker()
                self.toggleFailureText = toggleResultText(
                    code: failureCode,
                    transportDescription: transportError,
                    escape: escape
                )
                // 排队的 Quit 优先于常规收尾:不管刚落定的这个动作成不成功,
                // 用户已经确认要退出,不能让他们再点一次。
                if let pending = self.pendingQuit {
                    self.pendingQuit = nil
                    if pending.chainsTurnOffBeforeQuitting {
                        // 刚落定的是 turnOn(quitDisposition 只在这种情况下才会
                        // 产出 chainsTurnOffBeforeQuitting == true)——退出前必须
                        // 已关闭,再补一次 turnOff,它的 completion 才真正终止进程。
                        self.performToggle(.turnOff) { [weak self] turnedOff in
                            self?.finishQuit(turnedOff: turnedOff)
                        }
                    } else {
                        // 刚落定的就是那次 turnOff:它成了才退出。
                        self.finishQuit(turnedOff: succeeded)
                    }
                    return
                }
                self.refresh(userInitiated: true)
                completion?(succeeded)
            }
        }
    }

    /// 每秒重画一次,好让已用秒数往前走。
    private func startToggleTicker() {
        stopToggleTicker()
        // .common 模式:这个计数器最该走字的时刻,正是用户把菜单打开盯着它的时刻。
        toggleTicker = commonModeTimer(every: 1) { [weak self] in
            guard let self, self.toggleInFlight != nil else { return }
            self.rebuildMenu()
            self.updateIcon()
        }
    }

    private func stopToggleTicker() {
        toggleTicker?.invalidate()
        toggleTicker = nil
    }

    private func openTerminal(_ command: String) {
        let bashCommand = "/bin/bash -lc \(shellSingleQuoted(command))"
        let script = """
        tell application "Terminal"
          activate
          do script \(shellQuoted(bashCommand))
        end tell
        """
        if !runAppleScript(script) {
            showMessage("Terminal Permission Needed", "Allow bx to control Terminal when macOS asks, then try again. You can review this in System Settings > Privacy & Security > Automation.")
        }
    }

    private func runAppleScript(_ source: String) -> Bool {
        var error: NSDictionary?
        NSAppleScript(source: source)?.executeAndReturnError(&error)
        return error == nil
    }

    private func promptForClientLink() -> String? {
        let alert = NSAlert()
        alert.messageText = "Set Up bx"
        alert.informativeText = "Paste your bx link."
        alert.addButton(withTitle: "Set Up")
        alert.addButton(withTitle: "Cancel")

        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 420, height: 24))
        field.placeholderString = "bx://..."
        alert.accessoryView = field

        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        let link = field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !link.isEmpty else {
            showMessage("No Link", "Paste a bx link to continue.")
            return nil
        }
        guard looksLikeClientLink(link) else {
            showMessage("Link Not Recognized", "Paste a bx link to continue.")
            return nil
        }
        return link
    }

    private func confirmStartProtection(title: String = "Start protection?", cancelTitle: String = "Cancel") -> Bool {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = "bx will take over system traffic until you turn it off."
        alert.addButton(withTitle: "Start Protection")
        alert.addButton(withTitle: cancelTitle)
        return alert.runModal() == .alertFirstButtonReturn
    }

    private func looksLikeClientLink(_ link: String) -> Bool {
        link.hasPrefix("bx://") || link.hasPrefix("blink://") || link.hasPrefix("brook://")
    }

    private func showMessage(_ title: String, _ message: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    private func showFailure(_ title: String, _ message: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = "\(message) Run Doctor to collect diagnostics."
        alert.addButton(withTitle: "Run Doctor")
        alert.addButton(withTitle: "OK")
        if alert.runModal() == .alertFirstButtonReturn {
            runDoctor()
        }
    }

    private func diagnoseStopped(version: String?, fallback: String) -> BxState {
        let doctor = runBx(["doctor", "--json", "--skip-probe"])
        guard doctor.code == 0, let report = try? JSONDecoder().decode(DoctorReport.self, from: Data(doctor.stdout.utf8)) else {
            let message = fallback.trimmingCharacters(in: .whitespacesAndNewlines)
            return .warning(message.isEmpty ? "Status unavailable" : message, version: version)
        }
        if check(report, "service_installed")?.status == "fail" {
            return .setupNeeded("Run sudo bx setup <client-link>")
        }
        if check(report, "service_active")?.status != "ok" {
            return .off
        }
        if let socket = check(report, "status_socket"), socket.status != "ok" {
            return .warning(socket.detail ?? "Status socket unavailable", version: version)
        }
        return .warning("Needs attention", version: version)
    }

    private func check(_ report: DoctorReport, _ name: String) -> DoctorCheck? {
        report.checks.first { $0.name == name }
    }

    private func loadVersion() -> String? {
        let result = runBx(["--version"])
        guard result.code == 0 else { return nil }
        return result.stdout.replacingOccurrences(of: "bx version ", with: "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func bundleReleaseVersion() -> String? {
        guard let url = Bundle.main.url(forResource: "release", withExtension: "json") else { return nil }
        guard let data = try? Data(contentsOf: url) else { return nil }
        return decodeRuntimeVersion(data)
    }

    private func cliSupportsDiagnosticsArchive() -> Bool {
        let result = runBx(["logs", "--help"])
        return result.code == 0 && result.stdout.contains("--archive") && result.stdout.contains("--dir")
    }

    private func refreshUpdateCheck() {
        DispatchQueue.global(qos: .utility).async { [weak self] in
            guard let self else { return }
            let result = self.runBx(["update", "--check", "--json"])
            let check = result.code == 0
                ? try? JSONDecoder().decode(UpdateCheck.self, from: Data(result.stdout.utf8))
                : nil
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                self.updateCheck = check
                self.rebuildMenu()
            }
        }
    }

    private func runBx(_ arguments: [String]) -> CommandResult {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: bxPath)
        process.arguments = arguments
        let output = Pipe()
        let errors = Pipe()
        process.standardOutput = output
        process.standardError = errors
        do {
            try process.run()
        } catch {
            return CommandResult(code: 127, stdout: "", stderr: error.localizedDescription)
        }
        // **先排空管道,再等退出。** 反过来写(原实现)在子进程写满管道缓冲区
        // (约 64KB)时死锁:子进程阻塞在 write、我们阻塞在 waitUntilExit,谁也不动。
        // 今天的四条命令输出都只有几 KB,够不着;但这条路径现在跑在 refreshGate
        // 后面,一旦死锁,闸门永久关死、菜单**无声无息**停止更新,连个报错都没有。
        // 代价是两个后台读取,不值得为「今天够不着」留着。
        var outData = Data()
        var errData = Data()
        let drain = DispatchGroup()
        let queue = DispatchQueue.global(qos: .userInitiated)
        queue.async(group: drain) { outData = output.fileHandleForReading.readDataToEndOfFile() }
        queue.async(group: drain) { errData = errors.fileHandleForReading.readDataToEndOfFile() }
        process.waitUntilExit()
        drain.wait()
        let stdout = String(data: outData, encoding: .utf8) ?? ""
        let stderr = String(data: errData, encoding: .utf8) ?? ""
        return CommandResult(code: process.terminationStatus, stdout: stdout, stderr: stderr)
    }

    private func shellQuoted(_ value: String) -> String {
        let escaped = value.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
        return "\"\(escaped)\""
    }

    private func shellSingleQuoted(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
    }
}

private extension NSMenu {
    func addHeader(_ title: String, subtitle: String) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.attributedTitle = NSAttributedString(
            string: title,
            attributes: [.font: NSFont.systemFont(ofSize: 14, weight: .semibold)]
        )
        addItem(item)
        addItem(NSMenuItem(title: subtitle, action: nil, keyEquivalent: ""))
        addItem(.separator())
    }

    func addInfo(_ label: String, _ value: String) {
        let item = NSMenuItem(title: "\(label): \(value)", action: nil, keyEquivalent: "")
        item.isEnabled = false
        addItem(item)
    }

    /// 一行不带 "label: " 前缀的纯文本(禁用态)。`addInfo("", text)` 会渲染成
    /// 带孤零零冒号的 ": text",专门给不需要标签的提示行用。
    func addPlainText(_ text: String) {
        let item = NSMenuItem(title: text, action: nil, keyEquivalent: "")
        item.isEnabled = false
        addItem(item)
    }

    func addAction(_ title: String, symbol: String, target: AnyObject, action: Selector, enabled: Bool = true) {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
        item.target = target
        item.image = NSImage(systemSymbolName: symbol, accessibilityDescription: title)
        item.isEnabled = enabled
        addItem(item)
    }
}

private let bxMenuDelegate = BxMenuApp()
let bxApplication = NSApplication.shared
bxApplication.delegate = bxMenuDelegate
bxApplication.setActivationPolicy(.accessory)
bxApplication.run()
