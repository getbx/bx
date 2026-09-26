import AppKit

/// 「Diagnostics」窗口:两页 —— Checks(/v1/doctor 的结论)与 Logs(/v1/logs 的尾部)。
///
/// **这个文件只做摆放。** 哪些行要高亮(失败码定位)由 LogsModel 的纯函数
/// `logLinesMatching` 决定;Checks 页的排序、合计、标题由 DiagnosticsModel 的
/// `sortedDoctorChecks`/`doctorSummaryLine`/`doctorCheckTitle` 决定 —— 判定一句
/// 都不在这里。
///
/// **唯一的例外是 `statusColor`**,它按状态字面量选颜色。那不是判定(它不改变
/// 说了什么,只改变那句话长什么样),而是呈现映射,与 AppKit 绑死、搬进纯模型
/// 只会让纯模型 import AppKit。写下来是因为原话是「连一次字符串比较都不做」——
/// 一句当场就能被同一个文件证伪的自述,会让下一个人连带不再相信旁边那些还成立
/// 的话。
final class DiagnosticsWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var tabs: NSTabView?
    private var checksStack: NSStackView?
    private var logsStack: NSStackView?
    /// 两页各自的滚动视图。**留着只为渲染完能滚回顶部** —— 见 scrollToTop:
    /// 重画之后停在旧位置,是 2026-09-10 真机上「最严重的那条被挡在屏幕外面」
    /// 与「点了 Run again 像是没反应」这两件事的同一个根因。
    private var checksScroll: NSScrollView?
    private var logsScroll: NSScrollView?

    /// 用户点了底部「Export Diagnostics…」—— 走原来那条终端归档路(spec §1 表里保留的)。
    var onExportDiagnostics: (() -> Void)?

    /// 用户点了 Checks 页的「Run again」/ 空页上的「Check Now」。**两处同一个出口**
    /// —— 第二个回调会让「谁在触发那次隧道外的探测」变成两个答案。
    var onRunAgain: (() -> Void)?

    /// 用户点了 Logs 空页上的「Load Logs」。
    var onLoadLogs: (() -> Void)?

    /// 两页各自的能力(来自 Guardian 的 capabilities)。**默认按「有」画** ——
    /// 在问出来之前把入口扣住,与「这一版没有这个功能」在界面上完全一样,而后者
    /// 是一句可能不真的断言。`setAvailability` 由 main.swift 在开窗之前调。
    private var doctorCapable = true
    private var logsCapable = true

    /// 这一页有没有被真数据渲染过。**占位只许覆盖没被渲染过的那一页** —— 否则
    /// 一次能力刷新会把用户正在读的报告换成一句「No checks yet.」。
    private var checksRendered = false
    private var logsRendered = false

    /// 能力门。**它不藏标签页**:两页都在,缺的那一页如实说这一版没有这个功能。
    /// 藏掉标签页会让用户以为菜单坏了(他刚在别处见过 Logs 这两个字);说出来
    /// 才是「旧 Guardian」这个事实本身。
    func setAvailability(doctor: Bool, logs: Bool) {
        doctorCapable = doctor
        logsCapable = logs
        if !checksRendered { seedChecksPlaceholder() }
        if !logsRendered { seedLogsPlaceholder() }
    }

    func showChecks(_ report: DoctorReport) {
        let window = ensureWindow()
        renderChecks(report)
        tabs?.selectTabViewItem(at: 0)
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    func showLogs(_ report: LogsReport, highlightingCode code: String?) {
        let window = ensureWindow()
        renderLogs(report, code: code)
        tabs?.selectTabViewItem(at: 1)
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 760, height: 480),
            styleMask: [.titled, .closable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Diagnostics"
        window.isReleasedWhenClosed = false
        // 窗口可缩放,而日志行是任意长度的散文 —— 给一个下限,免得被拖到一格
        // 宽度、每行折成十几行。上限不设:日志本来就该越宽越好读。
        window.minSize = NSSize(width: 480, height: 320)
        window.center()
        window.delegate = self

        guard let content = window.contentView else { return window }

        // 两页一个窗口(不是两个窗口):用户排查时要在「结论」与「原始日志」之间
        // 来回看,两个窗口会互相盖住,而标签页保住「同一件事的两个视角」这层关系。
        let tabs = NSTabView()
        tabs.translatesAutoresizingMaskIntoConstraints = false
        content.addSubview(tabs)
        NSLayoutConstraint.activate([
            tabs.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            tabs.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            tabs.topAnchor.constraint(equalTo: content.topAnchor),
            tabs.bottomAnchor.constraint(equalTo: content.bottomAnchor),
        ])

        // **顺序即索引**:showChecks 选 0、showLogs 选 1,别调换。
        let checksItem = NSTabViewItem(identifier: "checks")
        checksItem.label = "Checks"
        let (checksScroll, checksStack) = makePageStack()
        checksItem.view = hosting(checksScroll)
        tabs.addTabViewItem(checksItem)

        let logsItem = NSTabViewItem(identifier: "logs")
        logsItem.label = "Logs"
        let (logsScroll, logsStack) = makePageStack()
        logsItem.view = hosting(logsScroll)
        tabs.addTabViewItem(logsItem)

        self.tabs = tabs
        self.checksStack = checksStack
        self.logsStack = logsStack
        self.checksScroll = checksScroll
        self.logsScroll = logsScroll
        self.window = window
        // **两页都要先有东西。** 只渲染被请求的那一页,另一页就是一张白纸 ——
        // 用户切过去看到的不是「还没拉」,而是一个坏掉的界面;而 Checks 那页
        // 在渲染之前连「Run again」都没有,于是没有任何办法把它填上。
        seedChecksPlaceholder()
        seedLogsPlaceholder()
        return window
    }

    /// 一页的骨架:滚动视图 + 翻转的文档视图 + 竖栈。两页各调一次 —— 约束与此前
    /// 那份单页的完全一样,只是 `content` 换成对应 `NSTabViewItem` 的宿主视图。
    /// 一页的骨架。组装走共用原语(MenuLayout.swift):四扇窗口此前各抄了一份,
    /// 而那份拷贝里有同一个缺陷 —— 行没被钉到容器宽度,塞不下时整行溢出到窗口
    /// 外面,行尾按钮点不到(2026-09-17 离屏快照量出来的)。挂载在 hosting() 里。
    private func makePageStack() -> (NSScrollView, NSStackView) {
        makeScrollingStack(
            insets: NSEdgeInsets(top: 16, left: 18, bottom: 16, right: 18),
            spacing: 8
        )
    }

    private func hosting(_ scroll: NSScrollView) -> NSView {
        let page = NSView()
        page.addSubview(scroll)
        NSLayoutConstraint.activate([
            scroll.leadingAnchor.constraint(equalTo: page.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: page.trailingAnchor),
            scroll.topAnchor.constraint(equalTo: page.topAnchor),
            scroll.bottomAnchor.constraint(equalTo: page.bottomAnchor),
        ])
        return page
    }

    /// 把一页滚回顶部。**每次重画之后都要调。**
    ///
    /// 排序把最严重的放在最前,而 NSScrollView 重画后停在原来的偏移上:真机上
    /// 打开 Checks 页第一眼看到的是最末尾那几行 OK,合计句与那条 WARN 全在屏幕
    /// 外面 —— 一个以「坏的排前」为卖点的页面,第一眼给的是最不重要的一端。
    /// 同一件事也让 Run again 看起来没反应:重画完画面停在同一个位置。
    ///
    /// `layoutSubtreeIfNeeded` 不能省:不先让布局落定,documentView 还是上一次的
    /// 高度,滚到的是一个按旧内容算出来的坐标。文档视图是 FlippedView,所以
    /// 顶部就是 .zero。
    private func scrollToTop(_ scroll: NSScrollView?) {
        guard let scroll else { return }
        scroll.layoutSubtreeIfNeeded()
        scroll.contentView.scroll(to: .zero)
        scroll.reflectScrolledClipView(scroll.contentView)
    }

    private func clear(_ stack: NSStackView) {
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
    }

    /// Checks 页在拿到报告之前的样子:一句话 + 一个能把它填上的按钮。
    ///
    /// 按钮走的是**同一个** `onRunAgain` —— 「再查一次」与「现在查一次」在这条路
    /// 上是同一件事(都是一次 `/v1/doctor`),给它第二个回调只会让「那次隧道外的
    /// 探测由谁触发」多出一个答案。
    private func seedChecksPlaceholder() {
        guard let stack = checksStack else { return }
        clear(stack)
        guard doctorCapable else {
            stack.addFullWidthRow(hint("This version of bx Guardian does not provide checks."))
            return
        }
        stack.addFullWidthRow(hint("No checks yet."))
        stack.addFullWidthRow(gap())
        let now = NSButton(title: "Check Now", target: self, action: #selector(runAgain))
        now.bezelStyle = .rounded
        now.controlSize = .small
        now.toolTip = "Asks bx to check now. This probes your server once, outside the tunnel."
        stack.addFullWidthRow(now)
    }

    /// Logs 页在拉到日志之前的样子。同一条:说一句 + 给一个出口。
    private func seedLogsPlaceholder() {
        guard let stack = logsStack else { return }
        clear(stack)
        guard logsCapable else {
            stack.addFullWidthRow(hint("This version of bx Guardian does not provide logs."))
            return
        }
        stack.addFullWidthRow(hint("No logs loaded yet."))
        stack.addFullWidthRow(gap())
        let load = NSButton(title: "Load Logs", target: self, action: #selector(loadLogs))
        load.bezelStyle = .rounded
        load.controlSize = .small
        load.toolTip = "Reads the tail of bx's own logs."
        stack.addFullWidthRow(load)
    }

    @objc private func loadLogs() {
        onLoadLogs?()
    }

    /// Checks 页:合计一句在顶,坏的排前,每条 = 状态标签 + 名字 + detail,hint 另起一行暗色小字。
    /// **排序、合计、标题全由纯模型给**(DiagnosticsModel),这里只摆。
    private func renderChecks(_ report: DoctorReport) {
        guard let stack = checksStack else { return }
        clear(stack)
        checksRendered = true
        let summary = NSTextField(labelWithString: doctorSummaryLine(report.checks))
        summary.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        stack.addFullWidthRow(summary)
        var subtitle = doctorCheckedAtLine(Date())
        if !report.version.isEmpty {
            subtitle = "bx \(report.version) · " + subtitle
        }
        stack.addFullWidthRow(hint(subtitle))
        stack.addFullWidthRow(gap())
        for check in sortedDoctorChecks(report.checks) {
            let row = NSStackView()
            row.orientation = .horizontal
            row.alignment = .firstBaseline
            row.spacing = 8
            row.translatesAutoresizingMaskIntoConstraints = false
            // 状态画成带颜色的 SF Symbol(与泄漏检测页的状态徽章同一套词汇),
            // 那个词进读屏描述与悬停提示 —— 颜色不是唯一的信号。
            let look = doctorStatusLook(check.status)
            let badge = NSImageView()
            badge.image = NSImage(systemSymbolName: look.symbol, accessibilityDescription: look.label)
            badge.contentTintColor = statusColor(check.status)
            badge.toolTip = look.label
            badge.setContentHuggingPriority(.required, for: .horizontal)
            badge.setContentCompressionResistancePriority(.required, for: .horizontal)
            row.addArrangedSubview(badge)
            let title = NSTextField(labelWithString: doctorCheckTitle(check.name))
            title.font = .systemFont(ofSize: NSFont.systemFontSize, weight: .medium)
            title.setContentHuggingPriority(.required, for: .horizontal)
            row.addArrangedSubview(title)
            if !check.detail.isEmpty {
                let detail = hint(check.detail)
                // 这一列会被截断,所以必须同时给出看全的办法 —— 窗口不横向滚动,
                // 少了 toolTip 那段文字就**永久不可见**。
                detail.lineBreakMode = .byTruncatingTail
                detail.toolTip = check.detail
                // **截断要能发生,得先有个边界让它去撞。** 抗压缩降到最低(badge 与
                // title 都是 .required 抗拉伸,于是让位的一定是这一列),再由下面那条
                // 行宽约束给出边界 —— 与本文件 renderLogs 里那个 wrappingLabel 同一
                // 条理由:「少了它会按自己的内在宽度摊成一行」,只是那边摊出去的后果
                // 是折行错、这边是整行横向溢出到一个没有横向滚动条的滚动视图外面。
                detail.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
                row.addArrangedSubview(detail)
            }
            stack.addFullWidthRow(row)
            // 行宽跟着栈走(减去左右 18pt 的 edgeInsets),与 renderLogs 里那条
            // `text.widthAnchor…constant: -36` 同一个写法。
            row.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -36).isActive = true
            if !check.hint.isEmpty {
                let h = hint("→ " + check.hint)
                h.textColor = .tertiaryLabelColor
                stack.addFullWidthRow(h)
            }
        }
        stack.addFullWidthRow(gap())
        let again = NSButton(title: "Run again", target: self, action: #selector(runAgain))
        again.bezelStyle = .rounded
        again.controlSize = .small
        again.toolTip = "Asks bx to check again. This probes your server once, outside the tunnel."
        stack.addFullWidthRow(again)
        scrollToTop(checksScroll)
    }

    @objc private func runAgain() {
        onRunAgain?()
    }

    private func statusColor(_ status: String) -> NSColor {
        switch status {
        case "fail": return .systemRed
        case "warn": return .systemOrange
        case "ok": return .systemGreen
        default: return .secondaryLabelColor
        }
    }

    private func gap() -> NSView {
        let spacer = NSView()
        spacer.translatesAutoresizingMaskIntoConstraints = false
        spacer.heightAnchor.constraint(equalToConstant: 6).isActive = true
        return spacer
    }

    private func renderLogs(_ report: LogsReport, code: String?) {
        guard let stack = logsStack else { return }
        clear(stack)
        logsRendered = true
        if let code, !code.isEmpty {
            let banner = NSTextField(labelWithString: "Highlighting lines that mention \(code).")
            banner.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            banner.textColor = .secondaryLabelColor
            stack.addFullWidthRow(banner)
        }
        for tail in report.logs {
            stack.addFullWidthRow(header("\(tail.name)  ·  \(tail.path)"))
            if !tail.unavailable.isEmpty {
                stack.addFullWidthRow(hint("Not available: \(tail.unavailable)"))
                continue
            }
            if tail.lines.isEmpty {
                stack.addFullWidthRow(hint("This log is empty."))
                continue
            }
            let marks = logLinesMatching(tail.lines, code: code)
            let body = NSMutableAttributedString()
            for (index, line) in tail.lines.enumerated() {
                var attributes: [NSAttributedString.Key: Any] = [
                    .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .regular),
                    .foregroundColor: NSColor.labelColor,
                ]
                if marks.contains(index) {
                    attributes[.backgroundColor] = NSColor.systemYellow.withAlphaComponent(0.35)
                }
                body.append(NSAttributedString(string: line + "\n", attributes: attributes))
            }
            // **换行的 NSTextField,不是裸 NSTextView。** NSTextView 在
            // NSStackView 里没有内在高度(它靠自己的 scroll view 定尺寸),裸摆
            // 进来会塌成零高或者把整栈撑爆;而此前那条 `>= 700` 的宽度约束在一个
            // 可缩放窗口里更糟 —— 窗口被拖窄到 700 以下时,约束与栈的宽度直接冲突。
            // wrappingLabel 有内在高度、按宽度自己折行,是「一段只读文本」的正解。
            let text = NSTextField(wrappingLabelWithString: "")
            text.attributedStringValue = body
            text.isSelectable = true
            text.lineBreakMode = .byWordWrapping
            text.maximumNumberOfLines = 0
            text.translatesAutoresizingMaskIntoConstraints = false
            // 水平抗压缩降到最低:窗口变窄时让它折行,而不是把栈顶出去。
            text.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
            stack.addFullWidthRow(text)
            // 宽度跟着栈走(减去左右 18pt 的 edgeInsets),这是它知道该在哪折行的
            // 唯一依据 —— 少了它 wrappingLabel 会按自己的内在宽度摊成一行。
            text.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -36).isActive = true
        }
        let export = NSButton(title: "Export Diagnostics…", target: self, action: #selector(exportDiagnostics))
        export.bezelStyle = .rounded
        export.controlSize = .small
        export.toolTip = "Runs bx doctor in Terminal and collects a diagnostics folder you can share."
        stack.addFullWidthRow(export)
        scrollToTop(logsScroll)
    }

    @objc private func exportDiagnostics() {
        onExportDiagnostics?()
    }

    private func header(_ title: String) -> NSTextField {
        let label = NSTextField(labelWithString: title)
        label.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        return label
    }

    private func hint(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        label.textColor = .secondaryLabelColor
        return label
    }
}
