// Package setup 实现 bx setup 的两块:生成最小配置、连通检测 brook 服务器。
package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/getbx/bx/internal/tunnel"
	"gopkg.in/yaml.v3"
)

// minimalConfig 是 setup 写出的最小可用配置(能被 config.Parse 读回)。
type minimalConfig struct {
	Server     string `yaml:"server"`
	Global     bool   `yaml:"global"`
	Killswitch bool   `yaml:"killswitch"`
}

// WriteConfig 写最小配置(global+killswitch 默认开)。文件已存在且 !force 则报错。
func WriteConfig(path, link string, force bool) error {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("配置已存在 %s(加 --force 覆盖)", path)
		}
	}
	b, err := yaml.Marshal(minimalConfig{Server: link, Global: true, Killswitch: true})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

// ProbeServer 临时起 brook 隧道探测服务器连通,返回延迟 ms;不建 TUN。
func ProbeServer(brookPath, brookLink, probe string, timeout time.Duration) (int64, error) {
	t, err := tunnel.NewBrook(brookPath, brookLink, probe, "") // setup 仅连通性探测,不需 HTTP 代理
	if err != nil {
		return 0, err
	}
	t.Start()
	defer t.Stop()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			if last := t.Stats().LastError; last != "" {
				return 0, fmt.Errorf("%s 内未连通(最近错误: %s)", timeout, last)
			}
			return 0, fmt.Errorf("%s 内未连通(检查 server/密码/网络)", timeout)
		case <-tick.C:
			if t.Healthy() {
				return t.Stats().LatencyMS, nil
			}
		}
	}
}
