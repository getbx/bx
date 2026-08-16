import AppKit

/// 「Routing Rules」窗口。
///
/// **为什么是窗口而不是子菜单。** 菜单打开时 bx 每 2 秒 `removeAllItems()` 重建一次;
/// 进子菜单再移到某一项通常超过 2 秒,那一刻 item 已经被拆掉重填 —— 点了没反应。
/// 真机上就是这么坏的。组开关这种需要停留的交互本来就不该塞进一个会自我重建的菜单。
///
/// **这个文件只做摆放。** 哪些行、什么顺序、副标题写什么,全在 RulesModel 的纯函数里
/// (`main.swift` 与 AppKit 这一半在 CI 里编都不编,判断放这儿等于没测)。
final class RulesWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var stack: NSStackView?

    /// 用户拨动了一个组开关。参数是组名与目标状态。
    var onToggleGroup: ((String, Bool) -> Void)?
    /// 用户要求打开配置文件所在位置。
    var onRevealConfig: (() -> Void)?

    func show(rows: [RuleGroupRow], custom: [String], configPath: String) {
        let window = ensureWindow()
        render(rows: rows, custom: custom, configPath: configPath)
        // LSUIElement 应用不会自动到前台;不激活的话窗口会开在别的应用后面,
        // 用户以为"点了没反应"——正是这一版要消灭的那种体验。
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// 数据更新时就地重画。**窗口不存在就什么都不做** —— 不要因为后台刷新
    /// 把一个用户没打开的窗口弹出来。
    func refreshIfVisible(rows: [RuleGroupRow], custom: [String], configPath: String) {
        guard let window, window.isVisible else { return }
        render(rows: rows, custom: custom, configPath: configPath)
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 420, height: 320),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )
        window.title = "Routing Rules"
        window.isReleasedWhenClosed = false
        window.center()
        window.delegate = self

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 10
        stack.edgeInsets = NSEdgeInsets(top: 16, left: 18, bottom: 16, right: 18)
        stack.translatesAutoresizingMaskIntoConstraints = false

        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.drawsBackground = false
        scroll.translatesAutoresizingMaskIntoConstraints = false
        let clip = NSView()
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

    private func render(rows: [RuleGroupRow], custom: [String], configPath: String) {
        guard let stack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        // **坏消息排最上面。** 人打开这个窗口十有八九就是因为有东西working,
        // 而它此前埋在某一组下面的一行红色小字里。正常时这里一个字都没有 ——
        // 一句永远在的「一切正常」是墙纸。
        if let headline = rulesHeadline(rows) {
            let warn = NSTextField(labelWithString: headline)
            warn.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
            warn.textColor = .systemRed
            stack.addArrangedSubview(warn)
        }

        stack.addArrangedSubview(sectionTitle("Skip the tunnel"))
        for row in rows {
            stack.addArrangedSubview(groupRow(row))
        }

        if !custom.isEmpty {
            stack.addArrangedSubview(sectionTitle("Your own"))
            // **一行一条。** 上一版把它们用空格拼成一段,规则一多就是一堵墙。
            // 这些是只读的(菜单没有资格替用户删他手写的规则),所以只排版、不给控件。
            for pattern in custom {
                let label = NSTextField(labelWithString: pattern)
                label.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
                label.textColor = .secondaryLabelColor
                stack.addArrangedSubview(label)
            }
        }

        // **页脚收成两行。** 上一版是「按钮 / 路径 / 说明」三样各占一行、彼此平级,
        // 于是最不重要的东西占了最多的地方。
        stack.addArrangedSubview(separator())
        stack.addArrangedSubview(caption("Changes apply when you reconnect."))
        if !configPath.isEmpty {
            let row = NSStackView()
            row.orientation = .horizontal
            row.alignment = .centerY
            row.spacing = 8
            let path = caption(configPath)
            path.lineBreakMode = .byTruncatingMiddle
            path.preferredMaxLayoutWidth = 260
            row.addArrangedSubview(path)
            let reveal = NSButton(title: "Show", target: self, action: #selector(revealConfig))
            reveal.bezelStyle = .rounded
            reveal.controlSize = .small
            row.addArrangedSubview(reveal)
            stack.addArrangedSubview(row)
        }
    }

    /// 一组一行:左边勾选框,**右边一列状态**。
    ///
    /// 上一版是勾选框下面缩进一行小字,而那行小字多半是空的 —— 于是一屏里全是
    /// 参差不齐的留白,正是「太丑」的来源。右对齐之后眼睛只需要扫一列。
    private func groupRow(_ row: RuleGroupRow) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.alignment = .firstBaseline
        box.spacing = 8

        let toggle = NSButton(checkboxWithTitle: row.group.title, target: self, action: #selector(toggleGroup(_:)))
        toggle.identifier = NSUserInterfaceItemIdentifier(row.group.name)
        // 半装的组用 mixed 状态显示 —— 它既不是开也不是关,而勾选框恰好有第三态。
        toggle.allowsMixedState = row.isMixed
        toggle.state = row.isOn ? .on : (row.isMixed ? .mixed : .off)
        box.addArrangedSubview(toggle)
        box.setHuggingPriority(.defaultLow, for: .horizontal)

        let trailing = NSTextField(labelWithString: row.trailing ?? "")
        trailing.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        trailing.textColor = row.failing > 0 ? .systemRed : .secondaryLabelColor
        trailing.alignment = .right
        trailing.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        box.addArrangedSubview(trailing)
        return box
    }

    private func sectionTitle(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
        label.textColor = .secondaryLabelColor
        return label
    }

    @objc private func toggleGroup(_ sender: NSButton) {
        guard let name = sender.identifier?.rawValue else { return }
        // 从 mixed 点出去一律当作"打开":用户看到半装的组去点它,想要的是补齐。
        let enable = sender.state != .off
        sender.allowsMixedState = false
        sender.state = enable ? .on : .off
        onToggleGroup?(name, enable)
    }

    @objc private func revealConfig() {
        onRevealConfig?()
    }

    private func caption(_ text: String) -> NSTextField {
        let label = NSTextField(wrappingLabelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        label.textColor = .secondaryLabelColor
        label.preferredMaxLayoutWidth = 370
        return label
    }

    private func separator() -> NSView {
        let line = NSBox()
        line.boxType = .separator
        return line
    }
}
