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

	"github.com/getbx/bx/internal/deploy"
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
	// onResult 在写本机配置之前拿到部署结果(菜单要知道「这台本来就装好、钥匙沿用」)。nil = 不要。
	onResult func(deploy.Result)
}

// runServerDeploy 执行一次部署:Mac 这一侧的适配。判断全在 internal/deploy(手机那边经 bxkit
// 用同一份);这里只把系统的 ssh / scp 包成 deploy.Session,把本机下载与上传接成钩子。
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
	sshCall := func(extra ...string) []string {
		return append(append([]string{}, opts.SSHOptions...), extra...)
	}
	session := deploySessionFunc(func(cmd string, stdin *string, tty bool) (string, error) {
		if stdin != nil && deps.runInput != nil {
			return deps.runInput(*stdin, "ssh", sshCall(opts.Host, cmd)...)
		}
		return deps.run("ssh", sshCall(append(sshArgsFor(opts.Host, tty, tty), cmd)...)...)
	})
	hooks := deploy.Hooks{
		Step:         deps.step,
		Say:          say,
		HasTTY:       deps.hasTTY,
		CanFeedStdin: deps.runInput != nil,
		FetchBinary:  deps.remoteFetch,
		LocalFallback: func(arch, upload string) error {
			local, err := deps.fetchBinary(arch)
			if err != nil {
				return fmt.Errorf("preparing the linux/%s bx binary: %w", arch, err)
			}
			if _, err := deps.run("scp", sshCall(local, opts.Host+":"+upload)...); err != nil {
				return fmt.Errorf("uploading the binary: %w", err)
			}
			return nil
		},
	}
	if deps.fetchBinary == nil {
		hooks.LocalFallback = nil
	}
	res, err := deploy.Run(deploy.Options{
		Target: opts.Host, Address: deployAddress(opts.Host), Protocol: opts.Protocol, SNI: opts.SNI,
		Port: opts.Port, Force: opts.Force, Password: opts.Password,
	}, session, hooks)
	if err != nil {
		return err
	}
	if deps.onResult != nil {
		deps.onResult(res)
	}
	if res.UDP != "" {
		return deps.writeLocalConfig(res.Main + " --udp " + res.UDP)
	}
	return deps.writeLocalConfig(res.Main)
}

// deploySessionFunc adapts a function to deploy.Session.
type deploySessionFunc func(cmd string, stdin *string, tty bool) (string, error)

func (f deploySessionFunc) Run(cmd string, stdin *string, tty bool) (string, error) {
	return f(cmd, stdin, tty)
}

// deployAddress is the host part of user@host (an ssh_config alias stays as it is).
func deployAddress(target string) string {
	if i := strings.LastIndex(target, "@"); i >= 0 {
		return target[i+1:]
	}
	return target
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
	text, err := runRemote(deploy.FetchCommand(tag, asset))
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(text))
	}
	return nil
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
