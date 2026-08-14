package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
}

// deployDeps 把所有会碰外界的东西注入进来,好让判定可测。
//
// **bx 不碰任何凭据**:run 走系统的 `ssh`/`scp`,密码、密钥、agent、known_hosts
// 全由用户自己的 ssh 客户端处理。上一轮关于「留不留凭据」的争论,在这一层
// 根本不存在 —— 我们从来没有拿到过它。
type deployDeps struct {
	run func(name string, args ...string) (string, error)
	// hasTTY 决定要不要给 ssh 加 -t(只有 sudo 可能问密码时才需要)。
	hasTTY      bool
	fetchBinary func(arch string) (localPath string, err error)
	// remoteFetch 让远端自己把二进制取到 remoteUploadPaths 的临时路径。
	// nil = 不试远端,直接本机下载(测试与降级用)。
	remoteFetch      func(host, arch string, sudo, hasTTY bool) error
	writeLocalConfig func(link string) error
}

// deployPlanSteps 是这条流程的固定顺序。
//
// **先探架构**决定了传哪个二进制,放到传输之后就没有意义了;**装完才取链接**,
// 而链接取不到就绝不往下走。
func deployPlanSteps() []string {
	return []string{"detect-arch", "upload", "install", "read-link"}
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
	return "", fmt.Errorf("远端架构无法识别(uname -m 说的是 %q);bx 只提供 linux/amd64 与 linux/arm64",
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

// clientLinkFromInstallOutput 从远端安装输出里取出客户端链接。
//
// **取不到就硬失败,并把远端的原话原样交出去。** 在没拿到链接的情况下继续,
// 会写出一份没有服务器的本机配置,而用户以为部署成功了。
func clientLinkFromInstallOutput(out string) (string, error) {
	main, _, err := clientLinksFromInstallOutput(out)
	return main, err
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
	var links []string
	for _, field := range strings.Fields(out) {
		field = strings.Trim(field, "'\"`")
		if strings.HasPrefix(field, "bx://") {
			links = append(links, field)
		}
	}
	if len(links) == 0 {
		return "", "", fmt.Errorf("远端没有给出 bx:// 客户端链接;它说的是:\n%s", strings.TrimSpace(out))
	}
	if len(links) > 1 {
		return links[0], links[1], nil
	}
	return links[0], "", nil
}

// runServerDeploy 执行一次部署。
//
// **任何一步失败都不写本机配置** —— 半成功的部署留下一份指向不存在服务器的配置,
// 比彻底失败更难查(用户会以为已经换过去了)。
func runServerDeploy(opts deployOptions, deps deployDeps) error {
	if strings.TrimSpace(opts.Host) == "" {
		return fmt.Errorf("缺目标主机(形如 root@1.2.3.4)")
	}
	// 一次往返同时问「我是谁」和「什么架构」—— 两个都决定后面怎么做。
	probe, err := deps.run("ssh", opts.Host, "id -u; uname -m")
	if err != nil {
		return fmt.Errorf("连不上 %s:%w", opts.Host, err)
	}
	idLine, unameLine, ok := strings.Cut(strings.TrimSpace(probe), "\n")
	if !ok {
		return fmt.Errorf("远端的探测输出读不懂:%q", strings.TrimSpace(probe))
	}
	sudo, err := needsSudo(idLine)
	if err != nil {
		return err
	}
	arch, err := releaseArchFromUname(unameLine)
	if err != nil {
		return err
	}
	if sudo {
		fmt.Println("• 远端不是 root,后续命令走 sudo")
		if !deps.hasTTY {
			// 没有终端就问不了密码 —— 与其让它挂住或吐一句无关的错误,
			// 不如提前说清楚。
			fmt.Println("  (当前没有终端,sudo 若需要密码会失败;那时请配 NOPASSWD 或在终端里重跑)")
		}
	}
	// runRemote 把「要不要 sudo」「要不要 TTY」收在一处 —— 散在各调用点就会
	// 有某一条忘了包,而那条的失败方式极难查(前面都成功,只有写文件那步失败)。
	runRemote := func(script string) (string, error) {
		args := append(sshArgsFor(opts.Host, sudo, deps.hasTTY), remoteScript(script, sudo))
		return deps.run("ssh", args...)
	}
	upload, final := remoteUploadPaths()
	// **先让远端自己下。** 它就在目的地那一侧:真机实测 8.36 MB/s,
	// 而本机经隧道只有 17 KB/s(差 490 倍)。校验和仍然来自本机验过签的清单。
	if deps.remoteFetch != nil {
		err := deps.remoteFetch(opts.Host, arch, sudo, deps.hasTTY)
		switch {
		case err == nil:
			fmt.Println("• 远端已自行取到二进制并核对通过")
		case !shouldFallBackToLocalUpload(err):
			// 校验和不符 —— **绝不回落**。换条路再拿一遍只会掩盖问题。
			return fmt.Errorf("远端校验失败:%w", err)
		default:
			fmt.Printf("• 远端取不到(%v),改由本机下载后上传\n", err)
			local, ferr := deps.fetchBinary(arch)
			if ferr != nil {
				return fmt.Errorf("准备 linux/%s 的 bx 二进制:%w", arch, ferr)
			}
			if _, serr := deps.run("scp", local, opts.Host+":"+upload); serr != nil {
				return fmt.Errorf("上传二进制:%w", serr)
			}
		}
	} else {
		local, ferr := deps.fetchBinary(arch)
		if ferr != nil {
			return fmt.Errorf("准备 linux/%s 的 bx 二进制:%w", arch, ferr)
		}
		if _, serr := deps.run("scp", local, opts.Host+":"+upload); serr != nil {
			return fmt.Errorf("上传二进制:%w", serr)
		}
	}
	if _, err := runRemote(fmt.Sprintf("chmod +x %s && mv %s %s",
		shellSingleQuoted(upload), shellSingleQuoted(upload), shellSingleQuoted(final))); err != nil {
		return fmt.Errorf("就位二进制:%w", err)
	}
	out, err := runRemote(remoteInstallCommand(opts))
	if err != nil {
		return fmt.Errorf("远端安装失败:%w\n%s", err, strings.TrimSpace(out))
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
	fwOut, err := runRemote(remoteFirewallCommand(port))
	switch {
	case err != nil:
		fmt.Printf("⚠ 未能自动放行防火墙端口 %d,若外部连不上请手动开:%v\n", port, err)
	case strings.TrimSpace(fwOut) != "":
		// **改了别人的防火墙就要说出来。** 静默修改系统状态,用户既无从复核也
		// 无从撤销 —— 而这条命令的其余每一步都会打一行。
		fmt.Printf("• %s\n", strings.TrimSpace(fwOut))
	default:
		fmt.Println("• 远端没有启用 ufw,未改动防火墙(若有云安全组,记得放行该端口)")
	}
	// 装完就启动 —— 一条命令该留下一台**在跑**的服务器,而不是一台装好没开的。
	if _, err := runRemote(shellSingleQuoted(final) + " server start"); err != nil {
		fmt.Printf("⚠ 远端服务未能自动启动,登上去跑一次 `bx server start` 即可:%v\n", err)
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
		return errors.New("用法:bx server deploy <user@host>(host 也可以是 ssh_config 里的别名)")
	}
	opts := deployOptions{
		Host:     host,
		Protocol: c.String("protocol"),
		SNI:      c.String("sni"),
		Port:     c.Int("port"),
		Force:    c.Bool("force"),
	}
	fmt.Printf("• 目标 %s(bx 不经手你的 SSH 凭据 —— 密码/密钥/agent 全由系统 ssh 处理)\n", host)
	return runServerDeploy(opts, deployDeps{
		run:              runDeployCommand,
		hasTTY:           stdinIsTerminal(),
		remoteFetch:      remoteFetchBinary,
		fetchBinary:      fetchLinuxBinary,
		writeLocalConfig: applyDeployedLink,
	})
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
		return "", fmt.Errorf("查最新版本:%w", err)
	}
	// **走签名 manifest,不是裸 sha256。** manifest 由 ed25519 签名,校验不过
	// verifiedReleaseManifest 就直接失败 —— 这是这条路上唯一的供应链依据,
	// 而我们正要把这个文件放进一台机器的 /usr/local/bin。
	manifest, err := verifiedReleaseManifest(client, tag)
	if err != nil {
		return "", fmt.Errorf("校验发布清单:%w", err)
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
		return "", fmt.Errorf("下载 %s:%w", asset.Name, err)
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
	return updatepkg.Asset{}, fmt.Errorf("这一版没有 %s 的发布产物", want)
}

func sha256Sum(data []byte) []byte {
	sum := sha256.Sum256(data)
	return sum[:]
}

// applyDeployedLink 把远端给出的链接写进本机配置。
func applyDeployedLink(link string) error {
	fmt.Println("• 远端已就绪,客户端链接已取到")
	main, udp := splitDeployedLink(link)

	// **不偷偷提权。** 写 /etc/bx 要 root,而这条命令的其余部分不需要 ——
	// 让一条只做 ssh 的命令中途弹密码框是坏意外。
	if os.Geteuid() != 0 {
		fmt.Printf("\n下一步(需要 root):\n  sudo bx setup %s\n  sudo bx up\n", quoteSetupArgs(link))
		return nil
	}

	// **重新执行我们自己的 `bx setup`,不复制它那条路径。**
	// setup 还要做连通探测、装服务、设自启;把那些抄一遍必然会漂,
	// 而这个仓库全部的事故都在这种「同一件事两个实现」上。
	self, err := os.Executable()
	if err != nil {
		fmt.Printf("\n下一步:\n  bx setup %s\n  bx up\n", quoteSetupArgs(link))
		return nil
	}
	args := []string{"setup", main}
	if udp != "" {
		args = append(args, "--udp", udp)
	}
	fmt.Println("• 写本机配置(bx setup)…")
	cmd := exec.Command(self, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("写本机配置失败:%w(可手动跑 bx setup %s)", err, quoteSetupArgs(link))
	}
	fmt.Println("\n✅ 部署完成。下一步:sudo bx up")
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
		&cli.StringFlag{Name: "protocol", Value: "reality", Usage: "协议:reality(默认)| hysteria2 | brook"},
		&cli.StringFlag{Name: "sni", Usage: "reality/hysteria2 借用的真站(默认 www.cloudflare.com)"},
		&cli.IntFlag{Name: "port", Usage: "监听端口(默认 443)"},
		&cli.BoolFlag{Name: "force", Usage: "覆盖远端已存在的 server 配置"},
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
		return fmt.Errorf("发布清单里没有 %s 的校验和 —— 拒绝上传一个无法核对的二进制", asset.Name)
	}
	got := hex.EncodeToString(sha256Sum(data))
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("%s 校验和不符(清单 %s,实际 %s)—— 拒绝上传", asset.Name, want, got)
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
func remoteFetchBinary(host, arch string, sudo, hasTTY bool) error {
	client := &http.Client{Transport: stallSafeTransport()}
	tag, err := latestReleaseTag(client)
	if err != nil {
		return fmt.Errorf("查最新版本:%w", err)
	}
	manifest, err := verifiedReleaseManifest(client, tag)
	if err != nil {
		return fmt.Errorf("校验发布清单:%w", err)
	}
	asset, err := linuxAssetFor(manifest, arch)
	if err != nil {
		return err
	}
	fmt.Printf("• 让远端自行取 %s(%s),校验和由本机的签名清单提供\n", asset.Name, tag)
	args := append(sshArgsFor(host, sudo, hasTTY), remoteScript(remoteFetchCommand(tag, asset), sudo))
	out, err := runDeployCommand("ssh", args...)
	if err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// quoteSetupArgs 把「主链接 --udp UDP链接」渲染成可直接粘贴的一行。
func quoteSetupArgs(combined string) string {
	parts := strings.Fields(combined)
	for i, p := range parts {
		if strings.HasPrefix(p, "bx://") {
			parts[i] = shellSingleQuoted(p)
		}
	}
	return strings.Join(parts, " ")
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
		"  echo 'bx: ufw 已放行 " + p + "/tcp 与 " + p + "/udp'",
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
		return false, errors.New("远端没有回报 uid(`id -u` 没有输出)")
	}
	switch uid {
	case "0":
		return false, nil
	}
	for _, r := range uid {
		if r < '0' || r > '9' {
			return false, fmt.Errorf("远端 `id -u` 的输出不像一个 uid:%q", uid)
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
