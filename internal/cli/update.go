package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/getbx/bx/internal/install"
	updatepkg "github.com/getbx/bx/internal/update"
	"github.com/getbx/bx/internal/version"
	"github.com/urfave/cli/v2"
)

const (
	repoReleasesLatest  = "https://github.com/getbx/bx/releases/latest"
	repoReleaseDL       = "https://github.com/getbx/bx/releases/download" // /<tag>/<asset>
	updateManifestName  = "bx-update.json"
	updateSignatureName = "bx-update.json.sig"
)

type updateCheckReport struct {
	Current   string `json:"current"`
	Latest    string `json:"latest"`
	Available bool   `json:"available"`
	Verified  bool   `json:"verified"`
}

// assetName 是某平台的 release 资产名,与 release.yml 的命名一致。
func assetName(goos, goarch string) string {
	return fmt.Sprintf("bx_%s_%s.tar.gz", goos, goarch)
}

// parseReleaseTag 从 /releases/tag/<tag> 形态的 URL 提取 tag;非该形态(如无 release 时停在
// /releases)返回空。容忍尾斜杠与 query。
func parseReleaseTag(u string) string {
	i := strings.Index(u, "/releases/tag/")
	if i < 0 {
		return ""
	}
	tag := u[i+len("/releases/tag/"):]
	if j := strings.IndexAny(tag, "/?#"); j >= 0 {
		tag = tag[:j]
	}
	return tag
}

// newerAvailable 判断是否有可更新版本:拿不到 latest(空)保守判否;dev 构建总视为可更新;
// 否则字符串不等即认为有新版(GitHub latest 即权威最新,无需自己做 semver 排序)。
func newerAvailable(current, latest string) bool {
	if latest == "" {
		return false
	}
	if current == "dev" || current == "" || current == "unknown" {
		return true
	}
	return current != latest
}

// expectedSum 从 SHA256SUMS 内容里取某资产的十六进制校验和(缺失返回空)。
// 行格式:"<hex>  <filename>"(两空格,coreutils 风格)。
func expectedSum(sums, asset string) string {
	for _, line := range strings.Split(sums, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[len(f)-1] == asset {
			return f[0]
		}
	}
	return ""
}

// verifyChecksum 校验 data 的 sha256 是否等于 wantHex(空 wantHex 视为失败,拒绝未校验的下载)。
func verifyChecksum(data []byte, wantHex string) error {
	if strings.TrimSpace(wantHex) == "" {
		return fmt.Errorf("缺校验和,拒绝安装未经校验的下载")
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, strings.TrimSpace(wantHex)) {
		return fmt.Errorf("校验和不符:期望 %s,实得 %s", wantHex, got)
	}
	return nil
}

// extractBxFromTarGz 从 tar.gz 里取出名为 bx 的文件字节。
func extractBxFromTarGz(gzData []byte) ([]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(gzData))
	if err != nil {
		return nil, fmt.Errorf("解压 gzip: %w", err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读 tar: %w", err)
		}
		// 取 basename 为 bx 的常规文件
		name := hdr.Name
		if i := strings.LastIndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		}
		if name == "bx" && hdr.Typeflag == tar.TypeReg {
			return io.ReadAll(tr)
		}
	}
	return nil, fmt.Errorf("包内未找到 bx 二进制")
}

func updateFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "check", Usage: "只检查有无新版,不下载安装"},
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读更新状态"},
		&cli.BoolFlag{Name: "package", Usage: "macOS:更新 CLI 和菜单栏 App"},
		&cli.StringFlag{Name: "app-path", Hidden: true},
		&cli.StringFlag{Name: "app-owner", Hidden: true},
		&cli.BoolFlag{Name: "force", Usage: "即便已是最新(或 dev 构建)也强制下载安装最新版"},
		&cli.BoolFlag{Name: "no-restart", Usage: "已废弃:更新始终保留当前保护会话", Hidden: true},
	}
}

func httpGet(client *http.Client, url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "bx-update/"+version.Version)
	return client.Do(req)
}

// latestReleaseTag 跟随 /releases/latest 跳转,从落地 URL 解析出最新 tag。
func latestReleaseTag(client *http.Client) (string, error) {
	resp, err := httpGet(client, repoReleasesLatest)
	if err != nil {
		return "", fmt.Errorf("查询最新版本: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询最新版本返回 %d(仓库尚无 release?)", resp.StatusCode)
	}
	return parseReleaseTag(resp.Request.URL.String()), nil
}

func downloadBytes(client *http.Client, url string) ([]byte, error) {
	resp, err := httpGet(client, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("下载 %s 返回 %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func verifiedReleaseManifest(client *http.Client, tag string) (updatepkg.Manifest, error) {
	base := fmt.Sprintf("%s/%s", repoReleaseDL, tag)
	data, err := downloadBytes(client, base+"/"+updateManifestName)
	if err != nil {
		return updatepkg.Manifest{}, fmt.Errorf("download update manifest: %w", err)
	}
	signature, err := downloadBytes(client, base+"/"+updateSignatureName)
	if err != nil {
		return updatepkg.Manifest{}, fmt.Errorf("download update manifest signature: %w", err)
	}
	manifest, err := updatepkg.ParseAndVerify(data, signature, version.UpdatePublicKey)
	if err != nil {
		return updatepkg.Manifest{}, fmt.Errorf("verify update manifest: %w", err)
	}
	return manifest, nil
}

func updateAction(c *cli.Context) error {
	client := &http.Client{Timeout: 90 * time.Second}
	cur := version.Version

	if !c.Bool("json") {
		fmt.Printf("当前版本:%s\n⏳ 查询最新 release…\n", version.String())
	}
	latest, err := latestReleaseTag(client)
	if err != nil {
		return err
	}
	if latest == "" {
		return fmt.Errorf("解析最新版本失败(仓库可能尚无 release)")
	}
	releaseTag := latest
	manifest, err := verifiedReleaseManifest(client, releaseTag)
	if err != nil {
		return err
	}
	latest = manifest.Version
	available := newerAvailable(cur, latest)
	if c.Bool("json") {
		return json.NewEncoder(os.Stdout).Encode(updateCheckReport{Current: cur, Latest: latest, Available: available, Verified: true})
	}
	fmt.Printf("最新版本:%s (已验证)\n", latest)

	if !c.Bool("force") && !available {
		fmt.Println("✅ 已是最新,无需更新。")
		return nil
	}
	if c.Bool("check") {
		fmt.Printf("🆕 有新版可用:%s → 运行 sudo bx update 安装。\n", latest)
		return nil
	}
	if c.Bool("package") {
		return updateMacOSPackage(c, client, releaseTag, manifest, latest)
	}

	asset, err := updatepkg.FindAsset(manifest, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		return err
	}
	fmt.Printf("⏳ 下载 %s…\n", asset.Name)
	tgz, err := downloadBytes(client, fmt.Sprintf("%s/%s/%s", repoReleaseDL, releaseTag, asset.Name))
	if err != nil {
		return err
	}
	if int64(len(tgz)) != asset.Size {
		return fmt.Errorf("下载大小不符:期望 %d,实得 %d", asset.Size, len(tgz))
	}
	if err := verifyChecksum(tgz, asset.SHA256); err != nil {
		return fmt.Errorf("校验失败(已中止,未替换): %w", err)
	}
	fmt.Println("✅ SHA256 校验通过")

	bin, err := extractBxFromTarGz(tgz)
	if err != nil {
		return err
	}

	// 替换目标:优先当前运行路径(bx 装哪就更哪),取不到回退规范 BinPath。
	dst := install.BinPath
	if self, err := os.Executable(); err == nil && self != "" {
		dst = self
	}
	if err := install.ReplaceBinary(dst, bin); err != nil {
		return fmt.Errorf("替换二进制 %s: %w", dst, err)
	}
	fmt.Printf("✅ 已更新到 %s(%s)\n", latest, dst)

	// 绝不为了加载新二进制而重启服务。守护进程退出会撤销路由/TUN,在真正
	// 的进程交接实现前,保留当前受保护会话比"立刻生效"更重要。
	if install.UnitInstalled() && serviceState("is-active", install.ServiceName) == "active" {
		fmt.Println("  当前保护会话保持运行;新版会在下次启动保护时生效。")
		fmt.Println("  Reconnect 只安全更换传输,不会为了加载二进制而结束保护。")
	} else {
		fmt.Println("  (bx 未在运行,下次 sudo bx up 用新版)")
	}
	return nil
}

func updateMacOSPackage(c *cli.Context, client *http.Client, releaseTag string, manifest updatepkg.Manifest, latest string) error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("完整 App 更新仅支持 macOS")
	}
	if os.Geteuid() != 0 {
		return fmt.Errorf("完整 App 更新需要管理员权限;从菜单栏选择 Update bx 即可")
	}
	appPath := c.String("app-path")
	if !filepath.IsAbs(appPath) || filepath.Base(appPath) != "Bx.app" {
		return fmt.Errorf("完整 App 更新需要有效的菜单栏 App 路径")
	}
	owner, err := parseMacOSAppOwner(c.String("app-owner"))
	if err != nil {
		return fmt.Errorf("完整 App 更新需要菜单栏用户信息: %w", err)
	}
	asset, err := updatepkg.FindPackage(manifest, runtime.GOOS+"/"+runtime.GOARCH)
	if err != nil {
		return err
	}
	fmt.Printf("⏳ 下载完整 macOS 包 %s…\n", asset.Name)
	packageData, err := downloadBytes(client, fmt.Sprintf("%s/%s/%s", repoReleaseDL, releaseTag, asset.Name))
	if err != nil {
		return err
	}
	if int64(len(packageData)) != asset.Size {
		return fmt.Errorf("下载大小不符:期望 %d,实得 %d", asset.Size, len(packageData))
	}
	if err := verifyChecksum(packageData, asset.SHA256); err != nil {
		return fmt.Errorf("完整 macOS 包校验失败(已中止,未替换): %w", err)
	}
	payload, err := extractMacOSPackage(packageData, runtime.GOARCH)
	if err != nil {
		return err
	}
	destination := install.BinPath
	if self, err := os.Executable(); err == nil && self != "" {
		destination = self
	}
	if err := applyMacOSPackage(destination, appPath, payload, &owner); err != nil {
		return err
	}
	fmt.Printf("✅ 已更新 CLI 与菜单栏 App 到 %s。保护会话保持运行。\n", latest)
	if err := restartMacOSMenu(owner); err != nil {
		fmt.Printf("  菜单栏会在下次登录时加载新版(立即重启失败:%v)。\n", err)
	}
	return nil
}
