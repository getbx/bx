import AppKit

/// 「Traffic by App」悬浮窗 —— 哪些应用的流量走了隧道、哪些直连、哪些被拦下。
///
/// **为什么是悬浮窗而不是菜单栏常驻**(与项目所有者否掉「Direct rules: N
/// unreachable」常驻红字同一条判断):按应用分流是**常态不是事件**,常驻会变
/// 墙纸,而且会把真正要紧的信号一起淹掉。它也不该是子菜单 —— 菜单打开时每 2 秒
/// 就地重填一次,进子菜单再移到某一项通常超过 2 秒,那一刻 item 已经被拆掉重填。
///
/// **`level: .floating` 是刻意的**:用户开着它是为了一边用别的应用一边看流量
/// 在哪一组冒出来;沉到别的窗口后面就等于没开。
///
/// **窗口是订阅的载体。** Core 侧的采集靠每一次拉取续期(30 秒 TTL,惰性结算),
/// 而拉取只在这个窗口开着时发生 —— 窗口一关,订阅在一个 TTL 内自己过期、采集
/// 停掉、缓冲清空。「没人看时开销精确为零、不在这台机器上留下你开过什么应用的
/// 记录」就是这么成立的,不需要任何退订路径(菜单被强杀时也没人来退订,而 TTL
/// 一视同仁)。
///
/// **这个文件只做摆放。** 哪些行、副标题写什么、三种「空」各自说哪一句,全在
/// AppTrafficModel 的纯函数 `rows()` 里(AppKit 这一半在 CI 里编不了,判断放
/// 这儿等于没测)。
final class AppTrafficWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var stack: NSStackView?

    /// 最后一次真的读到的报告,以及此刻该不该说它已经不是「现在」了。
    /// **两者必须一起存**:陈旧提示要盖在那份快照上重画,而不是把快照丢掉 ——
    /// 丢掉它就只剩一句「读不到」,用户连刚才看到的那几行都找不回来。
    private var report: AppTrafficReport?
    private var staleNotice: String?

    /// 用户关掉了窗口。**接线方必须据此停掉心跳** —— 窗口关了而定时器还在跑,
    /// 订阅就永远续着,而界面上看不出任何异常。
    var onClose: (() -> Void)?

    /// 窗口是否开着。供环境刷新路径判断「有没有人在看」:关着就不拨(这个功能
    /// 要买到的收益),开着就说明有人正盯着,这时按需拉一次正是「按需」的本意。
    var isVisible: Bool { window?.isVisible ?? false }

    func show(report: AppTrafficReport) {
        let window = ensureWindow()
        self.report = report
        staleNotice = nil
        render()
        // LSUIElement 应用不会自动到前台;不激活的话窗口会开在别的应用后面,
        // 用户以为"点了没反应"。
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// 数据更新时就地重画。**窗口不存在或已关闭就什么都不做** —— 不要因为后台
    /// 刷新把一个用户没打开的窗口弹出来,也不要在这条路上抢焦点。
    func refreshIfVisible(report: AppTrafficReport) {
        guard let window, window.isVisible else { return }
        self.report = report
        // 拉到了就是拉到了 —— 一次成功抹掉陈旧标记,不留一句会自我永存的警告。
        staleNotice = nil
        render()
    }

    /// 连着几次拉不到之后,在窗口顶上盖一句「这不是此刻的事实」。
    ///
    /// **不清空那几行数据**:用户要的是「刚才看到的还在,只是别再当它是现在」。
    /// 判据(几次算陈旧、那句话怎么说)住在 AppTrafficModel 的纯函数里。
    func markStaleIfVisible(_ notice: String) {
        guard let window, window.isVisible else { return }
        staleNotice = notice
        render()
    }

    func windowWillClose(_ notification: Notification) {
        onClose?()
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 460, height: 360),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )
        window.title = "Traffic by App"
        // 悬浮:一边用别的应用一边看,沉下去就等于没开。
        window.level = .floating
        window.isReleasedWhenClosed = false
        window.center()
        window.delegate = self

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 8
        stack.edgeInsets = NSEdgeInsets(top: 16, left: 18, bottom: 16, right: 18)
        stack.translatesAutoresizingMaskIntoConstraints = false

        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.drawsBackground = false
        scroll.translatesAutoresizingMaskIntoConstraints = false
        // **必须是翻转坐标系**(与 ServersWindow 同一个坑):NSView 默认原点在
        // 左下,文档视图比可视区小时内容会沉到窗口底部,看起来像刻意的留白。
        let clip = FlippedView()
        clip.translatesAutoresizingMaskIntoConstraints = false
        clip.addSubview(stack)
        scroll.documentView = clip

        guard let content = window.contentView else { return window }
        content.addSubview(scroll)
        NSLayoutConstraint.activate([
            scroll.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            scroll.topAnchor.constraint(equalTo: content.topAnchor),
            scroll.bottomAnchor.constraint(equalTo: content.bottomAnchor),
            stack.leadingAnchor.constraint(equalTo: clip.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: clip.trailingAnchor),
            stack.topAnchor.constraint(equalTo: clip.topAnchor),
            stack.bottomAnchor.constraint(equalTo: clip.bottomAnchor),
            clip.widthAnchor.constraint(equalTo: scroll.widthAnchor),
        ])
        self.stack = stack
        self.window = window
        return window
    }

    /// 按 `rows()` 给的行摆。**顺序与内容一个字都不重新判断** —— 三种「空」
    /// 各自该说哪一句已经在纯模型里定死并测过,这里再判一次就是第二份判据。
    private func render() {
        guard let stack else { return }
        // **没有报告就什么都不画。** 上一版在这里用零值兜底
        // (`?? AppTrafficReport(subscribed: false)`),而那份零值渲染出来的正是
        // "Not collecting app traffic right now." —— 恰恰是拨号失败分支明令禁止
        // 的那句话:「没问出来」与「没在采集」是两件事,替 Core 回答一个它没被
        // 问过的问题就是编一句自洽的假话。今天三个调用点都保证有报告,这条
        // guard 是不让下一个调用点把那句谎话带回来。
        guard let report else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        // 陈旧提示排在**最上面**:它改变的是下面每一行该怎么读,摆在底部等于
        // 让用户先把一份过期快照读成事实、再发现它过期了。
        if let staleNotice {
            let banner = NSTextField(labelWithString: staleNotice)
            banner.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            banner.textColor = .systemOrange
            stack.addArrangedSubview(banner)
            stack.addArrangedSubview(gap())
        }

        for row in report.rows() {
            switch row {
            case .sectionHeader(let title):
                stack.addArrangedSubview(header(title))
            case .entry(let app, let detail):
                stack.addArrangedSubview(entry(app: app, detail: detail))
            case .notice(let text):
                stack.addArrangedSubview(NSTextField(labelWithString: text))
            }
        }

        // **那句「近似值」不是可选的。** 归因把字节数记在源端口上,而端口会被
        // 复用 —— 上一条连接的残留字节会算到新连接头上。spec 明写「界面不该把
        // 它显示成精确账」;那句话本身(以及它为什么必须点明端口复用)住在
        // AppTrafficModel 的常量里,由 Swift 套件钉住。
        stack.addArrangedSubview(gap())
        stack.addArrangedSubview(hint(appTrafficApproximateNote))
    }

    private func header(_ title: String) -> NSTextField {
        let label = NSTextField(labelWithString: title)
        label.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        return label
    }

    /// 一个应用一行:名字在左,连接数与字节数在右。
    ///
    /// 与 Servers/Routing Rules 同一次清理的结论:不要把副标题缩到第二行,
    /// 一屏参差不齐的留白比信息本身更抢眼。
    private func entry(app: String, detail: String) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.alignment = .firstBaseline
        box.spacing = 10

        let title = NSTextField(labelWithString: app)
        title.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        box.addArrangedSubview(title)

        let sub = hint(detail)
        sub.setContentHuggingPriority(.defaultLow, for: .horizontal)
        box.addArrangedSubview(sub)
        return box
    }

    private func gap() -> NSView {
        let spacer = NSView()
        spacer.translatesAutoresizingMaskIntoConstraints = false
        spacer.heightAnchor.constraint(equalToConstant: 6).isActive = true
        return spacer
    }

    private func hint(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        label.textColor = .secondaryLabelColor
        return label
    }
}
