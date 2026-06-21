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

struct DoctorReport: Decodable {
    let checks: [DoctorCheck]
}

struct DoctorCheck: Decodable {
    let name: String
    let status: String
    let detail: String?
    let hint: String?
}

struct CommandResult {
    let code: Int32
    let stdout: String
    let stderr: String
}

enum BxState {
    case connected(BxReport, version: String, dns: String?)
    case warning(String, version: String?)
    case updateNeeded(String, version: String?)
    case setupNeeded(String)
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
        let version = loadVersion()
        if !cliSupportsDiagnosticsArchive() {
            return .updateNeeded("Update bx CLI", version: version)
        }
        let status = runBx(["status", "--json"])
        guard status.code == 0 else {
            return diagnoseStopped(version: version, fallback: status.stderr)
        }
        let data = Data(status.stdout.utf8)
        guard let report = try? JSONDecoder().decode(BxReport.self, from: data) else {
            return .warning("Status unreadable", version: version)
        }
        return report.tunnelHealthy ? .connected(report, version: version ?? "unknown", dns: loadDNSStatus()) : .warning("Tunnel unhealthy", version: version)
    }

    private func updateIcon() {
        guard let button = statusItem.button else { return }
        let symbol: String
        let tint: NSColor
        switch state {
        case .connected:
            symbol = "checkmark.shield"
            tint = .controlAccentColor
        case .warning, .updateNeeded:
            symbol = "exclamationmark.triangle"
            tint = .systemYellow
        case .setupNeeded:
            symbol = "wrench.and.screwdriver"
            tint = .systemOrange
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
        case .connected(let report, let version, let dns):
            menu.addHeader("bx", subtitle: "Connected")
            menu.addInfo("Status", "Protected")
            menu.addInfo("Tunnel", "\(report.latencyMS) ms")
            menu.addInfo("UDP Relay", report.udpMode == "proxy" ? "On" : "Off")
            if let dns {
                menu.addInfo("DNS", dns)
            }
            menu.addInfo("Active", "\(report.active)")
            menu.addInfo("Version", version)
        case .warning(let message, let version):
            menu.addHeader("bx", subtitle: "Needs Attention")
            menu.addInfo("Status", message)
            if let version {
                menu.addInfo("Version", version)
            }
        case .updateNeeded(let message, let version):
            menu.addHeader("bx", subtitle: "Update Required")
            menu.addInfo("Status", message)
            if let version {
                menu.addInfo("Version", version)
            }
        case .setupNeeded(let message):
            menu.addHeader("bx", subtitle: "Setup Required")
            menu.addInfo("Status", message)
        case .missing(let message):
            menu.addHeader("bx", subtitle: "Not Installed")
            menu.addInfo("Status", message)
        case .off:
            menu.addHeader("bx", subtitle: "Off")
            menu.addInfo("Status", "Not running")
        }
        menu.addItem(.separator())
        switch state {
        case .setupNeeded:
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        case .missing, .updateNeeded:
            break
        default:
            menu.addAction("Open Status", symbol: "list.bullet.rectangle", target: self, action: #selector(openStatus))
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        }
        menu.addItem(.separator())
        switch state {
        case .connected:
            menu.addAction("Restart bx", symbol: "arrow.clockwise", target: self, action: #selector(restartBx))
            menu.addAction("Turn Off", symbol: "power", target: self, action: #selector(turnOff))
        case .warning, .off:
            menu.addAction("Start bx", symbol: "play.fill", target: self, action: #selector(startBx))
        case .updateNeeded:
            menu.addAction("Open Install Guide", symbol: "book", target: self, action: #selector(openInstallGuide))
        case .setupNeeded:
            menu.addAction("Set Up bx...", symbol: "link", target: self, action: #selector(setUpBx))
        case .missing:
            break
        }
        menu.addItem(.separator())
        menu.addAction("Quit", symbol: "xmark.circle", target: self, action: #selector(quit))
        statusItem.menu = menu
    }

    @objc private func openStatus() {
        openTerminal("'\(bxPath)' status; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func openLogs() {
        openTerminal("diag=\"$HOME/Library/Logs/bx/diagnostics\"; if [ -d \"$diag\" ]; then open \"$diag\"; else '\(bxPath)' logs -n 120; fi; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func runDoctor() {
        openTerminal("diag=\"$HOME/Library/Logs/bx/diagnostics\"; mkdir -p \"$diag\"; '\(bxPath)' doctor; '\(bxPath)' logs --archive --dir \"$diag\"; latest=$(find \"$diag\" -maxdepth 1 -type d -name 'bx-logs-*' | sort | tail -1); if [ -n \"$latest\" ]; then open \"$latest\"; fi; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func startBx() {
        if !runPrivileged("'\(bxPath)' up") {
            showMessage("Start Failed", "bx did not start. Run Doctor for details.")
        }
        refresh()
    }

    @objc private func setUpBx() {
        guard let link = promptForClientLink() else { return }
        let command = "'\(bxPath)' setup \(shellSingleQuoted(link))"
        guard runPrivileged(command) else {
            showMessage("Setup Failed", "bx was not configured. Run Doctor for details.")
            refresh()
            return
        }
        let alert = NSAlert()
        alert.messageText = "bx is set up"
        alert.informativeText = "Start bx now?"
        alert.addButton(withTitle: "Start")
        alert.addButton(withTitle: "Later")
        if alert.runModal() == .alertFirstButtonReturn {
            if !runPrivileged("'\(bxPath)' up") {
                showMessage("Start Failed", "bx is configured, but did not start. Run Doctor for details.")
            }
        }
        refresh()
    }

    @objc private func openInstallGuide() {
        openTerminal("cd \"$HOME/Documents/bx\" 2>/dev/null || true; echo 'Update bx CLI:'; echo 'sudo install -m 0755 ./bx /usr/local/bin/bx'; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func restartBx() {
        if !runPrivileged("'\(bxPath)' down && '\(bxPath)' up") {
            showMessage("Restart Failed", "bx did not restart. Run Doctor for details.")
        }
        refresh()
    }

    @objc private func turnOff() {
        let alert = NSAlert()
        alert.messageText = "Turn off bx?"
        alert.informativeText = "This stops bx and restores managed DNS settings."
        alert.addButton(withTitle: "Turn Off")
        alert.addButton(withTitle: "Cancel")
        if alert.runModal() == .alertFirstButtonReturn {
            if !runPrivileged("'\(bxPath)' down") {
                showMessage("Turn Off Failed", "bx did not stop. Run Doctor for details.")
            }
            refresh()
        }
    }

    @objc private func quit() {
        NSApp.terminate(nil)
    }

    private func runPrivileged(_ command: String) -> Bool {
        let script = "do shell script \(shellQuoted(command)) with administrator privileges"
        return runAppleScript(script)
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

    private func promptForClientLink() -> String? {
        let alert = NSAlert()
        alert.messageText = "Set Up bx"
        alert.informativeText = "Paste your bx link."
        alert.addButton(withTitle: "Set Up")
        alert.addButton(withTitle: "Cancel")

        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 420, height: 24))
        field.placeholderString = "bx://..."
        alert.accessoryView = field

        guard alert.runModal() == .alertFirstButtonReturn else { return nil }
        let link = field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !link.isEmpty else {
            showMessage("No Link", "Paste a bx link to continue.")
            return nil
        }
        return link
    }

    private func showMessage(_ title: String, _ message: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    private func diagnoseStopped(version: String?, fallback: String) -> BxState {
        let doctor = runBx(["doctor", "--json", "--skip-probe"])
        guard doctor.code == 0, let report = try? JSONDecoder().decode(DoctorReport.self, from: Data(doctor.stdout.utf8)) else {
            let message = fallback.trimmingCharacters(in: .whitespacesAndNewlines)
            return .warning(message.isEmpty ? "Status unavailable" : message, version: version)
        }
        if check(report, "service_installed")?.status == "fail" {
            return .setupNeeded("Run sudo bx setup <client-link>")
        }
        if check(report, "service_active")?.status != "ok" {
            return .off
        }
        if let socket = check(report, "status_socket"), socket.status != "ok" {
            return .warning(socket.detail ?? "Status socket unavailable", version: version)
        }
        return .warning("Needs attention", version: version)
    }

    private func check(_ report: DoctorReport, _ name: String) -> DoctorCheck? {
        report.checks.first { $0.name == name }
    }

    private func loadVersion() -> String? {
        let result = runBx(["--version"])
        guard result.code == 0 else { return nil }
        return result.stdout.replacingOccurrences(of: "bx version ", with: "").trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func cliSupportsDiagnosticsArchive() -> Bool {
        let result = runBx(["logs", "--help"])
        return result.code == 0 && result.stdout.contains("--archive") && result.stdout.contains("--dir")
    }

    private func loadDNSStatus() -> String? {
        let result = runBx(["dns", "status"])
        guard result.code == 0 else { return nil }
        let lines = result.stdout.split(separator: "\n").map(String.init)
        let enabled = value(in: lines, key: "enabled")
        let service = value(in: lines, key: "service")
        if enabled == "true", let service {
            return "\(service) managed"
        }
        if enabled == "true" {
            return "Managed"
        }
        return "Not managed"
    }

    private func value(in lines: [String], key: String) -> String? {
        let prefix = key + ":"
        guard let line = lines.first(where: { $0.hasPrefix(prefix) }) else { return nil }
        return line.dropFirst(prefix.count).trimmingCharacters(in: .whitespacesAndNewlines)
    }

    private func runBx(_ arguments: [String]) -> CommandResult {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: bxPath)
        process.arguments = arguments
        let output = Pipe()
        let errors = Pipe()
        process.standardOutput = output
        process.standardError = errors
        do {
            try process.run()
            process.waitUntilExit()
        } catch {
            return CommandResult(code: 127, stdout: "", stderr: error.localizedDescription)
        }
        let stdout = String(data: output.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        let stderr = String(data: errors.fileHandleForReading.readDataToEndOfFile(), encoding: .utf8) ?? ""
        return CommandResult(code: process.terminationStatus, stdout: stdout, stderr: stderr)
    }

    private func shellQuoted(_ value: String) -> String {
        let escaped = value.replacingOccurrences(of: "\\", with: "\\\\").replacingOccurrences(of: "\"", with: "\\\"")
        return "\"\(escaped)\""
    }

    private func shellSingleQuoted(_ value: String) -> String {
        "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
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
