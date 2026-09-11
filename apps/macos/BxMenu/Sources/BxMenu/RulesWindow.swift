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

    /// 表里每一行的容器,键是 `kind|pattern`。`markRemoved` 靠它找到那一行 ——
    /// 删完**不重画整张表**:重画会让这一行直接消失,而那正是这里要避免的。
    private var ruleRowBoxes: [String: NSStackView] = [:]

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

    func show(rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String, reviewNote: String?) {
        let window = ensureWindow()
        render(rows: rows, ruleRows: ruleRows, configPath: configPath, reviewNote: reviewNote)
        // LSUIElement 应用不会自动到前台;不激活的话窗口会开在别的应用后面,
        // 用户以为"点了没反应"——正是这一版要消灭的那种体验。
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// 数据更新时就地重画。**窗口不存在就什么都不做** —— 不要因为后台刷新
    /// 把一个用户没打开的窗口弹出来。
    func refreshIfVisible(rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String, reviewNote: String?) {
        guard let window, window.isVisible else { return }
        render(rows: rows, ruleRows: ruleRows, configPath: configPath, reviewNote: reviewNote)
    }

    /// 窗口是否开着。**供环境刷新路径判断「有没有人在看」** —— 与
    /// `ServersWindow` 那份逐字同一个理由:窗口关着就不拨(按需的本意),
    /// 窗口开着就说明有人正盯着,这时按需拉一次正是按需的本意。
    ///
    /// 少了它,每条规则的失败计数会冻在**打开窗口那一刻**,而这个窗口存在的
    /// 理由就是回答「哪条在失败」—— 服务器窗口 2026-08-17 就是这么坏过一次的。
    var isVisible: Bool { window?.isVisible ?? false }

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
    private func render(
        rows: [RuleGroupRow], ruleRows: [RuleRow], configPath: String, reviewNote: String?
    ) {
        guard let stack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
        ruleRowBoxes.removeAll()

        // **体检缺席要说出来,摆在最上面。** 这个窗口的词汇表里「一行没有副标题」
        // 读作「查过了,健康」;旧 Guardian(以及配置读不出来的那一次)根本没发
        // 体检,不说这句话就是替一份从没收到过的报告签字。判据在
        // `ruleReviewUnavailableNote`,这里只摆。
        if let reviewNote {
            let note = NSTextField(labelWithString: reviewNote)
            note.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            note.textColor = .secondaryLabelColor
            note.lineBreakMode = .byWordWrapping
            note.preferredMaxLayoutWidth = 380
            stack.addArrangedSubview(note)
            stack.addArrangedSubview(gap())
        }

        for row in rows {
            stack.addArrangedSubview(groupRow(row))
        }

        if !ruleRows.isEmpty {
            stack.addArrangedSubview(gap())
            for row in ruleRows {
                stack.addArrangedSubview(ruleRow(row))
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
        if !configPath.isEmpty {
            let reveal = NSButton(title: "Show Config", target: self, action: #selector(revealConfig))
            reveal.bezelStyle = .rounded
            reveal.controlSize = .small
            footer.addArrangedSubview(reveal)
        }
        stack.addArrangedSubview(footer)
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
        remove.identifier = NSUserInterfaceItemIdentifier(ruleKey(row.kind, row.pattern))
        remove.setContentHuggingPriority(.defaultHigh, for: .horizontal)
        box.addArrangedSubview(remove)

        ruleRowBoxes[ruleKey(row.kind, row.pattern)] = box
        return box
    }

    /// 那一行原地变成「Removed … · Undo」,**不从栈里移除**。
    ///
    /// 不弹确认框是刻意的(要清掉十一条冗余规则就得点十一次确认,那是在惩罚
    /// 正确的行为);但一次误点静默毁掉一条手写规则同样不行 —— 出路是删完
    /// 留一个撤销口,下一次 render(重拉规则之后)它才真的不见。
    func markRemoved(kind: RuleKind, pattern: String) {
        let key = ruleKey(kind, pattern)
        guard let box = ruleRowBoxes[key] else { return }
        for view in box.arrangedSubviews {
            box.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
        let label = NSTextField(labelWithString: "Removed \(pattern)")
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        label.textColor = .secondaryLabelColor
        box.addArrangedSubview(label)
        let undo = NSButton(title: "Undo", target: self, action: #selector(undoRemove(_:)))
        undo.bezelStyle = .rounded
        undo.controlSize = .small
        undo.identifier = NSUserInterfaceItemIdentifier(key)
        box.addArrangedSubview(undo)
    }

    private func ruleKey(_ kind: RuleKind, _ pattern: String) -> String {
        kind.rawValue + "|" + pattern
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

    @objc private func removeRule(_ sender: NSButton) {
        guard let (kind, pattern) = ruleIdentity(sender) else { return }
        onRemoveRule?(kind, pattern)
    }

    @objc private func undoRemove(_ sender: NSButton) {
        guard let (kind, pattern) = ruleIdentity(sender) else { return }
        onUndoRemove?(kind, pattern)
    }

    @objc private func addRule() {
        onAddRule?()
    }

    @objc private func revealConfig() {
        onRevealConfig?()
    }


}
