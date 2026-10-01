package cli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/sshpass"
	updatepkg "github.com/getbx/bx/internal/update"
	"github.com/urfave/cli/v2"
)

// deployOptions 是一次 `bx server deploy` 的输入。
type deployOptions struct {
	// Host 是 ssh 的目标(`root@1.2.3.4` 或 ssh_config 里的别名)。
	Host     string
	Protocol string
	SNI      string
	Port     int
	// Force 透传给远端的 `bx server install --force`。
	Force bool
	// Name 非空时,装好之后把它加进本机服务器清单(经 Guardian,**不改 current**)。
	Name string
	// SSHOptions 加在每一条 ssh/scp 前面(连接复用、端口、known_hosts 策略)——
	// 见 deploySSHOptions。放在一处,是因为漏掉任何一条调用,那一条就会再问一次密码
	// 或连错端口,而前面每一步都成功。
	SSHOptions []string
	// Password 只在菜单那条路(--password-stdin)上有:非 root 登录时喂给远端的
	// `sudo -S`。给 ssh 本身的那份走 internal/sshpass,不经这里。
	Password string
}

// deployDeps 把所有会碰外界的东西注入进来,好让判定可测。
//
// run 走系统的 `ssh`/`scp`。命令行那条路上密码、密钥、agent 全由 ssh 自己问;菜单那条路
// (--password-stdin,2026-09-30 所有者定的)密码只在内存里,经 internal/sshpass 交给 ssh
// 与远端 `sudo -S`,用完即丢,绝不写盘。
type deployDeps struct {
	run func(name string, args ...string) (string, error)
	// runInput 与 run 相同,只是把 stdin 喂给它(远端 `sudo -S` 要密码时)。nil = 不支持。
	runInput func(stdin, name string, args ...string) (string, error)
	// step 报告进行到哪一步(菜单那条路逐步显示);nil = 不报。
	step func(id string)
	// out 是给人看的逐行说明;nil = 标准输出。菜单那条路把它关掉,只发 JSON 事件。
	out io.Writer
	// hasTTY 决定要不要给 ssh 加 -t(只有 sudo 可能问密码时才需要)。
	hasTTY      bool
	fetchBinary func(arch string) (localPath string, err error)
	// remoteFetch 让远端自己把二进制取到 remoteUploadPaths 的临时路径;runRemote 是
	// 已经带好 sudo / 连接参数的那一个。nil = 不试远端,直接本机下载(测试与降级用)。
	remoteFetch      func(arch string, runRemote func(script string) (string, error)) error
	writeLocalConfig func(link string) error
}

// releaseArchFromUname 把远端 `uname -m` 的输出映射成 release 的架构名。
//
// **认不出来硬失败。** 装错架构的二进制,远端报的是 `exec format error` ——
// 一句与真实原因毫无关系的话,而那时文件已经传上去、服务装了一半。
func releaseArchFromUname(out string) (string, error) {
	switch strings.TrimSpace(out) {
	case "x86_64", "amd64":
		return "amd64", nil
	case "aarch64", "arm64":
		return "arm64", nil
	}
	return "", fmt.Errorf("the remote architecture was not recognized (uname -m says %q); bx only ships linux/amd64 and linux/arm64",
		strings.TrimSpace(out))
}

// remoteUploadPaths 是「先传到哪」和「再移到哪」。
//
// 分两步是因为直接覆盖一个**正在运行**的二进制会得到 `text file busy`,
// 而那时旧服务已经被停掉了。
func remoteUploadPaths() (upload, final string) {
	return "/tmp/bx.deploy", "/usr/local/bin/bx"
}

// remoteInstallCommand 拼远端要执行的安装命令。
//
// **用户给的每一段都要引起来。** 目标主机、SNI 这些来自命令行,而整条命令交给
// 远端 shell 执行 —— 少一对引号就是远程命令注入。
func remoteInstallCommand(opts deployOptions) string {
	_, final := remoteUploadPaths()
	parts := []string{shellSingleQuoted(final), "server", "install"}
	if p := strings.TrimSpace(opts.Protocol); p != "" {
		parts = append(parts, "--protocol", shellSingleQuoted(p))
	}
	if s := strings.TrimSpace(opts.SNI); s != "" {
		parts = append(parts, "--sni", shellSingleQuoted(s))
	}
	if opts.Port > 0 {
		parts = append(parts, "--port", fmt.Sprintf("%d", opts.Port))
	}
	if opts.Force {
		parts = append(parts, "--force")
	}
	return strings.Join(parts, " ")
}

// clientLinksFromInstallOutput 取出主链接与(如果有的)UDP 链接。
//
// **必须剥掉引号**:`bx server install` 打的是一条可直接复制的命令
// (`sudo bx setup 'bx://AAA' --udp 'bx://BBB'`),链接是带单引号的。
// 真机第一次跑就是栽在这里 —— 我的 fixture 用的是裸链接。
//
// **两条都要**:远端同时给了 reality(TCP)与 hysteria2(UDP),漏掉第二条会让
// UDP 退回主传输,白白丢掉那条 QUIC 加速。
func clientLinksFromInstallOutput(out string) (main, udp string, err error) {
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

// runServerDeploy 执行一次部署。
//
// **任何一步失败都不写本机配置** —— 半成功的部署留下一份指向不存在服务器的配置,
// 比彻底失败更难查(用户会以为已经换过去了)。
func runServerDeploy(opts deployOptions, deps deployDeps) error {
	if strings.TrimSpace(opts.Host) == "" {
		return fmt.Errorf("the target host is missing (it looks like root@1.2.3.4)")
	}
	say := deps.out
	if say == nil {
		say = os.Stdout
	}
	step := func(id string) {
		if deps.step != nil {
			deps.step(id)
		}
	}
	sshCall := func(extra ...string) []string {
		return append(append([]string{}, opts.SSHOptions...), extra...)
	}
	step("connect")
	// 一次往返同时问「我是谁」和「什么架构」—— 两个都决定后面怎么做。
	probe, err := deps.run("ssh", sshCall(opts.Host, "id -u; uname -m")...)
	if err != nil {
		return fmt.Errorf("could not connect to %s: %w", opts.Host, err)
	}
	idLine, unameLine, ok := strings.Cut(strings.TrimSpace(probe), "\n")
	if !ok {
		return fmt.Errorf("the remote probe output could not be understood: %q", strings.TrimSpace(probe))
	}
	sudo, err := needsSudo(idLine)
	if err != nil {
		return err
	}
	arch, err := releaseArchFromUname(unameLine)
	if err != nil {
		return err
	}
	// 非 root 且手里有密码(菜单那条路):sudo 从 stdin 读同一个密码,不需要终端。
	sudoWithPassword := sudo && opts.Password != "" && deps.runInput != nil
	if sudo {
		fmt.Fprintln(say, "• The remote login is not root, so the rest runs under sudo")
		if !deps.hasTTY && !sudoWithPassword {
			// 没有终端就问不了密码 —— 与其让它挂住或吐一句无关的错误,
			// 不如提前说清楚。
			fmt.Fprintln(say, "  (there is no terminal here, so sudo will fail if it asks for a password — set up NOPASSWD, or re-run this from a terminal)")
		}
	}
	// runRemote 把「要不要 sudo」「要不要 TTY」收在一处 —— 散在各调用点就会
	// 有某一条忘了包,而那条的失败方式极难查(前面都成功,只有写文件那步失败)。
	runRemote := func(script string) (string, error) {
		if sudoWithPassword {
			args := sshCall(opts.Host, remoteScriptWithPassword(script))
			return deps.runInput(opts.Password+"\n", "ssh", args...)
		}
		args := sshCall(append(sshArgsFor(opts.Host, sudo, deps.hasTTY), remoteScript(script, sudo))...)
		return deps.run("ssh", args...)
	}
	scpUp := func(local, remote string) error {
		_, err := deps.run("scp", sshCall(local, opts.Host+":"+remote)...)
		return err
	}
	step("download")
	upload, final := remoteUploadPaths()
	// **先让远端自己下。** 它就在目的地那一侧:真机实测 8.36 MB/s,
	// 而本机经隧道只有 17 KB/s(差 490 倍)。校验和仍然来自本机验过签的清单。
	if deps.remoteFetch != nil {
		err := deps.remoteFetch(arch, runRemote)
		switch {
		case err == nil:
			fmt.Fprintln(say, "• The remote host fetched the binary itself and it checks out")
		case !shouldFallBackToLocalUpload(err):
			// 校验和不符 —— **绝不回落**。换条路再拿一遍只会掩盖问题。
			return fmt.Errorf("the remote verification failed: %w", err)
		default:
			fmt.Fprintf(say, "• The remote host could not fetch it (%v), so it is downloaded here and uploaded\n", err)
			local, ferr := deps.fetchBinary(arch)
			if ferr != nil {
				return fmt.Errorf("preparing the linux/%s bx binary: %w", arch, ferr)
			}
			if serr := scpUp(local, upload); serr != nil {
				return fmt.Errorf("uploading the binary: %w", serr)
			}
		}
	} else {
		local, ferr := deps.fetchBinary(arch)
		if ferr != nil {
			return fmt.Errorf("preparing the linux/%s bx binary: %w", arch, ferr)
		}
		if serr := scpUp(local, upload); serr != nil {
			return fmt.Errorf("uploading the binary: %w", serr)
		}
	}
	step("install")
	if _, err := runRemote(fmt.Sprintf("chmod +x %s && mv %s %s",
		shellSingleQuoted(upload), shellSingleQuoted(upload), shellSingleQuoted(final))); err != nil {
		return fmt.Errorf("putting the binary in place: %w", err)
	}
	out, err := runRemote(remoteInstallCommand(opts))
	if err != nil {
		return fmt.Errorf("the remote installation failed: %w\n%s", err, strings.TrimSpace(out))
	}
	main, udp, err := clientLinksFromInstallOutput(out)
	if err != nil {
		return err
	}
	// **放行防火墙。** 真机上 Ubuntu 24.04 的 ufw 默认 `deny (incoming)`:
	// 服务装好了、端口 LISTEN 了,而外面进不来。`bx server install` 只打了一句
	// 提示,而一条声称「一条命令装好」的路径把最后一道留给用户去读提示,
	// 等于没装好。UDP 也要开 —— hysteria2 走 QUIC。
	port := opts.Port
	if port <= 0 {
		port = 443
	}
	step("firewall")
	fwOut, err := runRemote(remoteFirewallCommand(port))
	switch {
	case err != nil:
		fmt.Fprintf(say, "⚠ The firewall port %d could not be opened automatically; if it is unreachable from outside, open it by hand: %v\n", port, err)
	case strings.TrimSpace(fwOut) != "":
		// **改了别人的防火墙就要说出来。** 静默修改系统状态,用户既无从复核也
		// 无从撤销 —— 而这条命令的其余每一步都会打一行。
		fmt.Fprintf(say, "• %s\n", strings.TrimSpace(fwOut))
	default:
		fmt.Fprintln(say, "• ufw is not enabled on the remote host, so no firewall was changed (if there is a cloud security group, remember to open that port)")
	}
	// 装完就启动 —— 一条命令该留下一台**在跑**的服务器,而不是一台装好没开的。
	step("start")
	if _, err := runRemote(shellSingleQuoted(final) + " server start"); err != nil {
		fmt.Fprintf(say, "⚠ The remote service did not start by itself; log in and run bx server start once: %v\n", err)
	}
	if udp != "" {
		return deps.writeLocalConfig(main + " --udp " + udp)
	}
	return deps.writeLocalConfig(main)
}

// shellSingleQuoted 把一段文本包成 POSIX shell 的单引号字面量。
//
// **这是一道注入闸门,不是格式化。** 远端命令由 shell 执行,而其中的主机名、
// SNI 等来自命令行。单引号里除了单引号本身之外一切都是字面量,故只需把每个
// 单引号换成 `'\”`(收尾、转义一个、再开头)。
func shellSingleQuoted(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// serverDeployAction 是 `bx server deploy` 的入口。
//
// **它跑在管理员自己的机器上**,而不是服务器上 —— 这是与 `bx server install`
// 的全部区别:后者要求你已经在那台机器上了。
func serverDeployAction(c *cli.Context) error {
	host := strings.TrimSpace(c.Args().First())
	if host == "" {
		return errors.New("usage: bx server deploy <user@host>   (host may also be an alias from your ssh_config)")
	}
	menu := c.Bool("json")
	opts := deployOptions{
		Host:     host,
		Protocol: c.String("protocol"),
		SNI:      c.String("sni"),
		Port:     c.Int("port"),
		Force:    c.Bool("force"),
		Name:     strings.TrimSpace(c.String("name")),
	}
	if c.Bool("password-stdin") {
		// 一行,来自菜单窗口。只在内存里,交给 ssh(sshpass)与远端 sudo -S,进程结束即丢。
		line, err := readPasswordLine(os.Stdin)
		if err != nil {
			return fmt.Errorf("reading the password from stdin: %w", err)
		}
		opts.Password = line
	}
	sshPort := c.Int("ssh-port")

	controlDir, err := os.MkdirTemp("", "bxssh")
	if err != nil {
		return err
	}
	_ = os.Chmod(controlDir, 0o700)
	knownHosts := ""
	if menu {
		if knownHosts, err = deployKnownHostsPath(); err != nil {
			return err
		}
	}
	opts.SSHOptions = deploySSHOptions(sshOptionParams{
		Port: sshPort, ControlDir: controlDir,
		KnownHosts: knownHosts, Password: opts.Password != "", Menu: menu,
	})
	defer func() {
		// 收掉共用的那条连接,再删控制目录 —— 什么都不留下。
		_ = exec.Command("ssh", append(append([]string{}, opts.SSHOptions...), "-O", "exit", host)...).Run()
		os.RemoveAll(controlDir)
	}()
	if c.Bool("forget-host-key") && knownHosts != "" {
		forgetDeployHostKey(knownHosts, host, sshPort)
	}

	runner := &deployRunner{interactive: !menu}
	if opts.Password != "" {
		self, err := os.Executable()
		if err != nil {
			return err
		}
		pass, err := sshpass.Serve(opts.Password, self)
		if err != nil {
			return err
		}
		defer pass.Close()
		runner.env = pass.Env()
	}
	var say io.Writer = os.Stdout
	if menu {
		say = io.Discard
	}
	deps := deployDeps{
		run:      runner.run,
		runInput: runner.runInput,
		hasTTY:   !menu && stdinIsTerminal(),
		remoteFetch: func(arch string, runRemote func(string) (string, error)) error {
			return remoteFetchBinary(arch, runRemote, say)
		},
		fetchBinary: fetchLinuxBinary,
	}
	if menu {
		enc := json.NewEncoder(os.Stdout)
		if err := runDeployForMenu(opts, deps, guardian.NewClient(guardian.SocketPath), func(e deployEvent) { _ = enc.Encode(e) }); err != nil {
			// 那一行 error 事件已经说清楚了;非零退出码给调用方判断用。
			return cli.Exit("", 1)
		}
		return nil
	}
	fmt.Printf("• Target %s\n", host)
	deps.writeLocalConfig = func(link string) error { return applyDeployedLink(link, opts.Name) }
	return runServerDeploy(opts, deps)
}

// readPasswordLine 读一行(去掉行尾),不回显、不记录。
func readPasswordLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if line == "" {
		return "", errors.New("the password is empty")
	}
	return line, nil
}

// runDeployCommand 执行一条 ssh/scp。
//
// **stdin 接到终端**:ssh 可能要问密码或 known_hosts 确认,吞掉 stdin 会让它
// 静默失败,而用户只看到一句「连不上」。
func runDeployCommand(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	return string(out), err
}

// fetchLinuxBinary 准备好远端要用的那个 bx 二进制。
//
// **从本机下载而不是让服务器自己 curl。** 裸 VPS 的连通性是未知数,而本机
// 这一侧的环境是已知可用的(尤其是它多半正跑着 bx)。顺带,下载物在本机
// 校验过 sha256 才上传,供应链只有一跳。
func fetchLinuxBinary(arch string) (string, error) {
	client := &http.Client{Transport: stallSafeTransport()}
	tag, err := latestReleaseTag(client)
	if err != nil {
		return "", fmt.Errorf("looking up the latest version: %w", err)
	}
	// **走签名 manifest,不是裸 sha256。** manifest 由 ed25519 签名,校验不过
	// verifiedReleaseManifest 就直接失败 —— 这是这条路上唯一的供应链依据,
	// 而我们正要把这个文件放进一台机器的 /usr/local/bin。
	manifest, err := verifiedReleaseManifest(client, tag)
	if err != nil {
		return "", fmt.Errorf("verifying the release manifest: %w", err)
	}
	return buildLinuxBinary(manifest, arch, tag, func(url string) ([]byte, error) {
		return downloadBytes(client, url)
	})
}

// buildLinuxBinary 挑产物 → 下载 → **校验** → 解包 → 落到临时文件。
//
// 抽出来是为了让「有没有真的校验」这件事可测:上一版这段长在一个做网络 I/O 的
// 函数里,于是把校验整段删掉**没有任何测试会红**(变异验证当场发现)——
// 而这个仓库全部的事故都在这种够不着的接线上。
func buildLinuxBinary(manifest updatepkg.Manifest, arch, tag string, fetch func(url string) ([]byte, error)) (string, error) {
	asset, err := linuxAssetFor(manifest, arch)
	if err != nil {
		return "", err
	}
	data, err := fetch(repoReleaseDL + "/" + tag + "/" + asset.Name)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", asset.Name, err)
	}
	if err := verifyAssetBytes(data, asset); err != nil {
		return "", err
	}
	binary, err := extractBxFromTarGz(data)
	if err != nil {
		return "", err
	}
	dir, err := os.MkdirTemp("", "bx-deploy")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "bx")
	if err := os.WriteFile(path, binary, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

// linuxAssetFor 从签名清单里挑出这个架构的 linux 产物。
//
// **挑不到就硬失败**:那意味着这一版没有为该架构发布产物,而继续下去只会把一个
// 错架构的文件传上去,远端报一句与真实原因无关的 `exec format error`。
func linuxAssetFor(manifest updatepkg.Manifest, arch string) (updatepkg.Asset, error) {
	want := "linux/" + arch
	for _, asset := range manifest.Assets {
		if strings.EqualFold(asset.Platform, want) {
			return asset, nil
		}
	}
	return updatepkg.Asset{}, fmt.Errorf("this release has no artifact for %s", want)
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// applyDeployedLink 把远端给出的链接写进本机配置。
//
// name 非空时走「加进清单」那条路(经 Guardian,不提权、**不改 current**);
// 为空则维持原状:root 就 `bx setup`,非 root 就打印下一步。
func applyDeployedLink(link, name string) error {
	fmt.Println("• The remote host is ready, and the client link was retrieved")
	main, udp := splitDeployedLink(link)

	if strings.TrimSpace(name) != "" {
		if err := addDeployedServer(name, main, udp); err == nil {
			return nil
		} else {
			// **加不进去不是致命的** —— 机器已经装好了,链接就在眼前。
			// 报清楚原因,再退回原来那条「你自己敲一条」的路。
			fmt.Printf("⚠ It could not be added to the server list automatically: %v\n", err)
		}
	}

	// **不偷偷提权。** 写 /etc/bx 要 root,而这条命令的其余部分不需要 ——
	// 让一条只做 ssh 的命令中途弹密码框是坏意外。
	if os.Geteuid() != 0 {
		fmt.Printf("\nNext (needs root):\n  %s\n  "+elevate.Prefix+"bx up\n", setupCommandLine(main, udp))
		return nil
	}

	// **重新执行我们自己的 `bx setup`,不复制它那条路径。**
	// setup 还要做连通探测、装服务、设自启;把那些抄一遍必然会漂,
	// 而这个仓库全部的事故都在这种「同一件事两个实现」上。
	self, err := os.Executable()
	if err != nil {
		fmt.Printf("\nNext:\n  %s\n  bx up\n", setupCommandLine(main, udp))
		return nil
	}
	// **flag 必须在链接之前。** urfave/cli 遇到第一个位置参数就停止解析 flag,
	// 写成 `setup <链接> --udp <值>` 会让 --udp 被当成位置参数 —— bx 自己的
	// checkSetupArgs 会当场拒绝(2026-08-14 真机撞到)。
	args := []string{"setup"}
	if udp != "" {
		args = append(args, "--udp", udp)
	}
	args = append(args, main)
	fmt.Println("• Writing the local config (bx setup)…")
	cmd := exec.Command(self, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("could not write the local config: %w (you can run %s by hand)", err, setupCommandLine(main, udp))
	}
	fmt.Println("\n✅ Deployed. Next: " + elevate.Prefix + "bx up")
	return nil
}

// addDeployedServer 经 Guardian 把新装好的这台加进清单。
//
// **不改 current。** 刚装好一台不等于要换过去 —— 换出口是有后果的事,必须是
// 用户在清单里显式的一下(见 setup.AddServer)。
//
// 走 Guardian 而不是直接写文件,是因为 /etc/bx/config.yaml 要 root 而 deploy
// 跑在用户身份下;Guardian 那一侧的门是 authorizeOwnerPeer,与菜单换服务器同一道。
func addDeployedServer(name, main, udp string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := guardian.NewClient(guardian.SocketPath).AddServer(ctx, name, main, udp); err != nil {
		return err
	}
	fmt.Printf("✓ Added to the server list as %s (the one you are using did not change)\n", name)
	fmt.Printf("  To switch to it: bx server use %s\n", name)
	return nil
}

// splitDeployedLink 把「主链接 --udp UDP链接」拆回两半。
func splitDeployedLink(combined string) (main, udp string) {
	fields := strings.Fields(combined)
	for i := 0; i < len(fields); i++ {
		switch {
		case fields[i] == "--udp" && i+1 < len(fields):
			udp = fields[i+1]
			i++
		case strings.HasPrefix(fields[i], "bx://") && main == "":
			main = fields[i]
		}
	}
	return main, udp
}

func serverDeployFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "protocol", Value: "reality", Usage: "protocol: reality (default) | hysteria2 | brook"},
		&cli.StringFlag{Name: "sni", Usage: "the real site reality/hysteria2 borrows (default www.cloudflare.com)"},
		&cli.IntFlag{Name: "port", Usage: "listen port (default 443)"},
		&cli.BoolFlag{Name: "force", Usage: "overwrite an existing server config on the remote host"},
		// **给了名字就自动加进清单,但不换过去。** 换出口要用户在清单里显式点一下。
		&cli.StringFlag{Name: "name", Usage: "add it to this machine's server list under this name once installed (your current exit does not change)"},
		&cli.IntFlag{Name: "ssh-port", Usage: "the server's SSH port, if it is not 22"},
		// 菜单窗口用的三个:密码从 stdin 来(不在命令行、不在环境变量里),进度逐行 JSON,
		// 以及用户确认重装过之后忘掉旧指纹。
		&cli.BoolFlag{Name: "password-stdin", Usage: "read the SSH password from the first line of stdin (used by the menu's Set Up a New Server window; never stored)"},
		&cli.BoolFlag{Name: "json", Usage: "print progress as JSON lines (used by the menu)"},
		&cli.BoolFlag{Name: "forget-host-key", Usage: "forget the fingerprint bx recorded for this server (after you reinstalled it)"},
	}
}

// verifyAssetBytes 把下载物与签名清单里的校验和比对。
//
// **这是这条路上唯一的供应链闸门** —— 我们正要把这个文件放进一台机器的
// /usr/local/bin。抽成独立函数是因为它原本长在一个做网络 I/O 的函数里,
// 测试进不去(变异验证时是编译器碰巧拦住的,不是测试)。
//
// 清单里没有校验和时**拒绝**,不放行:一个空的 SHA256 意味着我们对这个文件
// 一无所知,而「不知道」在供应链上等同于「不可信」。
func verifyAssetBytes(data []byte, asset updatepkg.Asset) error {
	want := strings.TrimSpace(asset.SHA256)
	if want == "" {
		return fmt.Errorf("the release manifest has no checksum for %s — refusing to upload a binary that cannot be verified", asset.Name)
	}
	got := hex.EncodeToString(sha256Sum(data))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("the checksum of %s does not match (the manifest says %s, the file is %s) — refusing to upload it", asset.Name, want, got)
	}
	return nil
}

// remoteFetchCommand 让远端自己把二进制拉下来,并用**本机验过签的清单里那个
// 校验和**当场核对。
//
// 真机实测(2026-08-14):同一个 27.6MB 资产,VPS 直下 8.36 MB/s,本机经隧道
// 17 KB/s —— 差 490 倍。让远端下是因为它就在目的地那一侧;校验和仍然来自本机,
// 因为供应链的权威必须留在管理员手里(远端自己算自己的哈希毫无意义)。
func remoteFetchCommand(tag string, asset updatepkg.Asset) string {
	url := repoReleaseDL + "/" + tag + "/" + asset.Name
	upload, _ := remoteUploadPaths()
	tarball := upload + ".tar.gz"
	return strings.Join([]string{
		"set -e",
		"curl -fsSL --retry 2 -o " + shellSingleQuoted(tarball) + " " + shellSingleQuoted(url),
		// 校验和不符就**硬失败并删掉**:留着一个坏文件比没有更危险。
		`printf '%s  %s\n' ` + shellSingleQuoted(asset.SHA256) + " " + shellSingleQuoted(tarball) +
			" | sha256sum -c - || { rm -f " + shellSingleQuoted(tarball) + "; echo 'bx: checksum mismatch' >&2; exit 1; }",
		"tar -xzf " + shellSingleQuoted(tarball) + " -O bx > " + shellSingleQuoted(upload),
		"rm -f " + shellSingleQuoted(tarball),
	}, "\n")
}

// shouldFallBackToLocalUpload 判断远端那次失败该不该回落到「本机下载 + scp」。
//
// **取不到东西**(没 curl、连不上、超时)→ 换条路是对的。
// **拿到的东西不对**(校验和不符)→ **绝不回落**:换条路再拿一遍只会掩盖问题,
// 而问题可能是有人在中间换了文件。
func shouldFallBackToLocalUpload(err error) bool {
	if err == nil {
		return false
	}
	return !strings.Contains(strings.ToLower(err.Error()), "checksum mismatch")
}

// remoteFetchBinary 让远端自己下载并核对二进制。
func remoteFetchBinary(arch string, runRemote func(script string) (string, error), out io.Writer) error {
	client := &http.Client{Transport: stallSafeTransport()}
	tag, err := latestReleaseTag(client)
	if err != nil {
		return fmt.Errorf("looking up the latest version: %w", err)
	}
	manifest, err := verifiedReleaseManifest(client, tag)
	if err != nil {
		return fmt.Errorf("verifying the release manifest: %w", err)
	}
	asset, err := linuxAssetFor(manifest, arch)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "• Letting the remote host fetch %s (%s) itself; the checksum comes from the signed manifest read here\n", asset.Name, tag)
	text, err := runRemote(remoteFetchCommand(tag, asset))
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(text))
	}
	return nil
}

// remoteFirewallCommand 放行隧道端口。
//
// **ufw 不在或没启用时安静通过** —— 一台没装防火墙的机器不该因此部署失败。
// TCP 与 UDP 都要:reality 走 TCP,hysteria2 走 QUIC/UDP,只开一半会让另一半
// 静默失效(而 bx status 那时仍会显示主隧道健康)。
func remoteFirewallCommand(port int) string {
	p := fmt.Sprintf("%d", port)
	return strings.Join([]string{
		"if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | head -1 | grep -q active; then",
		"  ufw allow " + p + "/tcp >/dev/null || true",
		"  ufw allow " + p + "/udp >/dev/null || true",
		"  echo 'bx: ufw now allows " + p + "/tcp and " + p + "/udp'",
		"fi",
	}, "\n")
}

// needsSudo 从远端 `id -u` 的输出判断要不要 sudo。
//
// **问远端,不是让用户记得加 `--sudo`** —— 他多半不知道要加。
// `PermitRootLogin prohibit-password` 是 Debian/Ubuntu 的默认值,只有 sudo
// 用户的人今天会撞到一句没有指引的 `Permission denied`(2026-08-14 真机)。
//
// 解析不出来时报错而不是猜:猜 root 会让每条命令都失败,猜 sudo 会在真 root
// 的机器上多要一次不存在的密码。
func needsSudo(idOutput string) (bool, error) {
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

// remoteScript 把一段(可能多行的)脚本交给远端执行,必要时整段包进 sudo。
//
// **要害在「整段」。** 简单地在前面加 "sudo " 只作用于第一条命令,后面每一条
// 都会以普通用户身份跑 —— 而失败方式极难查:文件下下来了、校验过了,
// 却写不进 /usr/local/bin。
func remoteScript(script string, sudo bool) string {
	if !sudo {
		return script
	}
	return "sudo sh -c " + shellSingleQuoted(script)
}

// remoteScriptWithPassword 同 remoteScript,但 sudo 从 stdin 读密码(-S)且不打提示(-p ”)。
// 菜单那条路没有终端,而非 root 登录的 sudo 多半要密码 —— 用户在窗口里填的就是它。
// 同样**整段**包进去,理由同上。
func remoteScriptWithPassword(script string) string {
	return "sudo -S -p '' sh -c " + shellSingleQuoted(script)
}

// sshArgsFor 是连这台机器要带的 ssh 参数。
//
// **需要 sudo 时要一个 TTY**:sudo 可能要问密码,而没有 TTY 时那个提示出不来,
// 表现是命令莫名其妙地挂住或失败。
func sshArgsFor(host string, sudo, hasTTY bool) []string {
	// **本地没有 TTY 时不许硬要。** `ssh -t` 在没有终端的环境里只会打一句
	// "Pseudo-terminal will not be allocated because stdin is not a terminal"
	// 然后照跑 —— 噪声之外还给人一种「出错了」的错觉(2026-08-14 真机实测)。
	// 真正需要 TTY 的只有「sudo 要问密码」那一种,而那种情形本来就得有终端。
	if sudo && hasTTY {
		return []string{"-t", host}
	}
	return []string{host}
}

// (「有没有终端」用 appinstall.go 里那份现成的 stdinIsTerminal —— 同一个问题
// 不该有第二个实现,那正是这个仓库反复栽的形状。)
