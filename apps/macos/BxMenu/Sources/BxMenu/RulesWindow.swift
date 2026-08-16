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
        // **必须是翻转坐标系。** NSView 默认原点在左下,于是文档视图比可视区
        // 小时内容会**沉到窗口底部** —— 真机截图上那一大片空白就是这么来的,
        // 它看起来像刻意的留白,其实是坐标系。
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

    /// **这个窗口只有三样东西:开关、你自己写的、以及去看配置。**
    ///
    /// 上一版还有标题行、顶部告警、页脚说明、完整路径和分隔线 —— 而它们各自
    /// 都在重复别处已经说过的话:
    ///
    ///   · 顶部「Apple isn't working」与那一行的红色 `6 failed` 是同一件事
    ///   · 「Changes apply when you reconnect」是常驻的,而**改完本来就会弹
    ///     一次提示**(offerReconnectAfterRuleChange),所以它一年到头只是
    ///     在占地方
    ///   · 两块内容一个带勾选框、一个是灰色等宽字,已经分得清,不需要小标题
    ///   · 路径没人会去手打,按钮就是干这个的
    ///
    /// 删掉之后剩下的每一行都在回答一个问题:哪些开着、我自己写了什么、
    /// 怎么去改。**分隔靠留白,不靠线**。
    private func render(rows: [RuleGroupRow], custom: [String], configPath: String) {
        guard let stack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        for row in rows {
            stack.addArrangedSubview(groupRow(row))
        }

        if !custom.isEmpty {
            // 只读:菜单没有资格替用户删他手写的规则,而**没有勾选框本身就说明了
            // 这一点** —— 不必再写一句「这些不能改」。
            stack.addArrangedSubview(gap())
            for pattern in custom {
                let label = NSTextField(labelWithString: pattern)
                label.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
                label.textColor = .secondaryLabelColor
                stack.addArrangedSubview(label)
            }
        }

        if !configPath.isEmpty {
            stack.addArrangedSubview(gap())
            let reveal = NSButton(title: "Show Config", target: self, action: #selector(revealConfig))
            reveal.bezelStyle = .rounded
            reveal.controlSize = .small
            stack.addArrangedSubview(reveal)
        }
    }

    /// 一段留白。**分隔靠它,不靠分隔线** —— 三五行内容之间画线是给长文档用的。
    private func gap() -> NSView {
        let spacer = NSView()
        spacer.translatesAutoresizingMaskIntoConstraints = false
        spacer.heightAnchor.constraint(equalToConstant: 6).isActive = true
        return spacer
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


}
