// Package install 生成并安装 bx 的 systemd 服务(开机自启)。
package install

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	ServiceName       = "bx.service"
	ServerServiceName = "bx-server.service"
	unitPath          = "/etc/systemd/system/bx.service"
	serverUnitPath    = "/etc/systemd/system/bx-server.service"
	// BinPath 是 bx 自身安装到 PATH 的规范位置。
	BinPath = "/usr/local/bin/bx"
)

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
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
CapabilityBoundingSet=
LockPersonality=true

[Install]
WantedBy=multi-user.target
`
}

// WriteUnit 写入 unit 文件并 daemon-reload(不 enable、不 start)。需 root。
func WriteUnit(execStart string) error {
	return writeUnitFile(unitPath, UnitText(execStart))
}

// WriteServerUnit 写入 bx server unit 文件并 daemon-reload(不 enable、不 start)。需 root。
func WriteServerUnit(execStart string) error {
	return writeUnitFile(serverUnitPath, ServerUnitText(execStart))
}

func writeUnitFile(path, text string) error {
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("写 %s(需 root): %w", path, err)
	}
	return runSystemctl("daemon-reload")
}

// Enable 启动并设为开机自启。
func Enable() error { return runSystemctl("enable", "--now", ServiceName) }

// Disable 停止并取消开机自启。
func Disable() error { return runSystemctl("disable", "--now", ServiceName) }

// EnableServer 启动 bx server 并设为开机自启。
func EnableServer() error { return runSystemctl("enable", "--now", ServerServiceName) }

// DisableServer 停止 bx server 并取消开机自启。
func DisableServer() error { return runSystemctl("disable", "--now", ServerServiceName) }

// RestartServer 重启 bx server。
func RestartServer() error { return runSystemctl("restart", ServerServiceName) }

// UnitInstalled 报告 unit 文件是否已就位(用于 up 前置校验)。
func UnitInstalled() bool {
	_, err := os.Stat(unitPath)
	return err == nil
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
	b, err := os.ReadFile(unitPath)
	if err != nil {
		return "", fmt.Errorf("读 %s: %w", unitPath, err)
	}
	return execStartCmd(string(b)), nil
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

// Uninstall 停用并删除服务。
func Uninstall() error {
	_ = runSystemctl("disable", "--now", ServiceName)
	_ = os.Remove(unitPath)
	return runSystemctl("daemon-reload")
}

// UninstallServer 停用并删除 bx server 服务。
func UninstallServer() error {
	_ = runSystemctl("disable", "--now", ServerServiceName)
	_ = os.Remove(serverUnitPath)
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
