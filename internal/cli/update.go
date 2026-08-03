package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
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

	"github.com/getbx/bx/internal/guardian"
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

// shouldBypassManifest 是纯函数:判断 bx update 能否完全跳过网络(latest tag 查询 +
// manifest 下载),直接用本地包文件走统一布局安装。三个条件缺一不可——非统一布局没有
// "本地完整包"这条路(legacy 单文件更新必须靠 manifest 里的 asset 校验和);--check 是
// "只看有没有新版",即便给了 --package-file 也该照常问网络报告可用版本、不能被当作安装
// 触发器。
func shouldBypassManifest(unifiedLayout bool, packageFile string, checkOnly bool) bool {
	return unifiedLayout && packageFile != "" && !checkOnly
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
		&cli.BoolFlag{Name: "package", Hidden: true, Usage: "已废弃(legacy 布局报错;统一布局忽略,行为等同默认路径)"},
		&cli.StringFlag{Name: "package-file", Hidden: true, Usage: "统一布局:本地已下载的包文件路径,跳过下载与 manifest 查询(真机演练用)"},
		&cli.StringFlag{Name: "app-path", Hidden: true, Usage: "已废弃,忽略"},
		&cli.StringFlag{Name: "app-owner", Hidden: true, Usage: "已废弃,忽略"},
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

	// --package-file 在统一布局下应完全离线:本地包已是完整的、待校验的 macOS 包,
	// 无需(也不该)先打个查最新 tag + 下 manifest 的网络往返去凑一个用不上的
	// manifest/latest。updateUnifiedMacOS 在 localPackage != "" 时本就不碰这两个
	// 参数(FindPackage/版本比对都在 else 分支),故空值安全喂给它。
	if shouldBypassManifest(unifiedLayoutActive(), c.String("package-file"), c.Bool("check")) {
		return updateUnifiedMacOS(c, client, updatepkg.Manifest{}, "")
	}

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

	if unifiedLayoutActive() {
		return updateUnifiedMacOS(c, client, manifest, latest)
	}
	if c.Bool("package") {
		return fmt.Errorf("--package 已废弃:legacy 布局请用完整包重装(install.sh)")
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

// updateUnifiedMacOS 是统一布局(Bx.app)下 bx update 的主路径:先取到并校验一份完整
// macOS 包(pkg.CLI/Bridge/Release/App),再按 Guardian 当前保护状态选路——
// Protected 走 Guardian /v1/update 事务(barrier 切换+健康检查+失败自动回滚),
// Off 直接就地统一安装(反正无网络保护要保,没有回滚必要)。
func updateUnifiedMacOS(c *cli.Context, client *http.Client, manifest updatepkg.Manifest, latest string) error {
	if os.Geteuid() != 0 {
		return fmt.Errorf("统一布局更新需要管理员权限:请用 sudo bx update")
	}

	localPackage := c.String("package-file")
	var data []byte
	if localPackage != "" {
		var err error
		data, err = os.ReadFile(localPackage)
		if err != nil {
			return fmt.Errorf("读取本地包文件 %s: %w", localPackage, err)
		}
	} else {
		asset, err := updatepkg.FindPackage(manifest, runtime.GOOS+"/"+runtime.GOARCH)
		if err != nil {
			return err
		}
		fmt.Printf("⏳ 下载完整 macOS 包 %s…\n", asset.Name)
		data, err = downloadBytes(client, fmt.Sprintf("%s/%s/%s", repoReleaseDL, latest, asset.Name))
		if err != nil {
			return err
		}
		if int64(len(data)) != asset.Size {
			return fmt.Errorf("下载大小不符:期望 %d,实得 %d", asset.Size, len(data))
		}
		if err := verifyChecksum(data, asset.SHA256); err != nil {
			return fmt.Errorf("完整 macOS 包校验失败(已中止,未替换): %w", err)
		}
	}

	pkg, err := updatepkg.ExtractMacOSPackage(data, runtime.GOARCH)
	if err != nil {
		return err
	}
	if err := pkg.VerifyAssets(runtime.GOOS, runtime.GOARCH); err != nil {
		return err
	}
	target := pkg.Release.Version
	if localPackage == "" && target != latest {
		return fmt.Errorf("下载的包版本 %s 与最新版本 %s 不符,已中止", target, latest)
	}

	statusCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	status, statusErr := guardian.NewClient(guardian.SocketPath).Status(statusCtx)
	cancel()
	route, err := decideUnifiedUpdateRoute(status, statusErr, install.GuardianActive())
	if err != nil {
		return err
	}

	if route == "direct" {
		return updateUnifiedMacOSDirect(c, pkg, target)
	}
	return updateUnifiedMacOSGuarded(c, data, pkg, status, target)
}

// updateUnifiedMacOSDirect 处理 route=="direct"(Guardian 未开保护,或状态不明但也
// 没装 Guardian):没有在跑的受保护会话要保,不必走事务/回滚,直接把新版 Bx.app
// 就地统一安装即可。
func updateUnifiedMacOSDirect(c *cli.Context, pkg updatepkg.MacOSPackage, target string) error {
	asJSON := c.Bool("json")
	if !asJSON {
		fmt.Println("1/2 校验并安装新版本…")
	}

	tmpRoot, err := os.MkdirTemp("/var/lib/bx", ".bx-update-app-")
	if err != nil {
		return fmt.Errorf("创建临时目录: %w", err)
	}
	defer os.RemoveAll(tmpRoot)
	bundlePath := filepath.Join(tmpRoot, "Bx.app")
	if err := writeMacOSAppTree(bundlePath, pkg.App); err != nil {
		return fmt.Errorf("展开新版 Bx.app: %w", err)
	}

	if err := directInstallUnifiedUpdate(bundlePath, defaultConfigPath); err != nil {
		return fmt.Errorf("安装新版本失败: %w", err)
	}

	if !asJSON {
		fmt.Printf("2/2 完成 ✅ bx 已更新到 %s(保护未开启,无网络影响)\n", target)
		return nil
	}
	result := guardian.UpdateResult{
		FromVersion:     version.Version,
		ToVersion:       target,
		Phase:           guardian.PhaseCommitted,
		CoreActivated:   false,
		RolledBack:      false,
		ProtectionState: guardian.ProtectionOff,
	}
	return json.NewEncoder(os.Stdout).Encode(result)
}

// writeMacOSAppTree 把 pkg.App(Bx.app 内相对路径 → 内容)展开到 bundlePath 下,
// 供直接安装路径喂给 install.UnifiedInstall。目录 0755;Contents/MacOS/ 下的文件与
// Contents/Resources/bx-cli、Contents/Resources/bx-bridge 保留可执行位(0755),
// 其余(Info.plist、release.json 等)0644。
func writeMacOSAppTree(bundlePath string, app map[string][]byte) error {
	for name, content := range app {
		target := filepath.Join(bundlePath, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("建目录(为 %q): %w", name, err)
		}
		mode := os.FileMode(0o644)
		if strings.HasPrefix(name, "Contents/MacOS/") || name == "Contents/Resources/bx-cli" || name == "Contents/Resources/bx-bridge" {
			mode = 0o755
		}
		if err := os.WriteFile(target, content, mode); err != nil {
			return fmt.Errorf("写 %q: %w", name, err)
		}
	}
	return nil
}

// updateUnifiedMacOSGuarded 处理 route=="guardian"(Protected):不能说停就停——把
// 包暂存好、走 Guardian 的 /v1/update 事务(它自己管 barrier 切换、健康检查、失败
// 自动回滚),bx update 只管把包交过去、报告最终结果。
func updateUnifiedMacOSGuarded(c *cli.Context, data []byte, pkg updatepkg.MacOSPackage, status guardian.Status, target string) error {
	from := status.CoreVersion
	if from == "" {
		return fmt.Errorf("无法确认运行版本,先 bx doctor")
	}
	if from == target && !c.Bool("force") {
		if !c.Bool("json") {
			fmt.Println("✅ 已是最新,无需更新。")
		}
		return nil
	}

	var randomSuffix [4]byte
	if _, err := rand.Read(randomSuffix[:]); err != nil {
		return fmt.Errorf("生成事务 ID: %w", err)
	}
	txid := newUpdateTransactionID(time.Now().Unix(), randomSuffix[:])
	stagingDir := filepath.Join("/var/lib/bx/update/staging", txid)
	if err := os.MkdirAll(stagingDir, 0o700); err != nil {
		return fmt.Errorf("创建暂存目录: %w", err)
	}
	defer os.RemoveAll(stagingDir)
	packagePath := filepath.Join(stagingDir, "package.tgz")
	if err := os.WriteFile(packagePath, data, 0o600); err != nil {
		return fmt.Errorf("暂存更新包: %w", err)
	}

	asJSON := c.Bool("json")
	if !asJSON {
		fmt.Println("1/4 准备:包已验证并暂存")
		fmt.Println("2/4 更新中:网络可能短暂暂停,bx 将自动重连…")
	}

	sum := sha256.Sum256(data)
	request := buildGuardianUpdateRequest(txid, from, pkg, hex.EncodeToString(sum[:]), packagePath)
	result, err := guardian.NewClientWithTimeout(guardian.SocketPath, 90*time.Second).Update(context.Background(), request)
	if err != nil {
		return fmt.Errorf("更新失败:%w;运行 sudo bx status 与 bx doctor 检查保护状态", err)
	}
	// JSON body 无论成功还是回滚都先写出(调用方需要 to_version/rolled_back 等字段
	// 判读结果);但退出码绝不能在写完 JSON 后就地 return nil——回滚意味着更新失败,
	// 必须走下面统一的 RolledBack 判断落到非零退出,--json 和人读模式共用同一条
	// "回滚→非零退出"路径,不能因为选了 --json 就被绕过。
	if asJSON {
		if err := json.NewEncoder(os.Stdout).Encode(result); err != nil {
			return err
		}
	} else if result.RolledBack {
		fmt.Printf("3/4 新版本未通过健康检查,已自动回滚\n4/4 完成:保持 %s,保护未降级直连 ✅\n", result.FromVersion)
	} else {
		fmt.Printf("3/4 已重连(protection_state=%s)\n4/4 完成 ✅ bx 已更新到 %s\n", result.ProtectionState, result.ToVersion)
	}
	if result.RolledBack {
		return fmt.Errorf("更新已自动回滚,仍运行 %s", result.FromVersion)
	}
	return nil
}

// decideUnifiedUpdateRoute 是纯函数:按 Guardian /v1/status 的结果决定 bx update 走
// 哪条路。statusErr!=nil 是「问不到 Guardian」——若确认装了 Guardian(guardianLoaded)
// 这就是异常态,拒绝盲猜、指引 doctor;若压根没装 Guardian,则视为 Off 直接安装。
func decideUnifiedUpdateRoute(status guardian.Status, statusErr error, guardianLoaded bool) (string, error) {
	if statusErr == nil {
		switch status.Protection {
		case guardian.ProtectionProtected:
			return "guardian", nil
		case guardian.ProtectionOff:
			return "direct", nil
		case guardian.ProtectionStarting, guardian.ProtectionRecovering:
			return "", fmt.Errorf("Guardian 正在 %s,请稍后再试", status.Protection)
		default: // blocked、needs_attention 或未知取值
			return "", fmt.Errorf("Guardian 状态需处理(%s),请先运行 sudo bx doctor", status.Protection)
		}
	}
	if guardianLoaded {
		return "", fmt.Errorf("Guardian 状态不明,先 sudo bx doctor: %w", statusErr)
	}
	return "direct", nil
}

// newUpdateTransactionID 是纯函数,生成形如 "update-<unix>-<hex>" 的事务 ID
// (匹配 guardian 的 updateTransactionIDPattern),同输入确定性、便于测试。
func newUpdateTransactionID(now int64, random []byte) string {
	return fmt.Sprintf("update-%d-%s", now, hex.EncodeToString(random))
}

// buildGuardianUpdateRequest 是纯函数,把已校验好的本地状态组装成
// guardian.UpdateRequest。AppPath 留空,由服务端默认到 /Applications/Bx.app。
func buildGuardianUpdateRequest(txid, fromVersion string, pkg updatepkg.MacOSPackage, packageSHA, packagePath string) guardian.UpdateRequest {
	return guardian.UpdateRequest{
		TransactionID: txid,
		FromVersion:   fromVersion,
		ToVersion:     pkg.Release.Version,
		AssetSHA256:   packageSHA,
		PackagePath:   packagePath,
	}
}
