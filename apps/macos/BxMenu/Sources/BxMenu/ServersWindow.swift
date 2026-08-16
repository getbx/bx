import AppKit

/// 「Servers」窗口 —— 有哪几台、现在在哪台、换一台、以及「现在从哪出去」。
///
/// **为什么是窗口而不是子菜单**(与 RulesWindow 同一个理由):菜单打开时每 2 秒
/// 就地重填一次,进子菜单再移到某一项通常超过 2 秒,那一刻 item 已经被拆掉重填 ——
/// 点了没反应。换服务器这种要停留、要确认的交互不该塞进一个会自我重建的菜单。
///
/// **这个文件只做摆放。** 哪些行、副标题写什么、哪一行能点,全在 ServersModel 的
/// 纯函数里(AppKit 这一半在 CI 里编不了,判断放这儿等于没测)。
final class ServersWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var stack: NSStackView?

    /// 用户选了另一台。参数是名字与出口主机(后者只用来写确认文案)。
    var onSwitch: ((String, String) -> Void)?
    /// 用户点了「测一下现在从哪出去」。
    var onCheckExitIP: (() -> Void)?
    /// 用户点了「Test All」—— 逐台量直连往返时间。
    var onProbe: (() -> Void)?

    /// 正在测。按钮禁掉,免得连点几次发出几串探测。
    var probing = false

    private var probe: ExitIPProbe = .unknown

    func show(rows: [ServerRow], probe: ExitIPProbe) {
        let window = ensureWindow()
        self.probe = probe
        render(rows: rows)
        // LSUIElement 应用不会自动到前台;不激活的话窗口会开在别的应用后面,
        // 用户以为"点了没反应"。
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// 数据更新时就地重画。**窗口不存在就什么都不做** —— 不要因为后台刷新
    /// 把一个用户没打开的窗口弹出来。
    func refreshIfVisible(rows: [ServerRow], probe: ExitIPProbe) {
        guard let window, window.isVisible else { return }
        self.probe = probe
        render(rows: rows)
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 420, height: 300),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )
        window.title = "Servers"
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

    /// **一台一行,两个按钮,没有别的。**
    ///
    /// 与 Routing Rules 同一次清理:上一版有标题行(「Your traffic leaves from」——
    /// 一个叫 Servers 的窗口里说这句是废话)、每台占两行、`Test All` 下面挂着
    /// 一整段解释(那是设计笔记不是界面文案)、还有一条分隔线加一行常驻的
    /// 「Exit IP: not checked」。
    ///
    /// 「测出口会走隧道外面」这条**信息本身是要紧的**(它关系到隐私),但它属于
    /// 按钮的 tooltip,不属于一段常驻正文 —— 常驻的东西会被读一次然后永远忽略。
    private func render(rows: [ServerRow]) {
        guard let stack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        if rows.isEmpty {
            let empty = NSTextField(labelWithString: "No servers yet")
            stack.addArrangedSubview(empty)
            stack.addArrangedSubview(hint("bx setup --name <name> '<link>'"))
            return
        }

        for row in rows {
            stack.addArrangedSubview(serverView(row))
        }

        stack.addArrangedSubview(gap())
        let buttons = NSStackView()
        buttons.orientation = .horizontal
        buttons.spacing = 8
        let test = NSButton(title: probing ? "Testing…" : "Test", target: self, action: #selector(probeAll))
        test.bezelStyle = .rounded
        test.controlSize = .small
        test.isEnabled = !probing
        // 那条要紧但不该常驻的话,挂在这里。
        test.toolTip = "Measures the round trip from this Mac to each server, outside the tunnel."
        buttons.addArrangedSubview(test)

        let check = NSButton(title: "Exit IP", target: self, action: #selector(checkExitIP))
        check.bezelStyle = .rounded
        check.controlSize = .small
        check.isEnabled = probe != .checking
        check.toolTip = "Asks a public service where your traffic appears to come from."
        buttons.addArrangedSubview(check)
        stack.addArrangedSubview(buttons)

        // **只在有话说时才有这一行。** 「not checked」是常态不是信息。
        if probe != .unknown {
            stack.addArrangedSubview(hint(exitIPLine(probe)))
        }
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

    /// 一台一行:名字 + 出口 + 指标,右边是 Use。
    ///
    /// 上一版把出口与指标缩进到第二行 —— 与 Routing Rules 一样的毛病,
    /// 一屏里全是参差不齐的留白。
    private func serverView(_ row: ServerRow) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.alignment = .firstBaseline
        box.spacing = 10

        let title = NSTextField(labelWithString: (row.isCurrent ? "● " : "   ") + row.name)
        if row.isCurrent {
            title.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        }
        box.addArrangedSubview(title)

        let detail = hint(row.detail)
        if row.probeFailed {
            detail.textColor = .systemRed
        }
        detail.setContentHuggingPriority(.defaultLow, for: .horizontal)
        box.addArrangedSubview(detail)

        if row.isSelectable {
            let use = NSButton(title: "Use", target: self, action: #selector(switchTo(_:)))
            use.bezelStyle = .rounded
            use.controlSize = .small
            use.identifier = NSUserInterfaceItemIdentifier(row.name)
            use.toolTip = row.entry.host
            use.setContentHuggingPriority(.defaultHigh, for: .horizontal)
            box.addArrangedSubview(use)
        }
        return box
    }

    @objc private func switchTo(_ sender: NSButton) {
        guard let name = sender.identifier?.rawValue else { return }
        onSwitch?(name, sender.toolTip ?? "")
    }

    @objc private func checkExitIP() {
        onCheckExitIP?()
    }

    @objc private func probeAll() {
        onProbe?()
    }



}

/// 原点在左上的容器。见 ensureWindow 里那段注释。
final class FlippedView: NSView {
    override var isFlipped: Bool { true }
}
