import AppKit

/// 「Servers」窗口 —— 这条隧道现在怎么样,以及不好的话我能换到哪儿。
///
/// **为什么是窗口而不是子菜单**(与 RulesWindow 同一个理由):菜单打开时每 2 秒
/// 就地重填一次,进子菜单再移到某一项通常超过 2 秒,那一刻 item 已经被拆掉重填 ——
/// 点了没反应。换服务器这种要停留、要确认的交互不该塞进一个会自我重建的菜单。
///
/// **这个文件只做摆放。** 哪些行、每一行写什么、哪一行能点、哪句话该说,全在
/// `ServersModel` 的纯函数里(AppKit 这一半在 CI 里编不了,判断放这儿等于没测)。
/// 它连 `ServerList` 与 `CoreRuntime` 都不自己拆:拿到那两样之后立刻交给
/// `currentServerPanel` / `otherServerRows` / `serverListEmptyReason`,
/// **一次 `reachable` 都不自己判**(那份判据只有 `answeringCore` 一份)。
final class ServersWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var stack: NSStackView?
    private var scroll: NSScrollView?

    /// 用户选了另一台。参数是名字与出口主机(后者只用来写确认文案)。
    var onSwitch: ((String, String) -> Void)?
    /// 用户点了「测一下现在从哪出去」。
    var onCheckExitIP: (() -> Void)?
    /// 用户点了「Test All」—— 逐台量直连往返时间。
    var onProbe: (() -> Void)?
    /// 用户点了「Set Up a New VPS…」(从一级菜单搬进来的「Set Up a New Server…」)。
    var onDeploy: (() -> Void)?
    /// 用户点了「Add Existing Server…」—— 贴一条链接加进清单并切换过去(spec §4)。
    var onAddServer: (() -> Void)?
    /// `⋯` 里的删除。参数是名字、出口主机、以及**这一台此刻在不在承载流量**
    /// (三态)—— 三样都只用来写确认文案。
    ///
    /// 最后那一样必须从这里带过去,**不许让调用方自己再判一遍**:那个判据只有
    /// 一份(`serverTrafficState`,门是 `answeringCore`),而 main.swift 手里
    /// 那份清单里的 `running` 在 Core 静默时是可能陈旧的。
    ///
    /// **它是 `ServerTrafficState` 不是 `Bool`**:Core 静默、或者 Guardian 自己
    /// 说不出是哪一台时,`Bool` 会把「没问出来」交成 false,而确认框对 false
    /// 一个字都不说 —— 在这个窗口里沉默读作「确认闲着」。
    var onRemove: ((String, String, ServerTrafficState) -> Void)?
    /// `⋯` 里的换链接。
    var onReplaceLink: ((String) -> Void)?

    /// 正在测。按钮禁掉,免得连点几次发出几串探测。
    var probing = false

    private var list = ServerList()
    private var core: CoreRuntime?
    private var probe: ExitIPProbe = .unknown
    /// 正在切到哪一台。**非 nil ⇒ 每个 `Use` 都禁掉**,而那一行原地说
    /// 「Switching…」—— 此前这个标志是 `main.swift` 的私有量,于是点了确认之后
    /// 屏幕上二十几秒什么都不发生,再点一次连对话框都不弹(被在飞守卫挡掉)。
    private var switchingTo: String?
    /// 这一版 Guardian 认不认得 remove / replace。**认不得就一个动词都不画**
    /// (`serverEditingAvailable`,判据在纯模型里):对着只声明 `servers` 的
    /// 那一版发 remove,它会**换到那一台**去。
    private var canEdit = false

    /// 窗口是否开着。**供环境刷新路径判断「有没有人在看」**——rules/servers 改
    /// 按需拉之后,这是唯一能回答「要不要为这个窗口拉一次新数据」的信号:窗口关着
    /// 就不拨(按需的本意),窗口开着就说明有人正盯着,这时按需拉一次不是违背
    /// 按需,是它的本意。
    var isVisible: Bool { window?.isVisible ?? false }

    /// **参数而不是存储属性,理由与上一版那个 `emptyReason` 逐字相同**:它们
    /// 必须与这一份 `list` 同源同刻,而一个「记得设」的属性迟早会陈旧,编译器
    /// 也不会提醒谁忘了。`core` 尤其如此 —— 漏传它不会有任何编译错误(它是
    /// 可空的),而后果是当前那一块永远显示「Core not answering」,界面看起来
    /// 完全正常。
    func show(list: ServerList, core: CoreRuntime?, probe: ExitIPProbe,
              switchingTo: String?, canEdit: Bool) {
        let window = ensureWindow()
        adopt(list: list, core: core, probe: probe, switchingTo: switchingTo, canEdit: canEdit)
        render(preservingScroll: false)
        // LSUIElement 应用不会自动到前台;不激活的话窗口会开在别的应用后面,
        // 用户以为"点了没反应"。
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// 数据更新时就地重画。**窗口不存在就什么都不做** —— 不要因为后台刷新
    /// 把一个用户没打开的窗口弹出来。
    func refreshIfVisible(list: ServerList, core: CoreRuntime?, probe: ExitIPProbe,
                          switchingTo: String?, canEdit: Bool) {
        guard let window, window.isVisible else { return }
        adopt(list: list, core: core, probe: probe, switchingTo: switchingTo, canEdit: canEdit)
        render(preservingScroll: true)
    }

    private func adopt(list: ServerList, core: CoreRuntime?, probe: ExitIPProbe,
                       switchingTo: String?, canEdit: Bool) {
        self.list = list
        self.core = core
        self.probe = probe
        self.switchingTo = switchingTo
        self.canEdit = canEdit
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 520, height: 380),
            // **`.resizable` 与那几格的 toolTip 是同一件事的两半**(与 Rules /
            // Traffic by App 同一条):当前那一块会因为传输名字长而截断,而这个
            // 窗口不横向滚动 —— 少了把窗口拉宽这条出路,被截掉的那半永久不可见。
            styleMask: [.titled, .closable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Servers"
        window.isReleasedWhenClosed = false
        window.center()
        window.delegate = self

        guard let content = window.contentView else { return window }
        // 组装走共用原语(MenuLayout.swift)。四扇窗口此前各抄了一份,
        // 而那份拷贝里有同一个缺陷:行没被钉到容器宽度,塞不下时整行溢出
        // 到窗口外面,行尾按钮点不到 —— 2026-09-17 离屏快照量出来的。
        let (scroll, stack) = makeScrollingStack(
            insets: NSEdgeInsets(top: 16, left: 18, bottom: 16, right: 18),
            spacing: 10
        )
        pinToEdges(scroll, in: content)
        self.stack = stack
        self.scroll = scroll
        self.window = window
        return window
    }

    /// **当前那台一整块,其余是候选**(spec §4)。
    ///
    /// 上一版是一份平列的清单:一行一台、一句灰色 detail、一个 Use。它答得出
    /// 「有哪几台」,答不出用户打开这扇窗时脑子里的第一个问题 ——「我现在这条
    /// 隧道怎么样」。而答案早就每 2 秒落进菜单进程了(`/v1/status` 的
    /// `CoreRuntime`:传输、实时延迟、隧道健不健康、UDP 走哪儿),只是这扇窗的
    /// 管道一样都不带。**把版面给正在起作用的那一个**,与 Rules 窗口「有问题的
    /// 排最前、健康的一个字不说」是同一条判断的另一面。
    private func render(preservingScroll: Bool) {
        guard let stack else { return }
        // 滚动位置在拆视图之前取。**AppKit 的重建会把它清零**,而这个窗口跟着
        // 环境刷新重建 —— 不保住的话用户每翻到一半就被拽回顶部。
        let offset = preservingScroll ? scroll?.contentView.bounds.origin : nil
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        // 配置路径摆右上角(与 Rules 窗口同一处)。它会截断,而这个窗口不横向
        // 滚动 —— toolTip 是那条出路。
        if !list.configPath.isEmpty {
            stack.addFullWidthRow(configPathRow(list.configPath))
        }

        if let panel = currentServerPanel(list: list, core: core) {
            stack.addFullWidthRow(sectionTitle("Currently using"))
            stack.addFullWidthRow(currentPanelView(panel))
            stack.addFullWidthRow(gap())
        }

        let rows = otherServerRows(list: list, core: core)
        if !rows.isEmpty {
            stack.addFullWidthRow(sectionTitle("Other servers"))
            for row in rows {
                stack.addFullWidthRow(serverView(row))
            }
        }

        // **空清单不是死路,而这里此前就是一条死路。**
        //
        // 原来这一支摆一句「No servers yet」加一行 `bx setup --name …` 然后
        // `return` —— 而按钮带是在那个 return 之后才画的。于是零行时:没有
        // Add Server、没有 New Server、没有 Test、没有 Exit IP,只剩一条
        // **`urfave/cli` 会直接拒掉的命令**(`bx setup` 没有 `--name` 这个 flag)。
        // 空清单恰恰是最需要 Add Server… 的那一刻。
        //
        // 措辞也不再由行数决定,而由**配置里到底有没有 servers 清单**决定
        // (`serverListEmptyReason`):`bx setup` 从不写那个清单,所以对多数用户
        // 说「还没有服务器」是一句当场就能被证伪的假话 —— bx 此刻正跑着一台。
        //
        // **两句空状态的条件不是同一个**,所以并排放两条 if 而不是 if/else:
        // 清单里只有一台时 `serverListEmptyReason` 返回 nil,而候选行是空的,
        // 那一档由 `otherServersEmptyNote` 说。
        if let reason = serverListEmptyReason(list: list) {
            stack.addFullWidthRow(wrapped(reason))
        }
        if let note = otherServersEmptyNote(list: list, core: core) {
            stack.addFullWidthRow(wrapped(note))
        }

        stack.addFullWidthRow(gap())
        stack.addFullWidthRow(buttonBar())

        // **只在有话说时才有这一行。** 「not checked」是常态不是信息。
        if probe != .unknown {
            stack.addFullWidthRow(hint(exitIPLine(probe)))
        }

        if let offset, let scroll {
            // **先布局再滚。** 少了这一步滚的是按旧内容算出来的坐标,于是
            // 表变长/变短的那一拍位置照样会跳(Diagnostics 那两页同款)。
            scroll.documentView?.layoutSubtreeIfNeeded()
            scroll.contentView.scroll(to: offset)
            scroll.reflectScrolledClipView(scroll.contentView)
        }
    }

    /// 底部那条按钮带。**它在任何一支之外无条件画** —— 见 render 里那段注释:
    /// 把它挪进某个分支里,就是刚修掉的那个 bug 的镜像。
    private func buttonBar() -> NSView {
        let buttons = NSStackView()
        buttons.orientation = .horizontal
        buttons.spacing = 8
        let test = NSButton(title: probing ? "Testing…" : "Test All", target: self, action: #selector(probeAll))
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

        // 两个从一级菜单搬进来的入口:它们说的都是「服务器」这件事,归这里。
        // **「New Server…」与「Add Server…」曾经并排站着,而它们是两件完全不同的事**:
        // 前者 ssh 进一台空 VPS 把 bx server 装上去,后者只是把一条已有的链接加进清单。
        // 名字近义、动作不同,而点错第一个的代价是对着一台陌生机器跑 ssh。
        // 2026-09-18 用离屏快照第一次并排看到它们之后改名:现在一个说「我有台空机器」,
        // 另一个说「我已经有链接了」。
        let deploy = NSButton(title: "Set Up a New VPS…", target: self, action: #selector(deployServer))
        deploy.bezelStyle = .rounded
        deploy.controlSize = .small
        deploy.toolTip = "Install bx server on a fresh VPS over SSH."
        buttons.addArrangedSubview(deploy)

        let add = NSButton(title: "Add Existing Server…", target: self, action: #selector(addServer))
        add.bezelStyle = .rounded
        add.controlSize = .small
        add.toolTip = "Paste a bx link to add a server and switch to it. The previous server stays in the list."
        buttons.addArrangedSubview(add)

        return buttons
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

    private func wrapped(_ text: String) -> NSTextField {
        let label = NSTextField(wrappingLabelWithString: text)
        label.preferredMaxLayoutWidth = 460
        return label
    }

    private func sectionTitle(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
        label.textColor = .secondaryLabelColor
        return label
    }

    private func configPathRow(_ path: String) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.spacing = 8
        let spacer = NSView()
        spacer.setContentHuggingPriority(.defaultLow, for: .horizontal)
        box.addArrangedSubview(spacer)
        let label = hint(path)
        label.lineBreakMode = .byTruncatingHead
        // 截断了还看得全:这个窗口不横向滚动,少了 toolTip 那半路径就永久不可见。
        label.toolTip = path
        label.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        box.addArrangedSubview(label)
        box.setHuggingPriority(.defaultLow, for: .horizontal)
        return box
    }

    /// 当前那台的一整块:身份来自配置,纵深来自 Core。
    ///
    /// **哪几行有值全由 `CurrentServerPanel` 说了算。** Core 不答话时它把那几个
    /// 字段一律留成 nil 并给一句 `coreSilentNote` —— 于是这里少画几行,而不是
    /// 画出一行撒谎的 `0 ms`。
    private func currentPanelView(_ panel: CurrentServerPanel) -> NSView {
        let box = NSStackView()
        box.orientation = .vertical
        box.alignment = .leading
        box.spacing = 4

        let head = NSStackView()
        head.orientation = .horizontal
        head.alignment = .firstBaseline
        head.spacing = 10
        // **`●` 只在 Core 确认过的时候才加粗打点。** 热切换是先写配置再切,
        // 所以切换失败的那一刻配置已经是新那台了 —— 此时给它打点就是断言用户
        // 的流量从一台其实没在用的服务器出去。
        let dot = panel.runningConfirmed ? "● " : "○ "
        let title = NSTextField(labelWithString: dot + panel.name)
        title.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        head.addArrangedSubview(title)
        let endpoint = hint(panel.endpoint)
        endpoint.lineBreakMode = .byTruncatingTail
        endpoint.toolTip = panel.endpoint
        endpoint.setContentHuggingPriority(.defaultLow, for: .horizontal)
        head.addArrangedSubview(endpoint)
        head.setHuggingPriority(.defaultLow, for: .horizontal)
        if canEdit {
            head.addArrangedSubview(moreButton(name: panel.name, host: panel.host,
                                              isCurrent: true, traffic: panel.traffic))
        }
        box.addArrangedSubview(head)

        if let note = panel.coreSilentNote {
            let label = hint(note)
            label.textColor = .secondaryLabelColor
            box.addArrangedSubview(label)
        }
        if let line = panel.statusLine {
            let label = hint(line)
            label.lineBreakMode = .byTruncatingTail
            label.toolTip = line
            if panel.statusLineIsBad { label.textColor = .systemRed }
            box.addArrangedSubview(label)
        }
        if let line = panel.udpLine {
            let label = hint(line)
            label.lineBreakMode = .byTruncatingTail
            label.toolTip = line
            box.addArrangedSubview(label)
        }
        if let line = panel.probeLine {
            // **与候选行同一个三态判据**(`ProbePresentation.isFailure`):
            // 「没测过」不画、「没测成」是灰的,只有实测失败才红。窗口自己看
            // `reachable` 就会把 bx 没在跑那一档也画红 —— 把一台好服务器说成
            // 坏的,而这一半代码一行 Swift 测试都盖不到。
            let label = hint(line)
            if panel.probe.isFailure { label.textColor = .systemRed }
            label.lineBreakMode = .byTruncatingTail
            label.toolTip = line
            box.addArrangedSubview(label)
        }
        if let line = panel.throughput {
            box.addArrangedSubview(hint(line))
        }
        if let note = panel.runningNote {
            // **这一句是分歧,不是背景信息。** 配置指着这一台而 Core 在跑别的
            // (或者问不出来),用户真正的出口就不在这一块里 —— 画成灰色会让它
            // 混进上面那几行观测。
            let label = hint(note)
            label.textColor = .systemOrange
            label.lineBreakMode = .byWordWrapping
            label.preferredMaxLayoutWidth = 460
            box.addArrangedSubview(label)
        }
        return box
    }

    /// 一台候选一行:名字、`host:port`、探测呈现,右边是 Use 与 `⋯`。
    private func serverView(_ row: ServerRow) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.alignment = .firstBaseline
        box.spacing = 10

        let title = NSTextField(labelWithString: row.name)
        box.addArrangedSubview(title)

        let endpoint = hint(row.endpoint)
        endpoint.lineBreakMode = .byTruncatingTail
        endpoint.toolTip = row.endpoint
        box.addArrangedSubview(endpoint)

        if let note = row.note {
            let detail = hint(note)
            // **判据在纯模型里**(ProbePresentation.isFailure):只有「测过而且没通」
            // 才画红。窗口自己看 `reachable` 就会把「没测成」也画成红的 —— 那等于把
            // 一整排好服务器说成坏的,而这一半代码一行 Swift 测试都盖不到。
            if row.probe.isFailure {
                detail.textColor = .systemRed
            }
            detail.lineBreakMode = .byTruncatingTail
            detail.toolTip = note
            detail.setContentHuggingPriority(.defaultLow, for: .horizontal)
            box.addArrangedSubview(detail)
        }
        // 热切失败之后,用户真正的出口就在这一行 —— 不点名的话他会盯着上面
        // 那块加粗的当前那台找原因。
        if let running = row.runningNote {
            let label = hint(running)
            label.textColor = .systemOrange
            box.addArrangedSubview(label)
        }
        box.setHuggingPriority(.defaultLow, for: .horizontal)

        if row.isSelectable {
            // **切换中要看得见。** 此前 switchInFlight 是 main.swift 的私有量,
            // 于是点了确认之后二十几秒屏幕上什么都不发生,再点一次连对话框都不弹。
            let switching = switchingTo != nil
            let mine = switchingTo == row.name
            let use = NSButton(title: mine ? "Switching…" : "Use",
                               target: self, action: #selector(switchTo(_:)))
            use.bezelStyle = .rounded
            use.controlSize = .small
            use.identifier = NSUserInterfaceItemIdentifier(row.name)
            use.toolTip = row.entry.host
            use.isEnabled = !switching
            use.setContentHuggingPriority(.defaultHigh, for: .horizontal)
            box.addArrangedSubview(use)
        }
        if canEdit {
            box.addArrangedSubview(moreButton(name: row.name, host: row.entry.host,
                                              isCurrent: false, traffic: row.traffic))
        }
        return box
    }

    /// 那两个改清单的动词所在的 `⋯`(Add 表单里那个 UDP 框是第三个动词,
    /// 它不在这道门后面 —— 见 `serverEditingAvailable` 的注释)。
    ///
    /// **只有 `serverEditingAvailable` 说这一版认得 remove / replace 时才画**
    /// (`canEdit`,判据在纯模型里):只声明 `servers` 的那一版收到 remove 会
    /// **换到那一台**去,而那正是这个设计唯一明令禁止的事。
    private func moreButton(name: String, host: String, isCurrent: Bool,
                            traffic: ServerTrafficState) -> NSButton {
        let more = NSButton(title: "⋯", target: self, action: #selector(showRowMenu(_:)))
        more.bezelStyle = .rounded
        more.controlSize = .small
        // `name|host|current` 塞进 identifier:回调要的就是这三样,而从界面上的
        // 文字反推它们会在名字里含分隔符的时候悄悄取错一台。
        more.identifier = NSUserInterfaceItemIdentifier(
            rowMenuKey(name: name, host: host, isCurrent: isCurrent, traffic: traffic))
        more.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        more.toolTip = "More actions for \(name)"
        return more
    }

    private func rowMenuKey(name: String, host: String, isCurrent: Bool,
                            traffic: ServerTrafficState) -> String {
        "\(isCurrent ? "1" : "0")\u{1F}\(traffic.rawValue)\u{1F}\(host)\u{1F}\(name)"
    }

    private func parseRowMenuKey(_ raw: String)
        -> (name: String, host: String, isCurrent: Bool, traffic: ServerTrafficState)?
    {
        // 名字在最后一段:主机与那两段标志都不含分隔符,而名字是用户起的。
        let parts = raw.components(separatedBy: "\u{1F}")
        guard parts.count >= 4 else { return nil }
        // **认不出的那一段退回 `.unconfirmed`,不是 `.idle`** —— 编解码漂了的
        // 后果不该是替一份从没收到过的观测宣布「这台闲着」。
        let traffic = ServerTrafficState(rawValue: parts[1]) ?? .unconfirmed
        return (parts[3...].joined(separator: "\u{1F}"), parts[2], parts[0] == "1", traffic)
    }

    @objc private func showRowMenu(_ sender: NSButton) {
        guard let raw = sender.identifier?.rawValue, let row = parseRowMenuKey(raw) else { return }
        let menu = NSMenu()
        let replace = NSMenuItem(title: "Replace Link…", action: #selector(replaceLink(_:)), keyEquivalent: "")
        replace.target = self
        replace.representedObject = row.name
        menu.addItem(replace)

        let remove = NSMenuItem(title: "Remove…", action: #selector(removeServer(_:)), keyEquivalent: "")
        remove.target = self
        remove.representedObject = raw
        // **当前那台的删除置灰。** 删掉正在用的那一台会让下一次拨号无处可去,
        // 服务端也拦(409 servers_remove_current)—— 灰掉是为了让用户在点之前
        // 就知道,而不是点完读一句拒绝。
        remove.isEnabled = !row.isCurrent
        remove.toolTip = row.isCurrent
            ? "Switch to another server first, then you can remove this one."
            : nil
        menu.addItem(remove)
        menu.popUp(positioning: nil, at: NSPoint(x: 0, y: sender.bounds.height), in: sender)
    }

    @objc private func replaceLink(_ sender: NSMenuItem) {
        guard let name = sender.representedObject as? String else { return }
        onReplaceLink?(name)
    }

    @objc private func removeServer(_ sender: NSMenuItem) {
        guard let raw = sender.representedObject as? String, let row = parseRowMenuKey(raw) else { return }
        // 当前那台**在这里也拦一道**(纵深防御):菜单项已经置灰,而一个只靠
        // `isEnabled` 的保护会在下一次有人从别处触发这个 action 时失效。
        guard !row.isCurrent else { return }
        onRemove?(row.name, row.host, row.traffic)
    }

    @objc private func switchTo(_ sender: NSButton) {
        guard let name = sender.identifier?.rawValue else { return }
        onSwitch?(name, sender.toolTip ?? "")
    }

    @objc private func checkExitIP() {
        onCheckExitIP?()
    }

    @objc private func deployServer() {
        onDeploy?()
    }

    @objc private func addServer() {
        onAddServer?()
    }

    @objc private func probeAll() {
        onProbe?()
    }
}

