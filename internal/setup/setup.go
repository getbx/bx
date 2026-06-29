// Package setup 实现 bx setup 的两块:生成最小配置、连通检测 brook 服务器。
package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/provision"
	"github.com/getbx/bx/internal/tunnel"
	"gopkg.in/yaml.v3"
)

// minimalConfig 是 setup 写出的最小可用配置(能被 config.Parse 读回)。
type minimalConfig struct {
	Server     string   `yaml:"server,omitempty"`     // 单传输
	Transports []string `yaml:"transports,omitempty"` // 多传输 bundle(>1 时写这个,接 S1 容灾)
	Global     bool     `yaml:"global"`
	Killswitch bool     `yaml:"killswitch"`
	OwnerUID   int      `yaml:"owner_uid,omitempty"` // sudo bx setup 的真实用户;0 省略(root-only)
}

// ownerUIDFromEnv 从 SUDO_UID 取业主 uid(sudo bx setup 的真实用户)。
// 非数字/空/<=0 → 0(无业主,控制面退回 root-only)。注入 getenv 便于免环境单测。
func ownerUIDFromEnv(getenv func(string) string) int {
	n, err := strconv.Atoi(strings.TrimSpace(getenv("SUDO_UID")))
	if err != nil || n <= 0 {
		return 0
	}
	return n
}

// WriteConfig 写最小配置(global+killswitch 默认开)。links 单条→server:,多条→transports:
// (有序优先级,接 S1 自动容灾)。文件已存在且 !force 则报错。
func WriteConfig(path string, links []string, force bool) error {
	if len(links) == 0 {
		return fmt.Errorf("setup: 无传输链接")
	}
	if !force {
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("配置已存在 %s(加 --force 覆盖)", path)
		}
	}
	cfg := minimalConfig{Global: true, Killswitch: true, OwnerUID: ownerUIDFromEnv(os.Getenv)}
	if len(links) == 1 {
		cfg.Server = links[0]
	} else {
		cfg.Transports = links
	}
	b, err := yaml.Marshal(cfg)
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

// buildProbeTunnel 按 server link scheme 选引擎建探测隧道,引擎派发经 tunnel.Kind(唯一真相源,
// 与 supervisor 起隧道同源)。brook 走内嵌 brook,其余(reality/hysteria2/trojan/ss/vmess)
// 走内嵌 sing-box。返回隧道、清理闭包(删 sing-box 临时配置)与错误。仅构造不启动。
func buildProbeTunnel(dataDir, link, probe string) (*tunnel.Tunnel, func(), error) {
	kind := tunnel.Kind(link)
	if kind == "brook" {
		brookPath, err := provision.EnsureBrook(dataDir, "", embedded.Brook(), embedded.BrookVersion())
		if err != nil {
			return nil, nil, err
		}
		t, err := tunnel.NewBrook(brookPath, link, probe, "") // setup 仅连通性探测,不需 HTTP 代理
		if err != nil {
			return nil, nil, err
		}
		return t, func() {}, nil
	}
	// 其余引擎都基于内嵌 sing-box。
	sbPath, err := provision.EnsureSingbox(dataDir, "", embedded.Singbox(), embedded.SingboxVersion(), "", "")
	if err != nil {
		return nil, nil, err
	}
	confPath := filepath.Join(dataDir, "sing-box-probe.json")
	cleanup := func() { os.Remove(confPath) }
	var t *tunnel.Tunnel
	switch kind { // 探测不需 HTTP 代理,httpAddr 传 ""
	case "reality":
		t, err = tunnel.NewReality(sbPath, link, probe, confPath, "")
	case "hysteria2":
		t, err = tunnel.NewHysteria2(sbPath, link, probe, confPath, "")
	case "trojan":
		t, err = tunnel.NewTrojan(sbPath, link, probe, confPath, "")
	case "shadowsocks":
		t, err = tunnel.NewShadowsocks(sbPath, link, probe, confPath, "")
	case "vmess":
		t, err = tunnel.NewVmess(sbPath, link, probe, confPath, "")
	default:
		// Kind 新增了引擎但这里没跟上:响亮报错,绝不静默回落 brook 误判连通。
		return nil, nil, fmt.Errorf("setup 探测不支持的传输引擎: %s", kind)
	}
	if err != nil {
		return nil, nil, err
	}
	return t, cleanup, nil
}

// ProbeServer 临时起隧道探测服务器连通,返回延迟 ms;不建 TUN。
// 按 server link scheme 选引擎(vless→reality / 其余→brook),内嵌引擎落盘到 dataDir。
func ProbeServer(dataDir, link, probe string, timeout time.Duration) (int64, error) {
	t, cleanup, err := buildProbeTunnel(dataDir, link, probe)
	if err != nil {
		return 0, err
	}
	defer cleanup()
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
