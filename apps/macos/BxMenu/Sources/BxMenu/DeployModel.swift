import Foundation

/// 「把 bx server 装到一台 VPS 上」那一半的**纯逻辑**。
///
/// ## 为什么是「填表 → 交给 Terminal」而不是在 app 里跑完
///
/// **bx 一行 SSH 凭据都不经手。** 密码、密钥、agent、known_hosts 全归用户自己的
/// ssh 客户端 —— 这是 `bx server deploy` 从第一天起的设计,GUI 不该把它推翻。
/// 而菜单是 LSUIElement 应用,没有 TTY:ssh 要问密码时无处可问。两件事合起来
/// 只有一个诚实的答案 —— 表单负责把命令拼对(那才是小白真正卡住的地方),
/// 执行交给一个有终端的地方,ssh 在那里照常问它要问的。
///
/// 用户因此**看得见**将要执行的那条命令。这不是妥协,是这条路上唯一能同时满足
/// 「不碰凭据」与「不用背命令」的形状。
struct DeployTarget: Equatable {
    /// 机器地址,或 ~/.ssh/config 里的别名。
    var host: String = ""
    /// SSH 登录用户。deploy 今天假定能拿到 root(它要装 systemd 服务)。
    var user: String = "root"
    /// 装好之后在本机清单里叫什么。**留空 = 不自动加进清单**。
    var name: String = ""
}

/// 表单能不能提交。返回 nil = 可以。
///
/// **这里挡的不是攻击,是打字错误** —— 真正的防注入在拼命令那一步(整段单引号
/// 包起来)。但一个带空格的主机名会拼出一条看起来对、跑起来错的命令,而用户
/// 只会看到 ssh 的一句莫名其妙的报错。
func deployValidationError(_ target: DeployTarget) -> String? {
    let host = target.host.trimmingCharacters(in: .whitespaces)
    if host.isEmpty {
        return "Enter the server address (an IP, a hostname, or an ssh_config alias)."
    }
    if host.contains(where: { $0.isWhitespace }) || host.contains("'") || host.contains("@") {
        return "The address cannot contain spaces, quotes, or @ — put the login name in the User field."
    }
    let user = target.user.trimmingCharacters(in: .whitespaces)
    if user.isEmpty {
        return "Enter the SSH login name (usually root)."
    }
    if user.contains(where: { $0.isWhitespace }) || user.contains("'") || user.contains("@") {
        return "The login name cannot contain spaces, quotes, or @."
    }
    let name = target.name.trimmingCharacters(in: .whitespaces)
    if !name.isEmpty {
        // 与 Go 侧 config.ValidateServerName 同一条规则。**两边必须一致**:
        // 这里放行而那边拒绝,用户会看着一条成功的部署以一句配置错误收场。
        if name.count > 64 {
            return "The name is too long (64 characters maximum)."
        }
        // **必须是 ASCII 判据,不能用 CharacterSet.alphanumerics** —— 后者是
        // Unicode 的,`东京` 在它眼里是合法的字母,而 Go 侧的
        // `^[A-Za-z0-9._-]+$` 会当场拒绝。测试第一次跑就抓到了这个分叉。
        let allowed = Set("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-")
        if name.contains(where: { !allowed.contains($0) }) {
            return "The name may only contain letters, digits, and . _ -"
        }
    }
    return nil
}

/// 拼出要执行的那条命令。
///
/// **每一个插值都单引号包起来。** 值来自一个文本框;不包的话一个引号就能改变
/// 这条命令的结构 —— 与 GuardianClient 用 JSONSerialization 而不是手拼 JSON
/// 同一条纪律。
func deployCommandLine(_ target: DeployTarget, bxPath: String = "/usr/local/bin/bx") -> String {
    let host = target.host.trimmingCharacters(in: .whitespaces)
    let user = target.user.trimmingCharacters(in: .whitespaces)
    let name = target.name.trimmingCharacters(in: .whitespaces)
    var parts = [shellQuoted(bxPath), "server", "deploy"]
    if !name.isEmpty {
        parts.append("--name")
        parts.append(shellQuoted(name))
    }
    parts.append(shellQuoted("\(user)@\(host)"))
    return parts.joined(separator: " ")
}

/// 交给 Terminal 执行的那个脚本。
///
/// **头两行是给人看的**:用户在自己的终端里看到 bx 将要做什么,以及一句
/// 「凭据不经过 bx」。一个会 ssh 到别人机器上装东西的动作,不该只在别处解释过。
func deployScriptText(_ target: DeployTarget, bxPath: String = "/usr/local/bin/bx") -> String {
    """
    #!/bin/sh
    echo 'bx is about to install a server on \(target.host.trimmingCharacters(in: .whitespaces)) over ssh.'
    echo 'Your SSH password or key is handled by ssh itself — bx never sees it.'
    echo
    exec \(deployCommandLine(target, bxPath: bxPath))
    """
}

/// 表单上那句解释。**说清楚凭据去哪儿了**,这是这个设计唯一需要用户理解的事。
let deployCredentialNote =
    "bx never handles your SSH password or key. The command runs in Terminal, "
    + "where ssh asks for whatever it needs."

func shellQuoted(_ value: String) -> String {
    "'" + value.replacingOccurrences(of: "'", with: "'\\''") + "'"
}
