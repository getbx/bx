//go:build darwin

package install

import (
	"crypto/sha256"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/getbx/bx/internal/release"
	"github.com/getbx/bx/internal/runtimedir"
)

var bundleIdentifierPattern = regexp.MustCompile(`(?s)<key>CFBundleIdentifier</key>\s*<string>([^<]+)</string>`)

const (
	menuBundleIdentifier      = "com.getbx.bx.menu"
	menuLaunchAgentLabel      = "com.getbx.bx.menu"
	legacyMenuLaunchAgentName = "com.ggshr9.bx.menu.plist"
	menuLaunchAgentPlistName  = "com.getbx.bx.menu.plist"
	defaultAppDestinationPath = "/Applications/Bx.app"
	appBundleName             = "Bx.app"
	appInstallOldDirSuffix    = ".Bx.app.install-old"
	appInstallStagePrefix     = ".bx-app-stage-"
)

// UnifiedInstallOptions 描述一次特权统一安装(Bx.app + bx runtime + bridge + Guardian + 登录项)的输入。
type UnifiedInstallOptions struct {
	BundlePath     string // 源 Bx.app(必填,绝对路径,basename == "Bx.app")
	AppDestination string // 默认 /Applications/Bx.app
	ConfigPath     string // 默认 /etc/bx/config.yaml
	RuntimeRoot    string // 默认 runtimedir.Root(测试注入)
	BridgePath     string // 默认 BinPath(测试注入)
	ConsoleHome    string // 控制台用户家目录(登录项/legacy 清理;空则跳过这两步)
	ConsoleUID     int
	ConsoleGID     int
	Chown          func(string, int, int) error              // 默认 os.Chown(测试注入)
	WriteGuardian  func(executable, configPath string) error // 默认 WriteGuardianUnit(测试注入)
}

// UnifiedInstallResult 汇报一次 UnifiedInstall 的落盘结果。
type UnifiedInstallResult struct {
	Version           string
	AppPath           string
	RuntimeExecutable string
	BridgeReplaced    bool
	RemovedLegacyApp  bool
}

func (o UnifiedInstallOptions) withDefaults() UnifiedInstallOptions {
	if o.AppDestination == "" {
		o.AppDestination = defaultAppDestinationPath
	}
	if o.ConfigPath == "" {
		o.ConfigPath = "/etc/bx/config.yaml"
	}
	if o.RuntimeRoot == "" {
		o.RuntimeRoot = runtimedir.Root
	}
	if o.BridgePath == "" {
		o.BridgePath = BinPath
	}
	if o.Chown == nil {
		o.Chown = os.Chown
	}
	if o.WriteGuardian == nil {
		o.WriteGuardian = WriteGuardianUnit
	}
	return o
}

// UnifiedInstall 一次事务安装 App/runtime/bridge/Guardian/登录项:先全量验证(只读),
// 验证全部通过后才开始落盘;任何验证失败时文件系统零改动。
func UnifiedInstall(options UnifiedInstallOptions) (UnifiedInstallResult, error) {
	options = options.withDefaults()
	if !filepath.IsAbs(options.BundlePath) {
		return UnifiedInstallResult{}, fmt.Errorf("BundlePath 必须是绝对路径: %q", options.BundlePath)
	}
	if filepath.Base(options.BundlePath) != appBundleName {
		return UnifiedInstallResult{}, fmt.Errorf("BundlePath 必须以 %s 结尾: %q", appBundleName, options.BundlePath)
	}

	// 1. 全量验证(只读)
	verified, err := loadVerifiedBundle(options.BundlePath)
	if err != nil {
		return UnifiedInstallResult{}, err
	}
	result := UnifiedInstallResult{Version: verified.Info.Version}

	// 2. App 落位
	appPath, err := installAppBundle(options)
	if err != nil {
		return UnifiedInstallResult{}, err
	}
	result.AppPath = appPath

	// 3. runtime
	if _, err := runtimedir.InstallVersion(options.RuntimeRoot, runtimedir.Payload{CLI: verified.CLI, Info: verified.Info}); err != nil {
		return UnifiedInstallResult{}, err
	}
	if err := runtimedir.SwitchCurrent(options.RuntimeRoot, verified.Info.Version); err != nil {
		return UnifiedInstallResult{}, err
	}
	result.RuntimeExecutable = runtimedir.ExecutablePath(options.RuntimeRoot)

	// 4. bridge(仅内容变化时替换)
	replaced, err := installBridge(options, verified.Bridge)
	if err != nil {
		return UnifiedInstallResult{}, err
	}
	result.BridgeReplaced = replaced

	// 5. Guardian plist(不 enable)
	if err := options.WriteGuardian(result.RuntimeExecutable, options.ConfigPath); err != nil {
		return UnifiedInstallResult{}, err
	}

	// 6+7. 登录项与 legacy 迁移
	if options.ConsoleHome != "" {
		if err := writeMenuAgent(options, appPath); err != nil {
			return UnifiedInstallResult{}, err
		}
		removed, err := removeLegacyUserApp(options, appPath)
		if err != nil {
			return UnifiedInstallResult{}, err
		}
		result.RemovedLegacyApp = removed
	}
	return result, nil
}

// verifiedBundle 是 loadVerifiedBundle 的只读验证结果。
type verifiedBundle struct {
	Info   release.Info
	CLI    []byte
	Bridge []byte
}

// loadVerifiedBundle 只读校验源 Bx.app:release.json 平台匹配、bx-cli/bx-bridge 摘要匹配、
// BxMenu 可执行文件存在、Info.plist 存在且 bundle id 匹配 menuBundleIdentifier。
// 任何一步失败都不落盘,调用方不应据此改动文件系统。
func loadVerifiedBundle(bundlePath string) (verifiedBundle, error) {
	resources := filepath.Join(bundlePath, "Contents", "Resources")
	info, err := release.Load(filepath.Join(resources, release.FileName))
	if err != nil {
		return verifiedBundle{}, fmt.Errorf("读取 release 元数据: %w", err)
	}
	if err := info.MatchesPlatform(runtime.GOOS, runtime.GOARCH); err != nil {
		return verifiedBundle{}, err
	}
	cli, err := os.ReadFile(filepath.Join(resources, "bx-cli"))
	if err != nil {
		return verifiedBundle{}, fmt.Errorf("读取 bx-cli: %w", err)
	}
	if err := info.VerifyAsset("bx-cli", cli); err != nil {
		return verifiedBundle{}, err
	}
	bridge, err := os.ReadFile(filepath.Join(resources, "bx-bridge"))
	if err != nil {
		return verifiedBundle{}, fmt.Errorf("读取 bx-bridge: %w", err)
	}
	if err := info.VerifyAsset("bx-bridge", bridge); err != nil {
		return verifiedBundle{}, err
	}
	menuExecutable := filepath.Join(bundlePath, "Contents", "MacOS", "BxMenu")
	if _, err := os.Stat(menuExecutable); err != nil {
		return verifiedBundle{}, fmt.Errorf("BxMenu 可执行文件缺失: %w", err)
	}
	infoPlistPath := filepath.Join(bundlePath, "Contents", "Info.plist")
	bundleID, err := readBundleIdentifier(infoPlistPath)
	if err != nil {
		return verifiedBundle{}, err
	}
	if bundleID != menuBundleIdentifier {
		return verifiedBundle{}, fmt.Errorf("Info.plist CFBundleIdentifier %q 不是 %q", bundleID, menuBundleIdentifier)
	}
	return verifiedBundle{Info: info, CLI: cli, Bridge: bridge}, nil
}

// readBundleIdentifier 读取并从 Info.plist 中提取 CFBundleIdentifier。
func readBundleIdentifier(infoPlistPath string) (string, error) {
	data, err := os.ReadFile(infoPlistPath)
	if err != nil {
		return "", fmt.Errorf("读取 %s: %w", infoPlistPath, err)
	}
	matches := bundleIdentifierPattern.FindSubmatch(data)
	if matches == nil {
		return "", fmt.Errorf("%s 缺少 CFBundleIdentifier", infoPlistPath)
	}
	return string(matches[1]), nil
}

// installAppBundle 把源 Bx.app 落位到 options.AppDestination:两者(经 EvalSymlinks)相同时跳过;
// 否则 copyBundleTree 到不可预测名字的 stage 临时目录、整树 chown 到 root:wheel、旧 App 原子替换。
//
// stage 刻意不落在 destDir(如 /Applications,drwxrwxr-x root:admin,同 admin 组任意用户可写)下:
// 若用固定/可预测的 stage 名字,攻击者可在 RemoveAll(旧 stage) 与本次 copy 之间的窗口抢先在该路径
// 种一个指向自己目录的符号链接,而 MkdirAll/OpenFile(O_CREATE)/os.Chown 都会跟随符号链接写穿,
// chownTreeWith 就会把攻击者目录 chown 成 root(TOCTOU 提权)。改为在 RuntimeRoot 的父目录(仅
// root 可写,非 group-writable)下 os.MkdirTemp 出不可预测名字的 stage,race 窗口消失。
func installAppBundle(options UnifiedInstallOptions) (string, error) {
	dest := options.AppDestination
	same, err := samePath(options.BundlePath, dest)
	if err != nil {
		return "", err
	}
	if same {
		return dest, nil
	}

	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("建目录 %s: %w", destDir, err)
	}

	stageParent := filepath.Dir(options.RuntimeRoot)
	if err := os.MkdirAll(stageParent, 0o755); err != nil {
		return "", fmt.Errorf("建目录 %s: %w", stageParent, err)
	}
	stage, err := os.MkdirTemp(stageParent, appInstallStagePrefix)
	if err != nil {
		return "", fmt.Errorf("建 stage 临时目录: %w", err)
	}
	if err := copyBundleTree(options.BundlePath, stage); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}
	// MkdirTemp 默认以 0700 创建 stage 根目录,copyBundleTree 对已存在的根目录不会改权限
	// (os.MkdirAll 遇到已存在的目录直接返回,不改 mode),这里补 chmod 让落位后的 App 目录
	// 权限恢复常规的 0755(可执行/可读),不残留 0700。
	if err := os.Chmod(stage, 0o755); err != nil {
		_ = os.RemoveAll(stage)
		return "", fmt.Errorf("chmod stage %s: %w", stage, err)
	}
	if err := chownTreeWith(options.Chown, stage, 0, 0); err != nil {
		_ = os.RemoveAll(stage)
		return "", err
	}

	old := filepath.Join(destDir, appInstallOldDirSuffix)
	if err := os.RemoveAll(old); err != nil {
		_ = os.RemoveAll(stage)
		return "", fmt.Errorf("清理旧 old 目录 %s: %w", old, err)
	}
	if _, err := os.Lstat(dest); err == nil {
		if err := os.Rename(dest, old); err != nil {
			_ = os.RemoveAll(stage)
			return "", fmt.Errorf("移走旧 App %s: %w", dest, err)
		}
	} else if !os.IsNotExist(err) {
		_ = os.RemoveAll(stage)
		return "", fmt.Errorf("检查旧 App %s: %w", dest, err)
	}
	if err := os.Rename(stage, dest); err != nil {
		return "", fmt.Errorf("落位 App 到 %s: %w", dest, err)
	}
	_ = os.RemoveAll(old)
	return dest, nil
}

// samePath 报告两个路径经 EvalSymlinks 后是否指向同一位置。
// 目的路径不存在时,仅当两者字面量相同才视为相同(避免误判)。
func samePath(a, b string) (bool, error) {
	resolvedA, errA := filepath.EvalSymlinks(a)
	if errA != nil {
		if os.IsNotExist(errA) {
			resolvedA = a
		} else {
			return false, fmt.Errorf("解析 %s: %w", a, errA)
		}
	}
	resolvedB, errB := filepath.EvalSymlinks(b)
	if errB != nil {
		if os.IsNotExist(errB) {
			resolvedB = b
		} else {
			return false, fmt.Errorf("解析 %s: %w", b, errB)
		}
	}
	return resolvedA == resolvedB, nil
}

// copyBundleTree 把 src 目录树复制到 dst(dst 须不存在)。遇符号链接直接报错(拒绝跟随/复制),
// 目录与文件权限位从源保留。
func copyBundleTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(src, path)
		if relErr != nil {
			return relErr
		}
		target := filepath.Join(dst, rel)
		if entry.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("拒绝复制符号链接: %s", path)
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return statErr
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		return copyFileWithPerm(path, target, info.Mode().Perm())
	})
}

func copyFileWithPerm(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("读取 %s: %w", src, err)
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("建目录 %s: %w", filepath.Dir(dst), err)
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("创建 %s: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("复制到 %s: %w", dst, err)
	}
	if err := out.Chmod(perm); err != nil {
		out.Close()
		return fmt.Errorf("chmod %s: %w", dst, err)
	}
	return out.Close()
}

// chownTreeWith 用给定的 chown 函数把 root 目录树下所有文件/目录 chown 到 uid:gid。
func chownTreeWith(chown func(string, int, int) error, root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if chownErr := chown(path, uid, gid); chownErr != nil {
			return fmt.Errorf("chown %s: %w", path, chownErr)
		}
		return nil
	})
}

// installBridge 在 options.BridgePath 处的内容 sha256 与 bridgeData 不同(含不存在)时用
// ReplaceBinary 原子替换并 chown root:wheel,返回是否发生了替换。
func installBridge(options UnifiedInstallOptions, bridgeData []byte) (bool, error) {
	existing, err := os.ReadFile(options.BridgePath)
	if err == nil {
		existingSum := sha256.Sum256(existing)
		wantSum := sha256.Sum256(bridgeData)
		if existingSum == wantSum {
			return false, nil
		}
	} else if !os.IsNotExist(err) {
		return false, fmt.Errorf("读取现有 bridge %s: %w", options.BridgePath, err)
	}
	if err := ReplaceBinary(options.BridgePath, bridgeData); err != nil {
		return false, err
	}
	if err := options.Chown(options.BridgePath, 0, 0); err != nil {
		return false, fmt.Errorf("chown bridge %s: %w", options.BridgePath, err)
	}
	return true, nil
}

// writeMenuAgent 在 ConsoleHome 的 LaunchAgents 目录写入统一菜单栏 App 的登录项 plist,
// 并删除 legacy(com.ggshr9.bx.menu)登录项。
func writeMenuAgent(options UnifiedInstallOptions, appPath string) error {
	agentDir := filepath.Join(options.ConsoleHome, "Library", "LaunchAgents")
	newlyCreated := false
	if _, err := os.Stat(agentDir); os.IsNotExist(err) {
		newlyCreated = true
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		return fmt.Errorf("建目录 %s: %w", agentDir, err)
	}
	if newlyCreated {
		if err := options.Chown(agentDir, options.ConsoleUID, options.ConsoleGID); err != nil {
			return fmt.Errorf("chown %s: %w", agentDir, err)
		}
	}

	logDir := filepath.Join(options.ConsoleHome, "Library", "Logs", "bx")
	executable := filepath.Join(appPath, "Contents", "MacOS", "BxMenu")
	plistText := MenuAgentPlistText(executable, logDir)
	agentPath := filepath.Join(agentDir, menuLaunchAgentPlistName)
	if err := os.WriteFile(agentPath, []byte(plistText), 0o644); err != nil {
		return fmt.Errorf("写 %s: %w", agentPath, err)
	}
	if err := options.Chown(agentPath, options.ConsoleUID, options.ConsoleGID); err != nil {
		return fmt.Errorf("chown %s: %w", agentPath, err)
	}

	legacyAgentPath := filepath.Join(agentDir, legacyMenuLaunchAgentName)
	if err := os.Remove(legacyAgentPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除 legacy 登录项 %s: %w", legacyAgentPath, err)
	}
	return nil
}

// removeLegacyUserApp 删除 ConsoleHome 下的用户级 legacy Bx.app(<home>/Applications/Bx.app),
// 前提是它与新 appPath 不是同一路径,且其 Info.plist bundle id 确为 bx 的菜单栏 App(避免误删同名第三方 App)。
func removeLegacyUserApp(options UnifiedInstallOptions, appPath string) (bool, error) {
	legacy := filepath.Join(options.ConsoleHome, "Applications", "Bx.app")
	if _, err := os.Stat(legacy); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("检查 legacy App %s: %w", legacy, err)
	}
	same, err := samePath(legacy, appPath)
	if err != nil {
		return false, err
	}
	if same {
		return false, nil
	}
	bundleID, err := readBundleIdentifier(filepath.Join(legacy, "Contents", "Info.plist"))
	if err != nil {
		// 无法读取/解析 bundle id 的目录不当作 bx 的 legacy App,谨慎不删。
		return false, nil
	}
	if bundleID != menuBundleIdentifier {
		return false, nil
	}
	if err := os.RemoveAll(legacy); err != nil {
		return false, fmt.Errorf("删除 legacy App %s: %w", legacy, err)
	}
	return true, nil
}

// MenuAgentPlistText 返回统一菜单栏 App 的用户级 LaunchAgent plist 文本,与
// scripts/install-macos-menu.sh 的 write_launch_agent 和 scripts/package-macos-menu.sh
// 生成的 LaunchAgent 逐键一致(Label com.getbx.bx.menu、单元素 ProgramArguments、
// RunAtLoad true、KeepAlive={SuccessfulExit: false}、StandardOut/ErrPath 指向
// <logDir>/menu.log|menu.err.log)。
func MenuAgentPlistText(executable, logDir string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>`)
	writeXMLEscaped(&b, menuLaunchAgentLabel)
	b.WriteString(`</string>
  <key>ProgramArguments</key>
  <array>
    <string>`)
	writeXMLEscaped(&b, executable)
	b.WriteString(`</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>
  <key>StandardOutPath</key>
  <string>`)
	writeXMLEscaped(&b, filepath.Join(logDir, "menu.log"))
	b.WriteString(`</string>
  <key>StandardErrorPath</key>
  <string>`)
	writeXMLEscaped(&b, filepath.Join(logDir, "menu.err.log"))
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.String()
}
