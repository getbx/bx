import AppKit
import Foundation

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

final class BxMenuApp: NSObject, NSApplicationDelegate {
    private let bxPath = "/usr/local/bin/bx"
    private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private let statusPanel = StatusPanelController()
    private var timer: Timer?
    private var state: BxState = .off

    func applicationDidFinishLaunching(_ notification: Notification) {
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
        if !cliSupportsDiagnosticsArchive() || !cliSupportsSafeReconnect() {
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
        button.image = compactStatusImage(for: statusIndicator(for: statusIndicatorState()))
        button.imagePosition = .imageOnly
        button.title = ""
        button.toolTip = tooltipText()
    }

    private func statusIndicatorState() -> StatusIndicatorState {
        switch state {
        case .connected:
            return .connected
        case .warning:
            return .warning
        case .updateNeeded:
            return .updateNeeded
        case .setupNeeded:
            return .setupNeeded
        case .missing:
            return .missing
        case .off:
            return .off
        }
    }

    private func compactStatusImage(for indicator: StatusIndicator) -> NSImage {
        let size = NSSize(width: 18, height: 18)
        let image = NSImage(size: size)
        image.lockFocus()
        defer { image.unlockFocus() }

        let shield = NSImage(systemSymbolName: "shield", accessibilityDescription: "bx")!
            .withSymbolConfiguration(.init(pointSize: 16, weight: .regular))!
        shield.draw(in: NSRect(x: 0, y: 1, width: 16, height: 16))

        let color: NSColor
        switch indicator {
        case .green:
            color = .systemGreen
        case .yellow:
            color = .systemYellow
        case .red:
            color = .systemRed
        case .gray:
            color = .secondaryLabelColor
        }
        color.setFill()
        NSBezierPath(ovalIn: NSRect(x: 12, y: 0, width: 6, height: 6)).fill()
        image.isTemplate = false
        return image
    }

    private func tooltipText() -> String {
        switch state {
        case .connected(let report, _, _):
            return "bx: Protected, \(report.latencyMS) ms"
        case .warning(let message, _):
            return "bx: \(message)"
        case .updateNeeded:
            return "bx: Update Required"
        case .setupNeeded:
            return "bx: Setup Required"
        case .missing:
            return "bx: Not Installed"
        case .off:
            return "bx: Off"
        }
    }

    private func rebuildMenu() {
        let menu = NSMenu()
        switch state {
        case .connected(let report, let version, let dns):
            menu.addHeader("bx", subtitle: "Connected")
            menu.addInfo("Status", "Protected")
            menu.addInfo("Tunnel", "\(report.latencyMS) ms")
            menu.addInfo("UDP Relay", udpRelayLabel(report.udpMode))
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
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        case .missing, .updateNeeded:
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
        case .off:
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        case .connected, .warning:
            menu.addAction("Open Status", symbol: "list.bullet.rectangle", target: self, action: #selector(openStatus))
            menu.addAction("View Logs", symbol: "doc.text", target: self, action: #selector(openLogs))
            menu.addAction("Run Doctor", symbol: "stethoscope", target: self, action: #selector(runDoctor))
        }
        menu.addItem(.separator())
        switch state {
        case .connected:
            menu.addAction("Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
            menu.addAction("Turn Off", symbol: "power", target: self, action: #selector(turnOff))
        case .warning:
            menu.addAction("Reconnect", symbol: "arrow.clockwise", target: self, action: #selector(reconnectBx))
            menu.addAction("Turn Off", symbol: "power", target: self, action: #selector(turnOff))
        case .off:
            menu.addAction("Start Protection", symbol: "play.fill", target: self, action: #selector(startBx))
        case .updateNeeded:
            menu.addAction("Open Install Guide", symbol: "book", target: self, action: #selector(openInstallGuide))
        case .setupNeeded:
            menu.addAction("Set Up bx...", symbol: "link", target: self, action: #selector(setUpBx))
        case .missing:
            menu.addAction("Open Install Guide", symbol: "book", target: self, action: #selector(openInstallGuide))
        }
        menu.addItem(.separator())
        menu.addAction("Quit", symbol: "xmark.circle", target: self, action: #selector(quit))
        statusItem.menu = menu
    }

    private func udpRelayLabel(_ mode: String?) -> String {
        switch mode {
        case nil, "", "proxy":
            return "On"
        case "direct-realtime":
            return "Direct"
        default:
            return "Blocked"
        }
    }

    @objc private func openStatus() {
        statusPanel.present(statusSnapshot())
    }

    private func statusSnapshot() -> StatusSnapshot {
        switch state {
        case .connected(let report, let version, let dns):
            return .protected(
                latency: "\(report.latencyMS) ms",
                udpRelay: udpRelayLabel(report.udpMode),
                dns: dns,
                active: "\(report.active)",
                version: version
            )
        case .warning(let message, let version):
            var rows = [StatusRow(label: "Status", value: message)]
            if let version {
                rows.append(StatusRow(label: "Version", value: version))
            }
            return StatusSnapshot(title: "Needs Attention", rows: rows)
        case .updateNeeded(let message, let version):
            var rows = [StatusRow(label: "Status", value: message)]
            if let version {
                rows.append(StatusRow(label: "Version", value: version))
            }
            return StatusSnapshot(title: "Update Required", rows: rows)
        case .setupNeeded(let message):
            return StatusSnapshot(title: "Setup Required", rows: [StatusRow(label: "Status", value: message)])
        case .missing(let message):
            return StatusSnapshot(title: "Not Installed", rows: [StatusRow(label: "Status", value: message)])
        case .off:
            return .off()
        }
    }

    @objc private func openLogs() {
        let url = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library")
            .appendingPathComponent("Logs")
            .appendingPathComponent("bx")
        do {
            try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
            NSWorkspace.shared.open(url)
        } catch {
            showMessage("Logs Unavailable", error.localizedDescription)
        }
    }

    @objc private func runDoctor() {
        openTerminal("diag=\"$HOME/Library/Logs/bx/diagnostics\"; mkdir -p \"$diag\"; sudo env BX_LOG_ARCHIVE_DIR=\"$diag\" '\(bxPath)' doctor; latest=$(find \"$diag\" -maxdepth 1 -type d -name 'bx-logs-*' | sort | tail -1); if [ -n \"$latest\" ]; then group=$(id -gn); sudo chown -R \"$USER:$group\" \"$latest\" 2>/dev/null || true; open \"$latest\"; fi; echo; read -n 1 -s -r -p 'Press any key to close'")
    }

    @objc private func startBx() {
        guard confirmStartProtection() else { return }
        if !runPrivileged("'\(bxPath)' up") {
            showFailure("Start Failed", "bx did not start.")
        }
        refresh()
    }

    @objc private func setUpBx() {
        guard let link = promptForClientLink() else { return }
        let command = "'\(bxPath)' setup \(shellSingleQuoted(link))"
        guard runPrivileged(command) else {
            showFailure("Setup Failed", "bx was not configured.")
            refresh()
            return
        }
        if confirmStartProtection(title: "bx is set up", cancelTitle: "Later") {
            if !runPrivileged("'\(bxPath)' up") {
                showFailure("Start Failed", "bx is configured, but did not start.")
            }
        }
        refresh()
    }

    @objc private func openInstallGuide() {
        let alert = NSAlert()
        alert.messageText = "Install bx"
        alert.informativeText = "Install the macOS bx package again, or update the CLI at /usr/local/bin/bx, then restart the menu bar app."
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    @objc private func reconnectBx() {
        if !runPrivileged("'\(bxPath)' reconnect") {
            showFailure("Reconnect Failed", "bx could not establish a replacement protected transport.")
        }
        refresh()
    }

    @objc private func turnOff() {
        let alert = NSAlert()
        alert.messageText = "Turn off protection?"
        alert.informativeText = "bx will stop protecting system traffic and restore managed DNS settings."
        alert.addButton(withTitle: "Turn Off")
        alert.addButton(withTitle: "Cancel")
        if alert.runModal() == .alertFirstButtonReturn {
            if !runPrivileged("'\(bxPath)' down") {
                showFailure("Turn Off Failed", "bx did not stop.")
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
        let bashCommand = "/bin/bash -lc \(shellSingleQuoted(command))"
        let script = """
        tell application "Terminal"
          activate
          do script \(shellQuoted(bashCommand))
        end tell
        """
        if !runAppleScript(script) {
            showMessage("Terminal Permission Needed", "Allow bx to control Terminal when macOS asks, then try again. You can review this in System Settings > Privacy & Security > Automation.")
        }
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
        guard looksLikeClientLink(link) else {
            showMessage("Link Not Recognized", "Paste a bx link to continue.")
            return nil
        }
        return link
    }

    private func confirmStartProtection(title: String = "Start protection?", cancelTitle: String = "Cancel") -> Bool {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = "bx will take over system traffic until you turn it off."
        alert.addButton(withTitle: "Start Protection")
        alert.addButton(withTitle: cancelTitle)
        return alert.runModal() == .alertFirstButtonReturn
    }

    private func looksLikeClientLink(_ link: String) -> Bool {
        link.hasPrefix("bx://") || link.hasPrefix("blink://") || link.hasPrefix("brook://")
    }

    private func showMessage(_ title: String, _ message: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = message
        alert.addButton(withTitle: "OK")
        alert.runModal()
    }

    private func showFailure(_ title: String, _ message: String) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = "\(message) Run Doctor to collect diagnostics."
        alert.addButton(withTitle: "Run Doctor")
        alert.addButton(withTitle: "OK")
        if alert.runModal() == .alertFirstButtonReturn {
            runDoctor()
        }
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

    private func cliSupportsSafeReconnect() -> Bool {
        let result = runBx(["reconnect", "--help"])
        return result.code == 0
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

private let bxMenuDelegate = BxMenuApp()
let bxApplication = NSApplication.shared
bxApplication.delegate = bxMenuDelegate
bxApplication.setActivationPolicy(.accessory)
bxApplication.run()
