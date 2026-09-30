import Foundation

/// 「Set Up a New Server」窗口的**纯逻辑**(AppKit 那一半在 CI 里编不了,判断放这儿才测得到)。
///
/// ## 密码在窗口里填(所有者 2026-09-30 定的)
///
/// 此前的形状是「表单拼命令 → 交给 Terminal,ssh 在那里问密码」,理由是 bx 不经手凭据。
/// 所有者否掉了它:这个窗口是给小白用的,让他面对一个终端、在里面盲打密码,就等于没做;
/// 而且「在终端里输入」在他们的理解里并不代表「bx 不知道密码」。于是:
///   - 密码只经 **stdin** 交给 `bx server deploy --password-stdin`(不进 argv —— 本机任何进程都
///     看得见 argv;不进环境变量),那边只在内存里交给 ssh(internal/sshpass),用完即丢;
///   - 这里不存、不写盘、不进钥匙串;窗口一开始部署就清空密码框。
/// 装好之后**只加进清单、不切换**:换出口是用户在清单里显式的一下。
struct DeployTarget: Equatable {
    /// 服务器地址(IP 或主机名,也可以是 ~/.ssh/config 里的别名)。
    var host: String = ""
    /// SSH 端口。空或 22 = 默认。
    var sshPort: String = "22"
    /// SSH 登录用户。多数 VPS 给的是 root。
    var user: String = "root"
    /// 在清单里叫什么。空 = 用地址。
    var name: String = ""
}

struct DeployOptions: Equatable {
    var hasPassword = false
    /// 这台上已经装过 bx server,用户确认要重装(新钥匙)。
    var reinstall = false
    /// 用户确认重装过这台服务器:忘掉 bx 记下的旧指纹。
    var forgetHostKey = false
}

/// 表单能不能提交。nil = 可以。挡的是打字错误;防注入在 Go 那边(整段单引号)。
func deployValidationError(_ target: DeployTarget) -> String? {
    let host = target.host.trimmingCharacters(in: .whitespaces)
    if host.isEmpty {
        return L("Enter the server address (an IP or a hostname).")
    }
    if host.contains(where: { $0.isWhitespace }) || host.contains("'") || host.contains("@") {
        return L("The address cannot contain spaces, quotes, or @ — put the login name in its own field.")
    }
    let port = target.sshPort.trimmingCharacters(in: .whitespaces)
    if !port.isEmpty {
        guard let n = Int(port), (1...65535).contains(n) else {
            return L("The SSH port is a number from 1 to 65535 (usually 22).")
        }
    }
    let user = target.user.trimmingCharacters(in: .whitespaces)
    if user.isEmpty {
        return L("Enter the SSH login name (usually root).")
    }
    if user.contains(where: { $0.isWhitespace }) || user.contains("'") || user.contains("@") {
        return L("The login name cannot contain spaces, quotes, or @.")
    }
    let name = target.name.trimmingCharacters(in: .whitespaces)
    if !name.isEmpty {
        // 与 Go 侧 config.ValidateServerName 同一条规则(ASCII 判据:Unicode 的 alphanumerics
        // 会放行「东京」而 Go 那边拒绝)。
        if name.count > 64 {
            return L("The name is too long (64 characters maximum).")
        }
        let allowed = Set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-")
        if name.contains(where: { !allowed.contains($0) }) {
            return L("The name may only contain letters, digits, and . _ -")
        }
    }
    return nil
}

/// 交给 bx 的参数。**密码不在这里** —— 只有 `--password-stdin` 这个开关。
func deployArguments(_ target: DeployTarget, _ options: DeployOptions) -> [String] {
    var args = ["server", "deploy", "--json"]
    let port = target.sshPort.trimmingCharacters(in: .whitespaces)
    if !port.isEmpty && port != "22" {
        args += ["--ssh-port", port]
    }
    if options.hasPassword {
        args.append("--password-stdin")
    }
    let name = target.name.trimmingCharacters(in: .whitespaces)
    if !name.isEmpty {
        args += ["--name", name]
    }
    if options.reinstall {
        args.append("--force")
    }
    if options.forgetHostKey {
        args.append("--forget-host-key")
    }
    args.append(target.user.trimmingCharacters(in: .whitespaces) + "@" + target.host.trimmingCharacters(in: .whitespaces))
    return args
}

// MARK: - 进度

struct DeployProbe: Decodable, Equatable {
    var measured: Bool
    var reachable: Bool
    var rttMS: Int?
    var errorCode: String?

    init(measured: Bool, reachable: Bool, rttMS: Int? = nil, errorCode: String? = nil) {
        self.measured = measured
        self.reachable = reachable
        self.rttMS = rttMS
        self.errorCode = errorCode
    }

    enum CodingKeys: String, CodingKey {
        case measured, reachable
        case rttMS = "rtt_ms"
        case errorCode = "error_code"
    }
}

/// `bx server deploy --json` 的一行。
struct DeployEvent: Decodable, Equatable {
    var event: String
    var step: String?
    var name: String?
    var host: String?
    var added: Bool?
    var replaced: Bool?
    var current: Bool?
    var probe: DeployProbe?
    var notSetUp: Bool?
    var link: String?
    var udp: String?
    var code: String?
    var detail: String?

    init(event: String, step: String? = nil, name: String? = nil, host: String? = nil, added: Bool? = nil,
         replaced: Bool? = nil, current: Bool? = nil, probe: DeployProbe? = nil, notSetUp: Bool? = nil,
         link: String? = nil, udp: String? = nil, code: String? = nil, detail: String? = nil) {
        self.event = event; self.step = step; self.name = name; self.host = host; self.added = added
        self.replaced = replaced; self.current = current; self.probe = probe; self.notSetUp = notSetUp
        self.link = link; self.udp = udp; self.code = code; self.detail = detail
    }

    enum CodingKeys: String, CodingKey {
        case event, step, name, host, added, replaced, current, probe, link, udp, code, detail
        case notSetUp = "not_set_up"
    }
}

func parseDeployEvent(_ line: String) -> DeployEvent? {
    guard let data = line.trimmingCharacters(in: .whitespacesAndNewlines).data(using: .utf8), !data.isEmpty else { return nil }
    return try? JSONDecoder().decode(DeployEvent.self, from: data)
}

/// 步骤的顺序 —— 与 Go 那边 runServerDeploy / runDeployForMenu 发出的 step id 一一对应。
let deploySteps = ["connect", "download", "install", "firewall", "start", "add", "test"]

func deployStepTitle(_ id: String) -> String {
    switch id {
    case "connect": return L("Connect to the server")
    case "download": return L("Download bx onto the server")
    case "install": return L("Install the bx server")
    case "firewall": return L("Open the port in the server's firewall")
    case "start": return L("Start it")
    case "add": return L("Add it to your server list")
    case "test": return L("Test the connection from this Mac")
    default: return id
    }
}

enum DeployStepState: Equatable { case pending, running, done, failed }

struct DeployProgress: Equatable, CustomStringConvertible {
    private var states: [String: DeployStepState] = [:]

    func state(_ id: String) -> DeployStepState { states[id] ?? .pending }

    mutating func apply(_ event: DeployEvent) {
        switch event.event {
        case "step":
            for (id, s) in states where s == .running { states[id] = .done }
            if let id = event.step { states[id] = .running }
        case "done":
            for (id, s) in states where s == .running { states[id] = .done }
        case "error":
            for (id, s) in states where s == .running { states[id] = .failed }
        default:
            break
        }
    }

    var description: String { deploySteps.map { "\($0)=\(state($0))" }.joined(separator: " ") }
}

// MARK: - 结局

enum DeployRetryAction: Equatable { case reinstall, forgetHostKey }

struct DeployFailurePresentation: Equatable {
    var headline: String
    var advice: String
    var action: DeployRetryAction?
}

/// 失败码 → 人话。认不出的码落到通用那句,**不猜**。
func deployFailure(_ code: String) -> DeployFailurePresentation {
    switch code {
    case "unreachable":
        return .init(headline: L("Could not reach the server"),
                     advice: L("Check the address and the SSH port, and that the server is running. A new server can take a few minutes to come up."))
    case "auth_failed":
        return .init(headline: L("The login was not accepted"),
                     advice: L("Check the login name and the password. Your provider shows both in its console, usually under the server's details."))
    case "host_key_changed":
        return .init(headline: L("This server's identity changed"),
                     advice: L("That is expected if you reinstalled the server. If you did not, someone may be in between — do not continue."),
                     action: .forgetHostKey)
    case "already_installed":
        return .init(headline: L("bx is already installed on this server"),
                     advice: L("Reinstalling creates new keys: links you shared from this server stop working."),
                     action: .reinstall)
    case "password_change_required":
        return .init(headline: L("The server wants a new password first"),
                     advice: L("Log in once from your provider's web console, set a new password, then try again with it."))
    case "sudo_password":
        return .init(headline: L("This login needs a password for administrator rights"),
                     advice: L("Enter the login's password, or log in as root."))
    case "unsupported_system":
        return .init(headline: L("This server's system is not supported"),
                     advice: L("bx needs 64-bit Linux with systemd (x86_64 or ARM), such as Ubuntu or Debian."))
    case "checksum":
        return .init(headline: L("The download did not match its signature"),
                     advice: L("Nothing was installed. Try again later."))
    default:
        return .init(headline: L("Installation failed"),
                     advice: L("See the details below. Nothing was added to your server list."))
    }
}

struct DeployResultPresentation: Equatable {
    var headline: String
    var detail: String
    var canOpenServers: Bool
    /// 这台 Mac 上 bx 还没配过:把这两条交给首次设置。
    var setUpLink: String?
    var setUpUDP: String?
}

func deployResult(_ e: DeployEvent) -> DeployResultPresentation {
    if e.notSetUp == true, let link = e.link {
        return .init(headline: L("The server is ready"),
                     detail: L("bx is not set up on this Mac yet. Set it up with this server now?"),
                     canOpenServers: false, setUpLink: link, setUpUDP: e.udp)
    }
    let name = e.name ?? e.host ?? ""
    var detail: String
    if e.replaced == true {
        detail = L("It now uses the new keys.")
        if e.current == true {
            detail += " " + L("You are using this server: turn protection off and on to switch to the new keys.")
        }
    } else {
        detail = L("It was added to your servers. Your current exit did not change — switch to it in Servers when you want.")
    }
    switch e.probe {
    case let probe? where probe.measured && probe.reachable:
        detail += "\n" + L("This Mac reached it in {0} ms.", probe.rttMS.map(String.init) ?? "?")
    case let probe? where probe.measured:
        detail += "\n" + L("But this Mac could not reach it. Your provider's firewall (often called a security group) is probably closed — open TCP and UDP port 443 there.")
    default:
        detail += "\n" + L("The connection was not tested (turn protection on, then check it in Servers).")
    }
    let unreachable = e.probe.map { $0.measured && !$0.reachable } ?? false
    let headline: String
    if e.replaced == true {
        headline = L("Updated “{0}”", name)
    } else if unreachable {
        headline = L("“{0}” is installed, but this Mac cannot reach it yet", name)
    } else {
        headline = L("“{0}” is ready", name)
    }
    return .init(headline: headline, detail: detail, canOpenServers: true)
}
