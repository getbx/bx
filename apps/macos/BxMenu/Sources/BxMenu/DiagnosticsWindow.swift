import AppKit

/// 「Diagnostics」窗口:两页 —— Checks(/v1/doctor 的结论)与 Logs(/v1/logs 的尾部)。
///
/// **这个文件只做摆放。** 哪些行要高亮(失败码定位)由 LogsModel 的纯函数
/// `logLinesMatching` 决定;Checks 页的排序、合计、标题由 DiagnosticsModel 的
/// `sortedDoctorChecks`/`doctorSummaryLine`/`doctorCheckTitle` 决定 —— 这里连一次
/// 字符串比较都不做。
final class DiagnosticsWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var tabs: NSTabView?
    private var checksStack: NSStackView?
    private var logsStack: NSStackView?

    /// 用户点了底部「Export Diagnostics…」—— 走原来那条终端归档路(spec §1 表里保留的)。
    var onExportDiagnostics: (() -> Void)?

    /// 用户点了 Checks 页的「Run again」。
    var onRunAgain: (() -> Void)?

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
        let (checksScroll, checksStack) = makeScrollingStack()
        checksItem.view = hosting(checksScroll)
        tabs.addTabViewItem(checksItem)

        let logsItem = NSTabViewItem(identifier: "logs")
        logsItem.label = "Logs"
        let (logsScroll, logsStack) = makeScrollingStack()
        logsItem.view = hosting(logsScroll)
        tabs.addTabViewItem(logsItem)

        self.tabs = tabs
        self.checksStack = checksStack
        self.logsStack = logsStack
        self.window = window
        return window
    }

    /// 一页的骨架:滚动视图 + 翻转的文档视图 + 竖栈。两页各调一次 —— 约束与此前
    /// 那份单页的完全一样,只是 `content` 换成对应 `NSTabViewItem` 的宿主视图。
    private func makeScrollingStack() -> (NSScrollView, NSStackView) {
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
        let clip = FlippedView()
        clip.translatesAutoresizingMaskIntoConstraints = false
        clip.addSubview(stack)
        scroll.documentView = clip

        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: clip.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: clip.trailingAnchor),
            stack.topAnchor.constraint(equalTo: clip.topAnchor),
            stack.bottomAnchor.constraint(equalTo: clip.bottomAnchor),
            clip.widthAnchor.constraint(equalTo: scroll.widthAnchor),
        ])
        return (scroll, stack)
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

    /// Checks 页:合计一句在顶,坏的排前,每条 = 状态标签 + 名字 + detail,hint 另起一行暗色小字。
    /// **排序、合计、标题全由纯模型给**(DiagnosticsModel),这里只摆。
    private func renderChecks(_ report: DoctorReport) {
        guard let stack = checksStack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
        let summary = NSTextField(labelWithString: doctorSummaryLine(report.checks))
        summary.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        stack.addArrangedSubview(summary)
        if !report.version.isEmpty {
            stack.addArrangedSubview(hint("bx \(report.version)"))
        }
        stack.addArrangedSubview(gap())
        for check in sortedDoctorChecks(report.checks) {
            let row = NSStackView()
            row.orientation = .horizontal
            row.alignment = .firstBaseline
            row.spacing = 8
            row.translatesAutoresizingMaskIntoConstraints = false
            let badge = NSTextField(labelWithString: check.status.uppercased())
            badge.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
            badge.textColor = statusColor(check.status)
            badge.setContentHuggingPriority(.required, for: .horizontal)
            row.addArrangedSubview(badge)
            let title = NSTextField(labelWithString: doctorCheckTitle(check.name))
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
            stack.addArrangedSubview(row)
            // 行宽跟着栈走(减去左右 18pt 的 edgeInsets),与 renderLogs 里那条
            // `text.widthAnchor…constant: -36` 同一个写法。
            row.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -36).isActive = true
            if !check.hint.isEmpty {
                let h = hint("→ " + check.hint)
                h.textColor = .tertiaryLabelColor
                stack.addArrangedSubview(h)
            }
        }
        stack.addArrangedSubview(gap())
        let again = NSButton(title: "Run again", target: self, action: #selector(runAgain))
        again.bezelStyle = .rounded
        again.controlSize = .small
        again.toolTip = "Asks bx to check again. This probes your server once, outside the tunnel."
        stack.addArrangedSubview(again)
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
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
        if let code, !code.isEmpty {
            let banner = NSTextField(labelWithString: "Highlighting lines that mention \(code).")
            banner.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            banner.textColor = .secondaryLabelColor
            stack.addArrangedSubview(banner)
        }
        for tail in report.logs {
            stack.addArrangedSubview(header("\(tail.name)  ·  \(tail.path)"))
            if !tail.unavailable.isEmpty {
                stack.addArrangedSubview(hint("Not available: \(tail.unavailable)"))
                continue
            }
            if tail.lines.isEmpty {
                stack.addArrangedSubview(hint("This log is empty."))
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
            stack.addArrangedSubview(text)
            // 宽度跟着栈走(减去左右 18pt 的 edgeInsets),这是它知道该在哪折行的
            // 唯一依据 —— 少了它 wrappingLabel 会按自己的内在宽度摊成一行。
            text.widthAnchor.constraint(equalTo: stack.widthAnchor, constant: -36).isActive = true
        }
        let export = NSButton(title: "Export Diagnostics…", target: self, action: #selector(exportDiagnostics))
        export.bezelStyle = .rounded
        export.controlSize = .small
        export.toolTip = "Runs bx doctor in Terminal and collects a diagnostics folder you can share."
        stack.addArrangedSubview(export)
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
