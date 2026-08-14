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
	run              func(name string, args ...string) (string, error)
	fetchBinary      func(arch string) (localPath string, err error)
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
	for _, field := range strings.Fields(out) {
		if strings.HasPrefix(field, "bx://") {
			return field, nil
		}
	}
	return "", fmt.Errorf("远端没有给出 bx:// 客户端链接;它说的是:\n%s", strings.TrimSpace(out))
}

// runServerDeploy 执行一次部署。
//
// **任何一步失败都不写本机配置** —— 半成功的部署留下一份指向不存在服务器的配置,
// 比彻底失败更难查(用户会以为已经换过去了)。
func runServerDeploy(opts deployOptions, deps deployDeps) error {
	if strings.TrimSpace(opts.Host) == "" {
		return fmt.Errorf("缺目标主机(形如 root@1.2.3.4)")
	}
	unameOut, err := deps.run("ssh", opts.Host, "uname -m")
	if err != nil {
		return fmt.Errorf("连不上 %s:%w", opts.Host, err)
	}
	arch, err := releaseArchFromUname(unameOut)
	if err != nil {
		return err
	}
	local, err := deps.fetchBinary(arch)
	if err != nil {
		return fmt.Errorf("准备 linux/%s 的 bx 二进制:%w", arch, err)
	}
	upload, final := remoteUploadPaths()
	if _, err := deps.run("scp", local, opts.Host+":"+upload); err != nil {
		return fmt.Errorf("上传二进制:%w", err)
	}
	if _, err := deps.run("ssh", opts.Host,
		fmt.Sprintf("chmod +x %s && mv %s %s", shellSingleQuoted(upload), shellSingleQuoted(upload), shellSingleQuoted(final))); err != nil {
		return fmt.Errorf("就位二进制:%w", err)
	}
	out, err := deps.run("ssh", opts.Host, remoteInstallCommand(opts))
	if err != nil {
		return fmt.Errorf("远端安装失败:%w\n%s", err, strings.TrimSpace(out))
	}
	link, err := clientLinkFromInstallOutput(out)
	if err != nil {
		return err
	}
	return deps.writeLocalConfig(link)
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
	if os.Geteuid() != 0 {
		// **不偷偷提权。** 写 /etc/bx 要 root,而这条命令的其余部分不需要 ——
		// 让一条只做 ssh 的命令中途弹密码框是坏意外。把下一步原样给出来。
		fmt.Printf("\n下一步(需要 root):\n  sudo bx setup %s\n  sudo bx up\n", link)
		return nil
	}
	fmt.Printf("\n下一步:\n  bx setup %s\n  bx up\n", link)
	return nil
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
