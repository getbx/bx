import AppKit

/// 「Set Up a New Server」窗口。
///
/// 地址、SSH 端口、登录名、密码、名字 —— 填完点一下,进度与结果都在这扇窗口里,**全程不出现
/// 终端**(所有者 2026-09-30:小白碰不了终端)。密码的去向见 DeployModel 顶部:只经 stdin 交给
/// `bx server deploy --password-stdin`,这里不存;开始部署的那一刻清空密码框。
///
/// 这个文件只做摆放与状态切换;校验、参数、进度、文案全在 DeployModel 的纯函数里。
final class DeployWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var hostField: NSTextField?
    private var portField: NSTextField?
    private var userField: NSTextField?
    private var passwordField: NSSecureTextField?
    private var nameField: NSTextField?
    private var formRows: [NSView] = []
    private var stepRows: [(id: String, mark: NSTextField, title: NSTextField)] = []
    private var progressBox: NSStackView?
    private var headline: NSTextField?
    private var message: NSTextField?
    private var details: NSTextField?
    private var closeButton: NSButton?
    private var actionButton: NSButton?
    private var primaryButton: NSButton?

    private var progress = DeployProgress()
    private var running = false
    private var lastTarget = DeployTarget()
    private var pendingAction: DeployRetryAction?
    private var setUpLink: String?
    private var setUpUDP: String?
    private var sawFinalEvent = false
    /// 成功之后主按钮是「Done」,再点就是关窗。
    private var finishedOK = false

    /// 用户点了部署。密码只在这一次调用里传过去,本类不留。
    var onRun: ((DeployTarget, String, DeployOptions) -> Void)?
    var onCancel: (() -> Void)?
    var onOpenServers: (() -> Void)?
    var onSetUp: ((String, String?) -> Void)?

    func show() {
        let window = ensureWindow()
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// 切换界面语言之后调。**跑着的时候不重建**(会丢掉进度);没开着就什么都不做。
    /// 保住用户已经填的格子(密码除外 —— 它不该在任何重建里被复制)。
    func relocalize() {
        guard let old = window, old.isVisible, !running else { return }
        let typed = currentTarget()
        let frame = old.frame
        old.delegate = nil
        old.close()
        window = nil
        let fresh = ensureWindow()
        hostField?.stringValue = typed.host
        portField?.stringValue = typed.sshPort
        userField?.stringValue = typed.user
        nameField?.stringValue = typed.name
        fresh.setFrame(frame, display: false)
        fresh.makeKeyAndOrderFront(nil)
    }

    // MARK: 由 main.swift 喂进来

    func handle(_ event: DeployEvent) {
        progress.apply(event)
        progressBox?.isHidden = false
        renderSteps()
        switch event.event {
        case "done":
            sawFinalEvent = true
            finish(success: event)
        case "error":
            sawFinalEvent = true
            finish(failure: event)
        default:
            break
        }
    }

    /// 进程退出了。正常情况下 done / error 那一行已经到过;没到(被取消、崩了)就在这里收尾。
    func processEnded(status: Int32, stderrTail: String) {
        guard running, !sawFinalEvent else { return }
        let code = status == 15 || status == 9 ? "canceled" : "install_failed"
        if code == "canceled" {
            running = false
            headline?.stringValue = L("Canceled")
            message?.stringValue = L("The server may be half set up. Deploying again is safe.")
            details?.isHidden = true
            setButtons(close: L("Close"), action: nil, primary: L("Try Again"))
            setForm(enabled: true)
            return
        }
        finish(failure: DeployEvent(event: "error", code: code, detail: stderrTail))
    }

    // MARK: 布局

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 480, height: 420),
                              styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = L("Set Up a New Server")
        window.isReleasedWhenClosed = false
        window.center()
        window.delegate = self

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = MenuStyle.rowSpacing
        stack.edgeInsets = MenuStyle.insets
        stack.translatesAutoresizingMaskIntoConstraints = false

        let intro = menuCaption(L("Enter the details your provider gave you when you bought the server. bx installs everything and adds it to your server list."))
        intro.preferredMaxLayoutWidth = 440
        stack.addFullWidthRow(intro)

        let host = textField(placeholder: "203.0.113.9")
        let port = textField(placeholder: "22")
        port.stringValue = "22"
        let user = textField(placeholder: "root")
        user.stringValue = "root"
        let password = NSSecureTextField()
        password.placeholderString = L("Leave empty if you log in with a key")
        let name = textField(placeholder: L("Optional — the address is used if empty"))
        hostField = host; portField = port; userField = user; passwordField = password; nameField = name

        let grid = NSGridView(views: [
            [label(L("Server address")), host],
            [label(L("SSH port")), port],
            [label(L("Login")), user],
            [label(L("Password")), password],
            [label(L("Name in your list")), name],
        ])
        grid.rowSpacing = 8
        grid.columnSpacing = 10
        grid.column(at: 0).xPlacement = .trailing
        for field in [host, user, password, name] as [NSView] {
            field.widthAnchor.constraint(equalToConstant: 300).isActive = true
        }
        port.widthAnchor.constraint(equalToConstant: 80).isActive = true
        stack.addFullWidthRow(grid)

        // 两件事合成一行,都是**披露**,不许挪进 tooltip:密码去哪了、出口会不会变。
        let note = menuCaption(L("The password is used once for this setup and is not saved. Your current exit does not change."))
        note.preferredMaxLayoutWidth = 440
        stack.addFullWidthRow(note)
        formRows = [intro, grid, note]

        let box = NSStackView()
        box.orientation = .vertical
        box.alignment = .leading
        box.spacing = 4
        stepRows = deploySteps.map { id in
            let mark = NSTextField(labelWithString: "○")
            mark.textColor = .tertiaryLabelColor
            mark.widthAnchor.constraint(equalToConstant: 18).isActive = true
            let title = NSTextField(labelWithString: deployStepTitle(id))
            title.textColor = .secondaryLabelColor
            let row = NSStackView(views: [mark, title])
            row.orientation = .horizontal
            row.spacing = 6
            box.addArrangedSubview(row)
            return (id, mark, title)
        }
        box.isHidden = true
        progressBox = box
        stack.addFullWidthRow(box)

        let headline = NSTextField(labelWithString: "")
        headline.font = .systemFont(ofSize: NSFont.systemFontSize, weight: .semibold)
        headline.isHidden = true
        self.headline = headline
        stack.addFullWidthRow(headline)
        let message = NSTextField(wrappingLabelWithString: "")
        message.preferredMaxLayoutWidth = 440
        self.message = message
        stack.addFullWidthRow(message)
        let details = NSTextField(wrappingLabelWithString: "")
        details.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
        details.textColor = .secondaryLabelColor
        details.isSelectable = true
        details.preferredMaxLayoutWidth = 440
        details.isHidden = true
        self.details = details
        stack.addFullWidthRow(details)

        let close = menuButton(L("Close"), target: self, action: #selector(closeOrCancel))
        let action = menuButton("", target: self, action: #selector(runAction))
        action.isHidden = true
        // 这扇窗口唯一的 primary:部署本身。
        let primary = menuButton(L("Set Up Server"), weight: .primary, target: self, action: #selector(run))
        closeButton = close; actionButton = action; primaryButton = primary
        stack.addFullWidthRow(menuButtonRow([close, action, primary], trailing: true))

        guard let content = window.contentView else { return window }
        content.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            stack.topAnchor.constraint(equalTo: content.topAnchor),
            stack.bottomAnchor.constraint(lessThanOrEqualTo: content.bottomAnchor),
        ])
        self.window = window
        return window
    }

    private func label(_ text: String) -> NSTextField {
        NSTextField(labelWithString: text)
    }

    private func textField(placeholder: String) -> NSTextField {
        let field = NSTextField()
        field.placeholderString = placeholder
        return field
    }

    private func currentTarget() -> DeployTarget {
        DeployTarget(host: hostField?.stringValue ?? "", sshPort: portField?.stringValue ?? "22",
                     user: userField?.stringValue ?? "", name: nameField?.stringValue ?? "")
    }

    // MARK: 动作

    @objc private func run() {
        if finishedOK {
            window?.close()
            return
        }
        start(options: DeployOptions())
    }

    @objc private func runAction() {
        if let link = setUpLink {
            window?.close()
            onSetUp?(link, setUpUDP)
            return
        }
        switch pendingAction {
        case .reinstall?:
            start(options: DeployOptions(reinstall: true))
        case .forgetHostKey?:
            start(options: DeployOptions(forgetHostKey: true))
        case nil:
            window?.close()
            onOpenServers?()
        }
    }

    private func start(options: DeployOptions) {
        let target = currentTarget()
        if let issue = deployValidationError(target) {
            showResult(headline: L("Check the form"), message: issue, details: nil)
            return
        }
        let password = passwordField?.stringValue ?? ""
        // **开始即清空。** 失败要重试时用户再填一次 —— 比让它在窗口里一直待着好。
        passwordField?.stringValue = ""
        var options = options
        options.hasPassword = !password.isEmpty
        lastTarget = target
        progress = DeployProgress()
        pendingAction = nil
        setUpLink = nil
        setUpUDP = nil
        sawFinalEvent = false
        finishedOK = false
        running = true
        setForm(enabled: false)
        progressBox?.isHidden = false
        renderSteps()
        headline?.isHidden = true
        message?.stringValue = L("This usually takes one to three minutes.")
        details?.isHidden = true
        setButtons(close: L("Cancel"), action: nil, primary: nil)
        onRun?(target, password, options)
    }

    @objc private func closeOrCancel() {
        if running {
            onCancel?()
            return
        }
        window?.close()
    }

    func windowShouldClose(_ sender: NSWindow) -> Bool {
        // 跑着的时候关窗 = 取消;不许留一个看不见的部署在后台跑。
        if running { onCancel?() }
        return true
    }

    // MARK: 渲染

    private func renderSteps() {
        for row in stepRows {
            switch progress.state(row.id) {
            case .pending:
                row.mark.stringValue = "○"; row.mark.textColor = .tertiaryLabelColor
                row.title.textColor = .secondaryLabelColor
            case .running:
                row.mark.stringValue = "◐"; row.mark.textColor = .controlAccentColor
                row.title.textColor = .labelColor
            case .done:
                row.mark.stringValue = "✓"; row.mark.textColor = .systemGreen
                row.title.textColor = .labelColor
            case .failed:
                row.mark.stringValue = "✕"; row.mark.textColor = .systemRed
                row.title.textColor = .labelColor
            }
        }
    }

    private func finish(success event: DeployEvent) {
        running = false
        let r = deployResult(event)
        setUpLink = r.setUpLink
        setUpUDP = r.setUpUDP
        showResult(headline: r.headline, message: r.detail, details: nil)
        if r.setUpLink != nil {
            setButtons(close: L("Later"), action: L("Set Up bx With This Server"), primary: nil)
        } else {
            finishedOK = true
            setButtons(close: nil, action: r.canOpenServers ? L("Open Servers") : nil, primary: L("Done"))
        }
    }

    private func finish(failure event: DeployEvent) {
        running = false
        let f = deployFailure(event.code ?? "")
        pendingAction = f.action
        showResult(headline: f.headline, message: f.advice, details: event.detail)
        setForm(enabled: true)
        switch f.action {
        case .reinstall?:
            setButtons(close: L("Close"), action: L("Reinstall"), primary: nil)
        case .forgetHostKey?:
            setButtons(close: L("Close"), action: L("I Reinstalled It"), primary: nil)
        case nil:
            setButtons(close: L("Close"), action: nil, primary: L("Try Again"))
        }
    }

    private func showResult(headline text: String, message body: String, details extra: String?) {
        headline?.stringValue = text
        headline?.isHidden = false
        message?.stringValue = body
        if let extra, !extra.isEmpty {
            details?.stringValue = extra
            details?.isHidden = false
        } else {
            details?.isHidden = true
        }
    }

    private func setButtons(close: String?, action: String?, primary: String?) {
        closeButton?.title = close ?? ""
        closeButton?.isHidden = close == nil
        actionButton?.title = action ?? ""
        actionButton?.isHidden = action == nil
        primaryButton?.title = primary ?? ""
        primaryButton?.isHidden = primary == nil
    }

    private func setForm(enabled: Bool) {
        for field in [hostField, portField, userField, passwordField, nameField] {
            field?.isEnabled = enabled
        }
    }
}
