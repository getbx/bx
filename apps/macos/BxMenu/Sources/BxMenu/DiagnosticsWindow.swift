import AppKit

/// 「Diagnostics」窗口。本期只有日志页;spec §6 的 Checks 页在 /v1/doctor 落地后加。
///
/// **这个文件只做摆放。** 哪些行要高亮(失败码定位)由 LogsModel 的纯函数
/// `logLinesMatching` 决定;这里连一次字符串比较都不做。
final class DiagnosticsWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var stack: NSStackView?

    /// 用户点了底部「Export Diagnostics…」—— 走原来那条终端归档路(spec §1 表里保留的)。
    var onExportDiagnostics: (() -> Void)?

    func showLogs(_ report: LogsReport, highlightingCode code: String?) {
        let window = ensureWindow()
        render(report, code: code)
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

    private func render(_ report: LogsReport, code: String?) {
        guard let stack else { return }
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
