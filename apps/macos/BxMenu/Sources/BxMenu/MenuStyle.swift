import AppKit

// 菜单几扇窗口共用的**样式**(布局组装仍在 MenuLayout.swift)。
//
// 2026-09-29 用离屏快照把几扇窗口并排看,问题不在单扇,在拼在一起:像五个人各做了一扇
// —— 灰底带标签框的诊断窗口挨着白底的其余几扇,标题字号各一套,几个按钮一样大排成一排、
// 分不出主次。这里是那一套共同的词汇;每扇窗口从这里取,不再各抄一份。
enum MenuStyle {
    /// 每扇窗口内容区的四边留白。
    static let insets = NSEdgeInsets(top: 20, left: 20, bottom: 20, right: 20)
    /// 行与行之间。
    static let rowSpacing: CGFloat = 10
    /// 分区之间多空的那一截。
    static let sectionGap: CGFloat = 8
}

/// 分区标题(「Currently using」「Presets」「Your rules」……):小号、半粗、次要色。
func menuSectionHeader(_ text: String) -> NSTextField {
    let label = NSTextField(labelWithString: text)
    label.font = .systemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
    label.textColor = .secondaryLabelColor
    return label
}

/// 说明性的小字:次要色、会折行。
func menuCaption(_ text: String) -> NSTextField {
    let label = NSTextField(wrappingLabelWithString: text)
    label.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
    label.textColor = .secondaryLabelColor
    return label
}

/// 分区之间的空隙。
func menuSectionGap() -> NSView {
    let spacer = NSView()
    spacer.translatesAutoresizingMaskIntoConstraints = false
    spacer.heightAnchor.constraint(equalToConstant: MenuStyle.sectionGap).isActive = true
    return spacer
}

/// 按钮的三种分量。**一扇窗口至多一个 primary**(那扇窗口存在的理由,如部署表单的「安装」);
/// 其余是 secondary;挂在每一行上的动作(规则那一行的 Remove)是 inline,不许是整块按钮 ——
/// 十一行十一个一样重的按钮,会让表看起来全是按钮。
enum MenuButtonWeight {
    case primary, secondary, inline
}

func menuButton(_ title: String, weight: MenuButtonWeight = .secondary, target: AnyObject?, action: Selector) -> NSButton {
    let button = NSButton(title: title, target: target, action: action)
    switch weight {
    case .primary:
        button.bezelStyle = .rounded
        button.controlSize = .regular
        button.keyEquivalent = "\r"
    case .secondary:
        button.bezelStyle = .rounded
        button.controlSize = .regular
    case .inline:
        button.isBordered = false
        button.controlSize = .small
        button.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        button.contentTintColor = .linkColor
    }
    return button
}

/// 一排按钮,靠左;`trailing` 为真时靠右(表单末尾的主按钮)。
func menuButtonRow(_ buttons: [NSView], trailing: Bool = false) -> NSView {
    let row = NSStackView()
    row.orientation = .horizontal
    row.spacing = 8
    if trailing {
        let spacer = NSView()
        spacer.setContentHuggingPriority(.defaultLow, for: .horizontal)
        row.addArrangedSubview(spacer)
    }
    for button in buttons {
        // 按钮保持自己的宽度:行被 addFullWidthRow 拉到整宽时,余量归弹簧,不归按钮 ——
        // 少了这一句,第一个按钮会被拉成通栏(2026-09-29 快照上看到的)。
        button.setContentHuggingPriority(.required, for: .horizontal)
        row.addArrangedSubview(button)
    }
    if !trailing {
        let spacer = NSView()
        spacer.setContentHuggingPriority(.defaultLow, for: .horizontal)
        row.addArrangedSubview(spacer)
    }
    row.setHuggingPriority(.defaultLow, for: .horizontal)
    return row
}

/// 配置文件放进标题栏(macOS 的文件代理图标:悬停出现、⌘ 点击看路径),不再印在内容区
/// 右上角 —— 那一行是给少数人看的,却让每个人每次都读到一个 `/etc/…` 路径。
func menuShowConfigFile(_ path: String, in window: NSWindow) {
    window.representedURL = path.isEmpty ? nil : URL(fileURLWithPath: path)
}
