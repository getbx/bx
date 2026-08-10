import AppKit
import Darwin
import Foundation

struct CommandResult {
    let code: Int32
    let stdout: String
    let stderr: String
}

enum BxState {
    case connected(GuardianStatus, version: String, dns: String?)
    case warning(String, version: String?)
    /// **今天没有生产者。** 唯一一处是「Guardian 没声明 diagnostics_archive 能力」
    /// 那道闸门,它已被降为一条并排的附注(见 loadState / outdatedRuntimeNotice)
    /// —— 一个只影响 Run Doctor 诊断包的判据,不该顶掉整个保护状态。
    ///
    /// 原样留着,与下面的 `.missing` 同一处置:删一个状态要连着改 quitPlan 的
    /// MenuStateKind、图标归属(那段裁决有它自己的守卫)、三处 rebuildMenu 分支
    /// 与 StatusIndicator —— 那是动状态机,而本次动的是「谁有资格决定状态」。
    /// 真出现一个**真正阻断**的不兼容(菜单再也读不懂 Guardian 的应答)时,
    /// 它就是那个落点。
    case updateNeeded(String, version: String?)
    case setupNeeded(String)
    case missing(String)
    case notInstalled(bundleVersion: String?)
    /// 保护没开。**必须带上来路**:两条来路的证据强度不同,Quit 的处置也不同
    /// (见 OffOrigin / quitPlan)。合并过一次,代价是 Guardian 服务已停时点 Quit
    /// 会弹一个意外的授权框,取消掉就换来一句「bx 还在跑」——而那时什么都没在跑。
    case off(OffOrigin)
}

/// Core 的控制 socket(`supervisor.SockPath`)。菜单只**拨**它、不说它的协议:
/// 「socket 在应答」本身就是存活观测,与 internal/observe 同一条依据。
let coreControlSocketPath = "/var/run/bx/core.sock"

/// Guardian 的 launchd plist,与 `install.GuardianInstalled()` 查的是同一个文件
/// (`install.guardianLaunchdPlistPath`)——drift 由 Go 侧守卫钉住。
///
/// **不要写成 `com.getbx.bx.plist` / `com.ggshr9.bx.plist`。** 那两个是 **Core**
/// 的(以及它的 legacy 标签)plist,`install.UnitInstalled()` 查的就是它们;而
/// 统一布局下 **Core 根本不是 launchd 服务**(由 Guardian 起停),所以在一台
/// 装好且正在保护的 mac 上它们**都不存在**(真机 2026-08-06,记在
/// `cli.go` 的 `darwinGuardianServiceName` 旁)。拿它们当「装没装」的判据,
/// 会让 `stoppedDiagnosis` 对一台配置完好的机器抢先返回 `.setupNeeded`
/// (「去跑 sudo bx setup」),并让 `.off(.serviceStopped)` 永远走不到。
let guardianLaunchdPlistPath = "/Library/LaunchDaemons/com.getbx.bx.guard.plist"

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
    // 首次刷新之前什么都还没观测过。默认取**不会直接退出**的那一支:
    // 这个窗口里点 Quit 应当走关闭路径,而不是凭一个还没问过的假设就退出。
    private var state: BxState = .off(.guardianResponding)
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
    /// Guardian 缺 diagnostics_archive 能力时的那条附注(见 outdatedRuntimeNotice)。
    /// nil 有两种来路:声明了能力,或**根本没问到**(Guardian 不应答时 resolve()
    /// 早就返回了)。两种都该让这一行消失 —— 「没问」不该被画成「已确认没问题」
    /// 的反面,而这一行只在**确知降级**时出现。
    private var outdatedRuntime: OutdatedRuntimeNotice?
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
        outdatedRuntime = outcome.outdatedRuntime
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
        let outdatedRuntime: OutdatedRuntimeNotice?
    }

    /// 收集一次状态。**跑在后台线程**(它要拨 Guardian 的 socket、还剩两处 spawn),
    /// 因此不碰 self 的可变状态:恢复快照与 repairVersions 作为局部量进出,由调用方
    /// 在主线程落定。
    ///
    /// **状态只有一个来源:Guardian 的 `/v1/status`。** 此前这里每次刷新 spawn
    /// `bx --version` + `bx status --json`,再把两份输出拼成状态 —— 那是把 UI 变成
    /// 第三个控制面:同一件事 Guardian 已经知道,菜单却用一个可能是旧版的二进制
    /// 重新推导一遍,两边一旦不一致,指示灯画的是错的那份。
    ///
    /// 判定体原样保在嵌套的 `resolve()` 里:那些 `recoverySnapshot = …` 紧跟
    /// `return .warning(…)` 的写法是 TestMacMenuWarningsDropGreenRecoverySnapshot
    /// 逐行审的对象,改写它等于把那条守卫连同它守的东西一起弄没。
    private func loadState(reconnectInFlight: Bool, snapshot: RecoverySnapshot?) -> RefreshOutcome {
        var recoverySnapshot = snapshot
        var repairVersions: (bundle: String?, runtime: String?, core: String?)?
        var outdatedRuntime: OutdatedRuntimeNotice?
        func resolve() -> BxState {
            let runtimeVersion = unifiedRuntimeVersion()
            // 「CLI 能不能执行」是关于**本机环境**的事实,Guardian 答不上来 —— 但
            // 它也不该由轮询路径每 2–30 秒 spawn 一次去问。真正需要这个答案的是
            // 要 shell out 的动作路径(Setup),那里执行之前问一次就够(见 setUpBx)。
            // 轮询这里只需要「装没装」,而那是一次 stat 就能答的。
            let cliUsable = cliIsInstalled()
            if installActionTitle(runtimeInstalled: runtimeVersion != nil, cliUsable: cliUsable) != nil {
                return .notInstalled(bundleVersion: bundleReleaseVersion())
            }
            // 这一支在**本次改动之前就已经到不了**:`installActionTitle` 在
            // `!cliUsable` 时就返回了 `.notInstalled`。原样留着(数据源变了,
            // 可达性没变),删它属于动状态机而不是动数据源。
            guard cliUsable else {
                return .missing("Install bx at /usr/local/bin/bx")
            }
            let report: GuardianStatus
            do {
                report = try guardianClient.status()
            } catch {
                if case GuardianClientError.socket(let code) = error {
                    // Guardian 的 socket 拨不通。「bx 没装」「装了但没跑」「还在但
                    // 挂住了」三者要在这里分开,而分开它们靠的是菜单**自己的直接
                    // 观测**(见 diagnoseStopped)——不是再 spawn 一个 CLI 转述,
                    // 也不可能是 Guardian 的某个端点:能走到这一支的前提就是它不应答。
                    return diagnoseStopped(
                        guardianErrno: code,
                        version: runtimeVersion,
                        detail: error.localizedDescription
                    )
                }
                // Guardian 应答了,只是答案读不动(协议损坏、解码失败)。这不是
                // 「没跑」—— 拿 doctor 去问 launchd 只会得到一个误导性的 off,而
                // 保护此刻可能正开着。必须先清恢复快照:updateIcon 让快照覆盖状态
                // 图标,留着一个绿快照会画出「状态是 warning、盾牌却是绿的」。
                recoverySnapshot = nil
                return .warning("Status unreadable", version: runtimeVersion)
            }
            if let banner = updatingBanner(phase: report.phase) {
                recoverySnapshot = nil
                return .warning(banner, version: runtimeVersion)
            }
            // 能力**由 Guardian 声明**,不再靠 spawn `bx logs --help` 去帮助文本里
            // 找 flag。位置从「问 Guardian 之前」挪到了「问到之后」——这是数据源
            // 改变的直接后果,不是顺手挪的:声明者就是应答者。放在 updatingBanner
            // 之后,免得升级过程中(新旧两版交接)多报一条正在解决中的降级。
            //
            // **它是一条附注,不是一道闸门。** 这里曾经是
            // `guard declaresDiagnosticsArchive(…) else { return .updateNeeded(…) }`,
            // 排在下面每一条保护判定**之前** —— 而旧 Guardian(本次能力契约之前的
            // 那一版)不声明能力,于是「Guardian 还在跑旧版」这个本产品明确建模、
            // 还会主动打印提示的处境(upgradeplan.go 的 upVersionMismatchMessage)
            // 里,菜单整个失去保护状态:没有 Protected/Off、没有 Turn Off、没有
            // Reconnect,只剩 "Update bx" 和一个错的补救("Open Install Guide" ——
            // 而 /v1/update-check 在旧 Guardian 上是 404,连那个更新入口都长不出来)。
            // **能力契约本身发布的那一次,每个既有用户都会撞上一回。**
            //
            // 判据影响的只有 Run Doctor 的诊断包这一项,状态照常从 Guardian 确实
            // 回答了的字段推导(protection_state / dns_* / core_version 都早于本轮
            // 改动就存在,旧 Guardian 一样给得出)。降级只报降级的那一项,连同真能
            // 解决它的那条命令,由 rebuildMenu 与保护状态**并排**显示。
            outdatedRuntime = outdatedRuntimeNotice(capabilities: report.capabilities)
            // 版本号原先来自 `bx --version`(每次刷新一次 spawn)。改报「正在保护
            // 你的那个 bx」:Guardian 说的 Core 版本;Core 没跑时回落到盘上的
            // runtime 版本(纯文件读)。健康态下二者本就相等 —— 不等即 Repair Required。
            let reportedCoreVersion = report.coreVersion ?? ""
            let version = reportedCoreVersion.isEmpty ? runtimeVersion : reportedCoreVersion
            let bundleVersion = bundleReleaseVersion()
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
                // Guardian 应答了、报告也解码了,它说保护是关的 —— 一个信念。
                return .off(.guardianResponding)
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
            repairVersions: repairVersions,
            outdatedRuntime: outdatedRuntime
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
        // 四态一律交系统上色:菜单栏图标随明暗反色,写死颜色必有一种模式看不见。
        // 形态本就承担全部信息(MenuIconTests 钉死去掉动效仍两两可分),颜色是多余的。
        button.image = compactStatusImage(for: style)
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
        // `.updateNeeded` 与 `.warning` **有意共用裂盾**,尽管「CLI 太旧」与
        // 「流量可能没被保护」是两件不同紧急程度的事。四态是固定的,可选只有两个:
        // ① 空心盾(.off):它**断言**「没在保护」。而 `.updateNeeded` 这条路径在
        //    问 Guardian 的 `/v1/status` **之前**就返回了 —— 菜单对保护开没开一无所知,
        //    画成「没在保护」是一句它无权说的话,而且是四态里最安静、最容易被
        //    忽略的一个,恰好把「指示灯已经不能再指示了」这件事藏起来。
        // ② 裂盾(.attention):它说的是「有事需要你看一眼」。旧 CLI 让菜单读不到
        //    状态,这**正是**需要看一眼的事——本项目一贯的立场是不许把「问不出来」
        //    伪装成一个自信的答案(internal/observe 的三态 Tristate、MenuRows 的
        //    .unknown 都是同一条原则)。
        // 紧急程度的差别由菜单正文承担:副标题是 "Update Required"、状态行是
        // "Update bx",与 .warning 的措辞完全不同。图标只负责「要不要看一眼」。
        //
        // 注意 `.updateNeeded` **今天没有任何产出点**(见其枚举定义处):能力缺席
        // 已降级成一条与保护状态并排的数据行,不再顶掉状态推导。这条裁决留着,是
        // 因为它约束的是「将来若有东西再产出这个状态,图标该怎么画」——而那个将来
        // 的产出点必然也在问过 Guardian 之后,届时空心盾就不只是「一句无权说的话」,
        // 而是一句可被当场证伪的谎。
        case .warning, .updateNeeded:
            return .attention
        case .off, .setupNeeded, .missing, .notInstalled:
            return .off
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

    private func compactStatusImage(for style: MenuIconStyle) -> NSImage {
        let image = NSImage(size: NSSize(width: 18, height: 18))
        image.lockFocus()
        defer { image.unlockFocus() }
        // template 只吃 alpha 通道,填什么颜色都一样;用不透明黑是为了蒙版是实的
        // ——换成半透明色(如 secondaryLabelColor)会让蒙版峰值只有 0.5,图标发虚。
        NSColor.black.setFill()
        NSColor.black.setStroke()
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
        image.isTemplate = true
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
            // 只有答过话的 Core 才有延迟可报。`.connected` 本就要求 reachable
            // (menuProtectionVerdict 拦在前面),这里的兜底是为了不让类型上的
            // 可选性被一句 `!` 抹掉 —— 零值延迟比没有延迟更像谎话。
            guard let core = report.core, core.reachable == true, let latency = core.latencyMS else {
                return "bx: Protected"
            }
            return "bx: Protected, \(latency) ms"
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
                menu.addAction("View Guardian Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
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
        // 「Guardian 跑的是旧版」是一条**与保护状态并排**的事实,不是一个顶掉它的
        // 状态。此前它被拿来门控整个状态机(见 loadState 里那段),于是升级窗口
        // 里保护状态整个消失;现在它只占这一行,降级的那一项与真能解决它的那条
        // 命令都写明,Protected/Off、Turn Off、Reconnect 一个不少。
        if let notice = outdatedRuntime {
            menu.addInfo("Guardian", notice.summary)
            menu.addPlainText(notice.remedy)
        }
        if let failure = toggleFailureText {
            menu.addItem(.separator())
            menu.addInfo("Last operation failed", failure)
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
        case .off(let origin):
            switch origin {
            case .guardianResponding: return .offGuardianResponding
            case .serviceStopped: return .offServiceStopped
            }
        }
    }

    private func menuRowsNow() -> MenuRowSet {
        switch state {
        case .connected(let report, _, let dns):
            return menuRows(status: report, dns: dns)
        default:
            return menuRows(status: nil, dns: nil)
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
        // 这是「CLI 在不在、跑不跑得起来」真正有意义的地方:下面那条 AppleScript
        // 会去执行它,而执行之前先弹一个授权框。轮询路径不再替这里探路(那是每几秒
        // 一次 spawn),所以在**要用它的那一刻**问一次 —— 让用户输完密码才被告知
        // 「没装」是最糟的顺序。`ensureCLIUsable` 里那次 `--version` 是本进程唯一
        // 一次真去执行 CLI 的探测,它接手了 `bx logs --help` 被删之后留下的那一档:
        // 文件在、却跑不起来。
        guard ensureCLIUsable() else { return }
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
            // 断网这句必须出现在这里:菜单调用走 osascript,CLI 的确认提示进了
            // 一个没人看的管道,这个 NSAlert 是 GUI 用户唯一看得到的告知。
            confirmMessage: "bx will install its command line tool and background protection service. macOS will ask for administrator authorization. If protection is already running, it is stopped and restarted to complete the upgrade — your network drops for a few seconds. On a fresh install, protection is not started until you set up and turn it on.",
            confirmButton: "Install"
        )
    }

    @objc private func repairBx() {
        runEmbeddedInstaller(
            confirmTitle: "Repair bx?",
            confirmMessage: "bx will reinstall its components from this app. Your connection settings are kept. If protection is running, it is stopped and restarted to finish the change — your network drops for a few seconds, then protection comes back on.",
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
        // --yes:这条命令跑在 osascript 里,没有终端可问,而同意已经在上面那个
        // NSAlert 里拿到了。不带它,CLI 会(正确地)因为无法确认而取消,Repair
        // 就成了一个弹完框却什么都没做的按钮。
        let command = "\(shellSingleQuoted(installer)) app-install --yes --app-source \(shellSingleQuoted(bundlePath))"
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
        // 同一条前置检查:这个动作也要 shell out 到 CLI(`bx update --json`),
        // 而且它跑在一次授权框之后 —— 跑不起来的二进制不该先向用户要密码。
        guard ensureCLIUsable() else { return }
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
            // confirmedOff 与 succeeded 是**两件事**,只在退出决策上用前者。
            // succeeded 说的是「这次调用没报错」,它还喂给逃生路径;而退出要问的是
            // 「保护真的关掉了吗」—— Guardian 现在会在无法向系统求证时回 200 但
            // protection_state != off。把 200 当成关掉了,菜单就会在 Core 还占着
            // TUN 的时候退出,留下一个没有任何指示灯的运行中保护。
            var confirmedOff = false
            var failureCode: String?
            var transportError: String?
            do {
                let status = action == .turnOn ? try self.guardianClient.turnOn() : try self.guardianClient.turnOff()
                failureCode = status.lastError
                succeeded = true
                if action == .turnOff {
                    confirmedOff = turnOffConfirmedProtectionStopped(protectionState: status.protectionState)
                }
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
                    // 特权 CLI `bx down` 走的是强制拆除:它经 Core 自己的控制 socket
                    // 请求退出,与那个 Core 由谁拉起来无关 —— 那是这条路能给出的最强确认。
                    confirmedOff = true
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
                        self.finishQuit(turnedOff: confirmedOff)
                    }
                    return
                }
                self.refresh(userInitiated: true)
                // **退出决策一律用 confirmedOff,不用 succeeded。**
                //
                // 这里曾写 completion?(succeeded) —— 而 completion 的两个调用方都是退出
                // 入口,常规那条(点 Quit → quitDisposition == .turnOffNow)正是走这里。
                // 我上一版只改了排队退出那个罕见分支,于是「200 但 protection_state != off
                // 时菜单照样退出」在正常路径上原样还在:修了一条路,不是那条路。
                completion?(action == .turnOff ? confirmedOff : succeeded)
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

    /// Guardian 的 socket 拨不通之后,拿**直接观测**判菜单该显示什么。
    ///
    /// 此前这里 spawn `bx doctor --json --skip-probe` 再从报告里挑三条检查。那次
    /// spawn 已经删掉,**而且没有换成 Guardian 的端点**:能走到这个函数的前提就是
    /// Guardian 不应答,同一个 socket 上再开一个端点照样答不了。CLI 当初替我们做的
    /// 也只是 stat 一个 plist、拨一次 Core 的控制 socket ——菜单自己就能做,少一层
    /// 可能是旧版的转述者(而那层正是这轮架构诊断要拆掉的东西)。
    ///
    /// **判定本身住在 StoppedDiagnosis.swift(有单测)**,这里只负责采集与落回
    /// `BxState`。判定的要点是顺序:任何「没在跑」的结论都必须先证明 **Core** 的
    /// 控制 socket 不应答 —— Guardian 不在不等于 Core 不在,而
    /// `.off(.serviceStopped)` 与 `.setupNeeded` 都会让 quitPlan 判
    /// terminateImmediately,在 Core 还活着时那就是「保护在跑但没有指示灯」。
    private func diagnoseStopped(guardianErrno: Int32, version: String?, detail: String) -> BxState {
        let core = probeCoreControlSocket()
        let diagnosis = stoppedDiagnosis(StoppedEvidence(
            serviceInstalled: guardianUnitInstalled(),
            guardianListening: socketObservation(connectErrno: guardianErrno),
            coreSocketAnswering: core.answering,
            coreSocketDetail: core.detail,
            guardianDetail: nonEmpty(detail)
        ))
        switch diagnosis {
        case .setupNeeded:
            return .setupNeeded("Run sudo bx setup <client-link>")
        case .serviceStopped:
            // 到这儿意味着两条新鲜的否定观测叠在一起:Core 的控制 socket 与
            // Guardian 的 socket **都**被内核明确告知没人在那儿。
            return .off(.serviceStopped)
        case .warning(let message):
            return .warning(message, version: version)
        }
    }

    /// Guardian 的 launchd plist 在不在盘上 —— 一次 stat,`install.GuardianInstalled()`
    /// 做的是同一件事。
    ///
    /// **返回 `Bool?` 而且真的会返回 `nil`。** 用 `FileManager.fileExists` 写这个
    /// 函数才是错的:它对「不存在」与「问不出来」(目录不可读、I/O 错误)一律回
    /// `false`,正是 `StoppedDiagnosis.swift` 通篇在禁止的那种压缩 —— 而这一项的
    /// `false` 会让 `stoppedDiagnosis` 抢在两条否定观测之前返回 `.setupNeeded`,
    /// 把一台配置完好的机器打回 Setup Required。所以走 `stat(2)` 看 errno:
    /// `ENOENT`/`ENOTDIR` 才是「确实没有」,其余一律「不知道」。
    private func guardianUnitInstalled() -> Bool? {
        var info = stat()
        if stat(guardianLaunchdPlistPath, &info) == 0 {
            return fileObservation(statErrno: nil)
        }
        return fileObservation(statErrno: errno)
    }

    /// 拨一次 Core 的控制 socket:应不应答,以及失败时的人话。
    ///
    /// 只 connect 再关掉,不发任何请求 —— 我们要的就是「有没有人在监听」这一个
    /// 事实,而 `bx doctor` 的 status_socket 检查做的也正是这件事
    /// (`net.DialTimeout` + `Close`)。判读住在 `socketObservation`(纯函数、有
    /// 单测),这里只负责 syscall。
    private func probeCoreControlSocket() -> (answering: Bool?, detail: String?) {
        guard let failure = connectUnixSocket(path: coreControlSocketPath, timeout: 0.5) else {
            return (true, nil)
        }
        return (
            socketObservation(connectErrno: failure),
            "\(coreControlSocketPath): \(String(cString: strerror(failure)))"
        )
    }

    private func nonEmpty(_ text: String) -> String? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    /// CLI 装没装 —— 一次 stat,**不执行它**。
    ///
    /// 「能不能执行」严格来说要执行一次才知道,而那正是轮询路径每几秒 spawn 一个
    /// `bx --version` 的由来。那一次 spawn 换来的额外信息只有「文件在、但跑不起来」
    /// (架构不符、损坏)这一种情形,而这种情形下真正会执行 CLI 的动作路径本来就
    /// 会失败并弹出它自己的失败框。所以这里只答「在不在」,把「跑不跑得起来」交给
    /// 真正要跑它的那次调用去回答。
    private func cliIsInstalled() -> Bool {
        FileManager.default.isExecutableFile(atPath: bxPath)
    }

    private func bundleReleaseVersion() -> String? {
        guard let url = Bundle.main.url(forResource: "release", withExtension: "json") else { return nil }
        guard let data = try? Data(contentsOf: url) else { return nil }
        return decodeRuntimeVersion(data)
    }

    /// 真·exec 探测:盘上那个二进制**跑不跑得起来**。
    ///
    /// `cliIsInstalled()` 那次 stat 答的是「在不在」;「架构不符 / 文件损坏 /
    /// 被 Gatekeeper 隔离」这几种「在、但一执行就失败」只有**真去执行一次**才能
    /// 知道。此前替所有人兜住这一档的是轮询路径上那次 `bx logs --help`;它已经
    /// 被删(能力改由 Guardian 声明),所以探测必须回到**真正要执行 CLI 的地方**
    /// ——那也是它唯一有意义的地方:在弹出授权框之前问,而不是每几秒问一次。
    ///
    /// `--version` 是最便宜且无副作用的一条:不碰配置、不碰网络、不碰路由。
    private func cliRuns() -> Bool {
        runBx(["--version"]).code == 0
    }

    /// 动作路径的统一前置检查:CLI 在不在、跑不跑得起来。
    /// 用 showMessage 而不是 showFailure —— 后者的 "Run Doctor" 要跑的正是这个
    /// 跑不起来的二进制。
    private func ensureCLIUsable() -> Bool {
        guard cliIsInstalled() else {
            showMessage("bx Not Found", "bx is not installed at \(bxPath). Install bx, then try again.")
            return false
        }
        guard cliRuns() else {
            showMessage(
                "bx Can't Run",
                "bx is installed at \(bxPath) but could not be started. Reinstall bx from Bx.app, then try again."
            )
            return false
        }
        return true
    }

    /// 有没有可装的新版 —— 问 Guardian(它代跑 `bx update --check` 那条路径),
    /// 不再 spawn 那条命令。
    ///
    /// 失败一律落回 `nil` = **不知道**,也就是不显示更新入口;绝不把「问不出来」
    /// 变成一个「有新版」或「已最新」的断言。Guardian 侧同样只在真拿到答案时才
    /// 回 200(见 updateCheckHandler)。
    private func refreshUpdateCheck() {
        DispatchQueue.global(qos: .utility).async { [weak self] in
            guard let self else { return }
            let fetched = try? self.guardianClient.updateCheck()
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                // 查不动时保留上一次的已知答案,别把「有新版」抹成 nil —— 判据住在
                // mergedUpdateCheck(有单测)。
                self.updateCheck = mergedUpdateCheck(previous: self.updateCheck, fetched: fetched)
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

/// 连一次 unix socket 就关掉。成功返回 nil,失败返回 errno。
///
/// 非阻塞 connect + poll:unix socket 的 connect 通常立刻返回,但 backlog 满时会
/// 阻塞,而这条路径跑在 `refreshGate` 后面 —— 一次挂住就等于菜单**无声无息**停止
/// 更新(runBx 那个死锁坑的同一课)。超时算作 ETIMEDOUT,由 `socketObservation`
/// 判成「问不出来」而不是「不在」。
func connectUnixSocket(path: String, timeout: TimeInterval) -> Int32? {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    guard fd >= 0 else { return errno }
    defer { close(fd) }
    var address = sockaddr_un()
    let bytes = Array(path.utf8CString)
    guard bytes.count <= MemoryLayout.size(ofValue: address.sun_path) else { return ENAMETOOLONG }
    address.sun_family = sa_family_t(AF_UNIX)
    withUnsafeMutablePointer(to: &address.sun_path) { destination in
        destination.withMemoryRebound(to: CChar.self, capacity: bytes.count) { slot in
            for index in bytes.indices {
                slot[index] = bytes[index]
            }
        }
    }
    let length = socklen_t(MemoryLayout<sa_family_t>.size + bytes.count)
    address.sun_len = UInt8(length)
    let flags = fcntl(fd, F_GETFL)
    guard flags >= 0, fcntl(fd, F_SETFL, flags | O_NONBLOCK) == 0 else { return errno }
    let result = withUnsafePointer(to: &address) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
            Darwin.connect(fd, $0, length)
        }
    }
    if result == 0 { return nil }
    guard errno == EINPROGRESS else { return errno }
    var descriptor = pollfd(fd: fd, events: Int16(POLLOUT), revents: 0)
    let milliseconds = Int32(max(1, ceil(timeout * 1_000)))
    while true {
        let ready = Darwin.poll(&descriptor, 1, milliseconds)
        if ready < 0 && errno == EINTR { continue }
        if ready == 0 { return ETIMEDOUT }
        guard ready > 0 else { return errno }
        break
    }
    var socketError: Int32 = 0
    var size = socklen_t(MemoryLayout.size(ofValue: socketError))
    guard getsockopt(fd, SOL_SOCKET, SO_ERROR, &socketError, &size) == 0 else { return errno }
    return socketError == 0 ? nil : socketError
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
