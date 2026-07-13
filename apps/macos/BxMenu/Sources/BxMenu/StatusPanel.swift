import AppKit

final class StatusPanelController {
    private var panel: NSPanel?

    func present(_ snapshot: StatusSnapshot) {
        let panel = self.panel ?? makePanel()
        panel.contentView = makeContent(snapshot)
        panel.title = "bx Status"
        panel.center()
        panel.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
        self.panel = panel
    }

    private func makePanel() -> NSPanel {
        let panel = NSPanel(
            contentRect: NSRect(x: 0, y: 0, width: 330, height: 260),
            styleMask: [.titled, .closable, .utilityWindow],
            backing: .buffered,
            defer: false
        )
        panel.isFloatingPanel = true
        panel.level = .floating
        panel.hidesOnDeactivate = false
        panel.collectionBehavior = [.moveToActiveSpace]
        panel.titlebarAppearsTransparent = true
        panel.isReleasedWhenClosed = false
        return panel
    }

    private func makeContent(_ snapshot: StatusSnapshot) -> NSView {
        let content = NSView(frame: NSRect(x: 0, y: 0, width: 330, height: 260))

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 10
        stack.translatesAutoresizingMaskIntoConstraints = false
        content.addSubview(stack)

        let title = NSTextField(labelWithString: snapshot.title)
        title.font = .systemFont(ofSize: 20, weight: .semibold)
        stack.addArrangedSubview(title)

        let divider = NSBox()
        divider.boxType = .separator
        stack.addArrangedSubview(divider)
        divider.widthAnchor.constraint(equalTo: stack.widthAnchor).isActive = true

        for row in snapshot.rows {
            stack.addArrangedSubview(makeRow(row))
        }

        NSLayoutConstraint.activate([
            stack.leadingAnchor.constraint(equalTo: content.leadingAnchor, constant: 24),
            stack.trailingAnchor.constraint(equalTo: content.trailingAnchor, constant: -24),
            stack.topAnchor.constraint(equalTo: content.topAnchor, constant: 22),
            stack.bottomAnchor.constraint(lessThanOrEqualTo: content.bottomAnchor, constant: -22),
            content.widthAnchor.constraint(equalToConstant: 330),
        ])
        return content
    }

    private func makeRow(_ row: StatusRow) -> NSView {
        let stack = NSStackView()
        stack.orientation = .horizontal
        stack.distribution = .fill
        stack.alignment = .firstBaseline

        let label = NSTextField(labelWithString: row.label)
        label.textColor = .secondaryLabelColor
        let value = NSTextField(labelWithString: row.value)
        value.alignment = .right
        value.lineBreakMode = .byTruncatingMiddle

        stack.addArrangedSubview(label)
        stack.addArrangedSubview(NSView())
        stack.addArrangedSubview(value)
        stack.widthAnchor.constraint(equalToConstant: 282).isActive = true
        return stack
    }
}
