// Package deploy is the platform-independent half of "install a bx server onto a VPS": what to run
// on the server, in what order, and what the answers mean. It touches nothing itself — the caller
// supplies a Session that runs commands on the server (the Mac: the system ssh; the iPhone: Go's
// x/crypto/ssh inside bxkit) and the Hooks for progress and fetching the binary. One copy of the
// judgment, two transports: the Mac window and the phone say the same things for the same server.
package deploy

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/getbx/bx/internal/releasemanifest"
)

// ReleaseDownloadBase is where release assets live: <base>/<tag>/<asset>.
const ReleaseDownloadBase = "https://github.com/getbx/bx/releases/download"

// Options is one deployment.
type Options struct {
	// Target is what the user typed (root@1.2.3.4, or an ssh_config alias) — for messages.
	Target string
	// Address is the server's address, for `bx server link --host` when an existing brook server
	// is reused (reality and hysteria2 links carry their own host).
	Address  string
	Protocol string
	SNI      string
	Port     int
	// Force reinstalls (new keys) even if a bx server is already there.
	Force bool
	// Password, when the login is not root, is fed to the remote `sudo -S`.
	Password string
}

// Session runs one command on the server as the login user. stdin, when non-nil, is fed to it;
// tty asks for a terminal (only an interactive ssh client can honor it — others ignore it).
type Session interface {
	Run(cmd string, stdin *string, tty bool) (string, error)
}

// Hooks are the caller's half.
type Hooks struct {
	// Step reports progress: connect, download, install, firewall, start.
	Step func(id string)
	// Say receives one human line per notable event; nil = discard.
	Say io.Writer
	// HasTTY: an interactive terminal is attached (sudo could prompt there).
	HasTTY bool
	// CanFeedStdin: the session can pass stdin (used for `sudo -S` with Options.Password).
	CanFeedStdin bool
	// FetchBinary has the server fetch and verify the linux/<arch> binary into UploadPaths' upload
	// path. nil = go straight to LocalFallback.
	FetchBinary func(arch string, runRemote func(script string) (string, error)) error
	// LocalFallback gets the binary on this machine and puts it at the upload path, for when the
	// server could not fetch it (but never after a checksum mismatch). nil = no fallback.
	LocalFallback func(arch, uploadPath string) error
}

// Result is what the client needs afterwards.
type Result struct {
	Main, UDP string
	// Reused: a working bx server was already there; its keys (and every link already handed
	// out) were kept.
	Reused bool
}

// Run deploys. On any failure nothing on this machine should be written by the caller.
func Run(opts Options, s Session, h Hooks) (Result, error) {
	say := h.Say
	if say == nil {
		say = io.Discard
	}
	step := func(id string) {
		if h.Step != nil {
			h.Step(id)
		}
	}
	step("connect")
	// One round trip answers "who am I" and "what architecture" — both decide what follows.
	probe, err := s.Run("id -u; uname -m", nil, false)
	if err != nil {
		return Result{}, fmt.Errorf("could not connect to %s: %w", opts.Target, err)
	}
	idLine, unameLine, ok := strings.Cut(strings.TrimSpace(probe), "\n")
	if !ok {
		return Result{}, fmt.Errorf("the remote probe output could not be understood: %q", strings.TrimSpace(probe))
	}
	sudo, err := NeedsSudo(idLine)
	if err != nil {
		return Result{}, err
	}
	arch, err := ArchFromUname(unameLine)
	if err != nil {
		return Result{}, err
	}
	// Not root and the password is at hand (the window): sudo reads the same password from stdin.
	sudoWithPassword := sudo && opts.Password != "" && h.CanFeedStdin
	if sudo {
		fmt.Fprintln(say, "• The remote login is not root, so the rest runs under sudo")
		if !h.HasTTY && !sudoWithPassword {
			fmt.Fprintln(say, "  (there is no terminal here, so sudo will fail if it asks for a password — set up NOPASSWD, or re-run this from a terminal)")
		}
	}
	// runRemote keeps "sudo or not, terminal or not" in one place: a single call that forgot to
	// wrap fails in the hardest way to read (every step before it succeeded).
	runRemote := func(script string) (string, error) {
		if sudoWithPassword {
			in := opts.Password + "\n"
			return s.Run(WrapSudoWithPassword(script), &in, false)
		}
		return s.Run(WrapSudo(script, sudo), nil, sudo && h.HasTTY)
	}

	// Look before acting: what is already on this server?
	port := opts.Port
	if port <= 0 {
		port = 443
	}
	surveyOut, _ := runRemote(SurveyCommand(port))
	found := ParseSurvey(surveyOut)
	reuse := found.Existing != "" && !opts.Force
	if !reuse && !opts.Force && len(found.PortHolders) > 0 {
		// Never take a port from someone's website or another proxy: say who holds it, change nothing.
		return Result{}, fmt.Errorf("port %d on the server is already used by %s; nothing was changed", port, strings.Join(found.PortHolders, ", "))
	}

	step("download")
	upload, final := UploadPaths()
	fetched := false
	if h.FetchBinary != nil {
		err := h.FetchBinary(arch, runRemote)
		switch {
		case err == nil:
			fetched = true
			fmt.Fprintln(say, "• The remote host fetched the binary itself and it checks out")
		case !ShouldFallBackToLocalUpload(err):
			// A checksum mismatch — **never** fall back: fetching it another way only hides it.
			return Result{}, fmt.Errorf("the remote verification failed: %w", err)
		case h.LocalFallback == nil:
			return Result{}, fmt.Errorf("the server could not download bx: %w", err)
		default:
			fmt.Fprintf(say, "• The remote host could not fetch it (%v), so it is downloaded here and uploaded\n", err)
		}
	}
	if !fetched {
		if h.LocalFallback == nil {
			return Result{}, fmt.Errorf("no way to get the bx binary onto the server")
		}
		if err := h.LocalFallback(arch, upload); err != nil {
			return Result{}, err
		}
	}

	step("install")
	if _, err := runRemote(fmt.Sprintf("chmod +x %s && mv %s %s", ShellQuote(upload), ShellQuote(upload), ShellQuote(final))); err != nil {
		return Result{}, fmt.Errorf("putting the binary in place: %w", err)
	}
	var out string
	if reuse {
		// Keep it: the keys, the users, every link already handed out. The running service keeps
		// the old binary until its next restart; nothing is interrupted.
		fmt.Fprintf(say, "• This server already runs a bx server (%s); keeping it and its keys\n", found.Existing)
		out, err = runRemote(ShellQuote(final) + " server link --host " + ShellQuote(opts.Address))
		if err != nil {
			return Result{}, fmt.Errorf("this server already has bx, but its configuration could not be read (reinstall to start over): %w\n%s", err, strings.TrimSpace(out))
		}
	} else {
		out, err = runRemote(InstallCommand(opts))
		if err != nil {
			return Result{}, fmt.Errorf("the remote installation failed: %w\n%s", err, strings.TrimSpace(out))
		}
	}
	main, udp, err := ClientLinksFromInstallOutput(out)
	if err != nil {
		return Result{}, err
	}

	// The firewall: Ubuntu's ufw denies incoming by default, and "installed" must mean reachable.
	// UDP too — hysteria2 is QUIC.
	step("firewall")
	fwOut, err := runRemote(FirewallCommand(port))
	switch {
	case err != nil:
		fmt.Fprintf(say, "⚠ The firewall port %d could not be opened automatically; if it is unreachable from outside, open it by hand: %v\n", port, err)
	case strings.TrimSpace(fwOut) != "":
		// Changing someone's firewall is said out loud.
		fmt.Fprintf(say, "• %s\n", strings.TrimSpace(fwOut))
	default:
		fmt.Fprintln(say, "• ufw is not enabled on the remote host, so no firewall was changed (if there is a cloud security group, remember to open that port)")
	}
	step("start")
	start := ShellQuote(final) + " server start"
	if reuse {
		start = "systemctl is-active --quiet bx-server || " + start
	}
	if _, err := runRemote(start); err != nil {
		fmt.Fprintf(say, "⚠ The remote service did not start by itself; log in and run bx server start once: %v\n", err)
	}
	return Result{Main: main, UDP: udp, Reused: reuse}, nil
}

// SurveyCommand asks the server what is already there: a bx server (its config's protocol) and
// who listens on the port bx would use. Read-only.
func SurveyCommand(port int) string {
	p := fmt.Sprintf("%d", port)
	holders := func(flags string) string {
		return `ss -H` + flags + ` "sport = :` + p + `" 2>/dev/null | grep -o 'users:(("[^"]*"' | cut -d'"' -f2 | sort -u | paste -sd, -`
	}
	return strings.Join([]string{
		`t=""`,
		`if [ -f /etc/bx/server.yaml ]; then t=$(sed -n 's/^type:[[:space:]]*//p' /etc/bx/server.yaml | head -n1); [ -n "$t" ] || t=unknown; fi`,
		`echo "existing=$t"`,
		`echo "tcp=$(` + holders("ltnp") + `)"`,
		`echo "udp=$(` + holders("lunp") + `)"`,
	}, "\n")
}

// Survey is what SurveyCommand found.
type Survey struct {
	// Existing is the protocol of a bx server already installed ("" = none).
	Existing string
	// PortHolders are the programs listening on the port (TCP or UDP), deduplicated.
	PortHolders []string
}

// ParseSurvey reads SurveyCommand's output. Anything it cannot read counts as "nothing found" —
// the install itself will then say what is wrong.
func ParseSurvey(out string) Survey {
	var s Survey
	seen := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "existing":
			s.Existing = strings.Trim(strings.TrimSpace(value), `"'`)
		case "tcp", "udp":
			for _, name := range strings.Split(value, ",") {
				if name = strings.TrimSpace(name); name != "" && !seen[name] {
					seen[name] = true
					s.PortHolders = append(s.PortHolders, name)
				}
			}
		}
	}
	return s
}

// ArchFromUname 把远端 `uname -m` 的输出映射成 release 的架构名。
//
// **认不出来硬失败。** 装错架构的二进制,远端报的是 `exec format error` ——
// 一句与真实原因毫无关系的话,而那时文件已经传上去、服务装了一半。
func ArchFromUname(out string) (string, error) {
	switch strings.TrimSpace(out) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("the remote architecture was not recognized (uname -m says %q); bx only ships linux/amd64 and linux/arm64",
		strings.TrimSpace(out))
}

// UploadPaths 是「先传到哪」和「再移到哪」。
//
// 分两步是因为直接覆盖一个**正在运行**的二进制会得到 `text file busy`,
// 而那时旧服务已经被停掉了。
func UploadPaths() (upload, final string) {
	return "/tmp/bx.deploy", "/usr/local/bin/bx"
}

// InstallCommand 拼远端要执行的安装命令。
//
// **用户给的每一段都要引起来。** 目标主机、SNI 这些来自命令行,而整条命令交给
// 远端 shell 执行 —— 少一对引号就是远程命令注入。
func InstallCommand(opts Options) string {
	_, final := UploadPaths()
	parts := []string{ShellQuote(final), "server", "install"}
	if p := strings.TrimSpace(opts.Protocol); p != "" {
		parts = append(parts, "--protocol", ShellQuote(p))
	}
	if s := strings.TrimSpace(opts.SNI); s != "" {
		parts = append(parts, "--sni", ShellQuote(s))
	}
	if opts.Port > 0 {
		parts = append(parts, "--port", fmt.Sprintf("%d", opts.Port))
	}
	if opts.Force {
		parts = append(parts, "--force")
	}
	return strings.Join(parts, " ")
}

// ClientLinksFromInstallOutput 取出主链接与(如果有的)UDP 链接。
//
// **必须剥掉引号**:`bx server install` 打的是一条可直接复制的命令
// (一整条可直接复制的 bx setup 命令,带 --udp 那一条),链接是带单引号的。
// 真机第一次跑就是栽在这里 —— 我的 fixture 用的是裸链接。
//
// **两条都要**:远端同时给了 reality(TCP)与 hysteria2(UDP),漏掉第二条会让
// UDP 退回主传输,白白丢掉那条 QUIC 加速。
func ClientLinksFromInstallOutput(out string) (main, udp string, err error) {
	// **按 `--udp` 认,不按位置认。** 现在打印的是 flag 在前(`--udp 'UDP' 'MAIN'`),以前是
	// 链接在前(`'MAIN' --udp 'UDP'`);按位置取「第一条 = 主链接」在新格式下把两条对调了。
	fields := strings.Fields(out)
	for i := 0; i < len(fields); i++ {
		field := strings.Trim(fields[i], "'\"`")
		if field == "--udp" && i+1 < len(fields) {
			if next := strings.Trim(fields[i+1], "'\"`"); strings.HasPrefix(next, "bx://") {
				udp = next
				i++
			}
			continue
		}
		if strings.HasPrefix(field, "bx://") && main == "" {
			main = field
		}
	}
	if main == "" {
		return "", "", fmt.Errorf("the remote host did not produce a bx:// client link; what it said was:\n%s", strings.TrimSpace(out))
	}
	return main, udp, nil
}

// ShellQuote 把一段文本包成 POSIX shell 的单引号字面量。
//
// **这是一道注入闸门,不是格式化。** 远端命令由 shell 执行,而其中的主机名、
// SNI 等来自命令行。单引号里除了单引号本身之外一切都是字面量,故只需把每个
// 单引号换成 `'\”`(收尾、转义一个、再开头)。
func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// FetchCommand 让远端自己把二进制拉下来,并用**本机验过签的清单里那个
// 校验和**当场核对。
//
// 真机实测(2026-08-14):同一个 27.6MB 资产,VPS 直下 8.36 MB/s,本机经隧道
// 17 KB/s —— 差 490 倍。让远端下是因为它就在目的地那一侧;校验和仍然来自本机,
// 因为供应链的权威必须留在管理员手里(远端自己算自己的哈希毫无意义)。
func FetchCommand(tag string, asset releasemanifest.Asset) string {
	url := ReleaseDownloadBase + "/" + tag + "/" + asset.Name
	upload, _ := UploadPaths()
	tarball := upload + ".tar.gz"
	return strings.Join([]string{
		"set -e",
		"curl -fsSL --retry 2 -o " + ShellQuote(tarball) + " " + ShellQuote(url),
		// 校验和不符就**硬失败并删掉**:留着一个坏文件比没有更危险。
		`printf '%s  %s\n' ` + ShellQuote(asset.SHA256) + " " + ShellQuote(tarball) +
			" | sha256sum -c - || { rm -f " + ShellQuote(tarball) + "; echo 'bx: checksum mismatch' >&2; exit 1; }",
		"tar -xzf " + ShellQuote(tarball) + " -O bx > " + ShellQuote(upload),
		"rm -f " + ShellQuote(tarball),
	}, "\n")
}

// ShouldFallBackToLocalUpload 判断远端那次失败该不该回落到「本机下载 + scp」。
//
// **取不到东西**(没 curl、连不上、超时)→ 换条路是对的。
// **拿到的东西不对**(校验和不符)→ **绝不回落**:换条路再拿一遍只会掩盖问题,
// 而问题可能是有人在中间换了文件。
func ShouldFallBackToLocalUpload(err error) bool {
	if err == nil {
		return false
	}
	return !strings.Contains(strings.ToLower(err.Error()), "checksum mismatch")
}

// FirewallCommand 放行隧道端口。
//
// **ufw 不在或没启用时安静通过** —— 一台没装防火墙的机器不该因此部署失败。
// TCP 与 UDP 都要:reality 走 TCP,hysteria2 走 QUIC/UDP,只开一半会让另一半
// 静默失效(而 bx status 那时仍会显示主隧道健康)。
func FirewallCommand(port int) string {
	p := fmt.Sprintf("%d", port)
	return strings.Join([]string{
		"if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | head -1 | grep -q active; then",
		"  ufw allow " + p + "/tcp >/dev/null || true",
		"  ufw allow " + p + "/udp >/dev/null || true",
		"  echo 'bx: ufw now allows " + p + "/tcp and " + p + "/udp'",
		"fi",
	}, "\n")
}

// NeedsSudo 从远端 `id -u` 的输出判断要不要 sudo。
//
// **问远端,不是让用户记得加 `--sudo`** —— 他多半不知道要加。
// `PermitRootLogin prohibit-password` 是 Debian/Ubuntu 的默认值,只有 sudo
// 用户的人今天会撞到一句没有指引的 `Permission denied`(2026-08-14 真机)。
//
// 解析不出来时报错而不是猜:猜 root 会让每条命令都失败,猜 sudo 会在真 root
// 的机器上多要一次不存在的密码。
func NeedsSudo(idOutput string) (bool, error) {
	uid := strings.TrimSpace(idOutput)
	if uid == "" {
		return false, errors.New("the remote host reported no uid (id -u printed nothing)")
	}
	switch uid {
	case "0":
		return false, nil
	}
	for _, r := range uid {
		if r < '0' || r > '9' {
			return false, fmt.Errorf("the remote id -u did not print something that looks like a uid: %q", uid)
		}
	}
	return true, nil
}

// WrapSudo 把一段(可能多行的)脚本交给远端执行,必要时整段包进 sudo。
//
// **要害在「整段」。** 简单地在前面加 "sudo " 只作用于第一条命令,后面每一条
// 都会以普通用户身份跑 —— 而失败方式极难查:文件下下来了、校验过了,
// 却写不进 /usr/local/bin。
func WrapSudo(script string, sudo bool) string {
	if !sudo {
		return script
	}
	return "sudo sh -c " + ShellQuote(script)
}

// WrapSudoWithPassword 同 WrapSudo,但 sudo 从 stdin 读密码(-S)且不打提示(-p ”)。
// 菜单那条路没有终端,而非 root 登录的 sudo 多半要密码 —— 用户在窗口里填的就是它。
// 同样**整段**包进去,理由同上。
func WrapSudoWithPassword(script string) string {
	return "sudo -S -p '' sh -c " + ShellQuote(script)
}
