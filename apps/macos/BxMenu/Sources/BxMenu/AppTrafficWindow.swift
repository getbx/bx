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
/// 停掉、缓冲清空。「没人看时不问内核、不记字节、不攒历史,不在这台机器上留下
/// 你开过什么应用的记录」就是这么成立的,不需要任何退订路径(菜单被强杀时也没人
/// 来退订,而 TTL 一视同仁)。
/// **旧说法「开销精确为零」在 2026-08-20 之后不再成立,别照抄**:Core 侧未订阅时
/// 仍维护一张活连接表,否则**订阅之前**就已经建好的连接(会议媒体流、SSH 这类
/// 长连接)永远不会出现在这个窗口里 —— 那是一个真机 bug。隐私前提不受影响:
/// 表里只有端口与判定,没有应用名。
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

    /// 速率由**相邻两次快照做差**得来(判据全在 AppTrafficRateTracker 里,
    /// 这里只负责在每一次真的读到报告时喂它一拍)。
    ///
    /// **窗口一关就整个重置**:关掉窗口意味着订阅在一个 TTL 内过期、Core 侧缓冲
    /// 清空,下次打开累计值从零重来 —— 跨越那一刀去做差没有任何意义。
    private var rateTracker = AppTrafficRateTracker()
    private var rates: [AppTrafficRateKey: AppTrafficRate] = [:]

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
        // 打开窗口 = 一份新订阅的第一帧。**第一帧没有速率**,而且不许编一个:
        // 见 AppTrafficRateTracker 头上那段。
        rateTracker = AppTrafficRateTracker()
        rates = rateTracker.ingest(report, at: Date())
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
        // **只在真的读到报告时才推进一拍。** 失败那几拍不喂它,于是下一次成功时
        // 的时长会自动把那几拍算进去 —— 那才是这两份快照之间真实流逝的时间。
        rates = rateTracker.ingest(report, at: Date())
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
        // 订阅会在一个 TTL 内过期、Core 侧缓冲清空,下次打开累计值从零重来。
        // 留着上一份快照做差会得到一个横跨那一刀的假速率。
        rateTracker = AppTrafficRateTracker()
        rates = [:]
        onClose?()
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            // 八列(图标 + 应用名 + 五个数字列 + 规则原文)摆得下的宽度;
            // 上一版是 460,那时一行是一句散文。
            contentRect: NSRect(x: 0, y: 0, width: 720, height: 420),
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

        let rows = report.rows(rates: rates)
        // 有应用行就摆成一张表(数字右对齐、跨组对得上);三种「空」那几句
        // 说明没有列可对齐,原样一行一行摆。
        if rows.contains(where: { if case .entry = $0 { return true }; return false }) {
            stack.addArrangedSubview(grid(for: rows))
        } else {
            for row in rows {
                if case .notice(let text) = row {
                    stack.addArrangedSubview(NSTextField(labelWithString: text))
                }
            }
        }

        // **那句「近似值」不是可选的。** 归因把字节数记在源端口上,而端口会被
        // 复用 —— 上一条连接的残留字节会算到新连接头上。spec 明写「界面不该把
        // 它显示成精确账」;那句话本身(以及它为什么必须点明端口复用)住在
        // AppTrafficModel 的常量里,由 Swift 套件钉住。
        stack.addArrangedSubview(gap())
        stack.addArrangedSubview(hint(appTrafficApproximateNote))
        // **第二句小字同样不是可选的。** 窗口打开之前就已经建好的连接由种子播进
        // 缓冲,而种子把一个 socket 上并存的多条流压成一条 —— 于是它们只会出现在
        // 一个组里。这件事此前有三份记档和一条测试,唯独用户看不到,而它恰好落在
        // 这个窗口最初的用例上(开会开到一半打开窗口看会议走哪)。措辞只陈述观测
        // 得到的现象、不断言原因,与那句「Protection may be off.」同一条纪律;
        // 那句话本身住在 AppTrafficModel 的常量里,由 Swift 套件钉住。
        stack.addArrangedSubview(hint(appTrafficPreexistingNote))
    }

    /// 把应用行摆成一张表:**数字列右对齐,跨组对得上大小**。
    ///
    /// 上一版每行是一句散文(`1 connection · 48 B up · 48 B down · …`),两行之间
    /// 没法比大小 —— 那正是「感觉 iStat 做得更好」里最实的一半。
    ///
    /// **哪几列是数字列由纯模型说了算**(`appTrafficNumericColumns`):判据放在
    /// 这个文件里就没有任何测试能读到它,AppKit 这一半在 CI 里编不了。
    private func grid(for rows: [AppTrafficReport.Row]) -> NSView {
        let grid = NSGridView(numberOfColumns: appTrafficColumnTitles.count, rows: 0)
        grid.translatesAutoresizingMaskIntoConstraints = false
        grid.rowSpacing = 5
        grid.columnSpacing = 14
        // 列标题只出现一次(整张表最上面),不是每组重复一遍 —— 三组各来一行
        // 表头会把这个窗口变成一屏表头。
        grid.addRow(with: appTrafficColumnTitles.map { columnTitle($0) })

        for row in rows {
            switch row {
            case .sectionHeader(let title):
                let cells = grid.addRow(with: [header(title)])
                cells.mergeCells(in: NSRange(location: 0, length: appTrafficColumnTitles.count))
            case .entry(let entry):
                grid.addRow(with: cells(for: entry))
            case .notice:
                continue // 说明行不进表格(它没有列可对齐)
            }
        }

        for index in appTrafficNumericColumns {
            grid.column(at: index).xPlacement = .trailing
        }
        return grid
    }

    /// 一行的八个格子。顺序必须与 `appTrafficColumnTitles` 一一对应。
    private func cells(for entry: AppTrafficReport.Entry) -> [NSView] {
        [
            icon(for: entry) ?? NSGridCell.emptyContentView,
            NSTextField(labelWithString: entry.app),
            hint(entry.conns),
            hint(entry.upRate),
            hint(entry.downRate),
            hint(entry.upTotal),
            hint(entry.downTotal),
            rule(entry.rule),
        ]
    }

    /// 应用图标。**路径为空就返回 nil,不画占位** —— 一格空白的占位图不是
    /// 「没有图标」,是「这个应用的图标长这样」,那是另一句话;那一行就退化成
    /// 没有图标的一行。
    ///
    /// 取的是 `.app` 包而不是包里那个可执行文件(`appIconPath`,纯函数、已测):
    /// 对后者取图标拿到的是通用可执行文件图标,一整列长一个样,等于没有图标。
    private func icon(for entry: AppTrafficReport.Entry) -> NSView? {
        guard !entry.execPath.isEmpty else { return nil }
        let image = NSWorkspace.shared.icon(forFile: appIconPath(forExecutable: entry.execPath))
        image.size = NSSize(width: 16, height: 16)
        let view = NSImageView(image: image)
        view.imageScaling = .scaleProportionallyDown
        return view
    }

    private func columnTitle(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
        label.textColor = .tertiaryLabelColor
        return label
    }

    /// 规则原文那一格:变长文本,放在最后一列,长了就截断 —— 它不许把数字列
    /// 挤出窗口(数字列是这次改动要买的东西)。
    private func rule(_ text: String) -> NSTextField {
        let label = hint(text)
        label.lineBreakMode = .byTruncatingTail
        label.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        return label
    }

    private func header(_ title: String) -> NSTextField {
        let label = NSTextField(labelWithString: title)
        label.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        return label
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
