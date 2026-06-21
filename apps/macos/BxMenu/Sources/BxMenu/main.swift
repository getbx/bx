import AppKit
import Foundation

private let bxPath = "/usr/local/bin/bx"

struct BxReport: Decodable {
    let tunnelHealthy: Bool
    let latencyMS: Int64
    let restarts: Int
    let udpMode: String?
    let udpNote: String?
    let active: Int64
    let proxy: Int64
    let direct: Int64
    let blocked: Int64

    enum CodingKeys: String, CodingKey {
        case tunnelHealthy = "tunnel_healthy"
        case latencyMS = "latency_ms"
        case udpMode = "udp_mode"
        case udpNote = "udp_note"
        case restarts, active, proxy, direct, blocked
    }
}

enum BxState {
    case connected(BxReport)
    case warning(String)
    case missing(String)
    case off
}

@main
final class BxMenuApp: NSObject, NSApplicationDelegate {
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
    private var timer: Timer?
    private var state: BxState = .off

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
        configureMenu()
        refresh()
        timer = Timer.scheduledTimer(withTimeInterval: 5, repeats: true) { [weak self] _ in
            self?.refresh()
        }
    }

    private func configureMenu() {
        statusItem.button?.target = self
        statusItem.button?.action = #selector(openMenu)
        statusItem.menu = NSMenu()
    }

    @objc private func openMenu() {
        refresh()
    }

    private func refresh() {
        state = loadState()
        updateIcon()
        rebuildMenu()
    }

    private func loadState() -> BxState {
        guard FileManager.default.isExecutableFile(atPath: bxPath) else {
            return .missing("Install bx at /usr/local/bin/bx")
        }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: bxPath)
        process.arguments = ["status", "--json"]
        let output = Pipe()
        let errors = Pipe()
        process.standardOutput = output
        process.standardError = errors
        do {
            try process.run()
            process.waitUntilExit()
        } catch {
            return .off
        }
        guard process.terminationStatus == 0 else {
            let data = errors.fileHandleForReading.readDataToEndOfFile()
            let message = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines)
            return .warning(message?.isEmpty == false ? message! : "Status unavailable")
        }
        let data = output.fileHandleForReading.readDataToEndOfFile()
        guard let report = try? JSONDecoder().decode(BxReport.self, from: data) else {
            return .warning("Status unreadable")
        }
        return report.tunnelHealthy ? .connected(report) : .warning("Tunnel unhealthy")
    }

    private func updateIcon() {
        guard let button = statusItem.button else { return }
        let symbol: String
        let tint: NSColor
        switch state {
        case .connected:
            symbol = "checkmark.shield"
            tint = .controlAccentColor
        case .warning:
            symbol = "exclamationmark.triangle"
            tint = .systemYellow
        case .missing:
            symbol = "questionmark.circle"
            tint = .secondaryLabelColor
        case .off:
            symbol = "shield"
            tint = .secondaryLabelColor
        }
        let image = NSImage(systemSymbolName: symbol, accessibilityDescription: "bx")
        image?.isTemplate = false
        button.image = image?.tinted(tint)
    }

    private func rebuildMenu() {
        let menu = NSMenu()
        switch state {
        case .connected(let report):
            menu.addHeader("bx", subtitle: "Connected")
            menu.addInfo("Status", "Protected")
            menu.addInfo("Tunnel", "\(report.latencyMS) ms")
            menu.addInfo("UDP Relay", report.udpMode == "proxy" ? "On" : "Off")
            menu.addInfo("Active", "\(report.active)")
        case .warning(let message):
            menu.addHeader("bx", subtitle: "Needs Attention")
            menu.addInfo("Status", message)
        case .missing(let message):
            menu.addHeader("bx", subtitle: "Not Installed")
            menu.addInfo("Status", message)
        case .off:
            menu.addHeader("bx", subtitle: "Off")
            menu.addInfo("Status", "Not running")
        }
        menu.addItem(.separator())
        menu.addAction("Open Status", symbol: "list.bullet.rectangle", target: self, action: #selector(openStatus))
        menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
        menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        menu.addItem(.separator())
        menu.addAction("Restart bx", symbol: "arrow.clockwise", target: self, action: #selector(restartBx))
        menu.addAction("Turn Off", symbol: "power", target: self, action: #selector(turnOff))
        menu.addItem(.separator())
        menu.addAction("Quit", symbol: "xmark.circle", target: self, action: #selector(quit))
        statusItem.menu = menu
    }

    @objc private func openStatus() {
        openTerminal("'\(bxPath)' status; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func openLogs() {
        openTerminal("'\(bxPath)' logs -n 120; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func runDoctor() {
        openTerminal("'\(bxPath)' doctor; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func restartBx() {
        runPrivileged("'\(bxPath)' down && '\(bxPath)' up")
        refresh()
    }

    @objc private func turnOff() {
        let alert = NSAlert()
        alert.messageText = "Turn off bx?"
        alert.informativeText = "This stops bx and restores managed DNS settings."
        alert.addButton(withTitle: "Turn Off")
        alert.addButton(withTitle: "Cancel")
        if alert.runModal() == .alertFirstButtonReturn {
            runPrivileged("'\(bxPath)' down")
            refresh()
        }
    }

    @objc private func quit() {
        NSApp.terminate(nil)
    }

    private func runPrivileged(_ command: String) {
        let script = "do shell script \(shellQuoted(command)) with administrator privileges"
        _ = runAppleScript(script)
    }

    private func openTerminal(_ command: String) {
        let script = """
        tell application "Terminal"
          activate
          do script \(shellQuoted(command))
        end tell
        """
        _ = runAppleScript(script)
    }

    private func runAppleScript(_ source: String) -> Bool {
        var error: NSDictionary?
        NSAppleScript(source: source)?.executeAndReturnError(&error)
        return error == nil
    }

    private func shellQuoted(_ value: String) -> String {
        let escaped = value.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
        return "\"\(escaped)\""
    }
}

private extension NSMenu {
    func addHeader(_ title: String, subtitle: String) {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.attributedTitle = NSAttributedString(
            string: title,
            attributes: [.font: NSFont.systemFont(ofSize: 14, weight: .semibold)]
        )
        addItem(item)
        addItem(NSMenuItem(title: subtitle, action: nil, keyEquivalent: ""))
        addItem(.separator())
    }

    func addInfo(_ label: String, _ value: String) {
        let item = NSMenuItem(title: "\(label): \(value)", action: nil, keyEquivalent: "")
        item.isEnabled = false
        addItem(item)
    }

    func addAction(_ title: String, symbol: String, target: AnyObject, action: Selector) {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: "")
        item.target = target
        item.image = NSImage(systemSymbolName: symbol, accessibilityDescription: title)
        addItem(item)
    }
}

private extension NSImage {
    func tinted(_ color: NSColor) -> NSImage {
        let copy = self.copy() as! NSImage
        copy.lockFocus()
        color.set()
        NSRect(origin: .zero, size: copy.size).fill(using: .sourceAtop)
        copy.unlockFocus()
        return copy
    }
}
