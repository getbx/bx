import AppKit

/// 「Set Up a New Server」表单。
///
/// **它只收目标,不收凭据**(理由见 DeployModel)。填完把命令交给 Terminal,
/// ssh 在那里问它要问的 —— bx 一行密码都不经手。
///
/// 这个文件只做摆放:校验、拼命令、文案全在 DeployModel 的纯函数里
/// (AppKit 这一半在 CI 里编不了,判断放这儿等于没测)。
final class DeployWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var hostField: NSTextField?
    private var userField: NSTextField?
    private var nameField: NSTextField?
    private var preview: NSTextField?
    private var problem: NSTextField?

    /// 用户按了「Open in Terminal」。参数是校验通过的目标。
    var onRun: ((DeployTarget) -> Void)?

    func show() {
        let window = ensureWindow()
        updatePreview()
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 460, height: 340),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )
        window.title = "Set Up a New Server"
        window.isReleasedWhenClosed = false
        window.center()
        window.delegate = self

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 8
        stack.edgeInsets = NSEdgeInsets(top: 16, left: 18, bottom: 16, right: 18)
        stack.translatesAutoresizingMaskIntoConstraints = false

        stack.addArrangedSubview(caption(
            "bx installs itself on a VPS you already own, over ssh."))

        let host = field(placeholder: "1.2.3.4  or  my-vps (an ssh_config alias)")
        hostField = host
        stack.addArrangedSubview(label("Server address"))
        stack.addArrangedSubview(host)

        let user = field(placeholder: "root")
        user.stringValue = "root"
        userField = user
        stack.addArrangedSubview(label("SSH login"))
        stack.addArrangedSubview(user)

        let name = field(placeholder: "tokyo  (optional)")
        nameField = name
        stack.addArrangedSubview(label("Name it in your list"))
        stack.addArrangedSubview(name)
        // **说清楚它不会换出口。** 装好一台不等于用它 —— 这与自动容灾被否掉
        // 是同一条理由,而用户在这个表单上最容易以为「装完就切过去了」。
        stack.addArrangedSubview(caption(
            "Added to your list when it finishes. Your current exit does not change."))

        stack.addArrangedSubview(separator())
        stack.addArrangedSubview(label("Will run"))
        let preview = caption("")
        preview.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
        // 用户看得见将要执行的那条命令 —— 一个会 ssh 到别人机器上装东西的动作,
        // 不该在别处解释过就算数。
        preview.isSelectable = true
        self.preview = preview
        stack.addArrangedSubview(preview)
        stack.addArrangedSubview(caption(deployCredentialNote))

        let problem = caption("")
        problem.textColor = .systemRed
        self.problem = problem
        stack.addArrangedSubview(problem)

        let run = NSButton(title: "Open in Terminal", target: self, action: #selector(run))
        run.bezelStyle = .rounded
        run.keyEquivalent = "\r"
        stack.addArrangedSubview(run)

        guard let content = window.contentView else { return window }
        content.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            stack.topAnchor.constraint(equalTo: content.topAnchor),
            host.widthAnchor.constraint(equalToConstant: 400),
            user.widthAnchor.constraint(equalToConstant: 400),
            name.widthAnchor.constraint(equalToConstant: 400),
        ])
        self.window = window
        return window
    }

    private func currentTarget() -> DeployTarget {
        DeployTarget(
            host: hostField?.stringValue ?? "",
            user: userField?.stringValue ?? "",
            name: nameField?.stringValue ?? ""
        )
    }

    private func updatePreview() {
        let target = currentTarget()
        preview?.stringValue = deployCommandLine(target)
        // 打字过程中不红脸:只有按下按钮才报问题。
        problem?.stringValue = ""
    }

    @objc private func run() {
        let target = currentTarget()
        if let issue = deployValidationError(target) {
            problem?.stringValue = issue
            return
        }
        problem?.stringValue = ""
        preview?.stringValue = deployCommandLine(target)
        onRun?(target)
        window?.close()
    }

    func controlTextDidChange(_ notification: Notification) {
        updatePreview()
    }

    private func label(_ text: String) -> NSTextField {
        let field = NSTextField(labelWithString: text)
        field.font = .boldSystemFont(ofSize: NSFont.smallSystemFontSize)
        return field
    }

    private func caption(_ text: String) -> NSTextField {
        let field = NSTextField(wrappingLabelWithString: text)
        field.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        field.textColor = .secondaryLabelColor
        field.preferredMaxLayoutWidth = 400
        return field
    }

    private func field(placeholder: String) -> NSTextField {
        let field = NSTextField()
        field.placeholderString = placeholder
        field.delegate = self
        return field
    }

    private func separator() -> NSView {
        let line = NSBox()
        line.boxType = .separator
        return line
    }
}

extension DeployWindowController: NSTextFieldDelegate {}
