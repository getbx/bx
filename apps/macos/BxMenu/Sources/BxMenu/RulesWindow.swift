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
    /// 滚动容器。**重画要保住滚动位置**,而位置只能从它身上取。
    private var scroll: NSScrollView?

    /// 删掉了、而用户还能撤销的那几条。**由窗口自己记着,因为它必须活过重画**
    /// —— 这个窗口跟着环境刷新每 2 秒重建一次整张表,而新鲜数据里已经没有
    /// 这条规则了;不记着它,那个 Undo 就是个两秒后无声消失的承诺(见
    /// `PendingRuleRemoval` 的注释)。
    private var pendingRemovals: [PendingRuleRemoval] = []

    /// 最近一次收到的那份数据。`markRemoved` 靠它**不重新问服务端**就地重画 ——
    /// 「Removed … · Undo」长什么样因此只有一份实现,而不是删除那条路一份、
    /// 重画那条路另一份(两份早晚说出两句不一样的话)。
    private var lastGroupRows: [RuleGroupRow] = []
    private var lastRuleRows: [RuleRow] = []
    private var lastConfigPath = ""
    private var lastCaveatNote: String?

    /// 用户拨动了一个组开关。参数是组名与目标状态。
    var onToggleGroup: ((String, Bool) -> Void)?
    /// 用户要求打开配置文件所在位置。
    var onRevealConfig: (() -> Void)?
    /// 用户要删掉一条规则。
    var onRemoveRule: ((RuleKind, String) -> Void)?
    /// 用户点了 Undo,要把刚删掉的那条原样加回去。
    var onUndoRemove: ((RuleKind, String) -> Void)?
    /// 用户点了 Add Rule…。
    var onAddRule: (() -> Void)?

    func show(rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String, caveatNote: String?) {
        let window = ensureWindow()
        adoptFreshRules(
            rows: rows, ruleRows: ruleRows, configPath: configPath, caveatNote: caveatNote)
        // **显式打开从头开始看。** 保住滚动位置是给环境重画准备的(用户正盯着
        // 某一行,不该每 2 秒被拽回顶部);他刚点开这扇窗,顶上那几行才是他要的。
        render(preservingScroll: false)
        // LSUIElement 应用不会自动到前台;不激活的话窗口会开在别的应用后面,
        // 用户以为"点了没反应"——正是这一版要消灭的那种体验。
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// 数据更新时就地重画。**窗口不存在就什么都不做** —— 不要因为后台刷新
    /// 把一个用户没打开的窗口弹出来。
    ///
    /// 这是**环境刷新**那条路(`applyRefresh` → `fetchRulesOnDemand(forceShow: false)`),
    /// 菜单开着时约每 2 秒一拍:滚动位置要保住,等着撤销的那几条也要保住。
    func refreshIfVisible(rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String, caveatNote: String?) {
        guard let window, window.isVisible else { return }
        adoptFreshRules(
            rows: rows, ruleRows: ruleRows, configPath: configPath, caveatNote: caveatNote)
        render(preservingScroll: true)
    }

    /// 收下服务端刚发来的一份规则。
    ///
    /// **挂起的删除在这里、也只在这里对账**:判据是 `survivingRuleRemovals` ——
    /// 新鲜数据里又出现了那条规则,就说明那次 Undo 成功了(从服务端那半看,
    /// 成功的 Undo 就长这样),这条挂起退场。窗口不去猜某个请求的结局,它看数据。
    ///
    /// **对账只发生在拿到新鲜数据的时候**,`markRemoved` 的就地重画走不到这里:
    /// 那一刻手里还是旧数据、那条规则仍在里头,对一次账就会把刚记下的挂起
    /// 当场抹掉 —— 于是这个修复在它自己的入口处失效。
    private func adoptFreshRules(
        rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String, caveatNote: String?
    ) {
        pendingRemovals = survivingRuleRemovals(pendingRemovals, freshRows: ruleRows)
        lastGroupRows = rows
        lastRuleRows = ruleRows
        lastConfigPath = configPath
        lastCaveatNote = caveatNote
    }

    /// 窗口是否开着。**供环境刷新路径判断「有没有人在看」** —— 与
    /// `ServersWindow` 那份逐字同一个理由:窗口关着就不拨(按需的本意),
    /// 窗口开着就说明有人正盯着,这时按需拉一次正是按需的本意。
    ///
    /// 少了它,每条规则的失败计数会冻在**打开窗口那一刻**,而这个窗口存在的
    /// 理由就是回答「哪条在失败」—— 服务器窗口 2026-08-17 就是这么坏过一次的。
    var isVisible: Bool { window?.isVisible ?? false }

    /// 窗口关掉 = 那些 Undo 再也点不到了。留着它们只会让下次打开时摆出一串
    /// 早已不相干的「Removed …」,而那几条规则**确实**已经不在了。
    func windowWillClose(_ notification: Notification) {
        pendingRemovals.removeAll()
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 420, height: 320),
            // **`.resizable` 与那一行的 toolTip 是同一件事的两半**:规则那一行
            // 的说明会截断,而这个窗口不横向滚动 —— 少了把窗口拉宽这条出路,
            // 被截掉的那半句就永久不可见了。
            styleMask: [.titled, .closable, .resizable],
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
        self.scroll = scroll
        self.window = window
        return window
    }

    /// **这个窗口只有三样东西:开关、你自己写的那些规则、以及去改它们。**
    ///
    /// 上一版还有标题行、顶部告警、页脚说明、完整路径和分隔线 —— 而它们各自
    /// 都在重复别处已经说过的话:
    ///
    ///   · 顶部「Apple isn't working」与那一行的红色 `6 failed` 是同一件事
    ///   · 「Changes apply when you reconnect」是常驻的,而**改完本来就会弹
    ///     一次提示**(followUpAfterRuleChange),所以它一年到头只是在占地方
    ///   · 两块内容一个带勾选框、一个一行一条带 Remove,已经分得清,不需要小标题
    ///   · 路径没人会去手打,按钮就是干这个的
    ///
    /// 剩下的每一行都在回答一个问题:哪些开着、我自己写了什么(哪条有毛病)、
    /// 怎么去改。**分隔靠留白,不靠线**。
    ///
    /// **自己写的那些规则不再是一坨灰字。** 上一版把它们摊成只读的等宽文本,
    /// 于是这个窗口回答不了「哪条有问题」「怎么删掉它」—— 而用户打开它十有八九
    /// 就是为了这两件事,只能去开终端。现在一行一条:模式、方向、以及**只有
    /// 出了问题才说的那句话**(`row.detail`,健康的一行一个字不写)。
    ///
    /// **顺序与那句话都来自 `ruleRows`(纯模型),这里一个字节都不算。** 判定
    /// 落进 AppKit 这半就等于没有测试盯着它:这个文件在 CI 里编都不编。
    ///
    /// **表上摆哪几行由 `ruleTableEntries` 说了算,不是直接遍历新鲜数据** ——
    /// 少了这一跳,一条刚删掉的规则连同它的 Undo 会在下一次环境重画(约 2 秒)
    /// 里无声消失,而删除刻意不弹确认框、Undo 正是那个确认框的替身。
    private func render(preservingScroll: Bool) {
        guard let stack else { return }
        // 滚动位置在拆视图之前取。**AppKit 的重建会把它清零**,而这个窗口每
        // 2 秒重建一次 —— 不保住的话用户每翻到一半就被拽回顶部。
        let offset = preservingScroll ? scroll?.contentView.bounds.origin : nil
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }

        // **问不出来的那半要说出来,摆在最上面。** 这个窗口的词汇表里「一行没有
        // 副标题」读作「查过了,健康」;体检可能整个没发(旧 Guardian,或配置
        // 读不出来),失败归因也可能整个没发(Core 不应答时 `failing_rules`
        // 按构造是空的)—— 任一半缺席都不说话,就是替一份从没收到过的报告签字。
        // 判据在 `ruleWindowCaveatNote`,这里只摆。
        let caveatNote = lastCaveatNote
        if let caveatNote {
            let note = NSTextField(labelWithString: caveatNote)
            note.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            note.textColor = .secondaryLabelColor
            note.lineBreakMode = .byWordWrapping
            note.preferredMaxLayoutWidth = 380
            stack.addArrangedSubview(note)
            stack.addArrangedSubview(gap())
        }

        // **两块之间要有分界。** 2026-09-14 真机反馈:三行组(Apple / China CDN /
        // Steam,带勾选)确实画着,但紧接着就是十几行平铺的自定义规则,中间没有
        // 任何标题 —— 于是「这里能按组勾选」整个看不出来,用户的原话是
        // 「分类要清晰……用户是否可以简单勾选」。
        if !lastGroupRows.isEmpty {
            let presetsHeading = sectionHeading("Presets")
            stack.addArrangedSubview(presetsHeading)
            for row in lastGroupRows {
                stack.addArrangedSubview(groupRow(row))
            }
        }

        let entries = ruleTableEntries(rows: lastRuleRows, pending: pendingRemovals)
        if !entries.isEmpty {
            stack.addArrangedSubview(gap())
            // 数量写进标题:一眼看出下面这一长串是「你自己加的」,而不是预设的一部分。
            let customHeading = sectionHeading("Your own rules (\(lastRuleRows.count))")
            stack.addArrangedSubview(customHeading)
            for entry in entries {
                switch entry {
                case .rule(let row):
                    stack.addArrangedSubview(ruleRow(row))
                case .removed(let kind, let pattern):
                    stack.addArrangedSubview(removedRow(kind: kind, pattern: pattern))
                }
            }
        }

        stack.addArrangedSubview(gap())
        let footer = NSStackView()
        footer.orientation = .horizontal
        footer.spacing = 8
        let add = NSButton(title: "Add Rule…", target: self, action: #selector(addRule))
        add.bezelStyle = .rounded
        add.controlSize = .small
        footer.addArrangedSubview(add)
        if !lastConfigPath.isEmpty {
            let reveal = NSButton(title: "Show Config", target: self, action: #selector(revealConfig))
            reveal.bezelStyle = .rounded
            reveal.controlSize = .small
            footer.addArrangedSubview(reveal)
        }
        stack.addArrangedSubview(footer)

        if let offset, let scroll {
            // **先布局再滚。** 少了这一步滚的是按旧内容算出来的坐标,于是
            // 表变长/变短的那一拍位置照样会跳(Diagnostics 那两页同款)。
            scroll.documentView?.layoutSubtreeIfNeeded()
            scroll.contentView.scroll(to: offset)
            scroll.reflectScrolledClipView(scroll.contentView)
        }
    }

    /// 一条规则一行:等宽的模式、方向、出问题那句话、以及一个 Remove。
    ///
    /// 按钮把 `kind|pattern` 存进 `identifier`:回调要的就是这两样,而
    /// 从界面上的文字反推它们会在模式里含 `|` 之类的时候悄悄取错一条规则。
    private func ruleRow(_ row: RuleRow) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.alignment = .firstBaseline
        box.spacing = 8

        let pattern = NSTextField(labelWithString: row.pattern)
        pattern.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
        box.addArrangedSubview(pattern)

        let kind = NSTextField(labelWithString: row.kind.rawValue)
        kind.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        kind.textColor = .secondaryLabelColor
        box.addArrangedSubview(kind)

        // **健康的一行不摆这个 label**,不是摆一个空的:一屏参差不齐的留白
        // 正是上一版「太丑」的来源,而每行都挂一句解释会把真要紧的那行淹掉。
        if let detail = row.detail {
            let note = NSTextField(labelWithString: detail)
            note.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            // 轻重由纯函数说了算(`ruleRowNoteIsSevere`)。按「有没有体检结论」
            // 分会把去匿名化那一条画得比一条连不通的规则还轻 —— 正好与模型
            // 自己的排序反着来。
            note.textColor = ruleRowNoteIsSevere(row) ? .systemRed : .systemOrange
            note.lineBreakMode = .byTruncatingTail
            // 截断了还看得全:这个窗口不横向滚动,少了 toolTip 那句话就永久不可见。
            note.toolTip = detail
            note.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
            box.addArrangedSubview(note)
        }

        box.setHuggingPriority(.defaultLow, for: .horizontal)
        let remove = NSButton(title: "Remove", target: self, action: #selector(removeRule(_:)))
        remove.bezelStyle = .rounded
        remove.controlSize = .small
        remove.identifier = NSUserInterfaceItemIdentifier(ruleEntryKey(row.kind, row.pattern))
        remove.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        box.addArrangedSubview(remove)

        return box
    }

    /// 一条删掉了、还能撤回的规则:「Removed … · Undo」。
    ///
    /// **它是每一次重画都会重新摆出来的一行**,不是某一行的临时改装 —— 那是
    /// 这个修复的要害:改装活不过下一次 `render`,而这个窗口每 2 秒 render 一次。
    private func removedRow(kind: RuleKind, pattern: String) -> NSView {
        let box = NSStackView()
        box.orientation = .horizontal
        box.alignment = .firstBaseline
        box.spacing = 8

        let label = NSTextField(labelWithString: "Removed \(pattern)")
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        label.textColor = .secondaryLabelColor
        box.addArrangedSubview(label)
        box.setHuggingPriority(.defaultLow, for: .horizontal)

        let undo = NSButton(title: "Undo", target: self, action: #selector(undoRemove(_:)))
        undo.bezelStyle = .rounded
        undo.controlSize = .small
        undo.identifier = NSUserInterfaceItemIdentifier(ruleEntryKey(kind, pattern))
        undo.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        box.addArrangedSubview(undo)
        return box
    }

    /// 那一行原地变成「Removed … · Undo」。
    ///
    /// 不弹确认框是刻意的(要清掉十一条冗余规则就得点十一次确认,那是在惩罚
    /// 正确的行为);但一次误点静默毁掉一条手写规则同样不行 —— 出路是删完
    /// 留一个撤销口。**那个撤销口要活到用户自己处置它为止**:点了 Undo,或者
    /// 关掉这扇窗。它**不**随下一次刷新到期 —— 环境刷新每 2 秒一拍,那等于
    /// 给了个两秒的撤销窗口,而没有人做过这个决定。
    func markRemoved(kind: RuleKind, pattern: String) {
        let key = ruleEntryKey(kind, pattern)
        guard !pendingRemovals.contains(where: { ruleEntryKey($0.kind, $0.pattern) == key })
        else { return }
        // 行号取自**此刻表上摆着的那几行**(含已经挂起的那些),这样插回去
        // 的位置就是它消失前的位置,Undo 不会在光标底下跳走。
        let entries = ruleTableEntries(rows: lastRuleRows, pending: pendingRemovals)
        let index =
            entries.firstIndex(where: { entry in
                guard case .rule(let row) = entry else { return false }
                return ruleEntryKey(row.kind, row.pattern) == key
            }) ?? entries.count
        pendingRemovals.append(PendingRuleRemoval(kind: kind, pattern: pattern, index: index))
        if pendingRemovals.count > maxPendingRuleRemovals {
            pendingRemovals.removeFirst(pendingRemovals.count - maxPendingRuleRemovals)
        }
        // 就地重画:数据没变(服务端已经答过了),变的只是挂起那一份。
        render(preservingScroll: true)
    }

    /// 从按钮的 identifier 还原出这一行是谁。**按第一个 `|` 切**:方向那一段
    /// 取值只有 direct/proxy,不含分隔符,而模式里含分隔符时后半段要原样留着。
    private func ruleIdentity(_ sender: NSButton) -> (RuleKind, String)? {
        guard let raw = sender.identifier?.rawValue,
            let separator = raw.firstIndex(of: "|"),
            let kind = RuleKind(rawValue: String(raw[raw.startIndex..<separator]))
        else { return nil }
        return (kind, String(raw[raw.index(after: separator)...]))
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
    /// 一行小标题。**只分区,不解释** —— 组是干什么的由组名与展开后的域名说,
    /// 而不是把服务端那句中文 summary 摆上来(菜单通篇英文;本仓库为
    /// 「服务端写的是中文」栽过一次)。
    private func sectionHeading(_ text: String) -> NSView {
        let label = NSTextField(labelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
        label.textColor = .secondaryLabelColor
        return label
    }

    /// 展开状态按组名记 —— 每次刷新都重建视图树,存在视图上会跟着一起没。
    private var expandedGroups: Set<String> = []

    @objc private func toggleGroupExpansion(_ sender: NSButton) {
        guard let name = sender.identifier?.rawValue else { return }
        if expandedGroups.contains(name) {
            expandedGroups.remove(name)
        } else {
            expandedGroups.insert(name)
        }
        render(preservingScroll: true)
    }

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

        // **「apple 里面有哪些」** —— 这一整条诉求缺的只是渲染:`RuleGroup` 的
        // `domains` 早就解码到客户端了(CodingKeys 里就有),而在此之前没有任何
        // 地方画过它。一个只给勾选框、不肯说自己管哪些域名的开关,用户没有理由
        // 相信它 —— 与 leakcheck 那条「Evidence 是必须的那一半」同源。
        let name = row.group.name
        let expanded = expandedGroups.contains(name)
        let disclose = NSButton(
            title: expanded ? "Hide" : "Show",
            target: self, action: #selector(toggleGroupExpansion(_:)))
        disclose.bezelStyle = .inline
        disclose.controlSize = .small
        disclose.identifier = NSUserInterfaceItemIdentifier(name)
        disclose.isHidden = row.group.domains.isEmpty
        box.addArrangedSubview(disclose)

        guard expanded, !row.group.domains.isEmpty else { return box }
        let column = NSStackView()
        column.orientation = .vertical
        column.alignment = .leading
        column.spacing = 2
        column.addArrangedSubview(box)
        for domain in row.group.domains {
            let line = NSTextField(labelWithString: "    " + domain)
            line.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
            line.textColor = .secondaryLabelColor
            column.addArrangedSubview(line)
        }
        return column
    }


    @objc private func toggleGroup(_ sender: NSButton) {
        guard let name = sender.identifier?.rawValue else { return }
        // 从 mixed 点出去一律当作"打开":用户看到半装的组去点它,想要的是补齐。
        let enable = sender.state != .off
        sender.allowsMixedState = false
        sender.state = enable ? .on : .off
        onToggleGroup?(name, enable)
    }

    @objc private func removeRule(_ sender: NSButton) {
        guard let (kind, pattern) = ruleIdentity(sender) else { return }
        onRemoveRule?(kind, pattern)
    }

    /// 点了 Undo:这条挂起当场退场,剩下的交给数据 —— `addRuleBack` 成功也好
    /// 失败也好都会重拉一次,那一拉带回来的才是真相。
    ///
    /// **这里刻意不立刻重画**:撤销请求还在飞,把那一行当场抹掉、一秒后又让它
    /// 冒回来,是一次没有信息量的闪烁;而如果撤销失败了,那一拉会如实让它消失
    /// (弹窗已经说过为什么)。
    @objc private func undoRemove(_ sender: NSButton) {
        guard let (kind, pattern) = ruleIdentity(sender) else { return }
        let key = ruleEntryKey(kind, pattern)
        pendingRemovals.removeAll { ruleEntryKey($0.kind, $0.pattern) == key }
        onUndoRemove?(kind, pattern)
    }

    @objc private func addRule() {
        onAddRule?()
    }

    @objc private func revealConfig() {
        onRevealConfig?()
    }


}
