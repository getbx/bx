// Package install 生成并安装 bx 的 systemd 服务(开机自启)。
package install

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const (
	ServiceName = "bx.service"
	unitPath    = "/etc/systemd/system/bx.service"
)

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
