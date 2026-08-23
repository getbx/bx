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
/// 横向挤压顺序,**从最先让位到最后让位**。
///
/// 三档必须**严格递增**:规则原文(变长、会截断)< 应用名(有上界但不小)
/// < 数字列(这次改动买到的东西,不许被挤扁)。上一版应用名与数字列同为默认的
/// 750,于是「谁让位」由 Auto Layout 在同优先级里任选 —— 一个不确定的布局。
/// 方向由 Go 侧的守卫钉住(只钉常量存在钉不住方向,把两个数字对调不会有任何
/// 东西转红)。
let rulePriority = NSLayoutConstraint.Priority(250)
let appNamePriority = NSLayoutConstraint.Priority(500)
let numberPriority = NSLayoutConstraint.Priority(750)

final class AppTrafficWindowController: NSObject, NSWindowDelegate, NSSearchFieldDelegate {
    private var window: NSWindow?
    private var stack: NSStackView?

    /// 搜索框。**它在 `ensureWindow()` 里创建一次,住在每次刷新都会被拆掉重填的
    /// 那棵树(`stack`)之外。**
    ///
    /// 这一条是承重的,不是整洁问题:`render()` 每次刷新都把 `stack` 的
    /// arrangedSubviews 全部拆掉重建,而报告 5 秒一拍。搜索框若长在那棵树里,
    /// 用户打了两个字就会在下一次刷新时连同**焦点和已输入的文字**一起消失,
    /// 而现象只是「输入没反应」—— 窗口看起来完全正常,没有任何报错。
    private var searchField: NSSearchField?

    /// 当前查询串。存在这里(而不是每次去问搜索框)是因为重建时要读它,
    /// 而判据本身住在纯模型 `AppTrafficReport.rows(query:)` 里 —— 这个文件只
    /// 负责把它传进去。
    private var query = ""

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
            // 七列(应用名 + 五个数字列 + 规则原文)摆得下的宽度。图标不再单独
            // 占一列 —— 它搬进了应用名那一格。上一版是 460,那时一行是一句散文。
            contentRect: NSRect(x: 0, y: 0, width: 720, height: 420),
            // **`.resizable` 是承重的,不是讲究。** 最后一列是规则原文(变长文本、
            // 会截断),而这个窗口既不横向滚动(clip 的宽度锚死在 scroll 上)、
            // 也没有别的地方能读到全文 —— 少了它,一条被截断的规则就**永久不可见**,
            // 而上一版那句散文是整行读得到的。第二条出路是那一格的 toolTip,见
            // `rule(_:)`;两条都留着,一条是发现得了的(拖宽),一条是不用改布局的。
            styleMask: [.titled, .closable, .resizable],
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

        // **搜索框在这里创建一次,不在 render() 里** —— 见 `searchField` 头上那段。
        let search = NSSearchField()
        search.translatesAutoresizingMaskIntoConstraints = false
        search.placeholderString = "Filter by app, destination, or rule"
        search.sendsSearchStringImmediately = true
        search.delegate = self
        self.searchField = search

        guard let content = window.contentView else { return window }
        content.addSubview(search)
        content.addSubview(scroll)
        NSLayoutConstraint.activate([
            search.leadingAnchor.constraint(equalTo: content.leadingAnchor, constant: 18),
            search.trailingAnchor.constraint(equalTo: content.trailingAnchor, constant: -18),
            search.topAnchor.constraint(equalTo: content.topAnchor, constant: 12),
            scroll.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            scroll.topAnchor.constraint(equalTo: search.bottomAnchor, constant: 8),
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

        let rows = report.rows(query: query)
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
            case .emptySection(let text):
                // 「这一组在当前过滤下没有匹配」。**组标题仍然在**(上一分支已经
                // 发过),这一行只是说明它为什么空着 —— 让标题自己消失会让读者
                // 分不清「这个组没有匹配」与「这个组本来就是空的」。
                let cells = grid.addRow(with: [hint(text)])
                cells.mergeCells(in: NSRange(location: 0, length: appTrafficColumnTitles.count))
            case .notice:
                continue // 说明行不进表格(它没有列可对齐)
            }
        }

        for index in appTrafficNumericColumns {
            grid.column(at: index).xPlacement = .trailing
        }
        return grid
    }

    /// 一行的七个格子。顺序必须与 `appTrafficColumnTitles` 一一对应。
    ///
    /// **图标不再单独占一格** —— 它和名字一起住在第一格里(`appCell`)。
    private func cells(for entry: AppTrafficReport.Entry) -> [NSView] {
        [
            appCell(entry),
            number(entry.conns),
            number(entry.upRate),
            number(entry.downRate),
            number(entry.upTotal),
            number(entry.downTotal),
            rule(entry.rule),
        ]
    }

    /// 应用名那一格:**图标 + 名字**,名字下面(有目的地时)再加一行暗色小字。
    ///
    /// **图标住在这一格里,不再单独占一列。** 上一版那个图标列被真机截图撑到
    /// ~350pt、把规则列挤没了;搬进来消灭的不是那一次的宽度,是「图标列宽」
    /// 这一整类问题 —— Finder、活动监视器都是这么排的。
    ///
    /// 第二行是目的地摘要(第一条 + `+N`)。**摘要为 nil 就整行不加**,不是留一行
    /// 空白 —— 一行空白读作「这个应用没连任何地方」,那是另一句话。完整清单在
    /// toolTip 里,与规则列同一条纪律:凡是会截断的格子必须同时给出看全的办法。
    private func appCell(_ entry: AppTrafficReport.Entry) -> NSView {
        let title = NSStackView()
        title.orientation = .horizontal
        title.alignment = .centerY
        title.spacing = 6
        if let icon = icon(for: entry) {
            title.addArrangedSubview(icon)
        }
        title.addArrangedSubview(appName(entry.app))

        let cell = NSStackView()
        cell.orientation = .vertical
        cell.alignment = .leading
        cell.spacing = 1
        cell.translatesAutoresizingMaskIntoConstraints = false
        cell.addArrangedSubview(title)
        if let summary = entry.destSummary {
            let line = hint(summary)
            line.textColor = .tertiaryLabelColor
            line.lineBreakMode = .byTruncatingTail
            // 摘要跟规则原文同一档压缩阻力:它是这一格里最先该让位的东西,
            // 名字和数字列都不许被它挤。
            line.setContentCompressionResistancePriority(rulePriority, for: .horizontal)
            line.toolTip = entry.destTooltip.isEmpty ? nil : entry.destTooltip
            cell.addArrangedSubview(line)
        }
        cell.setContentCompressionResistancePriority(appNamePriority, for: .horizontal)
        cell.toolTip = entry.destTooltip.isEmpty ? nil : entry.destTooltip
        return cell
    }

    /// 搜索框内容变了:**只重画,不去拉新数据**。报告 5 秒一拍自己会来,而每敲
    /// 一个键就拨一次本机 socket 既无必要、也会把「按需取数」那条纪律弄脏。
    func controlTextDidChange(_ obj: Notification) {
        guard let field = obj.object as? NSSearchField, field === searchField else { return }
        query = field.stringValue
        render()
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
    ///
    /// **凡是会截断的格子,必须同时给出看全的办法。** 这个窗口不横向滚动,
    /// 截断之后那段文字就再没有别的地方能读到;`toolTip` 是不用改布局的那条出路
    /// (另一条是把窗口拖宽,见 `styleMask` 里的 `.resizable`)。
    private func rule(_ text: String) -> NSTextField {
        let label = hint(text)
        label.lineBreakMode = .byTruncatingTail
        label.setContentCompressionResistancePriority(rulePriority, for: .horizontal)
        label.toolTip = text.isEmpty ? nil : text
        return label
    }

    /// 一个数字格。**压缩阻力显式设成最高的一档** —— 数字列是这次改动买到的
    /// 东西,不许被任何变长文本挤扁。
    private func number(_ text: String) -> NSTextField {
        let label = hint(text)
        label.setContentCompressionResistancePriority(numberPriority, for: .horizontal)
        return label
    }

    /// 应用名那一格。**阻力低于数字列、高于规则列。**
    ///
    /// 上一版它是默认的 750,与五个数字格**同为一档** —— 规则列被压到零之后,
    /// 谁再让位由 Auto Layout 在同优先级里任选,而 `Microsoft Teams (work or
    /// school)` 这类名字长度有真实上界但不小。挤压顺序必须是确定的:
    /// **规则原文最先让位,其次应用名,数字列最后。**
    private func appName(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.lineBreakMode = .byTruncatingTail
        label.setContentCompressionResistancePriority(appNamePriority, for: .horizontal)
        label.toolTip = text.isEmpty ? nil : text
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
