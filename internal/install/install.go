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
	ServiceName = "bx.service"
	unitPath    = "/etc/systemd/system/bx.service"
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
	return `[Unit]
Description=bx 透明全局代理
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

// WriteUnit 写入 unit 文件并 daemon-reload(不 enable、不 start)。需 root。
func WriteUnit(execStart string) error {
	if err := os.WriteFile(unitPath, []byte(UnitText(execStart)), 0o644); err != nil {
		return fmt.Errorf("写 %s(需 root): %w", unitPath, err)
	}
	return runSystemctl("daemon-reload")
}

// Enable 启动并设为开机自启。
func Enable() error { return runSystemctl("enable", "--now", ServiceName) }

// Disable 停止并取消开机自启。
func Disable() error { return runSystemctl("disable", "--now", ServiceName) }

// UnitInstalled 报告 unit 文件是否已就位(用于 up 前置校验)。
func UnitInstalled() bool {
	_, err := os.Stat(unitPath)
	return err == nil
}

// Uninstall 停用并删除服务。
func Uninstall() error {
	_ = runSystemctl("disable", "--now", ServiceName)
	_ = os.Remove(unitPath)
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
