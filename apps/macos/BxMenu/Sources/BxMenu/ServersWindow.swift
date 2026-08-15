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

    private func render(rows: [ServerRow]) {
        guard let stack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        if rows.isEmpty {
            stack.addArrangedSubview(heading("No servers configured"))
            stack.addArrangedSubview(caption("Add one with: bx setup --name <name> '<link>'"))
            return
        }

        stack.addArrangedSubview(heading("Your traffic leaves from"))
        for row in rows {
            stack.addArrangedSubview(serverView(row))
        }

        let test = NSButton(title: probing ? "Testing…" : "Test All",
                            target: self, action: #selector(probeAll))
        test.bezelStyle = .rounded
        test.isEnabled = !probing
        stack.addArrangedSubview(test)
        // **说清楚这一下会发包,而且是在隧道外面发的。** 它是这个界面唯一一个
        // 会主动联网的动作,用户有权在按之前知道。
        stack.addArrangedSubview(caption(
            "Measures the round trip from this Mac to each server, outside the tunnel. "
            + "Nothing is measured until you press it."))

        stack.addArrangedSubview(separator())
        let line = caption(exitIPLine(probe))
        stack.addArrangedSubview(line)
        let check = NSButton(title: "Check Exit IP", target: self, action: #selector(checkExitIP))
        check.bezelStyle = .rounded
        // 探测进行中就禁掉,免得用户连点几次发出一串请求。
        check.isEnabled = probe != .checking
        stack.addArrangedSubview(check)
    }

    private func serverView(_ row: ServerRow) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.alignment = .centerY
        box.spacing = 10

        let label = NSStackView()
        label.orientation = .vertical
        label.alignment = .leading
        label.spacing = 2
        // 当前那台用实心圆点标出来 —— 与 CLI 的 `●` 同一个符号,两处一致。
        let title = NSTextField(labelWithString: (row.isCurrent ? "● " : "   ") + row.name)
        if row.isCurrent {
            title.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        }
        label.addArrangedSubview(title)
        let detail = caption("   " + row.detail)
        if row.probeFailed {
            detail.textColor = .systemRed
        }
        label.addArrangedSubview(detail)
        box.addArrangedSubview(label)

        if row.isSelectable {
            let use = NSButton(title: "Use", target: self, action: #selector(switchTo(_:)))
            use.bezelStyle = .rounded
            // 名字与出口主机都挂在按钮上,好让确认文案说得出「换到哪」。
            use.identifier = NSUserInterfaceItemIdentifier(row.name)
            use.toolTip = row.entry.host
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
