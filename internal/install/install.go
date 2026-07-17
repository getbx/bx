// Package install 生成并安装 bx 的系统服务(开机自启)。
package install

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	ServiceName            = "bx.service"
	ServerServiceName      = "bx-server.service"
	unitPath               = "/etc/systemd/system/bx.service"
	serverUnitPath         = "/etc/systemd/system/bx-server.service"
	shareUnitPrefix        = "/etc/systemd/system/bx-share-"
	launchdLabel           = "com.getbx.bx"
	launchdPlistPath       = "/Library/LaunchDaemons/com.getbx.bx.plist"
	legacyLaunchdLabel     = "com.ggshr9.bx"
	legacyLaunchdPlistPath = "/Library/LaunchDaemons/com.ggshr9.bx.plist"
	launchdStdoutPath      = "/var/log/bx.log"
	launchdStderrPath      = "/var/log/bx.err.log"
	dnsStatePath           = "/var/lib/bx/dns-original.json"
)

// BinPath 是 bx 自身安装到 PATH 的规范位置(OS-aware,见 paths_{windows,other}.go)。

type DNSStatus struct {
	Supported  bool     `json:"supported"`
	Enabled    bool     `json:"enabled"`
	Service    string   `json:"service,omitempty"`
	Servers    []string `json:"servers,omitempty"`
	StateSaved bool     `json:"state_saved"`
	StatePath  string   `json:"state_path,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

type dnsState struct {
	Service string   `json:"service"`
	Servers []string `json:"servers,omitempty"`
	Empty   bool     `json:"empty"`
}

type dnsCommandRunner interface {
	CombinedOutput(context.Context, string, ...string) ([]byte, error)
	Run(context.Context, string, ...string) error
}

type execDNSCommandRunner struct{}

func (execDNSCommandRunner) CombinedOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func (execDNSCommandRunner) Run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return cmd.Run()
}

// SelfInstall 把当前运行的 bx 二进制安装到 BinPath(原子覆盖,0755),返回该路径。
// 若已从 BinPath 运行则直接返回不复制。用于 setup 让用户免去手动 cp/install。
func SelfInstall() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("定位自身可执行文件: %w", err)
	}
	if self == BinPath {
		return BinPath, nil
	}
	if err := copyExecutable(self, BinPath); err != nil {
		return "", err
	}
	return BinPath, nil
}

// copyExecutable 原子复制 src 到 dst(同目录临时文件 + rename),权限 0755。
// 用 rename 覆盖:即便 dst 正是当前运行的二进制也安全(替换的是目录项,非 inode)。
func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("读 %s: %w", src, err)
	}
	defer in.Close()
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建目录 %s(需 root?): %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".bx-*")
	if err != nil {
		return fmt.Errorf("建临时文件于 %s(需 root?): %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("复制 bx: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("刷写临时文件: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("替换 %s(需 root?): %w", dst, err)
	}
	return nil
}

// ReplaceBinary 用 data 原子替换 dst 处的二进制(同目录临时文件 + chmod 0755 + rename)。
// 即便 dst 正是当前运行的二进制也安全(rename 换的是目录项而非 inode,避开 ETXTBSY)。
// 供 bx update 校验通过后落盘新版用。
func ReplaceBinary(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("建目录 %s(需 root?): %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".bx-*")
	if err != nil {
		return fmt.Errorf("建临时文件于 %s(需 root?): %w", dir, err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("写新二进制: %w", err)
	}
	if err := tmp.Chmod(0o755); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("刷写临时文件: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("替换 %s(需 root?): %w", dst, err)
	}
	return nil
}

// UnitText 返回 systemd unit 文件内容。execStart 是完整启动命令。
func UnitText(execStart string) string {
	return UnitTextWithDescription("bx 透明全局代理", execStart)
}

// UnitTextWithDescription 返回 systemd unit 文件内容。execStart 是完整启动命令。
func UnitTextWithDescription(description, execStart string) string {
	return `[Unit]
Description=` + description + `
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + execStart + `
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576

[Install]
WantedBy=multi-user.target
`
}

// ServerUnitText 返回 bx server 的 systemd unit。server 不需要 TUN/路由权限,
// 因此默认加一组保守沙箱,把可写范围收敛到运行期数据目录。
func ServerUnitText(execStart string) string {
	return `[Unit]
Description=bx server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=` + execStart + `
Restart=on-failure
RestartSec=3
LimitNOFILE=1048576
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadOnlyPaths=/etc/bx
ReadWritePaths=/var/lib/bx
# AF_NETLINK:reality/hysteria2 用的内嵌 sing-box 要订阅路由/接口更新,缺它启动即
# FATAL("subscribe route updates: address family not supported")。brook 用不到但无害。
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
# CAP_NET_BIND_SERVICE:reality 默认监听 443(特权端口),空 CapabilityBoundingSet 会把它
# 从 root 也剥掉,导致 bind 443 permission denied。给最小绑定特权(高端口的 brook 用不到但无害)。
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
AmbientCapabilities=CAP_NET_BIND_SERVICE
LockPersonality=true

[Install]
WantedBy=multi-user.target
`
}

// WriteUnit 写入 unit 文件并 daemon-reload(不 enable、不 start)。需 root/管理员。
// Windows:创建服务(手动启动、不启动),对齐 setup「只装不启」。
func WriteUnit(execStart string) error {
	switch runtime.GOOS {
	case "darwin":
		configPath, err := guardianConfigPathFromExecStart(execStart)
		if err != nil {
			return err
		}
		return WriteGuardianUnit(configPath)
	case "windows":
		return windowsInstallService(execStart)
	default:
		return writeUnitFile(unitPath, UnitText(execStart))
	}
}

func guardianConfigPathFromExecStart(execStart string) (string, error) {
	fields := strings.Fields(execStart)
	if len(fields) != 6 || fields[0] != BinPath || fields[1] != "guardian" ||
		fields[2] != "--config" || fields[4] != "--listen-dns" || fields[5] != "127.0.0.1:53" {
		return "", fmt.Errorf("refusing non-Guardian macOS service command")
	}
	if !filepath.IsAbs(fields[3]) {
		return "", fmt.Errorf("Guardian config path must be absolute: %q", fields[3])
	}
	return fields[3], nil
}

// WriteServerUnit 写入 bx server unit 文件并 daemon-reload(不 enable、不 start)。需 root。
func WriteServerUnit(execStart string) error {
	return writeUnitFile(serverUnitPath, ServerUnitText(execStart))
}

// ShareServiceName 返回命名分享对应的 systemd service 名。
func ShareServiceName(name string) string { return "bx-share-" + name + ".service" }

// WriteShareUnit 写入命名分享的 unit 文件并 daemon-reload。
func WriteShareUnit(name, execStart string) error {
	return writeUnitFile(shareUnitPrefix+name+".service", ServerUnitText(execStart))
}

func writeUnitFile(path, text string) error {
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写 %s(需 root): %w", path, err)
	}
	return runSystemctl("daemon-reload")
}

func writeLaunchdPlist(path, text string) error {
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写 %s(需 root): %w", path, err)
	}
	return nil
}

// Enable 启动并设为开机自启。
func Enable() error {
	switch runtime.GOOS {
	case "darwin":
		if anyLaunchdClientServiceLoaded(loadedLaunchdClientServices()) {
			return nil
		}
		if err := migrateLegacyLaunchdPlist(); err != nil {
			return err
		}
		if cmds := launchdEnableCommands(); len(cmds) > 0 {
			_ = runLaunchctlQuiet(cmds[0]...)
			for _, args := range cmds[1:] {
				if err := runLaunchctl(args...); err != nil {
					return err
				}
			}
		}
		return nil
	case "windows":
		return windowsEnableService()
	default:
		return runSystemctl("enable", "--now", ServiceName)
	}
}

func launchdEnableCommands() [][]string {
	label := "system/" + launchdLabel
	return [][]string{
		{"bootout", "system", launchdPlistPath},
		{"enable", label},
		{"bootstrap", "system", launchdPlistPath},
		{"kickstart", "-k", label},
	}
}

// Disable 停止并取消开机自启。
func Disable() error {
	switch runtime.GOOS {
	case "darwin":
		cmds := launchdDisableCommands(loadedLaunchdClientServices())
		for _, args := range cmds {
			if args[0] == "disable" {
				_ = runLaunchctl(args...)
				continue
			}
			if err := runLaunchctl(args...); err != nil {
				label := strings.TrimPrefix(args[1], "system/")
				if result := launchdBootoutResult(err, launchdServiceLoaded(label)); result != nil {
					return result
				}
			}
		}
		return migrateLegacyLaunchdPlist()
	case "windows":
		return windowsDisableService()
	default:
		return runSystemctl("disable", "--now", ServiceName)
	}
}

func launchdBootoutResult(err error, stillLoaded bool) error {
	if err != nil && stillLoaded {
		return err
	}
	return nil
}

func launchdDisableCommands(loaded map[string]bool) [][]string {
	var cmds [][]string
	for _, label := range launchdClientLabels() {
		cmds = append(cmds, []string{"disable", "system/" + label})
	}
	for _, label := range launchdClientLabels() {
		if loaded[label] {
			cmds = append(cmds, []string{"bootout", "system/" + label})
		}
	}
	return cmds
}

func loadedLaunchdClientServices() map[string]bool {
	loaded := make(map[string]bool)
	for _, label := range launchdClientLabels() {
		loaded[label] = launchdServiceLoaded(label)
	}
	return loaded
}

func anyLaunchdClientServiceLoaded(loaded map[string]bool) bool {
	for _, label := range launchdClientLabels() {
		if loaded[label] {
			return true
		}
	}
	return false
}

func launchdClientLabels() []string {
	return []string{launchdLabel, legacyLaunchdLabel}
}

func launchdServiceLoaded(label string) bool {
	return exec.Command("launchctl", "print", "system/"+label).Run() == nil
}

func migrateLegacyLaunchdPlist() error {
	if _, err := os.Stat(legacyLaunchdPlistPath); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return fmt.Errorf("检查旧版 launchd 服务 %s: %w", legacyLaunchdPlistPath, err)
	}
	if _, err := os.Stat(launchdPlistPath); err == nil {
		return os.Remove(legacyLaunchdPlistPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查 launchd 服务 %s: %w", launchdPlistPath, err)
	}
	b, err := os.ReadFile(legacyLaunchdPlistPath)
	if err != nil {
		return fmt.Errorf("读旧版 launchd 服务 %s: %w", legacyLaunchdPlistPath, err)
	}
	text, err := migrateLegacyLaunchdPlistText(string(b))
	if err != nil {
		return err
	}
	if err := writeLaunchdPlist(launchdPlistPath, text); err != nil {
		return err
	}
	if err := os.Remove(legacyLaunchdPlistPath); err != nil {
		return fmt.Errorf("移除旧版 launchd 服务 %s: %w", legacyLaunchdPlistPath, err)
	}
	return nil
}

func migrateLegacyLaunchdPlistText(text string) (string, error) {
	oldLabel := "<string>" + legacyLaunchdLabel + "</string>"
	if !strings.Contains(text, oldLabel) {
		return "", fmt.Errorf("旧版 launchd 服务缺少标签 %s", legacyLaunchdLabel)
	}
	return strings.Replace(text, oldLabel, "<string>"+launchdLabel+"</string>", 1), nil
}

func firstExistingPath(paths ...string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return ""
}

// Restart 重启已安装的 bx 客户端服务,不改变开机自启状态。
func Restart() error {
	switch runtime.GOOS {
	case "darwin":
		return runLaunchctl("kickstart", "-k", "system/"+launchdLabel)
	case "windows":
		return windowsRestartService()
	default:
		return runSystemctl("restart", ServiceName)
	}
}

// EnableServer 启动 bx server 并设为开机自启。
func EnableServer() error { return runSystemctl("enable", "--now", ServerServiceName) }

// DisableServer 停止 bx server 并取消开机自启。
func DisableServer() error { return runSystemctl("disable", "--now", ServerServiceName) }

// RestartServer 重启 bx server。
func RestartServer() error { return runSystemctl("restart", ServerServiceName) }

// EnableShare 启动命名分享并设为开机自启。
func EnableShare(name string) error { return runSystemctl("enable", "--now", ShareServiceName(name)) }

// DisableShare 停止命名分享并取消开机自启。
func DisableShare(name string) error { return runSystemctl("disable", "--now", ShareServiceName(name)) }

// UnitInstalled 报告 unit 文件是否已就位(用于 up 前置校验)。
func UnitInstalled() bool {
	switch runtime.GOOS {
	case "darwin":
		return firstExistingPath(launchdPlistPath, legacyLaunchdPlistPath) != ""
	case "windows":
		return windowsServiceInstalled()
	default:
		_, err := os.Stat(unitPath)
		return err == nil
	}
}

// ServerUnitInstalled 报告 bx server unit 是否已就位。
func ServerUnitInstalled() bool {
	_, err := os.Stat(serverUnitPath)
	return err == nil
}

// ExecStartCmd 读取已安装 unit,返回其 ExecStart 的子命令(run/up/…)。
// 命令模型重排后 up=systemctl enable;若旧 unit 仍写 `bx up`,新二进制启动 service
// 会让 up 递归调用自身。up 前用它防呆。unit 不存在或无法读时报错。
func ExecStartCmd() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		path := firstExistingPath(launchdPlistPath, legacyLaunchdPlistPath)
		if path == "" {
			return "", fmt.Errorf("launchd 服务未安装")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("读 %s: %w", path, err)
		}
		return launchdExecStartCmd(string(b)), nil
	case "windows":
		return windowsServiceExecCmd()
	default:
		b, err := os.ReadFile(unitPath)
		if err != nil {
			return "", fmt.Errorf("读 %s: %w", unitPath, err)
		}
		return execStartCmd(string(b)), nil
	}
}

// execStartCmd 从 unit 文本里取出 ExecStart 的子命令(二进制路径后的第一个参数)。
// 例:"ExecStart=/usr/local/bin/bx run -c /etc/bx/config.yaml" → "run"。
// 没有 ExecStart 或其后无子命令则返回 ""。
func execStartCmd(unitText string) string {
	for _, line := range strings.Split(unitText, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "ExecStart=")
		if !ok {
			continue
		}
		if fields := strings.Fields(rest); len(fields) >= 2 {
			return fields[1] // fields[0] 是 bx 二进制路径,[1] 是子命令
		}
		return ""
	}
	return ""
}

// LaunchdPlistText 返回 macOS LaunchDaemon plist。execStart 是完整启动命令。
func LaunchdPlistText(execStart string) string {
	args := strings.Fields(execStart)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>`)
	writeXMLEscaped(&b, launchdLabel)
	b.WriteString(`</string>
  <key>ProgramArguments</key>
  <array>
`)
	for _, arg := range args {
		b.WriteString("    <string>")
		writeXMLEscaped(&b, arg)
		b.WriteString("</string>\n")
	}
	b.WriteString(`  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>`)
	writeXMLEscaped(&b, launchdStdoutPath)
	b.WriteString(`</string>
  <key>StandardErrorPath</key>
  <string>`)
	writeXMLEscaped(&b, launchdStderrPath)
	b.WriteString(`</string>
</dict>
</plist>
`)
	return b.String()
}

func writeXMLEscaped(b *strings.Builder, s string) {
	_ = xml.EscapeText(b, []byte(s))
}

func launchdExecStartCmd(plistText string) string {
	dec := xml.NewDecoder(bytes.NewBufferString(plistText))
	var values []string
	inString := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			inString = t.Name.Local == "string"
		case xml.EndElement:
			if t.Name.Local == "string" {
				inString = false
			}
		case xml.CharData:
			if inString {
				values = append(values, string(t))
			}
		}
	}
	for i, value := range values {
		if value == BinPath && i+1 < len(values) {
			return values[i+1]
		}
	}
	return ""
}

// Uninstall 停用并删除服务。
func Uninstall() error {
	switch runtime.GOOS {
	case "darwin":
		for _, label := range launchdClientLabels() {
			if launchdServiceLoaded(label) {
				_ = runLaunchctl("bootout", "system/"+label)
			}
		}
		_ = os.Remove(launchdPlistPath)
		_ = os.Remove(legacyLaunchdPlistPath)
		return nil
	case "windows":
		return windowsUninstallService()
	default:
		_ = runSystemctl("disable", "--now", ServiceName)
		_ = os.Remove(unitPath)
		return runSystemctl("daemon-reload")
	}
}

// UninstallServer 停用并删除 bx server 服务。
func UninstallServer() error {
	_ = runSystemctl("disable", "--now", ServerServiceName)
	_ = os.Remove(serverUnitPath)
	return runSystemctl("daemon-reload")
}

// UninstallShare 停用并删除命名分享服务。
func UninstallShare(name string) error {
	_ = runSystemctl("disable", "--now", ShareServiceName(name))
	_ = os.Remove(shareUnitPrefix + name + ".service")
	return runSystemctl("daemon-reload")
}

func runSystemctl(args ...string) error {
	cmd := exec.Command("systemctl", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func runLaunchctl(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func runLaunchctlQuiet(args ...string) error {
	cmd := exec.Command("launchctl", args...)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("launchctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// ShowLogs streams recent service logs to stdout/stderr.
func ShowLogs(service string, lines int, follow bool) error {
	if lines <= 0 {
		lines = 100
	}
	if runtime.GOOS == "darwin" && service == ServiceName {
		paths := existingPaths(launchdStdoutPath, launchdStderrPath)
		if len(paths) == 0 {
			return fmt.Errorf("未找到 bx 日志文件(%s, %s);服务可能尚未启动", launchdStdoutPath, launchdStderrPath)
		}
		args := []string{"-n", fmt.Sprint(lines)}
		if follow {
			args = append(args, "-f")
		}
		args = append(args, paths...)
		cmd := exec.Command("tail", args...)
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("tail %s: %w", strings.Join(args, " "), err)
		}
		return nil
	}
	args := []string{"-u", service, "--no-pager", "-n", fmt.Sprint(lines)}
	if follow {
		args = append(args, "-f")
	}
	cmd := exec.Command("journalctl", args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("journalctl %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

// TailLogs 返回服务末 lines 行日志(非 follow,供 mcp bx_logs 返回文本)。
// 与 ShowLogs 同源选择:darwin launchd 日志文件 tail、linux journalctl;返回合并输出。
func TailLogs(service string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100
	}
	if runtime.GOOS == "darwin" && service == ServiceName {
		paths := existingPaths(launchdStdoutPath, launchdStderrPath)
		if len(paths) == 0 {
			return "", fmt.Errorf("未找到 bx 日志文件(服务可能尚未启动)")
		}
		args := append([]string{"-n", fmt.Sprint(lines)}, paths...)
		out, err := exec.Command("tail", args...).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("tail: %w", err)
		}
		return string(out), nil
	}
	out, err := exec.Command("journalctl", "-u", service, "--no-pager", "-n", fmt.Sprint(lines)).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("journalctl: %w", err)
	}
	return string(out), nil
}

// ClientLogPaths returns the raw client log files used by macOS launchd.
func ClientLogPaths() []string {
	if runtime.GOOS == "darwin" {
		return []string{launchdStdoutPath, launchdStderrPath}
	}
	return nil
}

func InspectDNS(service string) (DNSStatus, error) {
	return inspectDNSContextWithRunner(context.Background(), execDNSCommandRunner{}, service)
}

func inspectDNSContextWithRunner(ctx context.Context, runner dnsCommandRunner, service string) (DNSStatus, error) {
	if err := ctx.Err(); err != nil {
		return DNSStatus{}, err
	}
	if runtime.GOOS != "darwin" {
		return DNSStatus{Supported: false, Detail: "DNS 接管仅支持 macOS"}, nil
	}
	return inspectDNSDarwinContextWithRunner(ctx, runner, dnsStatePath, service)
}

func inspectDNSDarwinContextWithRunner(ctx context.Context, runner dnsCommandRunner, statePath, service string) (DNSStatus, error) {
	resolved, err := resolveDNSServiceContextWithRunner(ctx, runner, service)
	if err != nil {
		return DNSStatus{Supported: true, StatePath: statePath}, err
	}
	servers, err := currentDNSServersContextWithRunner(ctx, runner, resolved)
	if err != nil {
		return DNSStatus{Supported: true, Service: resolved, StatePath: statePath}, err
	}
	_, stateErr := os.Stat(statePath)
	return DNSStatus{
		Supported:  true,
		Enabled:    len(servers) == 1 && servers[0] == "127.0.0.1",
		Service:    resolved,
		Servers:    servers,
		StateSaved: stateErr == nil,
		StatePath:  statePath,
	}, nil
}

func EnableDNS(service string) (DNSStatus, error) {
	if runtime.GOOS != "darwin" {
		return DNSStatus{Supported: false, Detail: "DNS 接管仅支持 macOS"}, fmt.Errorf("DNS 接管仅支持 macOS")
	}
	resolved, err := resolveDNSService(service)
	if err != nil {
		return DNSStatus{Supported: true, StatePath: dnsStatePath}, err
	}
	state, stateErr := readDNSState()
	if stateErr != nil || shouldRefreshDNSState(state, resolved) {
		if stateErr != nil && !errors.Is(stateErr, os.ErrNotExist) {
			return DNSStatus{Supported: true, Service: resolved, StatePath: dnsStatePath}, stateErr
		}
		if stateErr == nil && shouldRefreshDNSState(state, resolved) {
			if err := runNetworksetup(dnsRestoreArgs(state)...); err != nil {
				return DNSStatus{Supported: true, Service: resolved, StatePath: dnsStatePath}, err
			}
		}
		servers, err := currentDNSServers(resolved)
		if err != nil {
			return DNSStatus{Supported: true, Service: resolved, StatePath: dnsStatePath}, err
		}
		state := dnsState{Service: resolved, Servers: servers, Empty: len(servers) == 0}
		if err := writeDNSState(state); err != nil {
			return DNSStatus{Supported: true, Service: resolved, StatePath: dnsStatePath}, err
		}
	}
	if err := runNetworksetup("setdnsservers", resolved, "127.0.0.1"); err != nil {
		return DNSStatus{Supported: true, Service: resolved, StatePath: dnsStatePath}, err
	}
	if err := flushDNSCache(); err != nil {
		return DNSStatus{Supported: true, Service: resolved, StatePath: dnsStatePath}, err
	}
	return InspectDNS(service)
}

func shouldRefreshDNSState(state dnsState, resolvedService string) bool {
	return strings.TrimSpace(state.Service) != strings.TrimSpace(resolvedService)
}

func DisableDNS(service string) (DNSStatus, error) {
	return DisableDNSContext(context.Background(), service)
}

// DisableDNSContext restores the saved macOS DNS configuration while honoring
// cancellation across every external command in the restore path.
func DisableDNSContext(ctx context.Context, service string) (DNSStatus, error) {
	return disableDNSContextWithRunner(ctx, execDNSCommandRunner{}, service)
}

func disableDNSContextWithRunner(ctx context.Context, runner dnsCommandRunner, service string) (DNSStatus, error) {
	if err := ctx.Err(); err != nil {
		return DNSStatus{}, err
	}
	if runtime.GOOS != "darwin" {
		return DNSStatus{Supported: false, Detail: "DNS 接管仅支持 macOS"}, fmt.Errorf("DNS 接管仅支持 macOS")
	}
	return disableDNSDarwinContextWithRunner(ctx, runner, dnsStatePath, service)
}

func disableDNSDarwinContextWithRunner(ctx context.Context, runner dnsCommandRunner, statePath, service string) (DNSStatus, error) {
	state, err := readDNSStateAtPath(statePath)
	if err != nil {
		if dnsStateMissing(err) {
			st, inspectErr := inspectDNSDarwinContextWithRunner(ctx, runner, statePath, service)
			if inspectErr != nil {
				return st, inspectErr
			}
			return st, dnsStateMissingRecoveryError(st)
		}
		return DNSStatus{Supported: true, StatePath: statePath}, err
	}
	resolved := strings.TrimSpace(service)
	if resolved == "" {
		resolved = state.Service
	}
	state.Service = resolved
	args := dnsRestoreArgs(state)
	if err := runNetworksetupContextWithRunner(ctx, runner, args...); err != nil {
		return DNSStatus{Supported: true, Service: resolved, StatePath: statePath}, err
	}
	if err := ctx.Err(); err != nil {
		return DNSStatus{Supported: true, Service: resolved, StatePath: statePath}, err
	}
	if err := flushDNSCacheContextWithRunner(ctx, runner); err != nil {
		return DNSStatus{Supported: true, Service: resolved, StatePath: statePath}, err
	}
	status, err := inspectDNSDarwinContextWithRunner(ctx, runner, statePath, resolved)
	if err != nil {
		return status, err
	}
	if !dnsServersMatchState(state, status.Servers) {
		return status, fmt.Errorf("DNS restoration verification failed for service %s", resolved)
	}
	if err := ctx.Err(); err != nil {
		return status, err
	}
	if err := os.Remove(statePath); err != nil {
		return status, fmt.Errorf("删除 DNS 状态 %s: %w", statePath, err)
	}
	status.StateSaved = false
	return status, nil
}

func dnsStateMissing(err error) bool {
	return errors.Is(err, os.ErrNotExist)
}

// dnsStateMissingRecoveryError keeps shutdown safe when a prior DNS rollback
// removed its state file. A still-managed resolver must never be left behind.
func dnsStateMissingRecoveryError(st DNSStatus) error {
	if st.Enabled {
		return fmt.Errorf("DNS 当前指向 bx,但没有保存的原始 DNS 状态(%s)", dnsStatePath)
	}
	return nil
}

func dnsRestoreArgs(state dnsState) []string {
	if state.Empty || len(state.Servers) == 0 {
		return []string{"setdnsservers", state.Service, "Empty"}
	}
	return append([]string{"setdnsservers", state.Service}, state.Servers...)
}

func dnsServersMatchState(state dnsState, servers []string) bool {
	if state.Empty || len(state.Servers) == 0 {
		return len(servers) == 0
	}
	if len(state.Servers) != len(servers) {
		return false
	}
	for i := range state.Servers {
		if strings.TrimSpace(state.Servers[i]) != strings.TrimSpace(servers[i]) {
			return false
		}
	}
	return true
}

func existingPaths(paths ...string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			out = append(out, path)
		}
	}
	return out
}

func resolveDNSService(service string) (string, error) {
	return resolveDNSServiceContextWithRunner(context.Background(), execDNSCommandRunner{}, service)
}

func resolveDNSServiceContextWithRunner(ctx context.Context, runner dnsCommandRunner, service string) (string, error) {
	if strings.TrimSpace(service) != "" {
		return strings.TrimSpace(service), nil
	}
	dev, err := defaultDeviceDarwinContextWithRunner(ctx, runner)
	if err != nil {
		return "", err
	}
	return serviceForDeviceDarwinContextWithRunner(ctx, runner, dev)
}

func defaultDeviceDarwin() (string, error) {
	return defaultDeviceDarwinContextWithRunner(context.Background(), execDNSCommandRunner{})
}

func defaultDeviceDarwinContextWithRunner(ctx context.Context, runner dnsCommandRunner) (string, error) {
	out, err := runner.CombinedOutput(ctx, "route", "-n", "get", "default")
	if err != nil {
		return "", fmt.Errorf("route -n get default: %w", err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimSuffix(fields[0], ":") == "interface" {
			return fields[1], nil
		}
	}
	return "", fmt.Errorf("无法检测默认网络接口")
}

func serviceForDeviceDarwin(dev string) (string, error) {
	return serviceForDeviceDarwinContextWithRunner(context.Background(), execDNSCommandRunner{}, dev)
}

func serviceForDeviceDarwinContextWithRunner(ctx context.Context, runner dnsCommandRunner, dev string) (string, error) {
	out, err := runner.CombinedOutput(ctx, "networksetup", "-listnetworkserviceorder")
	if err != nil {
		return "", fmt.Errorf("networksetup -listnetworkserviceorder: %w", err)
	}
	service := ""
	needle := "Device: " + dev + ")"
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if isNetworkServiceLine(line) {
			if i := strings.Index(line, ") "); i >= 0 {
				service = line[i+2:]
			}
			continue
		}
		if strings.Contains(line, needle) && service != "" {
			return service, nil
		}
	}
	return "", fmt.Errorf("无法从接口 %s 检测 macOS 网络服务名", dev)
}

func isNetworkServiceLine(line string) bool {
	return len(line) > 3 && line[0] == '(' && line[1] >= '0' && line[1] <= '9'
}

func currentDNSServers(service string) ([]string, error) {
	return currentDNSServersContextWithRunner(context.Background(), execDNSCommandRunner{}, service)
}

func currentDNSServersContextWithRunner(ctx context.Context, runner dnsCommandRunner, service string) ([]string, error) {
	out, err := runner.CombinedOutput(ctx, "networksetup", "-getdnsservers", service)
	if err != nil {
		return nil, fmt.Errorf("networksetup -getdnsservers %s: %w", service, err)
	}
	text := strings.TrimSpace(string(out))
	if text == "" || strings.Contains(text, "There aren't any DNS Servers set") {
		return nil, nil
	}
	var servers []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			servers = append(servers, line)
		}
	}
	return servers, nil
}

func writeDNSState(state dnsState) error {
	return writeDNSStateAtPath(dnsStatePath, state)
}

func writeDNSStateAtPath(statePath string, state dnsState) error {
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return fmt.Errorf("创建 DNS 状态目录: %w", err)
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(statePath, b, 0o600); err != nil {
		return fmt.Errorf("写 DNS 状态 %s: %w", statePath, err)
	}
	return nil
}

func readDNSState() (dnsState, error) {
	return readDNSStateAtPath(dnsStatePath)
}

func readDNSStateAtPath(statePath string) (dnsState, error) {
	var state dnsState
	b, err := os.ReadFile(statePath)
	if err != nil {
		return state, fmt.Errorf("读 DNS 状态 %s: %w", statePath, err)
	}
	if err := json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("解析 DNS 状态 %s: %w", statePath, err)
	}
	if state.Service == "" {
		return state, fmt.Errorf("DNS 状态缺少 service")
	}
	return state, nil
}

func runNetworksetup(args ...string) error {
	return runNetworksetupContextWithRunner(context.Background(), execDNSCommandRunner{}, args...)
}

func runNetworksetupContextWithRunner(ctx context.Context, runner dnsCommandRunner, args ...string) error {
	if len(args) == 0 {
		return fmt.Errorf("networksetup: missing arguments")
	}
	args = append([]string{}, args...)
	if !strings.HasPrefix(args[0], "-") {
		args[0] = "-" + args[0]
	}
	if err := runner.Run(ctx, "networksetup", args...); err != nil {
		return fmt.Errorf("networksetup %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func flushDNSCache() error {
	return flushDNSCacheContextWithRunner(context.Background(), execDNSCommandRunner{})
}

func flushDNSCacheContextWithRunner(ctx context.Context, runner dnsCommandRunner) error {
	var unavailable []string
	flushed := false
	for _, command := range [][]string{
		{"dscacheutil", "-flushcache"},
		{"killall", "-HUP", "mDNSResponder"},
	} {
		if _, err := runner.CombinedOutput(ctx, command[0], command[1:]...); err != nil {
			if errors.Is(err, exec.ErrNotFound) {
				unavailable = append(unavailable, command[0])
				continue
			}
			return fmt.Errorf("%s %s: %w", command[0], strings.Join(command[1:], " "), err)
		}
		flushed = true
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	if !flushed {
		return fmt.Errorf("DNS cache flush commands unavailable (%s): %w", strings.Join(unavailable, ", "), exec.ErrNotFound)
	}
	return nil
}

// ServiceState 返回服务状态。action 使用 systemctl 风格:is-active/is-enabled。
func ServiceState(action, service string) string {
	if runtime.GOOS == "darwin" && service == ServiceName {
		return launchdState(action)
	}
	if runtime.GOOS == "windows" {
		return windowsServiceState(action)
	}
	return systemctlState(action, service)
}

func systemctlState(action, service string) string {
	out, err := exec.Command("systemctl", action, service).CombinedOutput()
	if err != nil {
		s := strings.TrimSpace(string(out))
		if s != "" {
			return s
		}
		return "unknown"
	}
	return strings.TrimSpace(string(out))
}

func launchdState(action string) string {
	switch action {
	case "is-active":
		for _, label := range launchdClientLabels() {
			if launchdServiceLoaded(label) {
				return "active"
			}
		}
		return "inactive"
	case "is-enabled":
		if firstExistingPath(launchdPlistPath, legacyLaunchdPlistPath) != "" {
			return "enabled"
		}
		return "disabled"
	default:
		return "unknown"
	}
}
