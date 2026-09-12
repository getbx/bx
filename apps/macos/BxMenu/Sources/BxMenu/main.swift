import AppKit
import Darwin
import Foundation
import UserNotifications

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
    /// 状态转换通知的判据(TransitionNotice.swift,纯状态机)。每次刷新喂一次
    /// Guardian 的应答;它说要响才响。
    private var transitionNoticeTracker = TransitionNoticeTracker()
    /// UNUserNotificationCenter 只在**打包成 bundle** 的进程里可用 —— 裸
    /// `swift run` 下调它会直接崩(bundleProxyForCurrentProcess 为 nil)。
    private lazy var notificationsAvailable: Bool = Bundle.main.bundleIdentifier != nil
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
    /// 同一时刻只跑一次泄漏检测:每次点击都会起一个新的 loopback 服务,连点会攒
    /// 一堆,每个都带着自己的 2 分钟超时。
    private var leakCheckInFlight = false
    /// 本轮菜单里,更新入口是否已经由版本行承担。每次 rebuildMenu 开头复位。
    /// Guardian 最近一次**应答并解码成功**的那份报告,只用来问一件事:此刻有没有
    /// 维护挂起(见 maintenanceRow)。挂起期间 `protection_state` 就是 `off`,
    /// `BxState.off` 又不带 payload,不留住这份报告菜单就无从分辨「bx 正在自我升级」
    /// 与「用户把它关了」。Guardian 不应答、或答案解不动时它保持 nil ——
    /// 「没问到」不许拿一份旧报告冒充。
    private var maintenanceReport: GuardianStatus?
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
    /// 菜单此刻开着还是关着。**只用于 watch 起跑那一刻**判断该不该立刻换成兜底
    /// 间隔 —— 菜单开合本身仍由 `menuWillOpen`/`menuDidClose` 直接传参调用
    /// `rescheduleRefreshTimer`,不经这个属性绕一道。
    private var menuIsOpen = false
    /// 长轮询循环专属的后台串行队列。**不用 `.global()`**:那是并发队列,而这里
    /// 只该有一个循环在跑,用专属队列让这件事从命名上就清楚。
    private let watchQueue = DispatchQueue(label: "com.getbx.bx.menu.statuswatch")
    /// watch 循环是否已经起来。**只在主线程读写。** 正常情况下只会从 false 变成
    /// true 一次,循环本身此后永远跑着(直到进程退出)。唯一的例外是服务端应答里
    /// 没有 `status_generation` 键 —— 这一版 Guardian 没有 watch 这个概念(字段
    /// 自己的文档注释就是这么写的),此时 `runWatchLoop` 会退出并把这个标志翻回
    /// false,下一次 `applyRefresh`(经既有轮询节奏)会自然地重新判断要不要
    /// 起跑(见 `runWatchLoop` 里 `status.statusGeneration` 为 nil 的分支)。
    private var watchLoopRunning = false
    /// watch 循环此刻已知的代际号。**起跑前那一次赋值发生在主线程,经
    /// `watchQueue.async` 建立的 happens-before 关系保证安全;此后只有
    /// `watchQueue` 上的 `runWatchLoop` 读写它,主线程再也不碰。**
    private var watchGeneration: UInt64 = 0

    func applicationDidFinishLaunching(_ notification: Notification) {
        enforceSingleInstance()
        ensureLoginItemIfCanonical()
        configureMenu()
        // 启动那一次不可能撞上在途刷新,补跑与否无意义;标 false 以免被读成用户动作。
        // **引导必须在刷新落地之后跑,而不是紧跟其后。** refresh 把采集扔到后台队列,
        // 结果稍后才在主线程写回 state —— 早先写在下一行,读到的是初始值
        // (.off),于是 firstRunAction 永远落到 default、引导**从来不触发**,
        // 而那正是这个功能的全部意义。判定住在 FirstRun.swift(编得进测试套件)。
        refresh(userInitiated: false) { [weak self] in
            self?.runFirstRunGuidance()
        }
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
    ///
    /// **watch 起来之后,只有「菜单关着」这一档换成 `menuWatchBackstopSeconds`。**
    /// 那一档此后只是「watch 是不是哑了」的保险,不再是取数据的手段(见
    /// StatusWatch.swift 里 `menuWatchBackstopSeconds` 的注释)。「菜单开着」的
    /// 2 秒档**不受影响**:用户此刻正在看数据行,而且 rules/servers 今天仍靠
    /// 这一拍的常规 `refresh` 带回(Task 6 才改成按需拉),watch 只管 status 那半。
    private func rescheduleRefreshTimer(menuOpen: Bool) {
        timer?.invalidate()
        let interval = (!menuOpen && watchLoopRunning) ? menuWatchBackstopSeconds : menuPollInterval(menuOpen: menuOpen)
        timer = commonModeTimer(every: interval) { [weak self] in
            self?.refresh(userInitiated: false)
        }
    }

    /// 引导序列的后半段:capabilities 到手之后,声明了 `status_watch` 才转入
    /// watch 循环。**绝不试拨**——判据只有 `watchIsAvailable`,不手抄字符串比较
    /// (见 StatusWatch.swift)。只会成功起跑一次:`watchLoopRunning` 一旦置
    /// true,循环体本身永远不停(直到进程退出),不需要停止的路径。
    private func startWatchLoopIfAvailable() {
        guard !watchLoopRunning, watchIsAvailable(capabilities: maintenanceReport?.capabilities) else { return }
        watchLoopRunning = true
        watchGeneration = maintenanceReport?.statusGeneration ?? 0
        // 兜底轮询从这一刻起可能换挡(见 rescheduleRefreshTimer 的注释),按
        // 菜单此刻的开合状态重排一次,而不是等下一次 menuWillOpen/menuDidClose。
        rescheduleRefreshTimer(menuOpen: menuIsOpen)
        watchQueue.async { [weak self] in
            self?.runWatchLoop()
        }
    }

    /// 长轮询循环。**每一轮结果都经既有的 `refresh(userInitiated:)` →
    /// `loadState` → `applyRefresh` 落定**,不新写一条状态落定路径——那会变成
    /// 第二个控制面,而这个仓库的架构诊断整篇讲的就是这件事(见 CLAUDE.md
    /// 「控制面架构诊断」一节)。这里只负责「什么时候该去刷」。
    ///
    /// 服务端返回的代际号与请求的相同 = 只是挂住到了它自己的 25 秒上限、什么
    /// 都没变,直接再等一轮,不必触发一次刷新去重新问一遍已经知道没变的答案——
    /// 但这一等要先付 `menuWatchIdleDelaySeconds` 这个 floor(第二道防线,
    /// 见其注释),否则一台绕过了能力门控或声明能力却仍秒回的服务端会把
    /// 这个循环烧到满速。
    private func runWatchLoop() {
        var consecutiveFailures = 0
        while true {
            let requested = watchGeneration
            do {
                let status = try guardianClient.statusWatch(generation: requested)
                consecutiveFailures = 0
                guard let observed = status.statusGeneration else {
                    // status_generation 缺席意味着这一版 Guardian 没有 watch 这个
                    // 概念(字段自己的文档注释,GuardianStatus.swift),不是「这一
                    // 轮没变」。`?? requested` 曾经把两者混成一件事:观测值恒等于
                    // 请求值,永远命中下面「未变化」那一支,困在一个 1 Hz 的
                    // menuWatchIdleDelaySeconds 轮询里、图标要等到
                    // menuWatchBackstopSeconds(60 秒)兜底才会被修正 —— 比这个
                    // 功能要取代的 30 秒轮询还差。老实地退出循环、把
                    // watchLoopRunning 翻回 false,退回今天的轮询节奏——与 CLI
                    // 侧硬门(requireStatusWatchCapability)给的降级路径一致。
                    DispatchQueue.main.async { [weak self] in
                        guard let self else { return }
                        self.watchLoopRunning = false
                        self.rescheduleRefreshTimer(menuOpen: self.menuIsOpen)
                    }
                    return
                }
                guard observed != requested else {
                    // 第二道防线(见 menuWatchIdleDelaySeconds 的注释):一台
                    // 声明了能力却仍然秒回、代际号没推进的服务端,不加这个
                    // floor 就会把这个分支烧到满速——只在这一支生效,代际号
                    // 真的变了要立刻走下面那条落定路径,不许被这个下限拖慢。
                    Thread.sleep(forTimeInterval: menuWatchIdleDelaySeconds)
                    continue
                }
                watchGeneration = observed
                DispatchQueue.main.async { [weak self] in
                    // userInitiated: true —— 一次 watch 事件是一个不重复的、
                    // 「结果必须尽快出现」的时刻,与用户直接点击同一类(见
                    // RefreshGate.begin 的文档注释)。若传 false,一旦这次刷新
                    // 撞上已经在飞的一次而被丢弃,菜单不会补跑:watchGeneration
                    // 已经推进到新值,循环下一轮 wait 的就是它,不会再收到一次
                    // 「变了」的广播——不像定时器每隔几秒自己会再来一拍,这一次
                    // 事件错过就是永久错过,直到下一次真的状态变化或 60 秒兜底。
                    self?.refresh(userInitiated: true)
                }
            } catch {
                consecutiveFailures += 1
                let backoff = watchBackoffSeconds(consecutiveFailures: consecutiveFailures)
                // Guardian 消失是一个会改变「该显示什么」的事件(bootout、崩溃、
                // 升级窗口),不能把纠正拖到下一次成功的长轮询或 60 秒兜底——那正
                // 是本条修复要解决的问题:此前这个分支什么都不做,图标停在最后
                // 一次成功状态上直到兜底才被拉回来,比它替换掉的 30 秒轮询还差。
                //
                // **每次失败都刷新,不只是第一次,但这不是在打一个死 socket 的
                // 满速循环**:退避本身已经把重试间隔拉开(1s → 2s → 4s → … 封顶
                // 30s),这里只是搭上这趟已经在走的车 —— 没有另开一条独立定时器
                // 或立即重试。RefreshGate 还会在一次刷新仍在飞时丢弃重叠的那次,
                // 是又一层节流。
                DispatchQueue.main.async { [weak self] in
                    self?.refresh(userInitiated: false)
                }
                if backoff > 0 {
                    Thread.sleep(forTimeInterval: backoff)
                }
            }
        }
    }

    /// 菜单显示前重填。**只用缓存状态,不 spawn 任何子进程** —— 这条路径在
    /// 用户点击与菜单出现之间,跑一次 status --json(封顶 5 秒)就是肉眼可见的卡顿。
    func menuNeedsUpdate(_ menu: NSMenu) {
        rebuildMenu()
    }

    func menuWillOpen(_ menu: NSMenu) {
        menuIsOpen = true
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
        menuIsOpen = false
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
    private func refresh(userInitiated: Bool, then completion: (() -> Void)? = nil) {
        // 上一次还没回来就丢掉这一次:排队只会堆出一串拿到时已作废的刷新。
        //
        // **但回调不许跟着丢。** 被丢掉的是那次**采集**(它确实多余,在途那次
        // 马上就会带回新数据),不是调用方要在数据落地之后做的那件事。首次引导
        // 就是这么没的:启动时它排在别的刷新后面,gate 一挡,那个 completion
        // 连同整个功能一起消失,而 refreshGate.end() 的补跑不带回调、补不回来。
        guard refreshGate.begin(userInitiated: userInitiated) else {
            if let completion { pendingRefreshCompletions.append(completion) }
            return
        }
        let inFlight = reconnectInFlight
        let snapshot = recoverySnapshot
        let generation = recoveryGeneration.value
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let outcome = self.loadState(reconnectInFlight: inFlight, snapshot: snapshot)
            DispatchQueue.main.async { [weak self] in
                self?.applyRefresh(outcome, capturedGeneration: generation)
                // **落地之后才回调。** refresh 是异步的:紧跟在它后面读 state 读到的
                // 是上一轮(启动时就是初始值),而首次引导正是靠 state 决定问哪一个。
                completion?()
                // 在途这次带回的数据同样是新的,被挡掉的调用方等的就是它。
                self?.drainPendingRefreshCompletions()
            }
        }
    }

    /// 因 refreshGate 被挡掉、但仍欠着的回调。**只在主线程碰。**
    private var pendingRefreshCompletions: [() -> Void] = []

    /// 跑完所有欠着的回调。**先清空再跑** —— 回调里可能又发起一次 refresh,
    /// 那次若也被挡就会往这个数组里再塞一个,边跑边清会漏掉它或无限循环。
    private func drainPendingRefreshCompletions() {
        let pending = pendingRefreshCompletions
        pendingRefreshCompletions = []
        for completion in pending { completion() }
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
        maintenanceReport = outcome.maintenanceReport
        observeTransition(outcome.maintenanceReport)
        // capabilities 刚到手,若这一版 Guardian 声明了 status_watch 且循环还没
        // 起,就在这里转入 watch——引导序列的后半段(前半段是这次 refresh 本身)。
        startWatchLoopIfAvailable()
        // rules/servers 已经改按需拉(fetchRulesOnDemand/fetchServersOnDemand,
        // 另外 applyGroupChange 换规则组之后也会直接顶替 lastRules),
        // `outcome.rules`/`.servers` 现在恒为 nil ——这两个 `if let` 今天永远不会
        // 执行。留着不删是防御性的:一旦哪天刷新路径又长出一条真的写它们的支线
        // (比如某个界面确实需要跟着环境刷新),这里「不覆盖」的语义能保证半路
        // 失败的一次不会用 nil 抹掉已经取到的数据,不必重新推一遍这条纪律。
        if let fresh = outcome.rules {
            lastRules = fresh
        }
        if let fresh = outcome.servers {
            lastServers = fresh
        }
        // 服务器窗口的实时更新曾经就藏在上面那个已经死掉的分支里
        // (`outcome.servers` 恒 nil,`refreshIfVisible` 从此再也不会被这条路调用)——
        // 于是打开着的服务器窗口会冻在打开那一刻,直到用户关掉重开、或恰好触发
        // probeServers/checkExitIP/一次切换。改为按窗口可见性触发:
        // **窗口关着 = 没人在看,不拨**(这个 task 要保住的收益,没人看时不再
        // 每次刷新都解析一遍 config);**窗口开着 = 有人正盯着**,这时候按需拉
        // 一次 servers 正是「按需」的本意,不是违背它。`forceShow: false` 让它
        // 用 `refreshIfVisible` 就地重画,不会像 `openServersWindow` 那样抢焦点
        // 弹出窗口。
        if serversWindow.isVisible {
            fetchServersOnDemand(forceShow: false)
        }
        // 规则窗口同理,而且它此前**整个没有这条路**:`RulesWindow` 连
        // `isVisible` 都没有,`applyRefresh` 也不为它做任何事,于是每条规则的失败
        // 计数冻在打开窗口那一刻 —— 而「哪条在失败」正是这个窗口存在的理由。
        // 与服务器窗口同一个形状、同一条纪律:窗口关着不拨,开着就按需拉一次;
        // `forceShow: false` 走 `refreshIfVisible` 就地重画,不抢焦点、不弹 alert,
        // 并且这一路(也只有这一路)会被在飞守卫拦住。
        if rulesWindow.isVisible {
            fetchRulesOnDemand(forceShow: false)
        }
        // 应用流量同理:**窗口关着就不拨**(没人看时 Core 不问内核、不记字节、
        // 不攒历史,也不在这台机器上留下你开过什么应用的记录 —— 注意不是「开销
        // 精确为零」,那句旧说法 2026-08-20 之后不再成立,Core 侧仍要维护一张
        // 活连接表);窗口开着说明有人正盯着,跟着状态变化再拉
        // 一次是「按需」的本意。心跳定时器管的是稳态节拍,这一路管的是「状态刚
        // 变了」那一刻 —— 两者都过同一个在飞守卫,叠不起来。
        if appTrafficWindow.isVisible {
            fetchAppTrafficOnDemand(forceShow: false)
        }
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

    /// 把这一轮 Guardian 的应答喂给转换通知的状态机;它说要响才响。
    ///
    /// **Guardian 没应答(nil)就什么都不喂**:那是「问不出来」,不是一个状态;
    /// 状态机对没喂的一拍什么都不做,下一拍拿到真答案再判。
    private func observeTransition(_ report: GuardianStatus?) {
        guard let report else { return }
        let signal = protectionSignal(protectionState: report.protectionState, tunnelHealthy: report.core?.tunnelHealthy)
        if let notice = transitionNoticeTracker.observe(signal, at: Date()) {
            deliverTransitionNotice(notice)
        }
    }

    /// 投递一条系统通知。同一个 identifier:「已恢复」那条顶掉「阻断」那条,
    /// 通知中心里不会攒一串。授权被拒就静默 —— 用户说过不要,就不要。
    private func deliverTransitionNotice(_ notice: TransitionNotice) {
        guard notificationsAvailable else { return }
        let center = UNUserNotificationCenter.current()
        center.requestAuthorization(options: [.alert, .sound]) { granted, _ in
            guard granted else { return }
            let content = UNMutableNotificationContent()
            content.title = notice.title
            content.body = notice.body
            content.sound = .default
            let identifier = "bx.protection.transition"
            center.removeDeliveredNotifications(withIdentifiers: [identifier])
            center.add(UNNotificationRequest(identifier: identifier, content: content, trigger: nil))
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
        /// 这一次 Guardian 应答并解码成功的报告(见 maintenanceReport)。
        let maintenanceReport: GuardianStatus?
        /// 这一次读到的规则。**nil 表示没读到**,与「一条规则都没有」是两回事 ——
        /// 后者会让菜单摆出一个空列表,用户据此以为自己从没配过规则。
        let rules: RuleList?
        /// 这一次读到的服务器清单。**nil 表示没读到**,同上。
        let servers: ServerList?
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
        var maintenanceReport: GuardianStatus?
        // 恒 nil:rules/servers 已改按需拉(见下方 fetchRulesOnDemand /
        // fetchServersOnDemand),这条刷新路径不再碰它们。字段仍在
        // `RefreshOutcome` 里是因为 nil 有意义(「这一轮没读到」),
        // applyRefresh 那段「保留上一轮」的逻辑仍然依赖它。
        let rules: RuleList? = nil
        let servers: ServerList? = nil
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
            // 维护挂起随这份报告一起到,而它在**下面每一个**分支里都要能被说出来:
            // 升级窗口里状态可能是 .warning("Updating…"),也可能已经是 .off。故落点
            // 紧挨着「解码成功」,早于任何一次 return —— 挪到某个分支里去,就会有
            // 一整类状态下菜单再次把「bx 正在自我升级」显示成「你把它关了」。
            maintenanceReport = report
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
                service: report.dnsService,
                servers: report.dnsServers ?? []
            )
            guard dns.allowsProtected else {
                recoverySnapshot = recoverySnapshotSurvivingWarning(recoverySnapshot)
                return .warning(dns.menuWarning ?? "DNS status unavailable", version: version)
            }
            return .connected(report, version: version ?? "unknown", dns: dns.label)
        }
        let state = resolve()
        // 规则与服务器**不在刷新路径里拉了**——图标不依赖它们(menuRowsNow 一个
        // 字都不碰 rules/servers),而每次刷新都带上会让 Guardian 各多读并
        // YAML 解析一遍 /etc/bx/config.yaml。轮询时代这是浪费;watch 时代刷新
        // 变成「每次状态变化都有一次」,带着它就会变成更糟。改为按需:见
        // fetchRulesOnDemand / fetchServersOnDemand,分别在打开规则子菜单 /
        // 服务器窗口时才拨。这里两个局部量维持 nil,`RefreshOutcome` 的字段
        // 保留(「这一轮没读到就保留上一轮的」那段逻辑仍然有用),只是这条路径
        // 恒不写入。
        return RefreshOutcome(
            state: state,
            recoverySnapshot: recoverySnapshot,
            repairVersions: repairVersions,
            outdatedRuntime: outdatedRuntime,
            maintenanceReport: maintenanceReport,
            rules: rules,
            servers: servers
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
            // 与表头同一个判定同一份数据(offSubtitle):鼠标悬停是不点开菜单就
            // 看得到的唯一一句话,让它和菜单里说的不一样毫无道理。
            return "bx: \(offSubtitle(status: maintenanceReport, now: Date()))"
        }
    }

    /// 就地重填菜单项。**不建新 NSMenu**(见 configureMenu),这样重填会落在用户
    /// 正看着的那个菜单上,而不是下一次打开才生效的另一个对象上。只读缓存状态,
    /// 不 spawn 子进程,因此放在菜单显示前的路径上也是安全的。
    /// 上一次读到的规则。nil 表示**没读到**,与「一条规则都没有」是两回事 ——
    /// 后者会让用户以为自己从没配过规则。
    private var lastRules: RuleList?
    private var lastServers: ServerList?
    /// 出口 IP 探测的当前状态。**默认是 .unknown(「没查过」),不是某个地址** ——
    /// 与这个仓库里 Tristate 同一条纪律。
    private var exitIPProbe: ExitIPProbe = .unknown
    /// **正在切到哪一台**,nil = 没有切换在飞。见 confirmAndSwitchServer。
    ///
    /// 它此前是个裸 Bool,而且只有 `main.swift` 看得见 —— 于是用户点完确认之后
    /// 屏幕上二十几秒什么都不发生,再点一次连对话框都不弹(被在飞守卫挡掉,
    /// 而那个守卫本身是对的)。存名字而不是 Bool,是为了让窗口能在**那一行**
    /// 上说「Switching…」;**只有这一个状态,没有第二个 Bool** —— 两个要
    /// 「记得一起设」的变量迟早会漂开,而漂开的后果是界面永远停在切换中,
    /// 或者永远不显示切换中。
    private var switchingTo: String?
    /// 有一次按需拉规则/服务器正在飞。与 `probing`/`switchingTo` 同一个模式。
    ///
    /// **服务器这边不是可选的**:`fetchServersOnDemand` 现在不止由菜单点击触发
    /// (`forceShow: true`),服务器窗口开着时每一次 `applyRefresh` 也会调它一次
    /// (`forceShow: false`)——watch 时代刷新是事件驱动的、可能连着来,没有这个
    /// 守卫,重叠的取数会真的发生(旧的还没回来,新的又拨了一次)。
    ///
    /// **但这个标志被两条路共用,拦截判据必须只压其中一条**(见
    /// `shouldSuppressFetch`,`StatusWatch.swift`):环境刷新那一路(`forceShow:
    /// false`)在已有一次在飞时被拦是安全的,漏一次最多晚一拍;而显式点击那一路
    /// (`forceShow: true`)绝不能被这个标志拦——拦住的后果是用户点了「Servers…」,
    /// 窗口没出现、没有 alert、什么都没发生。这曾经是一次真实的回归(点开窗口
    /// 前一刻恰好撞上一次环境刷新在飞,`guard !serversFetchInFlight` 直接把显式
    /// 打开吞掉),按上面这条不对称改掉。
    ///
    /// 规则这边**自 2026-09-11 起也有两条路**(菜单点击,以及删/加一条规则之后的
    /// 重拉),同一条不对称照样适用:重拉那一路可以被拦,显式打开那一路绝不能。
    private var rulesFetchInFlight = false
    private var serversFetchInFlight = false

    /// 正在删的那几条规则(键只是本地去重用的 `kind|pattern`)。理由见
    /// `removeRuleFromWindow`:小按钮被双击会发两次删除,而第二次会为一条
    /// **确实删成功了**的规则弹出一句失败。
    private var ruleRemovalsInFlight: Set<String> = []

    /// 规则窗口。**窗口而不是子菜单**:2026-09-08 之前菜单每 2 秒 removeAllItems()
    /// 重建一次(现在只在内容变了才重建,见 commitMenu,子菜单已可用);它仍是窗口
    /// 是因为规则要编辑、要看失败归因,那不是子菜单能装下的。
    private lazy var rulesWindow: RulesWindowController = {
        let controller = RulesWindowController()
        controller.onToggleGroup = { [weak self] name, enable in
            self?.applyGroupChange(group: name, enable: enable)
        }
        controller.onRevealConfig = { [weak self] in
            guard let path = self?.lastRules?.configPath, !path.isEmpty else { return }
            NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
        }
        controller.onRemoveRule = { [weak self] kind, pattern in
            self?.removeRuleFromWindow(kind, pattern)
        }
        controller.onUndoRemove = { [weak self] kind, pattern in
            self?.addRuleBack(kind, pattern)
        }
        controller.onAddRule = { [weak self] in
            self?.addRuleFromWindow()
        }
        return controller
    }()

    @objc private func openRulesWindow() {
        fetchRulesOnDemand(forceShow: true)
    }

    /// 在**失败点**留痕(stderr → launchd 的 menu.err.log)。留痕必须住在
    /// catch 里而不是弹窗文案的构造里:有旧缓存兜着时不弹窗、服务器窗口的
    /// 环境刷新(forceShow=false)也不弹窗 —— 把留痕挂在弹窗上,这两类失败
    /// 就又回到 `try?` 时代的零记录(2026-08-29 code review 抓到,而那正是
    /// 这批改动自称要堵的洞)。
    private func logGuardianFetchFailure(_ what: String, _ error: Error) {
        FileHandle.standardError.write(
            Data("bx-menu \(what) fetch failed: \(error.localizedDescription)\n".utf8))
    }

    /// 把一次 Guardian 拉取失败折成弹窗说明:只抽事实,措辞由纯函数
    /// guardianFetchFailureInfo(RulesModel.swift,可测)决定。留痕不在这里 ——
    /// 见 logGuardianFetchFailure。
    ///
    /// **`logsAvailable:` 传的必须是 showGuardianFailure 用来决定画不画
    /// 「Show Details」的同一个表达式**:文案许诺一个按钮而能力门把它扣住,
    /// 用户就会在弹窗里找一个不存在的东西。同一个判据算两遍就够漂开一次。
    private func fetchFailureAlertInfo(_ error: Error?, what: String) -> String {
        var httpStatus: Int?
        var failureCode: String?
        if let clientError = error as? GuardianClientError,
            case let .status(status, code) = clientError {
            httpStatus = status
            failureCode = code
        }
        return guardianFetchFailureInfo(
            httpStatus: httpStatus, failureCode: failureCode,
            describedError: error?.localizedDescription,
            logsAvailable: logsAvailable(capabilities: maintenanceReport?.capabilities))
    }

    /// 把一份规则摆进窗口。**摆这张表的唯一出口。**
    ///
    /// 收口的理由不是好看:此前有**五处**各自算一遍这三样(组行、规则行、顶上
    /// 那句话),而每一处都得记得 ① 失败归因只能取自**答过话的** Core、
    /// ② 顶上那句话要带上问不出来的半边。五处独立地记住两件事,就是十次机会
    /// 漏掉其中一次 —— 而这个 bug 正是五处**一致地**都漏了同一件:
    /// `self.maintenanceReport?.core?.failingRules ?? []`。
    ///
    /// **Core 不应答时那个数组按构造就是空的**(`CoreRuntime.Reachable=false`
    /// 时其余字段一律零值,`internal/guardian/types.go`),而这个窗口把
    /// 「一行没有副标题」读作「查过了、健康」—— 于是保护关着、Core 崩了或正在
    /// 重启时,那条把用户招来的失败规则会和其它规则一样安安静静地排在健康档里。
    /// `/v1/rules` 这一跳照样成功(Guardian 自己读配置、自己算体检,不需要
    /// Core),所以体检那句话也不会出现,窗口从头到尾看起来干干净净。
    ///
    /// 判据因此是 `reachable` 而不是「数组空不空」,并且**复用**菜单数据行那份
    /// `answeringCore` —— 同一个问题不许有第二份判据。
    private func presentRules(_ list: RuleList, forceShow: Bool) {
        // 局部名字刻意不叫 `core`:那会拼成 `core?.failingRules`,与这个 bug 的
        // 原形 `maintenanceReport?.core?.failingRules` 逐字重合,守卫再也分不开
        // 「过了 reachable 那道门的」与「直接从 status 上摸的」。
        let answering = answeringCore(maintenanceReport)
        let failing = answering?.failingRules ?? []
        let caveat = ruleWindowCaveatNote(list, coreAnswering: answering != nil)
        let groups = ruleGroupRows(from: list, failing: failing)
        let table = ruleRows(from: list, failing: failing, customOnly: true)
        if forceShow {
            rulesWindow.show(
                rows: groups, ruleRows: table, configPath: list.configPath, caveatNote: caveat)
        } else {
            rulesWindow.refreshIfVisible(
                rows: groups, ruleRows: table, configPath: list.configPath, caveatNote: caveat)
        }
    }

    /// 按需拉一次规则。**只在用户真的要看规则时拨** ——
    /// 图标不依赖它(menuRowsNow 一个字都不碰 rules),而每次拨都让一个 root
    /// 守护进程读并 YAML 解析一遍 /etc/bx/config.yaml。
    ///
    /// 在轮询时代这是浪费;watch 时代刷新变成「每次状态变化都有一次」,
    /// 带着它就会变成更糟。
    ///
    /// **拨号在后台队列,结果回主线程落定**——与 applyGroupChange/probeServers
    /// 同一个已有模式,不阻塞主线程一秒。读不到就照既有逻辑说读不到
    /// (保留 `lastRules` 原样,可能仍是 nil),**不摆一个空列表**。
    ///
    /// **两个调用方,两种呈现,由 `forceShow` 区分**(与 `fetchServersOnDemand`
    /// 同一个形状):`true` 是用户点了「Routing Rules…」——弹出窗口,读不到就
    /// 明说读不到;`false` 是改完一条规则之后的重拉——就地重画、不抢焦点、
    /// 不弹 alert(窗口本来就开着,他正看着它)。
    ///
    /// **`rulesFetchInFlight` 的拦截判据不是裸的 `guard !rulesFetchInFlight`**:
    /// 那条写法会让一次环境重拉设的标志把紧跟着来的显式打开也拦住——点了菜单项、
    /// 窗口没出现、没有 alert,什么都没发生(2026-08-17 服务器窗口那边真的
    /// 这样坏过)。判据抽在 `shouldSuppressFetch`(`StatusWatch.swift`,已表驱动
    /// 测过四种组合):只拦 `forceShow: false` 那一路。
    private func fetchRulesOnDemand(forceShow: Bool) {
        guard !shouldSuppressFetch(inFlight: rulesFetchInFlight, explicit: forceShow) else { return }
        rulesFetchInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let fetched: RuleList?
            let fetchError: Error?
            do {
                fetched = try GuardianClient().listRules()
                fetchError = nil
            } catch {
                fetched = nil
                fetchError = error
                self?.logGuardianFetchFailure("rules", error)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                self.rulesFetchInFlight = false
                if let fetched {
                    self.lastRules = fetched
                }
                guard let rules = self.lastRules else {
                    guard forceShow else { return }
                    self.showGuardianFailure(
                        title: "Routing rules are not available",
                        message: self.fetchFailureAlertInfo(fetchError, what: "rules"),
                        error: fetchError)
                    return
                }
                self.presentRules(rules, forceShow: forceShow)
            }
        }
    }

    /// 部署表单。
    private lazy var deployWindow: DeployWindowController = {
        let controller = DeployWindowController()
        controller.onRun = { [weak self] target in
            self?.handOffDeployToTerminal(target)
        }
        return controller
    }()

    @objc private func openDeployWindow() {
        deployWindow.show()
    }

    /// 把部署命令交给 Terminal 去跑。
    ///
    /// **不在 app 里执行,是因为 bx 一行 SSH 凭据都不经手。** 菜单是 LSUIElement
    /// 应用,没有 TTY —— ssh 要问密码时无处可问;真在 app 里收密码,就等于把
    /// 「凭据全归系统 ssh」这条设计推翻。写一个临时脚本再 open 它,是**不需要
    /// 自动化权限**的那条路(AppleScript `do script` 会弹「BxMenu 想要控制
    /// 终端」),而且脚本内容与表单上显示的完全一致,用户可以自己打开看。
    private func handOffDeployToTerminal(_ target: DeployTarget) {
        let script = deployScriptText(target)
        let path = (NSTemporaryDirectory() as NSString).appendingPathComponent("bx-deploy.command")
        do {
            try script.write(toFile: path, atomically: true, encoding: .utf8)
            // 只有自己能读能执行:这个文件里有目标主机与登录名。
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: path)
        } catch {
            let alert = NSAlert()
            alert.messageText = "Could not start the installer"
            alert.informativeText = "\(error.localizedDescription)\n\n"
                + "You can run this yourself in Terminal:\n\(deployCommandLine(target))"
            NSApp.activate(ignoringOtherApps: true)
            alert.runModal()
            return
        }
        NSWorkspace.shared.open(URL(fileURLWithPath: path))
    }

    /// 服务器窗口。窗口而不是子菜单,理由同 rulesWindow。
    private lazy var serversWindow: ServersWindowController = {
        let controller = ServersWindowController()
        controller.onSwitch = { [weak self] name, host in
            self?.confirmAndSwitchServer(name: name, host: host)
        }
        controller.onCheckExitIP = { [weak self] in
            self?.checkExitIP()
        }
        controller.onProbe = { [weak self] in
            self?.probeServers()
        }
        controller.onDeploy = { [weak self] in
            self?.openDeployWindow()
        }
        controller.onAddServer = { [weak self] in
            self?.addServerFromWindow()
        }
        controller.onRemove = { [weak self] name, host in
            self?.confirmAndRemoveServer(name: name, host: host)
        }
        controller.onReplaceLink = { [weak self] name in
            self?.replaceServerLinkFromWindow(name: name)
        }
        return controller
    }()

    /// **服务器窗口唯一的渲染入口。**
    ///
    /// 六个渲染点此前各自拼一遍 `otherServerRows(list:core:)` +
    /// `serverListEmptyReason(list:)`,而漏掉 `core:` 那一半**不会有任何编译
    /// 错误**(它是可空的),后果只是当前那一块永远说「Core not answering」——
    /// 界面看起来完全正常。规则窗口那次就是漏了一个渲染点。收成一个漏斗之后,
    /// 「每个渲染点都带上了实时数据」这件事由构造保证,不由记性保证。
    private func presentServers(_ list: ServerList, forceShow: Bool) {
        let core = maintenanceReport?.core
        // 能力清单取自 maintenanceReport(上一次完整的 /v1/status),与
        // 「Servers…」那个菜单项同一份数据。**判据是 servers_edit,不是
        // servers** —— 见 serverEditingAvailable。
        let canEdit = serverEditingAvailable(capabilities: maintenanceReport?.capabilities)
        if forceShow {
            serversWindow.show(list: list, core: core, probe: exitIPProbe,
                               switchingTo: switchingTo, canEdit: canEdit)
        } else {
            serversWindow.refreshIfVisible(list: list, core: core, probe: exitIPProbe,
                                           switchingTo: switchingTo, canEdit: canEdit)
        }
    }

    /// 逐台量「从这台 Mac 直连过去多远」。
    ///
    /// **走的是 Core 的直连拨号器**(经 Guardian 转)。bx 开着的时候一个普通
    /// socket 会被 TUN 抓走、经**当前**那条隧道绕出去,量到的是
    /// 「你 → 当前服务器 → 目标」—— 那个数字对「该换哪一台」毫无意义,而且
    /// 它看起来完全正常。
    ///
    /// **只在用户点的时候发。** 探测走在隧道外面,会让网络上看得见这台机器
    /// 联系过那几个地址;绝不做后台定时探测(与 2026-08-13 否掉菜单栏那行常驻
    /// 红字同一条理由:被动观测优于主动探测)。
    private func probeServers() {
        guard !serversWindow.probing else { return }
        serversWindow.probing = true
        presentServers(lastServers ?? ServerList(), forceShow: false)
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().probeServers() }
            DispatchQueue.main.async {
                guard let self else { return }
                self.serversWindow.probing = false
                switch result {
                case .success(let list):
                    // 带着探测结论的完整清单直接顶替缓存 —— 界面据此重画,
                    // 不必自己把结论并回去(推演出来的状态与真实状态漂开,
                    // 正是这个仓库反复栽的形状)。
                    self.lastServers = list
                    self.presentServers(list, forceShow: false)
                case .failure(let error):
                    // **测不成不许把服务器画成红的。** 保持上一轮的清单原样,
                    // 只说这次没测成 —— 把「没问出来」画成「不可达」,等于把
                    // 一台好服务器说成坏的。
                    self.presentServers(self.lastServers ?? ServerList(), forceShow: false)
                    let alert = NSAlert()
                    alert.messageText = "Could not test the servers"
                    alert.informativeText = "\(error.localizedDescription)\n\n"
                        + "Testing needs bx to be running: it measures from outside the tunnel, "
                        + "which only bx itself can do."
                    NSApp.activate(ignoringOtherApps: true)
                    alert.runModal()
                }
            }
        }
    }

    @objc private func openServersWindow() {
        fetchServersOnDemand(forceShow: true)
    }

    /// 应用流量悬浮窗。**窗口是订阅的载体**:开着才拉、拉才采集,关掉之后没有
    /// 人再续期,Core 侧的订阅在一个 TTL(30 秒)内自己过期、采集停掉、缓冲清空。
    private lazy var appTrafficWindow: AppTrafficWindowController = {
        let controller = AppTrafficWindowController()
        controller.onClose = { [weak self] in
            self?.stopAppTrafficTimer()
        }
        controller.onAddRule = { [weak self] kind, pattern in
            self?.addRuleFromAppTraffic(kind: kind, pattern: pattern)
        }
        return controller
    }()

    /// Diagnostics 窗口(Checks 页 + Logs 页)。归档出口接回原来那条终端路;
    /// Checks 页的「Run again」接回同一条按需拉取,不另开一条路。
    private lazy var diagnosticsWindow: DiagnosticsWindowController = {
        let controller = DiagnosticsWindowController()
        controller.onExportDiagnostics = { [weak self] in
            self?.exportDiagnostics()
        }
        controller.onRunAgain = { [weak self] in
            self?.openDiagnosticsChecks()
        }
        controller.onLoadLogs = { [weak self] in
            self?.openDiagnosticsLogs(highlighting: nil)
        }
        return controller
    }()

    /// Diagnostics 的两条拉取各有一个在飞标志。与 `rulesFetchInFlight` 同一个模式:
    /// 两条路都只由用户显式触发(Open Logs / 弹窗里的 Show Details / Check for
    /// Problems / 两页上的 Run again 与 Load Logs),重叠只可能来自双击连点 ——
    /// 而重叠在这里比在规则那边更难看:两次成功会把窗口连开两遍,两次失败会连弹
    /// 两个「… are not available」。
    ///
    /// **它们曾经是同一个标志,那是错的。** 共用的理由写的是「两页顶来顶去用户
    /// 看不出发生了什么」,而代价是:checks 那次拉取最长 20 秒,这段时间里点
    /// 「Open Logs」被这个标志**静默**吞掉 —— 窗口不出现、没有 alert、什么都没
    /// 发生。那正是 `shouldSuppressFetch` 那次回归的形状,而这里两条**都是显式
    /// 动作**,没有「环境刷新」那一路可以让位。两次落定的先后至多让用户多切一次
    /// 标签页;吞掉一次点击则让他以为菜单坏了。代价不对称,取吵的那边。
    private var checksFetchInFlight = false
    private var logsFetchInFlight = false

    /// 把两页的能力告诉窗口。**在每次开窗之前调** —— 没被请求的那一页停在占位
    /// 上,而占位要说的是「还没拉」还是「这一版没有这个功能」,只有能力门知道。
    ///
    /// 判据取的是与 `runDoctorFromMenu` / `openLogs` 那两道门**同一个表达式**:
    /// 一边按能力把入口指向 Checks 页、另一边在页上画一个按不动的按钮,是同一个
    /// 判据算两遍就够漂开一次的老形状。
    private func applyDiagnosticsAvailability() {
        diagnosticsWindow.setAvailability(
            doctor: doctorAvailable(capabilities: maintenanceReport?.capabilities),
            logs: logsAvailable(capabilities: maintenanceReport?.capabilities))
    }

    /// 拉一次 /v1/logs 再开窗口;拉不到就明说(这条路本身就是「看失败原因」的路,
    /// 它自己失败时不能再指向别的什么)。
    private func openDiagnosticsLogs(highlighting code: String?) {
        guard !logsFetchInFlight else { return }
        logsFetchInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().fetchLogs() }
            DispatchQueue.main.async {
                guard let self else { return }
                self.logsFetchInFlight = false
                switch result {
                case .success(let report):
                    self.applyDiagnosticsAvailability()
                    self.diagnosticsWindow.showLogs(report, highlightingCode: code)
                case .failure(let error):
                    self.showMessage("Logs are not available", "bx could not read its logs: \(error.localizedDescription)")
                }
            }
        }
    }

    /// 拉一次 /v1/doctor 再开 Checks 页。**它让 Guardian 出网探测一次服务器**,所以只
    /// 由用户点击触发(菜单项与 Run again),绝不放进任何定时器或刷新路径。
    private func openDiagnosticsChecks() {
        guard !checksFetchInFlight else { return }
        checksFetchInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().fetchDoctor() }
            DispatchQueue.main.async {
                guard let self else { return }
                self.checksFetchInFlight = false
                switch result {
                case .success(let report):
                    self.applyDiagnosticsAvailability()
                    self.diagnosticsWindow.showChecks(report)
                case .failure(let error):
                    self.showMessage("Checks are not available", "bx could not run its checks: \(error.localizedDescription)")
                }
            }
        }
    }

    /// 失败弹窗的唯一出口。**完整原因由 Guardian 经 /v1/logs 发布**,弹窗只带失败码
    /// 与一个「Show Details」;旧 Guardian(没声明 logs)只说失败码。
    /// 措辞纪律:只说发生了什么与下一步,不断言原因。
    ///
    /// 一行转给 `message:error:` 那个真正的漏斗——说明文案就是 error 自己的
    /// localizedDescription,失败码同样从 error 取。
    private func showGuardianFailure(title: String, error: Error) {
        showGuardianFailure(title: title, message: error.localizedDescription, error: error)
    }

    /// 与上面同一个漏斗,只是说明文案由调用方先经 guardianFetchFailureInfo(纯函数)
    /// 算好——`fetchRulesOnDemand`/`fetchServersOnDemand` 的措辞取决于 HTTP 状态码
    /// 而不只是 error 的 localizedDescription,但按下「Show Details」时打开的仍是
    /// 同一个日志页,失败码仍从 error 取(不是从文案里解析)。`error` 可选是因为
    /// `fetchRulesOnDemand`/`fetchServersOnDemand` 在没有任何缓存可用时,失败原因
    /// 可能压根没有一个 Error 对象(fetchError 为 nil 也要能弹窗)。
    private func showGuardianFailure(title: String, message: String, error: Error?) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: "OK")
        let canShow = logsAvailable(capabilities: maintenanceReport?.capabilities)
        if canShow {
            alert.addButton(withTitle: "Show Details")
        }
        NSApp.activate(ignoringOtherApps: true)
        let response = alert.runModal()
        if canShow, response == .alertSecondButtonReturn {
            openDiagnosticsLogs(highlighting: error.flatMap { guardianFailureCode(of: $0) })
        }
    }

    /// 有一次按需拉应用流量正在飞。与 `serversFetchInFlight` 同一个模式,而且
    /// 与它一样**不是可选的**:窗口开着时既有心跳定时器、又有环境刷新会触发,
    /// 而 watch 时代刷新是事件驱动、可能连着来。
    private var appTrafficFetchInFlight = false

    /// 窗口开着时的心跳。**只在窗口真的开出来之后才存在**(见
    /// fetchAppTrafficOnDemand 的成功分支与 openAppTrafficWindow 的注释)。
    private var appTrafficTimer: Timer?

    /// 连着多少次没拉到。**成功一次就清零。** 到门槛之后在窗口上盖一句「这不是
    /// 此刻的事实」—— 静默冻在上一份快照上,是这个界面最坏的失效模式:那份快照
    /// 读起来是「这些应用此刻正在走隧道」,而此刻保护可能已经被关掉了。
    private var appTrafficConsecutiveFailures = 0

    @objc private func openAppTrafficWindow() {
        // 显式那一路:弹出窗口,读不到就明说。
        //
        // **心跳不在这里起。** 它只能在窗口**真的开出来**之后起(见
        // fetchAppTrafficOnDemand 的成功分支):窗口是靠 show(report:) 才被创建的,
        // 首拉失败时根本没有窗口 —— 于是 windowWillClose 永不触发、onClose 永不
        // 被调用,而心跳会永远跑下去。那不是理论上的角落:菜单项在 .off 状态下
        // 照样在场(能力来自 Guardian 的静态清单,与 Core 死活无关),保护关着时
        // 点一下就正好走到这条路,后果是每分钟十几次失败拨号 + 十几行
        // guardian_apps_fetch_failed,永久,而界面上一点痕迹都没有。
        fetchAppTrafficOnDemand(forceShow: true)
    }

    /// 窗口开着时每隔 `appTrafficRefreshSeconds` 拉一次。
    ///
    /// **它同时是订阅的心跳**:Core 侧采集订阅靠每一次拉取续期(30 秒 TTL),
    /// 间隔必须明显短于 TTL,否则订阅会在两次刷新之间过期 —— 窗口开着而界面
    /// 反复跳回「Not collecting app traffic right now.」,且每次续上都从零计数
    /// (那条余量由 Swift 套件钉住)。
    ///
    /// 单靠 `applyRefresh` 那一路不够:watch 时代稳态下状态可以很久不变,而
    /// 兜底轮询是 60 秒 —— 比 TTL 还长,订阅会在两拍之间断掉。
    ///
    /// **必须是 commonModeTimer**:`Timer.scheduledTimer` 只进 `.default`,而
    /// 菜单展开期间主 runloop 在 `NSEventTrackingRunLoopMode`,实测一次都不触发。
    private func startAppTrafficTimer() {
        guard appTrafficTimer == nil else { return }
        appTrafficTimer = commonModeTimer(every: appTrafficRefreshSeconds) { [weak self] in
            self?.fetchAppTrafficOnDemand(forceShow: false)
        }
    }

    /// 停掉心跳。**窗口一关就必须调它** —— 窗口关了而定时器还在跑,订阅就永远
    /// 续着,采集也就永远开着,而界面上看不出任何异常。
    private func stopAppTrafficTimer() {
        appTrafficTimer?.invalidate()
        appTrafficTimer = nil
    }

    /// 按需拉一次应用流量报告。
    ///
    /// **两个调用方,两种呈现,由 `forceShow` 区分**(与 fetchServersOnDemand
    /// 同一个形状):`true` 是用户点了菜单项 —— `show()` 弹出窗口,读不到就用
    /// `NSAlert` 明说;`false` 是心跳与环境刷新 —— `refreshIfVisible` 就地重画,
    /// 不抢焦点、不弹 alert(每 3 秒 `NSApp.activate` 一次会让这台 Mac 没法用)。
    ///
    /// **拦截判据不是裸的 `guard !appTrafficFetchInFlight`**:那条写法会让心跳
    /// 设的标志把紧跟着来的显式打开也拦住 —— 用户点了菜单项,窗口没出现、没有
    /// alert,什么都没发生。这在服务器窗口上是一次真实的回归,判据抽在
    /// `shouldSuppressFetch`(`StatusWatch.swift`,已表驱动测过四种组合):
    /// 只拦 `forceShow: false` 那一路,显式那一路仍然**设置**这个标志、但永远
    /// 不会**被它拦**。
    private func fetchAppTrafficOnDemand(forceShow: Bool) {
        guard !shouldSuppressFetch(inFlight: appTrafficFetchInFlight, explicit: forceShow) else { return }
        appTrafficFetchInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let outcome = Result { try GuardianClient().appTraffic() }
            DispatchQueue.main.async {
                guard let self else { return }
                self.appTrafficFetchInFlight = false
                guard case .success(let fetched) = outcome else {
                    self.appTrafficConsecutiveFailures += 1
                    // **心跳只能活在一个真的开着的窗口旁边。** 首拉失败时窗口
                    // 从来没被创建过,onClose 永远不会来停它 —— 这里必须自己停,
                    // 否则它每 5 秒拨一次、永久,而界面上一点痕迹都没有。
                    if !self.appTrafficWindow.isVisible {
                        self.stopAppTrafficTimer()
                    }
                    // 窗口还开着:不清空那几行,只盖一句「别再把它当成现在」。
                    if let notice = appTrafficStaleNotice(
                        consecutiveFailures: self.appTrafficConsecutiveFailures) {
                        self.appTrafficWindow.markStaleIfVisible(notice)
                    }
                    // **读不到就说读不到,不摆一个空报告** —— 一份 subscribed:false
                    // 的假报告会把「没问出来」显示成「没在采集」,而那是两件事。
                    guard forceShow else { return }
                    if case .failure(let error) = outcome {
                        self.showGuardianFailure(title: "App traffic is not available", error: error)
                    }
                    return
                }
                self.appTrafficConsecutiveFailures = 0
                // 能力清单取自 maintenanceReport(上一次完整的 /v1/status),与
                // 「Routing Rules…」那个菜单项同一个判据、同一份数据。
                self.appTrafficWindow.ruleEditingAvailable =
                    rulesEditingAvailable(capabilities: self.maintenanceReport?.capabilities)
                if forceShow {
                    self.appTrafficWindow.show(report: fetched)
                    // **心跳在这里起,不在菜单点击处起** —— show 是窗口唯一的
                    // 创建点,起在它之后才保证「有心跳 ⇒ 有窗口 ⇒ 关窗口能停掉它」。
                    self.startAppTrafficTimer()
                } else {
                    self.appTrafficWindow.refreshIfVisible(report: fetched)
                }
            }
        }
    }

    /// 按需拉一次服务器清单。理由与 fetchRulesOnDemand 相同——图标不依赖它,
    /// 每次拨都让 Guardian 多读并解析一遍 config.yaml。
    ///
    /// 拨号在后台队列,结果回主线程落定。读不到就保留 `lastServers` 原样
    /// (可能仍是 nil,照既有逻辑说读不到),不摆一个空列表。
    ///
    /// **两个调用方,两种呈现,由 `forceShow` 区分**:
    /// - `forceShow: true`(用户点了「Servers…」)—— 用 `show()` 弹出/前置窗口,
    ///   读不到就用 `NSAlert` 明说读不到。
    /// - `forceShow: false`(`applyRefresh` 在服务器窗口**已经开着**时按需刷一次)
    ///   —— 用 `refreshIfVisible` 就地重画,**不抢焦点、不弹 NSAlert**:每次环境
    ///   刷新都 `NSApp.activate` 会把窗口推到用户面前、把弹一次 alert 变成弹
    ///   很多次,而这条路径本就只在窗口已经可见时才会被触发。
    ///
    /// **`serversFetchInFlight` 守卫是必需的,不是可选的**:窗口开着时它跟着
    /// 每一次刷新触发,而 watch 时代刷新是事件驱动、可能连着来的——没有这个
    /// 守卫,上一次还没回来、下一次又拨了一次的重叠会真的发生。
    ///
    /// **但拦截判据不是裸的 `guard !serversFetchInFlight`**——那条曾经的写法
    /// 让环境刷新设的标志把紧跟着来的显式打开也拦住(点了「Servers…」,窗口
    /// 没出现、没有 alert,什么都没发生,是一次真实的回归)。判据抽在
    /// `shouldSuppressFetch`(`StatusWatch.swift`,已表驱动测过四种组合):
    /// 只拦 `forceShow: false` 那一路,`forceShow: true` 永不被这个标志拦——
    /// 显式动作即便撞上一次仍在飞的环境刷新也会照常继续(代价至多是一次多余的
    /// 本机 socket 往返,判定无害)。显式那一路仍然会**设置**这个标志(见下方
    /// `serversFetchInFlight = true`),只是不会**被它拦**——这样它自己发起的
    /// 取数也能防住后续环境刷新的重叠。
    private func fetchServersOnDemand(forceShow: Bool) {
        guard !shouldSuppressFetch(inFlight: serversFetchInFlight, explicit: forceShow) else { return }
        serversFetchInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let fetched: ServerList?
            let fetchError: Error?
            do {
                fetched = try GuardianClient().listServers()
                fetchError = nil
            } catch {
                fetched = nil
                fetchError = error
                self?.logGuardianFetchFailure("servers", error)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                self.serversFetchInFlight = false
                if let fetched {
                    self.lastServers = fetched
                }
                guard let servers = self.lastServers else {
                    guard forceShow else { return }
                    self.showGuardianFailure(
                        title: "Servers are not available",
                        message: self.fetchFailureAlertInfo(fetchError, what: "servers"),
                        error: fetchError)
                    return
                }
                self.presentServers(servers, forceShow: forceShow)
            }
        }
    }

    /// 换服务器 —— **先确认再换**。
    ///
    /// 换出口是有后果的事(正在登录的会话、风控、正在下载的东西),这正是项目
    /// 所有者拒绝自动容灾的理由:自动切会在用户不知情时换掉出口 IP。既然选择由
    /// 人来做,那就必须让人先看见后果。
    private func confirmAndSwitchServer(name: String, host: String) {
        // **一次只许有一次切换在飞。** 服务端也会拒(409 servers_switch_busy),
        // 这里挡一道只是为了不让用户撞上一个他看不懂的失败:窗口在那二十几秒里
        // 一直开着,再点一下是很自然的动作。**而窗口如今也会说出来** ——
        // 那一行写着 Switching…、每个 Use 都灰着,所以这道守卫不再表现为
        // 「点了没反应」。
        guard switchingTo == nil else { return }
        let alert = NSAlert()
        alert.messageText = "Switch server?"
        alert.informativeText = serverSwitchConfirmMessage(name: name, host: host)
        alert.addButton(withTitle: "Switch")
        alert.addButton(withTitle: "Cancel")
        NSApp.activate(ignoringOtherApps: true)
        guard alert.runModal() == .alertFirstButtonReturn else { return }

        switchServer(name: name) { outcome in
            guard let outcome else { return }
            let done = NSAlert()
            // **热切没成功时不许说「已切换」** —— 判据在 ServersModel 的
            // 纯函数里,由 Swift 套件钉住。
            done.messageText = outcome.applied ? "Switched" : "Saved, but not applied yet"
            done.informativeText = switchOutcomeMessage(outcome)
            NSApp.activate(ignoringOtherApps: true)
            done.runModal()
        }
    }

    /// 无确认框的切换(确认在 confirmAndSwitchServer;Add Server 那条路的确认是
    /// 它自己的第一步)。**切换逻辑只有这一份** —— 第二份拷贝会让两条路上的
    /// in-flight 守卫、失败漏斗、刷新时机各自漂开。
    /// completion 收到 nil 有**两种**情形,措辞必须都说到:请求真的失败了(已经弹过
    /// 失败漏斗),**或者**被在飞守卫挡下、压根没发出去(什么都没弹)。原话只说了
    /// 前一种,于是 `confirmAndSwitchServer` 那边照着它把 nil 读成「用户已经看到
    /// 原因了」而什么都不做 —— 撞上在飞守卫时就是点了没反应。
    private func switchServer(name: String, completion: ((ServerSwitchResult?) -> Void)? = nil) {
        guard switchingTo == nil else { completion?(nil); return }
        // 换过去之后旧的探测结果就作废了 —— 留着它会让用户读到上一台的出口。
        exitIPProbe = .unknown
        switchingTo = name
        // **立刻重画一次。** 这一整条改动的理由就在这里:此前窗口拿不到这个
        // 状态,于是确认之后二十几秒屏幕上什么都不发生。
        presentServers(lastServers ?? ServerList(), forceShow: false)
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().switchServer(name: name) }
            DispatchQueue.main.async {
                guard let self else { return }
                self.switchingTo = nil
                switch result {
                case .success(let outcome):
                    completion?(outcome)
                case .failure(let error):
                    self.showGuardianFailure(title: "Could not switch server", error: error)
                    completion?(nil)
                }
                // 用户刚做完动作,这一次刷新不许被丢掉。
                self.refresh(userInitiated: true)
            }
        }
    }

    /// Servers 窗口的「Add Server…」:贴链接 → 起名(可空)→ Guardian add(同名 409)→ 用
    /// 应答里的 added 热切换 → 一句结果。**全程不提权、不开终端**:两步都是 owner 门
    /// 的 Guardian 端点(spec §4)。旧的那台留在清单里,随时能 Use 回去。
    private func addServerFromWindow() {
        guard let links = promptForServerLinks(
            title: "Add Server",
            hint: "Paste the bx link for the new server. It will be added to your list and used right away.",
            confirmTitle: "Add and Switch",
            udpHint: udpFieldHint(replacing: false)
        ) else { return }
        let name = promptForServerName()
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try GuardianClient().addServer(name: name, link: links.link, udp: links.udp)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    self.lastServers = list
                    // **切到 Guardian 说的那个名字,不是用户输入的那个** —— 名字留空时
                    // 最终名字是服务端按链接推的,客户端再推一遍就是第二份判据。
                    //
                    // `added` 缺席只有一种来源:一版**不发这个字段**的旧 Guardian。
                    // 那时退回**客户端刚刚发出去的那个名字** —— 它不是第二次推导
                    // (没有再解析一次链接),只是这次请求自己的输入。少了这条退路,
                    // 切换会拿一个空名字去发,而弹窗写的是「Added , but…」——
                    // 一句没写完的话。
                    let target = list.added.isEmpty ? name : list.added
                    self.switchServer(name: target) { outcome in
                        let alert = NSAlert()
                        alert.messageText = outcome?.applied == true ? "Switched" : "Added"
                        alert.informativeText = addServerOutcomeMessage(added: target, switched: outcome)
                        NSApp.activate(ignoringOtherApps: true)
                        alert.runModal()
                    }
                case .failure(let error):
                    // **失败码不是一句话。** `Guardian request failed (409,
                    // code=servers_name_exists).` 说的是协议,不是用户能做的事;
                    // 而这条路上最常见的两种失败(名字撞车、名字带空格)恰恰都是
                    // 用户改一下就能过的。措辞由纯函数给,认不出的码返回 nil ——
                    // 那时仍走原来那个通用漏斗,绝不编一句像模像样的解释。
                    if case GuardianClientError.status(let status, let code) = error,
                        let sentence = addServerFailureMessage(code: code, status: status)
                    {
                        self.showGuardianFailure(
                            title: "Could not add that server", message: sentence, error: error)
                    } else {
                        self.showGuardianFailure(title: "Could not add that server", error: error)
                    }
                }
            }
        }
    }

    /// `⋯` 里的删除。**先弹确认,而这与规则窗口刻意相反。**
    ///
    /// 规则删除不弹确认、只留 Undo(为十一条冗余点十一次确认是在惩罚正确的
    /// 行为);这里两条理由都不成立:链接是凭据、`/v1/servers` 刻意从不发它,
    /// 所以菜单**在构造上做不到 Undo** —— 一个撤不回的 Undo 比没有 Undo 更糟;
    /// 而服务器很少、删除很罕见,不存在「要点十一次」那种惩罚(spec §7.1)。
    ///
    /// 文案由 `serverRemoveConfirmMessage` 给(它要说清链接会跟着没)。
    private func confirmAndRemoveServer(name: String, host: String) {
        let alert = NSAlert()
        alert.alertStyle = .warning
        alert.messageText = "Remove server?"
        alert.informativeText = serverRemoveConfirmMessage(name: name, host: host)
        alert.addButton(withTitle: "Remove")
        alert.addButton(withTitle: "Cancel")
        NSApp.activate(ignoringOtherApps: true)
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().removeServer(name: name) }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    // 服务端返回改动**之后**的完整清单,界面据此重画 —— 不自己
                    // 从旧清单里减一行(推演出来的状态与盘上真实的状态漂开,
                    // 正是这个仓库反复栽的形状)。
                    self.lastServers = list
                    self.presentServers(list, forceShow: false)
                case .failure(let error):
                    self.showServerEditFailure(title: "Could not remove that server", error: error)
                }
            }
        }
    }

    /// `⋯` 里的换链接:凭据轮换,或者 VPS 重建换了地址(spec §7.2)。
    ///
    /// **它不动出口。** 换的是当前那台时,配置改了而跑着的隧道还连着旧地址 ——
    /// 如实说「已写入,重连后生效」并给一条现在就重连的路,**但绝不替他重连**
    /// (与规则热生效那条收尾同一条:重连会断掉正在跑的连接)。
    private func replaceServerLinkFromWindow(name: String) {
        guard let links = promptForServerLinks(
            title: "Replace Link",
            hint: "Paste the new bx link for \(name). Nothing else about this server changes, "
                + "and your exit stays where it is.",
            confirmTitle: "Replace",
            udpHint: udpFieldHint(replacing: true)
        ) else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try GuardianClient().replaceServerLink(name: name, link: links.link, udp: links.udp)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    self.lastServers = list
                    self.presentServers(list, forceShow: false)
                    // **「是不是当前那台」取自服务端刚返回的这份清单**,不是窗口
                    // 传来的一个可能已经陈旧的标志:replace 不动 current,所以
                    // 这份应答就是此刻的真相。
                    let isCurrent = list.servers.contains { $0.current && $0.name == name }
                    self.followUpAfterLinkReplaced(replaceLinkFollowUp(name: name, isCurrent: isCurrent))
                case .failure(let error):
                    self.showServerEditFailure(title: "Could not replace that link", error: error)
                }
            }
        }
    }

    /// 换完链接之后那一句,以及给不给「现在就重连」。**判据在纯函数里**
    /// (`replaceLinkFollowUp`),这里只摆。
    private func followUpAfterLinkReplaced(_ follow: ReplaceLinkFollowUp) {
        let alert = NSAlert()
        alert.messageText = "Link replaced"
        alert.informativeText = follow.message
        guard follow.offersReconnect else {
            alert.addButton(withTitle: "OK")
            NSApp.activate(ignoringOtherApps: true)
            alert.runModal()
            return
        }
        alert.addButton(withTitle: "Reconnect Now")
        alert.addButton(withTitle: "Later")
        NSApp.activate(ignoringOtherApps: true)
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        reconnectBx()
    }

    /// remove / replace 的失败漏斗。**认得出的码给一句用户做得了的话,认不出的
    /// 退回通用漏斗** —— 绝不编一句像模像样的解释(与 addServerFailureMessage
    /// 同一条不对称)。
    private func showServerEditFailure(title: String, error: Error) {
        if case GuardianClientError.status(let status, let code) = error,
            let sentence = serverEditFailureMessage(code: code, status: status)
        {
            showGuardianFailure(title: title, message: sentence, error: error)
            return
        }
        showGuardianFailure(title: title, error: error)
    }

    /// 一条主链接 + 一条**可选**的 UDP 链接。
    ///
    /// UDP 那个框不是锦上添花:`bx server install` 默认就给两条链接,而此前这个
    /// 表单只有一个框 —— 从菜单加一台 reality+hysteria2 的 VPS 会**静默丢掉
    /// QUIC 那半**,而界面上什么都看不出来。
    ///
    /// 「留空」是什么意思由 `udpFieldHint(replacing:)` 说,两条路不一样:add 是
    /// 「这台没有 UDP 链接」,replace 是「保持它原来那条」—— 菜单今天清不掉一条
    /// UDP 链接,把它写成「留空 = 删掉」就是一句后果静默的假话。
    private func promptForServerLinks(
        title: String, hint: String, confirmTitle: String, udpHint: String
    ) -> (link: String, udp: String)? {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = hint
        alert.addButton(withTitle: confirmTitle)
        alert.addButton(withTitle: "Cancel")

        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 420, height: 24))
        field.placeholderString = "bx://..."
        // **预填剪贴板。** 用户十有八九刚从聊天窗口复制过来;这是各家客户端的
        // 默认行为,没有它会被当成缺陷。预填而不是直接用 —— 他要看得见自己在装什么。
        if let candidate = clipboardCandidateLink(NSPasteboard.general.string(forType: .string)) {
            field.stringValue = candidate
        }
        let udpField = NSTextField(frame: NSRect(x: 0, y: 0, width: 420, height: 24))
        udpField.placeholderString = "UDP link (optional)"
        let udpNote = NSTextField(labelWithString: udpHint)
        udpNote.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        udpNote.textColor = .secondaryLabelColor
        udpNote.lineBreakMode = .byWordWrapping
        udpNote.preferredMaxLayoutWidth = 420

        let box = NSStackView(views: [field, udpField, udpNote])
        box.orientation = .vertical
        box.alignment = .leading
        box.spacing = 6
        box.frame = NSRect(x: 0, y: 0, width: 420, height: 92)
        alert.accessoryView = box
        NSApp.activate(ignoringOtherApps: true)

        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        let link = field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        let udp = udpField.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !link.isEmpty else {
            showMessage("No Link", "Paste a bx link to continue.")
            return nil
        }
        guard looksLikeClientLink(link) else {
            showMessage("Link Not Recognized", "Paste a bx link to continue.")
            return nil
        }
        // **UDP 那条也要过同一道校验。** 空是合法的(它是可选的);填了却不是
        // 一条 bx 链接,就在这里说,而不是让服务端回一句关于 base64 的错误。
        guard udp.isEmpty || looksLikeClientLink(udp) else {
            showMessage("UDP Link Not Recognized", "Paste a bx link, or leave the second box empty.")
            return nil
        }
        return (link, udp)
    }

    /// 名字可空:空就让 Guardian 按链接推导。
    private func promptForServerName() -> String {
        let alert = NSAlert()
        alert.messageText = "Name this server"
        alert.informativeText = "Leave it empty to name it after the server's address."
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 260, height: 24))
        field.placeholderString = "e.g. tokyo"
        alert.accessoryView = field
        alert.addButton(withTitle: "Continue")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
        return field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// 「现在从哪出去」。
    ///
    /// **这一次请求由菜单自己发,不是 Guardian 发。** 菜单以普通用户身份跑,它的
    /// 流量和浏览器走同一条路 —— 那才是「网站看到的是什么」这个问题的忠实答案;
    /// 而让一个 root 守护进程多长一条对外请求的能力,换不来更准的结果。
    ///
    /// 端点用 icanhazip:它是这个仓库为泄漏检测选定的那一个,已经过「不在 china
    /// 直连列表里」那道守卫 —— 用一个在列表里的域名(ipify 就栽过)会让探测走直连,
    /// 报出用户真实的 ISP 出口,方向正好相反。
    private func checkExitIP() {
        guard exitIPProbe != .checking else { return }
        exitIPProbe = .checking
        presentServers(lastServers ?? ServerList(), forceShow: false)

        var request = URLRequest(url: URL(string: "https://ipv4.icanhazip.com")!)
        request.timeoutInterval = 10
        // 不要缓存:用户点它就是要一个**现在**的答案。
        request.cachePolicy = .reloadIgnoringLocalAndRemoteCacheData
        URLSession.shared.dataTask(with: request) { [weak self] data, _, _ in
            let parsed = data.flatMap { parseExitIPResponse(String(decoding: $0, as: UTF8.self)) }
            DispatchQueue.main.async {
                guard let self else { return }
                // 解不出来就是「没问出来」,**绝不编一个地址**。
                self.exitIPProbe = parsed.map { ExitIPProbe.address($0) } ?? .failed
                self.presentServers(self.lastServers ?? ServerList(), forceShow: false)
            }
        }.resume()
    }

    /// 整组打开或关掉。
    private func applyGroupChange(group: String, enable: Bool) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try GuardianClient().changeRuleGroup(name: group, enable: enable)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    self.lastRules = list
                    self.presentRules(list, forceShow: false)
                    self.followUpAfterRuleChange(title: enable ? "Turned on \(group)" : "Turned off \(group)", list: list)
                case .failure(let error):
                    self.showGuardianFailure(title: "Could not change that group", error: error)
                }
            }
        }
    }

    /// 从「按应用看分流」窗口的右键菜单加一条规则。与 applyGroupChange 同一条路:
    /// 后台拨 Guardian,成功就顶替 lastRules、刷新规则窗口、按服务端的答案决定
    /// 说「已生效」还是「要重连」;失败走 showGuardianFailure —— 这一版 Guardian
    /// 发布日志时弹窗带「Show Details」直接打开那份日志,旧版才只说失败码。
    private func addRuleFromAppTraffic(kind: String, pattern: String) {
        guard let ruleKind = RuleKind(rawValue: kind) else { return }
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try GuardianClient().changeRule(action: "add", kind: ruleKind, pattern: pattern)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    self.lastRules = list
                    self.presentRules(list, forceShow: false)
                    let verb = ruleKind == .direct ? "direct" : "through the tunnel"
                    self.followUpAfterRuleChange(title: "\(pattern) will always go \(verb)", list: list)
                case .failure(let error):
                    self.showGuardianFailure(title: "Could not add that rule", error: error)
                }
            }
        }
    }

    /// 删一条规则。**不弹确认框**:方向都是更安全的那一边(删一条 direct 规则 =
    /// 那些流量回到隧道),而要清掉十一条冗余规则就得点十一次确认,那是在惩罚
    /// 正确的行为。但删完那一行**不消失**,原地留一句 Removed · Undo ——
    /// 一次误点不该静默且不可逆地毁掉一条手写规则。
    ///
    /// 成功之后刻意**不重拉**:`changeRule` 的应答本身就是那份新表,再问一遍
    /// 只是一次多余的往返。那一行由 `markRemoved` 记进窗口的「等着撤销」里,
    /// **之后每一次重画都会把它插回原位**(`ruleTableEntries`)—— 这一点是
    /// 承重的:这个窗口跟着环境刷新每 2 秒重画一次,靠改装某一行的撤销口活不过
    /// 下一拍,而删除刻意不弹确认框、Undo 正是那个确认框的替身。
    ///
    /// **在飞守卫**(与 `probing`/`switchingTo`/`rulesFetchInFlight` 同一个模式):
    /// `Remove` 是个 small 按钮,双击一下就发两次删除,而第二次撞上 Guardian
    /// 「删不存在的规则要如实报错」那条契约 —— 用户会为一条**确实删成功了**的
    /// 规则收到一句「Could not remove that rule」。这条路上没有环境触发,
    /// 两次都是显式动作,所以拦住后一次就是对的,不存在 `shouldSuppressFetch`
    /// 那个不对称。
    private func removeRuleFromWindow(_ kind: RuleKind, _ pattern: String) {
        // 只是本地去重的键,不是跨进程协议 —— 与窗口里那个 identifier 各写各的。
        let inFlightKey = "\(kind.rawValue)|\(pattern)"
        guard !ruleRemovalsInFlight.contains(inFlightKey) else { return }
        ruleRemovalsInFlight.insert(inFlightKey)
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try GuardianClient().changeRule(action: "remove", kind: kind, pattern: pattern)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                self.ruleRemovalsInFlight.remove(inFlightKey)
                switch result {
                case .success(let list):
                    self.lastRules = list
                    self.rulesWindow.markRemoved(kind: kind, pattern: pattern)
                case .failure(let error):
                    self.showGuardianFailure(title: "Could not remove that rule", error: error)
                }
            }
        }
    }

    /// Undo:把刚删掉的那一条原样加回去。**带 force** —— 它本来就在配置里,
    /// 风险门拦的是「新开一个洞」,不该拦一次撤销(拦了用户就再也放不回去了)。
    private func addRuleBack(_ kind: RuleKind, _ pattern: String) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try GuardianClient().changeRule(
                    action: "add", kind: kind, pattern: pattern, force: true)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                if case .failure(let error) = result {
                    self.showGuardianFailure(title: "Could not restore that rule", error: error)
                }
                // 成功也好失败也好,窗口上那一行现在写着 Removed —— 而它已经不是
                // 事实了。重拉一次让表回到真相;走 forceShow: false 是因为窗口
                // 就开在用户面前,这一拉不该抢焦点、也不该弹 alert。
                self.fetchRulesOnDemand(forceShow: false)
            }
        }
    }

    /// Add Rule…:窗口底部那个按钮。
    ///
    /// **输入框与方向选择只建一次**(就在这里),之后每一轮都把**同一组视图**
    /// 重新挂到一个新的 NSAlert 上 —— 被风险门拒绝时用户敲的那串因此还原样
    /// 留在框里,他要做的是把它改窄,不是重打一遍。文本活下来靠的是这个,
    /// 与拨号同步还是异步无关(拨号在后台队列,与本文件其余每一处一样)。
    private func addRuleFromWindow() {
        // 手摆 frame(与本文件另外两处 accessoryView 同一个写法,不用
        // NSStackView):NSAlert 按 accessoryView 的 frame 定尺寸,而一个交给
        // 自动布局的容器给不出这个 frame —— 那时输入框会缩成一条看不见的缝。
        // NSView 的原点在左下,所以输入框在上、方向选择在下。
        let field = NSTextField(frame: NSRect(x: 0, y: 32, width: 300, height: 24))
        field.placeholderString = "*.example.com"
        let picker = NSSegmentedControl(
            labels: ["Direct", "Through tunnel"], trackingMode: .selectOne, target: nil, action: nil)
        picker.translatesAutoresizingMaskIntoConstraints = true
        picker.sizeToFit()
        picker.setFrameOrigin(NSPoint(x: 0, y: 0))
        picker.selectedSegment = 0
        let accessory = NSView(frame: NSRect(x: 0, y: 0, width: 300, height: 56))
        accessory.addSubview(field)
        accessory.addSubview(picker)
        askForNewRule(
            accessory: accessory, field: field, picker: picker,
            note: "bx will always send this domain the way you pick.", refused: nil)
    }

    /// 摆一次表单,返回用户按了哪个按钮。**只做呈现,不含任何判断** ——
    /// 判断全在 `askForNewRule` 里,这样「摆」这件事没有第二份。
    private func presentAddRuleAlert(
        accessory: NSView, field: NSTextField, note: String, offerForce: Bool
    ) -> NSApplication.ModalResponse {
        let alert = NSAlert()
        alert.messageText = "Add a Routing Rule"
        alert.informativeText = note
        alert.accessoryView = accessory
        alert.addButton(withTitle: "Add")
        alert.addButton(withTitle: "Cancel")
        if offerForce {
            alert.addButton(withTitle: "Add Anyway")
        }
        NSApp.activate(ignoringOtherApps: true)
        alert.window.initialFirstResponder = field
        return alert.runModal()
    }

    /// 一轮:摆表单 → 校验 → 后台提交 → 结果回主线程。
    ///
    /// **409 `rules_risky_direct` 那一支不收摊**:把**同一组视图**再摆一次
    /// (输入还在),换上风险说明,并多给一个「Add Anyway」。那段说明写在
    /// Swift 侧,是因为 Guardian 的响应体按纪律只带失败码、不带原文
    /// (`policy.DirectRuleHazard` 的 reason/suggestion 到不了这里),两边同义即可。
    ///
    /// - Parameter refused: 上一轮被风险门拒掉的那个**归一化模式**;nil = 还没
    ///   被拒过。它同时是「给不给 Add Anyway」和「这次带不带 force」的依据 ——
    ///   **一把钥匙只开它自己那道门**:用户在 `*.amazonaws.com` 被拒之后改敲
    ///   另一个危险模式再按 Add Anyway,那个新模式从没被判过,必须让它也过一遍门。
    private func askForNewRule(
        accessory: NSView, field: NSTextField, picker: NSSegmentedControl,
        note: String, refused: String?
    ) {
        let response = presentAddRuleAlert(
            accessory: accessory, field: field, note: note, offerForce: refused != nil)
        let pressedAddAnyway = refused != nil && response == .alertThirdButtonReturn
        guard response == .alertFirstButtonReturn || pressedAddAnyway else { return }
        // 客户端这一份校验不替代服务端那一份,它存在只是为了在用户敲完的当下
        // 就说话 —— 有话说就把话说出来、把表单原样再摆一次,不提交。
        if let problem = validateRulePattern(field.stringValue) {
            askForNewRule(
                accessory: accessory, field: field, picker: picker,
                note: problem, refused: refused)
            return
        }
        let kind: RuleKind = picker.selectedSegment == 0 ? .direct : .proxy
        let pattern = normalizedRulePattern(field.stringValue)
        // 主路不带 force —— 让门真的拦一次,是它存在的全部意义(一道永远被绕过
        // 的门等于没有门);「Add Anyway」也**只放行刚刚被拒的那一条**。
        let force = pressedAddAnyway && pattern == refused
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result {
                try GuardianClient().changeRule(
                    action: "add", kind: kind, pattern: pattern, force: force)
            }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    self.lastRules = list
                    self.presentRules(list, forceShow: false)
                    let verb = kind == .direct ? "direct" : "through the tunnel"
                    self.followUpAfterRuleChange(
                        title: "\(pattern) will always go \(verb)", list: list)
                case .failure(let error):
                    if case GuardianClientError.status(409, let code) = error,
                        code == "rules_risky_direct"
                    {
                        self.askForNewRule(
                            accessory: accessory, field: field, picker: picker,
                            note: riskyDirectRuleWarning, refused: pattern)
                        return
                    }
                    self.showGuardianFailure(title: "Could not add that rule", error: error)
                }
            }
        }
    }

    /// 改完规则之后说哪句话,**由 Guardian 应答里的事实决定**(`ruleChangeFollowUp`,
    /// 纯函数):它改完会叫 Core 热重载,重载成了就是已生效;没成(或旧版 Guardian
    /// 没说)才要重连 —— 那句「要重连才生效」此前是常量,不说会让用户以为已经
    /// 生效,然后在问题依旧时把这一步排除掉;说了而其实已经生效,又会让他白断
    /// 一次网。
    private func followUpAfterRuleChange(title: String, list: RuleList) {
        let alert = NSAlert()
        alert.messageText = title
        switch ruleChangeFollowUp(requiresRestart: list.requiresRestart) {
        case .applied:
            alert.informativeText = "The change is already in effect. New connections follow the new rules."
            alert.addButton(withTitle: "OK")
            NSApp.activate(ignoringOtherApps: true)
            alert.runModal()
        case .reconnectNeeded:
            alert.informativeText = "bx applies routing rules when it reconnects. "
                + "Until then, traffic keeps following the old rules."
            alert.addButton(withTitle: "Reconnect Now")
            alert.addButton(withTitle: "Later")
            NSApp.activate(ignoringOtherApps: true)
            guard alert.runModal() == .alertFirstButtonReturn else { return }
            reconnectBx()
        }
    }

    /// 上一次真正摆进菜单栏的那份内容的签名(menuSignature)。内容没变就不重建
    /// —— 见 commitMenu。
    private var lastMenuSignature: String?

    /// 把 rebuildMenu 攒好的草稿摆进菜单栏,**内容没变就一个 item 都不动**。
    ///
    /// 菜单开着时每 2 秒刷新一次,此前每次都 `removeAllItems()` 重填:展开的子菜单
    /// 会被拆掉、高亮会丢、整个菜单闪一下。本仓库两次因此选了窗口而不是子菜单
    /// (规则、按应用看分流)。根因是「重建」与「有没有变化」没分开:这里按渲染
    /// 结果的签名比对,只有真的变了才换 —— 于是 `Troubleshoot ▸` 这样的子菜单能
    /// 用,稳态下菜单开着也不再每 2 秒闪一下。签名里带着计秒的行(Connecting Ns)
    /// 每秒都变,那几个状态照旧每拍重建,而它们本来就没有子菜单。
    private func commitMenu(_ draft: NSMenu) {
        guard let live = statusItem.menu else { return }
        let signature = menuSignature(draft)
        if signature == lastMenuSignature { return }
        lastMenuSignature = signature
        live.removeAllItems()
        for item in draft.items {
            draft.removeItem(item)
            live.addItem(item)
        }
    }

    /// 一份菜单**看得见的一切**折成一个字符串:标题(含富文本标题)、是否可点、
    /// 是否分隔符、图标名、动作名、子菜单(递归)。漏掉一项就是「那一项变了而菜单
    /// 不更新」—— 所以宁可多算(动作名、图标名),不许少算。
    private func menuSignature(_ menu: NSMenu) -> String {
        menu.items.map { item -> String in
            if item.isSeparatorItem { return "---" }
            var parts = [
                item.title,
                item.attributedTitle?.string ?? "",
                item.attributedTitle == nil ? "plain" : "rich",
                item.isEnabled ? "on" : "off",
                item.image?.name() ?? item.image?.accessibilityDescription ?? "",
                item.action.map { NSStringFromSelector($0) } ?? "",
            ]
            if let submenu = item.submenu {
                parts.append("[" + menuSignature(submenu) + "]")
            }
            // 开关行看得见的一切住在它自己的 signature 里;别的自定义视图按构造
            // 不存在,真出现了就当成「每次都变」,宁可多重建也不许漏更新。
            if let view = item.view {
                parts.append((view as? ProtectionSwitchRow)?.signature ?? UUID().uuidString)
            }
            return parts.joined(separator: "\u{1F}")
        }.joined(separator: "\n")
    }

    /// 菜单第一行的开关。显示与否、开关、可用全由 protectionSwitch(纯函数)判;
    /// 拨动接回原来那两个入口 —— 确认、免密、逃生口一个字不动。返回 nil 表示这个
    /// 状态没什么可拨,调用方保留原来的文字动作。
    private func protectionSwitchRow(subtitle: String?, subtitleIsBad: Bool) -> ProtectionSwitchRow? {
        guard case .shown(let isOn, let enabled) = protectionSwitch(
            state: menuStateKind(), inFlight: toggleInFlight?.action) else { return nil }
        let row = ProtectionSwitchRow(isOn: isOn, enabled: enabled, subtitle: subtitle, subtitleIsBad: subtitleIsBad)
        row.onFlip = { [weak self] turnedOn in
            switch protectionSwitchAction(turnedOn: turnedOn) {
            case .turnOn: self?.startBx()
            case .turnOff: self?.turnOffBx()
            }
        }
        return row
    }

    private func rebuildMenu() {
        // 攒进一份草稿,函数的每条出口都经 commitMenu 落定 —— 内容没变就不换。
        let menu = NSMenu()
        defer { commitMenu(menu) }
        if let startedAt = updateInFlight {
            let elapsed = Int(Date().timeIntervalSince(startedAt))
            menu.addHeadline("Updating bx")
            // 说清"在做什么"与"过了多久" —— 一个沉默的转圈光标与卡死无从区分。
            menu.addInfo("Status", "Downloading and installing… \(elapsed)s")
            menu.addPlainText("This can take a few minutes on a slow connection.")
            menu.addItem(.separator())
            menu.addQuit(quitBxActionTitle, target: self, action: #selector(quitBx))
            return
        }
        if let inFlight = toggleInFlight {
            let elapsed = Int(Date().timeIntervalSince(inFlight.startedAt))
            // 没有抬头:开关停在目标位置、禁用,进度就写在它下面 —— 用户刚拨的地方
            // 就是他在看的地方,Connecting/Disconnecting 这个词由进度文案自己说。
            if let row = protectionSwitchRow(
                subtitle: toggleProgressText(action: inFlight.action, elapsedSeconds: elapsed), subtitleIsBad: false) {
                menu.addProtectionSwitch(row)
            }
            if pendingQuit != nil {
                // Quit 已经排队:这是最重要的一句话,用户点了确认框,必须能
                // 看到"收到了",而不是一个看起来什么都没变的菜单。
                menu.addPlainText(quitQueuedStatusText())
            } else if let hint = toggleSlowHint(elapsedSeconds: elapsed) {
                menu.addPlainText(hint)
                menu.addAction("Open Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            }
            menu.addItem(.separator())
            menu.addQuit(quitBxActionTitle, target: self, action: #selector(quitBx))
            return
        }
        if let snapshot = recoverySnapshot {
            let presentation = recoveryPresentation(for: snapshot)
            menu.addHeadline(presentation.title)
            menu.addInfo("Status", presentation.shortReason ?? presentation.title)
            if !snapshot.recoveryID.isEmpty {
                menu.addInfo("Recovery", snapshot.recoveryID)
            }
            menu.addItem(.separator())
            if presentation.isRunning {
                menu.addAction(
                    "Reconnect",
                    symbol: "arrow.clockwise",
                    target: self,
                    action: #selector(reconnectBx),
                    enabled: false
                )
            } else if snapshot.state == "failed" {
                // **这个网络看起来要先登录 —— 给一个不用关掉保护的出路。**
                //
                // 咖啡馆/酒店 Wi-Fi 的强制门户靠劫持 DNS + 拦 HTTP 重定向弹登录页,
                // 而 bx 开着时那两条都被接管了,于是网关根本没看见你的请求、页面永远
                // 弹不出来。但**网关本身是私网、一直是直连的**(kill-switch 只拦
                // Proxy 判定),所以直接打开 http://<网关> 通常就能登录 —— 不用
                // `bx down`、没有 fail-open 窗口、没有要守的承诺。
                //
                // 这与被否掉的那个「登录此网络」入口是两回事:那个要**关掉保护**,
                // 而它把最危险的动作包装成小事、且自动重新武装是个可能悄悄违约的
                // 承诺。这一个只是打开一个 URL。
                if recoveryLooksLikeCaptiveNetwork(snapshot) {
                    menu.addAction("Open Wi-Fi Sign-In Page", symbol: "wifi.exclamationmark",
                                   target: self, action: #selector(openWiFiSignIn))
                }
                menu.addAction("Details", symbol: "info.circle", target: self, action: #selector(showRecoveryDetails))
                menu.addAction("Check for Problems", symbol: "stethoscope", target: self, action: #selector(runDoctorFromMenu))
            } else {
                menu.addAction("Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
            }
            return
        }
        switch state {
        case .connected(_, let version, _):
            // 没有抬头:菜单栏图标是身份,开关就是状态。「bx / Connected」两行外加
            // 一条分隔线与开关说的是同一件事(项目所有者 review:同一件事说了三遍)。
            // **这里曾经有 `Status: Protected` 与 `Network changes: …` 两行,已删。**
            // 前者与图标、开关说的是同一件事;后者是一个**永远不变的常量串** ——
            // 安慰文案不是状态,一行永远说同一句话的东西不是信息。
            //
            // `.warning` 那一支的 Status 行**留着**:那里装的是原因(Repair Required /
            // DNS not managed),图标说不出来。
            // 只摆压缩后的行(判据在 compactMenuRows,纯函数):健康时一行 Via,
            // 诊断行只在 ✗ 时露面。图标裂不裂仍看完整集合的 anomalyCount。
            // Via 那一行是开关行的第二行小字(控制中心式:标题下面一行摘要),
            // 其余压缩后的行照旧一行一行摆。
            let compact = compactMenuRows(menuRowsNow())
            let via = compact.first { $0.label == "Via" }
            if let row = protectionSwitchRow(subtitle: via?.value, subtitleIsBad: via?.mark == .bad) {
                menu.addProtectionSwitch(row)
            }
            for row in compact where row.label != "Via" {
                let suffix: String
                switch row.mark {
                case .ok: suffix = ""
                case .bad: suffix = "  ✗"
                case .unknown: suffix = ""
                }
                menu.addInfo(row.label, row.value + suffix)
            }
            // 版本号不再常驻(它只在有新版时才是信息,见 addUpdateActionIfAvailable);
            // 「装的是哪一版」搬进 Troubleshoot ▸ 里那一行。
            _ = version
        case .warning(let message, let version):
            // 原因(Repair Required / DNS not managed)写在开关下面那行、标红 ——
            // 图标说不出原因,这一行是唯一说得出的地方。
            if let row = protectionSwitchRow(subtitle: message, subtitleIsBad: true) {
                menu.addProtectionSwitch(row)
            }
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
            } else {
                _ = version
            }
        case .updateNeeded(let message, let version):
            menu.addHeadline("Update Required")
            menu.addInfo("Status", message)
            if let version {
                menu.addInfo("Version", version)
            }
        case .setupNeeded(let message):
            menu.addHeadline("Setup Required")
            menu.addInfo("Status", message)
        case .missing(let message):
            menu.addHeadline("Not Installed")
            menu.addInfo("Status", message)
        case .notInstalled(let bundleVersion):
            menu.addHeadline("Not Installed")
            if let bundleVersion {
                menu.addInfo("Version", bundleVersion)
            }
        case .off:
            // 副标题是这一屏最大的那几个字。维护挂起期间 protection_state 就是
            // `off`,写死 "Off" 会把「bx 正在自我升级」显示成「你把它关了」——
            // 判定在 offSubtitle(纯函数,MaintenancePresentationTests 钉着)。
            // 没有抬头:开关拨在「关」就是全部信息。维护挂起期间 protection_state
            // 就是 `off`,判定在 offSubtitle(纯函数):那时开关下面写一句 Paused,
            // 挂起的详情由下面那行 Maintenance 说;平时什么都不写。
            let off = offSubtitle(status: maintenanceReport, now: Date())
            if let row = protectionSwitchRow(subtitle: off == "Off" ? nil : off, subtitleIsBad: false) {
                menu.addProtectionSwitch(row)
            }
        }
        // 挂起在**每一个**状态下都要说出来:它就是「为什么保护不在」的答案。
        // `.connected` 那一支的数据行已经带出同一行(menuRows 的第一行),这里
        // 跳过它以免同一句话说两遍。
        if !stateShowsDataRows, let hold = maintenanceRow(status: maintenanceReport, now: Date()) {
            menu.addInfo(hold.label, hold.value)
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
            menu.addInfo("Last operation failed", failure)
        }
        // **更新入口只有一个,而且只在有新版时出现**(强调色,紧贴顶部那组)。
        // 平时的版本号不是信息,它住在 Troubleshoot ▸ 里。
        addUpdateActionIfAvailable(to: menu)
        menu.addItem(.separator())
        // 建设性主动作排在诊断入口之前:处在 off / 未配置 / 未安装 时,用户唯一
        // 想点的就是它,把它压在 View Logs 与 Run Doctor 下面是本末倒置。
        // 破坏性动作(Turn Off / Quit)反过来仍留在菜单底部——那是 macOS 惯例,
        // 也避免误点,所以 .connected/.warning 的顺序不动。
        switch state {
        case .off:
            // 打开保护 = 拨第一行那个开关(protectionSwitchRow),不再另给文字项。
            break
        case .setupNeeded:
            menu.addAction("Set Up bx...", symbol: "link", target: self, action: #selector(setUpBx))
        case .notInstalled:
            menu.addAction("Install bx…", symbol: "arrow.down.circle", target: self, action: #selector(installBx))
        case .missing, .updateNeeded:
            menu.addAction("Open Install Guide", symbol: "book", target: self, action: #selector(openInstallGuide))
        case .connected, .warning:
            break
        }
        // 关掉保护 = 拨第一行那个开关;这里只剩修复与重连。
        switch state {
        case .connected:
            menu.addAction("Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
        case .warning("Repair Required", _):
            menu.addAction(repairActionTitle, symbol: "wrench.and.screwdriver", target: self, action: #selector(repairBx))
            menu.addAction("Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
        case .warning:
            menu.addAction("Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
        case .off, .updateNeeded, .setupNeeded, .missing, .notInstalled:
            // 这些状态的主动作已经排在上面了。
            break
        }
        // ---- 窗口入口:天天会点的几扇门,与上面的动作同一组(少一条分隔线)----
        // 规则入口:只在这一版 Guardian 声明了 rules 能力时出现。**能力键缺席 =
        // 旧版**,少了这个判断用户会对着一个每次点都 404 的按钮,而 404 在菜单上
        // 根本表达不出来(rulesEditingAvailable 是纯函数,判据由 Swift 套件钉住)。
        // 能力清单取自 maintenanceReport —— 它就是「上一次拿到的完整 /v1/status」
        // (在任何分支之前落定)。**不另存一份副本**:两份会漂,而漂开时菜单会
        // 按一份的能力去画、按另一份的数据去填。
        if rulesEditingAvailable(capabilities: maintenanceReport?.capabilities) {
            menu.addAction("Routing Rules…", symbol: "arrow.triangle.branch", target: self, action: #selector(openRulesWindow))
        }
        // 服务器入口:同样只在 Guardian 声明了这个能力时出现。旧版没有
        // /v1/servers,画出来的按钮每次点都失败,而用户看不出为什么。
        // 「Set Up a New Server…」与「Replace Configuration…」2026-09-08 起住在
        // 这个窗口里(它们说的都是服务器这件事);后者在没有这个窗口的旧 Guardian
        // 上仍留在菜单里,见 replaceConfigurationLivesInMenu。
        if serverSwitchingAvailable(capabilities: maintenanceReport?.capabilities) {
            menu.addAction("Servers…", symbol: "globe", target: self, action: #selector(openServersWindow))
        }
        // 应用流量入口:同样只在 Guardian 声明了 apps 能力时出现。旧版没有
        // /v1/apps,画出来的菜单项每次点都 404,而 404 在菜单上根本表达不出来
        // —— 用户只会看到「点了没反应」。**绝不「试着拨一下看看」**:旧
        // Guardian 与「支持但此刻没数据」在客户端看来必须分得开(status watch
        // 那次真机实测过绕过门的代价:CPU 常驻 26%~46%、吞吐上千次/秒)。
        if appTrafficAvailable(capabilities: maintenanceReport?.capabilities) {
            menu.addAction("Traffic by App…", symbol: "chart.bar.doc.horizontal",
                           target: self, action: #selector(openAppTrafficWindow))
        }
        if replaceConfigurationLivesInMenu(capabilities: maintenanceReport?.capabilities) {
            menu.addAction("Replace Configuration…", symbol: "arrow.triangle.2.circlepath",
                           target: self, action: #selector(replaceConfiguration))
        }
        // Check for leaks 在**每一个**状态下都在场。这是刻意的:这个功能的
        // 立身之本就是「保护关着也有用」——只在 .connected 里给它,等于把它
        // 藏在最不需要它的那个状态里(TestMacMenuLeakCheckRunsUnprivileged
        // 按花括号深度钉住它在顶层)。
        menu.addAction("Check for Leaks…", symbol: "magnifyingglass", target: self, action: #selector(checkForLeaks))
        // ---- Troubleshoot ▸:一年点一次的东西收进一个子菜单 ----
        // 子菜单能用的前提是 commitMenu 只在内容变了才重建(否则每 2 秒被拆一次)。
        // 卸载入口也在这里:**只在装过的时候出现**(没装就没什么可卸),但它必须
        // 存在于其余每一个状态里 —— 想删掉 bx 的人最可能正处在「它出问题了」
        // 那几个状态。它排在子菜单最底,与破坏性动作归底部的通例一致。
        let troubleshoot = NSMenu()
        troubleshoot.addAction("Check for Problems", symbol: "stethoscope", target: self, action: #selector(runDoctorFromMenu))
        troubleshoot.addAction("Open Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
        // 「装的是哪一版」:从一级菜单搬进来的那行版本号,只答这一个问题。
        if let version = installedVersionForMenu() {
            troubleshoot.addItem(.separator())
            troubleshoot.addInfo("bx \(version)")
        }
        if cliIsInstalled() {
            troubleshoot.addItem(.separator())
            troubleshoot.addAction(UninstallPresentation.actionTitle, symbol: "trash", target: self, action: #selector(uninstallBx))
        }
        menu.addItem(.separator())
        menu.addSubmenu("Troubleshoot", symbol: "wrench.adjustable", troubleshoot)
        // 退出入口无条件加一次。**不要挪回上面任何一个 case**:此前它只在
        // .connected/.warning 里,于是 .off/.setupNeeded/.missing/.notInstalled/
        // .updateNeeded 下菜单没有任何退出入口(TestMacMenuQuitActionPresentInEveryState)。
        // 不带图标(电源符号在这条菜单里会被读成「关掉保护」,而真正的开关就在
        // 第一行),带 ⌘Q —— 与系统菜单栏应用同款。
        menu.addQuit(quitBxActionTitle, target: self, action: #selector(quitBx))
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

    /// `.connected` 是唯一会把 menuRows 的数据行铺进菜单的状态(见 rebuildMenu 与
    /// menuRowsNow)。维护挂起那一行两边都要有:在这个状态里它由数据行带出,其余
    /// 状态里由 rebuildMenu 单独补一行 —— 这个判据就是二者的分界,免得说两遍。
    private var stateShowsDataRows: Bool {
        if case .connected = state { return true }
        return false
    }

    /// 更新入口。**只在有新版时出现**,强调色 + 图标;措辞住在 InstallPresentation
    /// (`menuUpdateActionTitle`,那里编得进测试套件),这里只负责画。
    /// 平时的版本号不是信息(项目所有者 review:常驻在菜单中间是墙纸),它搬去
    /// Troubleshoot ▸ 里那一行(installedVersionForMenu)。
    private func addUpdateActionIfAvailable(to menu: NSMenu) {
        guard let title = menuUpdateActionTitle(check: updateCheck) else { return }
        let item = NSMenuItem(title: title, action: #selector(updateBx), keyEquivalent: "")
        item.target = self
        item.image = NSImage(systemSymbolName: "arrow.down.circle", accessibilityDescription: title)
        item.attributedTitle = NSAttributedString(
            string: title,
            attributes: [.foregroundColor: NSColor.controlAccentColor]
        )
        menu.addItem(item)
    }

    /// 「装的是哪一版」。只从状态里已经带着的版本取,取不到就不写 —— 不为一行
    /// 小字在每次重建时去读盘。
    private func installedVersionForMenu() -> String? {
        switch state {
        case .connected(_, let version, _):
            return version
        case .warning(_, let version), .updateNeeded(_, let version):
            return version
        case .notInstalled(let bundleVersion):
            return bundleVersion
        case .setupNeeded, .missing, .off:
            return nil
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
        // 这一版 Guardian 会发布日志就开日志页;旧版退回原来的文件夹(那是诊断包的落点)。
        if logsAvailable(capabilities: maintenanceReport?.capabilities) {
            openDiagnosticsLogs(highlighting: nil)
            return
        }
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

    @objc private func runDoctorFromMenu() {
        // 这一版 Guardian 会自己算 doctor 就开 Checks 页;旧版退回终端那条归档路。
        if doctorAvailable(capabilities: maintenanceReport?.capabilities) {
            openDiagnosticsChecks()
            return
        }
        exportDiagnostics()
    }

    private func exportDiagnostics() {
        openTerminal("diag=\"$HOME/Library/Logs/bx/diagnostics\"; mkdir -p \"$diag\"; sudo env BX_LOG_ARCHIVE_DIR=\"$diag\" '\(bxPath)' doctor; latest=$(find \"$diag\" -maxdepth 1 -type d -name 'bx-logs-*' | sort | tail -1); if [ -n \"$latest\" ]; then group=$(id -gn); sudo chown -R \"$USER:$group\" \"$latest\" 2>/dev/null || true; open \"$latest\"; fi; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    /// 打开泄漏检测。**不提权** —— bx leakcheck 自己会拒绝 root(以 root 跑会让
    /// 那个 loopback 服务变成 root 进程的端口),提权只会换来一个弹了密码框然后
    /// 失败的操作。浏览器由 bx 自己打开,菜单只负责起它。
    ///
    /// 在后台队列跑:leakcheck 最长等 2 分钟,压在主线程上会把整个菜单冻住
    /// (阶段①那次 71 分钟的冻结就是这么来的)。
    @objc private func checkForLeaks() {
        guard ensureCLIUsable() else { return }
        if leakCheckInFlight { return }
        leakCheckInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            guard let self else { return }
            let result = self.runBx(["leakcheck"])
            DispatchQueue.main.async {
                self.leakCheckInFlight = false
                if result.code != 0 {
                    self.showMessage(
                        "Leak Check Did Not Start",
                        "bx could not start the leak check. Run `bx leakcheck` in Terminal to see why."
                    )
                }
            }
        }
    }

    @objc private func startBx() {
        guard confirmStartProtection() else { return }
        performToggle(.turnOn)
    }

    /// 首次启动时主动开口。
    ///
    /// **在此之前,双击 Bx.app 之后什么都不发生** —— 菜单栏冒出一个小图标,而没有
    /// 任何东西告诉用户下一步该点哪里。一个从 dmg 里拖进来的普通用户到这里就卡住了。
    ///
    /// 判定(问不问、问哪个)住在 FirstRun.swift,这里只负责把它变成弹框 ——
    /// main.swift 编不进 Swift 测试套件,凡是能搬出去的判断都不该留在这里。
    private func runFirstRunGuidance() {
        switch firstRunAction(state: menuStateKind(), alreadyOfferedSetup: hasOfferedSetup) {
        case .none:
            return
        case .offerInstall:
            beginInstall()
        case .offerSetup:
            // **先记下再问。** 反过来的话,用户在弹框上点了取消、而写标记那一步没走到
            // (崩溃、被强退),下次登录会再弹一次 —— 而「只问一次」正是这一支的全部约定。
            hasOfferedSetup = true
            beginSetup()
        }
    }

    /// hasOfferedSetup 记住「装好没配」那个引导已经问过了。
    ///
    /// 存在 UserDefaults 而不是内存:那个状态下 app 是登录项,每次开机都会起来,
    /// 而每次登录弹一个框是骚扰。用户拒绝之后菜单里那一项一直都在,不会失去入口。
    private var hasOfferedSetup: Bool {
        get { UserDefaults.standard.bool(forKey: "bx.offeredSetup") }
        set { UserDefaults.standard.set(newValue, forKey: "bx.offeredSetup") }
    }

    /// setUpBx 是菜单项的 #selector 入口 —— **只由 AppKit 在用户点击时调用**。
    /// 躯体在 beginSetup 里,好让首次引导也能走同一条路而不必调用这个选择器:
    /// 「选择器只由用户点击触发」是一条能被读者一眼验证的声明,给它开例外的代价
    /// 是下一个人必须重新验证它还成不成立。
    @objc private func setUpBx() {
        beginSetup()
    }

    /// 换掉当前配置(换服务器)。
    ///
    /// **与首次配置共用同一条路**(校验、提权、写配置),只是结尾从「要不要启动」
    /// 换成「重连使其生效」,并且换之前把**出口会从哪变到哪**摆出来。
    @objc private func replaceConfiguration() {
        guard ensureCLIUsable() else { return }
        let current = maintenanceReport?.core?.server
        guard let link = promptForClientLink(
            title: "Replace Configuration",
            hint: "Paste the bx link you were given.",
            confirmTitle: "Continue"
        ) else { return }

        let origin: ReplaceLinkOrigin =
            clipboardCandidateLink(NSPasteboard.general.string(forType: .string)) == link ? .clipboard : .typed
        let confirm = NSAlert()
        confirm.messageText = "Change where your traffic leaves?"
        confirm.informativeText = replaceConfigurationMessage(currentServer: current, pastedFrom: origin)
        confirm.addButton(withTitle: "Replace")
        confirm.addButton(withTitle: "Cancel")
        NSApp.activate(ignoringOtherApps: true)
        guard confirm.runModal() == .alertFirstButtonReturn else { return }

        guard runPrivileged("'\(bxPath)' setup \(shellSingleQuoted(link))") else {
            showFailure("Replace Failed", "bx kept its previous configuration.")
            refresh(userInitiated: true)
            return
        }
        // 配置不热重载 —— 不重连的话用户会以为已经换过去了(这正是 2026-08-06
        // 那次「以为换了服务器其实没换」的形状)。
        if !runPrivileged("'\(bxPath)' down") || !runPrivileged("'\(bxPath)' up") {
            showFailure("Reconnect Failed",
                        "The new configuration is saved, but bx did not come back up. Try Turn On from the menu.")
        }
        refresh(userInitiated: true)
    }

    private func beginSetup() {
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

    /// installBx 是菜单项的 #selector 入口 —— 只由 AppKit 在用户点击时调用。
    @objc private func installBx() {
        beginInstall()
    }

    private func beginInstall() {
        runEmbeddedInstaller(
            confirmTitle: "Install bx?",
            // 断网这句必须出现在这里:菜单调用走 osascript,CLI 的确认提示进了
            // 一个没人看的管道,这个 NSAlert 是 GUI 用户唯一看得到的告知。
            confirmMessage: "bx will install its command line tool and background protection service. macOS will ask for administrator authorization. If protection is already running, it is stopped and restarted to complete the upgrade — your network drops for a few seconds. On a fresh install, protection is not started until you set up and turn it on.",
            confirmButton: "Install"
        )
    }

    /// 卸载。**这个入口存在的理由是堵掉「拖进废纸篓」那条路** —— 那样只删界面,
    /// 而 Guardian 仍以 root 在跑、保护仍然开着,用户刚好删掉了唯一能关掉它的东西。
    @objc private func uninstallBx() {
        let alert = NSAlert()
        alert.messageText = UninstallPresentation.confirmTitle
        alert.informativeText = UninstallPresentation.confirmMessage
        alert.addButton(withTitle: UninstallPresentation.confirmButton)
        alert.addButton(withTitle: "Cancel")
        NSApp.activate(ignoringOtherApps: true)
        guard alert.runModal() == .alertFirstButtonReturn else { return }

        // **用系统里那个 bx,不是 app 内嵌的那份。** 卸载要停 launchd 服务、
        // 拆路由 —— 那是系统上正在跑的那一套的事;而内嵌那份属于这个 bundle,
        // 它马上就要被删掉了。
        // **不带 --yes**:`bx uninstall` 根本没有这个 flag(它只要求 root,不问确认),
        // 而 urfave/cli 遇到未知 flag 会直接失败 —— 失败在 osascript 里,用户看到的
        // 只是「卸载没成功」而完全不知道为什么。确认已经在上面那个 NSAlert 里拿到了。
        let succeeded = runPrivileged("\(shellSingleQuoted(bxPath)) uninstall")
        if UninstallPresentation.shouldQuitAfter(uninstallSucceeded: succeeded) {
            NSApp.terminate(nil)
            return
        }
        showFailure(UninstallPresentation.failureTitle, UninstallPresentation.failureMessage)
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
                // 同样要等刷新落地:否则读到的是**装之前**的状态,于是刚装完又弹一次
                // 「Install bx?」。
                refresh(userInitiated: true) { [weak self] in
                    self?.runFirstRunGuidance()
                }
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

    /// 问一次内核「默认路由的网关是谁」。**这是 main.swift 里第三个、也是唯一一个
    /// 只读的进程出口**,由 TestMacMenuSpawnsOnlyFromTheActionPath 的清单显式认可
    /// (`internal/cli/cli_test.go`,`Process` 类型那条规则的 callers 恰好三个)。
    ///
    /// 为什么它值得占一个名额:替代方案要么是让菜单自己解析 `NET_RT_DUMP` 路由表
    /// (一大段 C interop,判据反而更难看见),要么是把网关经 Guardian 的 wire 格式
    /// 发上来(要动协议 + 能力声明,而它只服务这一个按钮)。跑 `/sbin/route -n get
    /// default` 是**只读**的,不提权、不改任何东西,且只在**用户点了那一下**时发生
    /// —— 与 `ensureCLIUsable` 同一类,不在每 2 秒的轮询路径上。
    ///
    /// 拿到的字符串原样交给纯函数 `wifiSignInURL(fromRouteOutput:)` 判定,这里
    /// 不做任何解析 —— 「只许打开私网地址」那条安全判断必须住在测得到的地方。
    private func readDefaultRouteOffMainThread(_ completion: @escaping (String) -> Void) {
        DispatchQueue.global(qos: .userInitiated).async {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: "/sbin/route")
            process.arguments = ["-n", "get", "default"]
            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = Pipe()
            do {
                try process.run()
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                process.waitUntilExit()
                completion(String(data: data, encoding: .utf8) ?? "")
            } catch {
                completion("")
            }
        }
    }

    /// 打开这个网络的登录页(强制门户)。**不改变保护状态。**
    ///
    /// 判据全在纯函数 `wifiSignInURL(fromRouteOutput:)` 里(它只接受私网地址 ——
    /// 网关若是公网 IP,打开它就是隧道外的一个明文请求);这里只负责问一次内核、
    /// 把结果喂给它、再交给系统浏览器。
    ///
    /// **spawn 在动作路径上,不在轮询路径上** —— 与 `ensureCLIUsable` 同一类:
    /// 每 2 秒跑一次 `route` 是浪费,用户点一下跑一次不是。
    @objc private func openWiFiSignIn() {
        readDefaultRouteOffMainThread { output in
            let url = wifiSignInURL(fromRouteOutput: output)
            DispatchQueue.main.async {
                guard let url else {
                    // **读不到就说读不到,不摆一个猜出来的地址。** 网关是公网 IP 时
                    // 也走这一支:那种情况下打开它会在隧道外发一个明文请求。
                    let alert = NSAlert()
                    alert.messageText = "Couldn't find this network's sign-in page"
                    alert.informativeText = "bx couldn't read a private gateway address for this "
                        + "network. Turn bx off, sign in with your browser, then turn it back on."
                    alert.runModal()
                    return
                }
                NSWorkspace.shared.open(url)
            }
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
        alert.addButton(withTitle: "Check for Problems")
        alert.addButton(withTitle: "OK")
        if alert.runModal() == .alertFirstButtonReturn {
            exportDiagnostics()
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
        // **跑在后台队列。** 上一版在主线程上同步等 `bx update` 跑完 —— 那是一次
        // 几十 MB 的下载,真机上转了两分钟菜单全程无响应,用户以为要强制退出
        // (2026-08-14)。与阶段①把开关异步化是同一条理由:任何可能慢的动作都
        // 不许占着主线程,否则菜单连"我在做什么"都说不出来。
        updateInFlight = Date()
        rebuildMenu()
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            _ = self?.runPrivileged(command)
            // bx exits non-zero on rollback but still prints valid JSON; always inspect the log.
            let logData = FileManager.default.contents(atPath: logPath)
            DispatchQueue.main.async {
                guard let self else { return }
                self.updateInFlight = nil
                self.finishUpdate(logData: logData)
            }
        }
    }

    /// 更新正在跑的开始时刻。非 nil 时菜单显示进度行 —— 一个什么都不说的
    /// 转圈光标与"卡死了"在用户眼里没有区别。
    private var updateInFlight: Date?

    private func finishUpdate(logData: Data?) {
        rebuildMenu()
        guard let logData else {
            showFailure("Update Failed", updateFailureMessage(nil))
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
            // **把真正的原因说出来。** 它就在这份刚读过的日志里 —— 上一版
            // 读到了、解析了,然后只报一句"Run Doctor for details"。
            showFailure("Update Failed", updateFailureMessage(updateFailureDetail(logData)))
        }
    }

    private func updateFailureMessage(_ detail: String?) -> String {
        guard let detail, !detail.isEmpty else {
            return "bx could not complete the update. Run Doctor to collect diagnostics."
        }
        return detail + "\n\nRun Doctor to collect diagnostics."
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

    private func promptForClientLink(
        title: String = "Set Up bx",
        hint: String = "Paste your bx link.",
        confirmTitle: String = "Set Up"
    ) -> String? {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = hint
        alert.addButton(withTitle: confirmTitle)
        alert.addButton(withTitle: "Cancel")

        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 420, height: 24))
        field.placeholderString = "bx://..."
        // **预填剪贴板。** 用户十有八九刚从聊天窗口复制过来;这是各家客户端的
        // 默认行为,没有它会被当成缺陷。预填而不是直接用 —— 他要看得见自己在装什么。
        if let candidate = clipboardCandidateLink(NSPasteboard.general.string(forType: .string)) {
            field.stringValue = candidate
        }
        alert.accessoryView = field
        NSApp.activate(ignoringOtherApps: true)

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
        alert.addButton(withTitle: "Check for Problems")
        alert.addButton(withTitle: "OK")
        if alert.runModal() == .alertFirstButtonReturn {
            exportDiagnostics()
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
    /// 用 showMessage 而不是 showFailure —— 后者的 "Check for Problems" 要跑的正是这个
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
    /// 一行粗体状态词(Setup Required / Not Installed / Updating bx)。**只给没有
    /// 开关的状态用**:有开关时图标是身份、开关是状态,抬头是重复。
    func addHeadline(_ title: String) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.attributedTitle = NSAttributedString(
            string: title,
            attributes: [.font: NSFont.systemFont(ofSize: 13, weight: .semibold)]
        )
        item.isEnabled = false
        addItem(item)
    }

    /// Quit:不带图标、带 ⌘Q,与系统菜单栏应用同款。
    func addQuit(_ title: String, target: AnyObject, action: Selector) {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: "q")
        item.target = target
        addItem(item)
    }

    /// 整行文本已经拼好时用这个(版本行的措辞由 UpdatePresentation 决定,
    /// 不是「标签: 值」两段式)。
    func addInfo(_ title: String) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.isEnabled = false
        addItem(item)
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

    /// 菜单第一行的开关(ProtectionSwitchRow):一个挂了自定义视图的 item。
    func addProtectionSwitch(_ row: ProtectionSwitchRow) {
        let item = NSMenuItem(title: "Protection", action: nil, keyEquivalent: "")
        item.view = row
        addItem(item)
    }

    func addSubmenu(_ title: String, symbol: String, _ submenu: NSMenu) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.image = NSImage(systemSymbolName: symbol, accessibilityDescription: title)
        item.submenu = submenu
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
