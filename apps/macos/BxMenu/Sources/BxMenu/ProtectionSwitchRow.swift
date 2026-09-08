import AppKit

/// 菜单第一行:`[盾] Protection ………… ◉━━`,可选第二行暗色小字(连接摘要 / 进度)。
///
/// 控制中心的形态(Wi‑Fi / 蓝牙那一行)。**这个文件只摆**:显示不显示、开还是关、
/// 能不能拨,全在 ProtectionSwitch.swift 的纯函数里;拨动只把方向交给 `onFlip`,
/// 由 main.swift 接到原来那两个入口(startBx / turnOffBx),确认、逃生口一个字不动。
///
/// 自定义视图的菜单行没有键盘高亮导航(方向键跳过它),VoiceOver 读得到 NSSwitch
/// —— 这是控制中心同款的取舍。
final class ProtectionSwitchRow: NSView {
    /// 用户拨了。参数是拨到的那一边;**位置不在这里记**,下一次重建按状态重画。
    var onFlip: ((Bool) -> Void)?

    /// 进 menuSignature:开关行看得见的一切 —— 少一项就是「那一项变了而菜单不更新」。
    let signature: String

    private let toggle = NSSwitch()

    init(isOn: Bool, enabled: Bool, subtitle: String?, subtitleIsBad: Bool) {
        signature = ["protection", isOn ? "on" : "off", enabled ? "enabled" : "disabled",
                     subtitle ?? "", subtitleIsBad ? "bad" : "fine"].joined(separator: "\u{1F}")
        let hasSubtitle = !(subtitle ?? "").isEmpty
        super.init(frame: NSRect(x: 0, y: 0, width: 260, height: hasSubtitle ? 46 : 32))
        // 菜单按最宽的一项定宽,这一行跟着拉伸;固定的是高度。
        autoresizingMask = [.width]

        let icon = NSImageView()
        icon.image = NSImage(systemSymbolName: isOn ? "shield.fill" : "shield",
                             accessibilityDescription: "Protection")
        icon.symbolConfiguration = .init(pointSize: 14, weight: .regular)
        icon.contentTintColor = enabled ? .labelColor : .tertiaryLabelColor
        icon.translatesAutoresizingMaskIntoConstraints = false

        let title = NSTextField(labelWithString: "Protection")
        title.font = .menuFont(ofSize: 0)
        title.textColor = enabled ? .labelColor : .tertiaryLabelColor
        title.translatesAutoresizingMaskIntoConstraints = false

        toggle.controlSize = .small
        toggle.state = isOn ? .on : .off
        toggle.isEnabled = enabled
        toggle.target = self
        toggle.action = #selector(flipped(_:))
        toggle.setAccessibilityLabel("Protection")
        toggle.translatesAutoresizingMaskIntoConstraints = false

        addSubview(icon)
        addSubview(title)
        addSubview(toggle)
        // 左边距对齐普通菜单项的图标列与文字列(AppKit 菜单项:图标约 13pt 起、
        // 文字约 36pt 起),让这一行看起来是菜单的一部分而不是嵌进来的控件。
        var constraints = [
            icon.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 14),
            icon.widthAnchor.constraint(equalToConstant: 18),
            title.leadingAnchor.constraint(equalTo: leadingAnchor, constant: 38),
            title.trailingAnchor.constraint(lessThanOrEqualTo: toggle.leadingAnchor, constant: -10),
            toggle.trailingAnchor.constraint(equalTo: trailingAnchor, constant: -14),
            toggle.centerYAnchor.constraint(equalTo: centerYAnchor),
        ]
        if hasSubtitle, let subtitle {
            let detail = NSTextField(labelWithString: subtitle)
            detail.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            // ✗ 时用系统红:这一行的第二行是唯一会变红的地方,与 anomalyCount 同步。
            detail.textColor = subtitleIsBad ? .systemRed : .secondaryLabelColor
            detail.lineBreakMode = .byTruncatingTail
            detail.toolTip = subtitle
            detail.translatesAutoresizingMaskIntoConstraints = false
            addSubview(detail)
            constraints += [
                title.topAnchor.constraint(equalTo: topAnchor, constant: 6),
                detail.leadingAnchor.constraint(equalTo: title.leadingAnchor),
                detail.trailingAnchor.constraint(lessThanOrEqualTo: toggle.leadingAnchor, constant: -10),
                detail.topAnchor.constraint(equalTo: title.bottomAnchor, constant: 1),
                icon.centerYAnchor.constraint(equalTo: title.centerYAnchor),
            ]
        } else {
            constraints += [
                title.centerYAnchor.constraint(equalTo: centerYAnchor),
                icon.centerYAnchor.constraint(equalTo: centerYAnchor),
            ]
        }
        NSLayoutConstraint.activate(constraints)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { nil }

    @objc private func flipped(_ sender: NSSwitch) {
        onFlip?(sender.state == .on)
    }
}
