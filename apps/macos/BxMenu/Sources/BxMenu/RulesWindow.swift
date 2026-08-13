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
    private var footer: NSTextField?

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

        stack.addArrangedSubview(heading("Which traffic skips the tunnel"))
        stack.addArrangedSubview(caption(
            "Turning a group off sends that traffic through the tunnel instead."))

        for row in rows {
            stack.addArrangedSubview(groupView(row))
        }

        if !custom.isEmpty {
            stack.addArrangedSubview(separator())
            stack.addArrangedSubview(heading("Your own rules"))
            // **只显示,不给开关。** 这些是用户手写的,菜单没有资格替他删。
            stack.addArrangedSubview(caption(
                "\(custom.count) rule\(custom.count == 1 ? "" : "s") you added yourself. "
                    + "bx never changes these — edit the config file to change them."))
            stack.addArrangedSubview(caption(custom.joined(separator: "   ")))
        }

        stack.addArrangedSubview(separator())
        if !configPath.isEmpty {
            let reveal = NSButton(title: "Show Config File…", target: self, action: #selector(revealConfig))
            reveal.bezelStyle = .rounded
            stack.addArrangedSubview(reveal)
            stack.addArrangedSubview(caption(configPath))
        }
        let note = caption("Changes take effect the next time bx reconnects.")
        footer = note
        stack.addArrangedSubview(note)
    }

    private func groupView(_ row: RuleGroupRow) -> NSView {
        let box = NSStackView()
        box.orientation = .vertical
        box.alignment = .leading
        box.spacing = 2

        let toggle = NSButton(checkboxWithTitle: row.group.title, target: self, action: #selector(toggleGroup(_:)))
        toggle.identifier = NSUserInterfaceItemIdentifier(row.group.name)
        // 半装的组用 mixed 状态显示 —— 它既不是开也不是关,而勾选框恰好有第三态。
        toggle.allowsMixedState = row.isMixed
        toggle.state = row.isOn ? .on : (row.isMixed ? .mixed : .off)
        box.addArrangedSubview(toggle)

        let detail = caption(row.detail)
        if row.failing > 0 {
            detail.textColor = .systemRed
        }
        box.addArrangedSubview(detail)
        return box
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

    private func heading(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        return label
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
