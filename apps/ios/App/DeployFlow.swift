import Bxkit
import Foundation

// Setting up a server from the iPhone, for someone whose first bx is the phone. The judgment (what
// to run on the server, reuse vs install, what each failure means) is Go's internal/deploy — the
// same one the Mac's Set Up a New Server window runs; this file only shows it.
//
// The password: typed into a SecureField, handed to BxdeployDeploy for this one connection, never
// stored (not in the Keychain, not in settings), and cleared from the form when the deploy starts.

struct DeployEvent: Decodable, Equatable {
    var event: String
    var step: String?
    var link: String?
    var udp: String?
    var host: String?
    var reused: Bool?
    var code: String?
    var detail: String?
}

/// The steps the phone shows, in order (internal/deploy's ids).
let phoneDeploySteps = ["connect", "download", "install", "firewall", "start"]

func phoneDeployStepTitle(_ id: String) -> String {
    switch id {
    case "connect": return "Connect to the server"
    case "download": return "Download bx onto the server"
    case "install": return "Install the bx server"
    case "firewall": return "Open the port in the server's firewall"
    case "start": return "Start it"
    default: return id
    }
}

enum PhoneDeployRetry: Equatable { case reinstall, forgetHostKey }

struct PhoneDeployFailure: Equatable {
    var headline: String
    var advice: String
    var retry: PhoneDeployRetry?
}

/// Failure code → words. Unknown codes fall to the general one; nothing is guessed.
func phoneDeployFailure(_ code: String) -> PhoneDeployFailure {
    switch code {
    case "unreachable":
        return .init(headline: "Could not reach the server",
                     advice: "Check the address and the SSH port, and that the server is running. A new server can take a few minutes to come up.")
    case "auth_failed":
        return .init(headline: "The login was not accepted",
                     advice: "Check the login name and the password. Your provider shows both in its console, usually under the server's details.")
    case "host_key_changed":
        return .init(headline: "This server's identity changed",
                     advice: "That is expected if you reinstalled the server. If you did not, someone may be in between — do not continue.",
                     retry: .forgetHostKey)
    case "already_installed":
        return .init(headline: "bx on this server could not be used as it is",
                     advice: "Reinstalling creates new keys: links you shared from this server stop working.",
                     retry: .reinstall)
    case "port_in_use":
        return .init(headline: "The port bx needs is already used on this server",
                     advice: "Another program on the server uses it (see the details) — bx did not change anything. Stop that program, or use a server with nothing else on it.")
    case "password_change_required":
        return .init(headline: "The server wants a new password first",
                     advice: "Log in once from your provider's web console, set a new password, then try again with it.")
    case "sudo_password":
        return .init(headline: "This login needs a password for administrator rights",
                     advice: "Enter the login's password, or log in as root.")
    case "unsupported_system":
        return .init(headline: "This server's system is not supported",
                     advice: "bx needs 64-bit Linux with systemd (x86_64 or ARM), such as Ubuntu or Debian.")
    case "checksum":
        return .init(headline: "The download did not match its signature",
                     advice: "Nothing was installed. Try again later.")
    default:
        return .init(headline: "Installation failed", advice: "See the details below.")
    }
}

/// The command for a provider's web console, when password login is not possible. It picks the
/// right architecture itself and prints a `bx setup …` line the app accepts as it is.
let webConsoleInstallCommand = #"a=$(uname -m); case "$a" in x86_64) a=amd64;; aarch64) a=arm64;; esac; curl -fsSL "https://github.com/getbx/bx/releases/latest/download/bx_linux_$a.tar.gz" | tar xz && sudo ./bx server up --open-ufw"#

/// The main link from what a person pasted: a bare link, or the whole `sudo bx setup [--udp 'B'] 'A'`
/// line the server printed (the UDP one is for Macs; the iPhone runs the main reality link).
func mainLinkFromPaste(_ text: String) -> String {
    let words = text.split(whereSeparator: { $0 == " " || $0 == "\n" || $0 == "\t" })
        .map { $0.trimmingCharacters(in: CharacterSet(charactersIn: "'\"`")) }
    var skipNext = false
    for word in words {
        if skipNext { skipNext = false; continue }
        if word == "--udp" { skipNext = true; continue }
        if word.hasPrefix("bx://") || word.hasPrefix("vless://") { return word }
    }
    return text.trimmingCharacters(in: .whitespacesAndNewlines)
}

/// Runs a deploy. The real one calls Go; UI tests swap in a scripted one (no network).
protocol PhoneDeployRunner {
    func run(address: String, sshPort: Int, user: String, password: String,
             reinstall: Bool, forgetHostKey: Bool, onEvent: @escaping (DeployEvent) -> Void)
}

struct GoDeployRunner: PhoneDeployRunner {
    func run(address: String, sshPort: Int, user: String, password: String,
             reinstall: Bool, forgetHostKey: Bool, onEvent: @escaping (DeployEvent) -> Void) {
        let knownHosts = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("bx/known_hosts").path
        let listener = DeployListener(onEvent: onEvent)
        DispatchQueue.global(qos: .userInitiated).async {
            BxdeployDeploy(address, sshPort, user, password, reinstall, forgetHostKey, knownHosts, listener)
        }
    }
}

final class DeployListener: NSObject, BxdeployListenerProtocol {
    let onEvent: (DeployEvent) -> Void
    init(onEvent: @escaping (DeployEvent) -> Void) { self.onEvent = onEvent }
    func event(_ line: String?) {
        guard let data = line?.data(using: .utf8), let e = try? JSONDecoder().decode(DeployEvent.self, from: data) else { return }
        DispatchQueue.main.async { self.onEvent(e) }
    }
}

/// UI tests (`--fixture-deploy <ok|reused|port>`): the same events Go would send, without a network.
struct ScriptedDeployRunner: PhoneDeployRunner {
    let outcome: String
    func run(address: String, sshPort: Int, user: String, password: String,
             reinstall: Bool, forgetHostKey: Bool, onEvent: @escaping (DeployEvent) -> Void) {
        var events = [DeployEvent(event: "step", step: "connect")]
        if outcome == "port" {
            events.append(DeployEvent(event: "error", code: "port_in_use", detail: "port 443 on the server is already used by nginx; nothing was changed"))
        } else {
            events += ["download", "install", "firewall", "start"].map { DeployEvent(event: "step", step: $0) }
            events.append(DeployEvent(event: "done", link: FixtureLinks.reality, host: "203.0.113.9", reused: outcome == "reused"))
        }
        for (i, e) in events.enumerated() {
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.05 * Double(i + 1)) { onEvent(e) }
        }
    }
}
