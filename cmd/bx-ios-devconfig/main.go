// bx-ios-devconfig 是**开发工具**(不随 bx 发布):以 root 读 /etc/bx/config.yaml,把当前服务器
// 拼成 iOS 扩展要的 libbox 配置,写进 apps/ios/Dev/(gitignored),文件 0600 并 chown 给调用
// sudo 的人。链接是凭据:它只落在这个被忽略的目录里,不进仓库、不打到终端。
//
//	sudo ./bx-ios-devconfig --out apps/ios/Dev
//
// 产物:libbox-config.json(真服务器)、libbox-config-deadserver.json(代理指向 192.0.2.1,
// 验 fail-closed 用)、两个 rule-set 文件、expect.json(期望的出口地址,探测拿它比对)。
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/mobileconfig"
	"github.com/getbx/bx/internal/singboxrules"
	"github.com/getbx/bx/internal/tunnel"
)

// deadServerAddr 是文档保留段(RFC 5737),路由上不存在 —— 代理连它必然失败。
const deadServerAddr = "192.0.2.1"

func main() {
	cfgPath := flag.String("config", "/etc/bx/config.yaml", "bx config to read")
	outDir := flag.String("out", "", "output directory (apps/ios/Dev)")
	flag.Parse()
	if err := run(*cfgPath, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, "bx-ios-devconfig:", err)
		os.Exit(1)
	}
}

func run(cfgPath, outDir string) error {
	if outDir == "" {
		return errors.New("--out is required")
	}
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return err
	}
	proxy, err := tunnel.SingboxOutbound(cfg.Server, singboxrules.OutboundProxy)
	if err != nil {
		return fmt.Errorf("current server: %w", err)
	}
	lists := singboxrules.Lists{
		ChinaDomain: strings.Split(string(embedded.ChinaDomain()), "\n"),
		ChinaCIDR:   strings.Split(string(embedded.ChinaCIDR()), "\n"),
	}
	live, err := mobileconfig.Build(cfg, lists, proxy)
	if err != nil {
		return err
	}
	dead, err := mobileconfig.Build(cfg, lists, deadServer(proxy))
	if err != nil {
		return err
	}
	expect := map[string]string{"server_host": fmt.Sprint(proxy["server"])}
	if a, err := netip.ParseAddr(expect["server_host"]); err == nil && a.Is4() {
		expect["exit_ip"] = a.String()
	}
	expectJSON, _ := json.Marshal(expect)

	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return err
	}
	files := map[string][]byte{
		"libbox-config.json":            live.Config,
		"libbox-config-deadserver.json": dead.Config,
		"expect.json":                   expectJSON,
	}
	for name, body := range live.RuleSets {
		files[name] = body
	}
	// explain 那一半(bxkit)要的是 bx 自己的意图与原始列表,不是翻译后的 sing-box 规则 ——
	// 它要按 route.Explain 说出「命中了你哪一行」。policy.json 不含服务器链接。
	policyJSON, err := json.Marshal(policyOf(cfg))
	if err != nil {
		return err
	}
	files["policy.json"] = policyJSON
	files["china_domain.txt"] = embedded.ChinaDomain()
	files["china_cidr.txt"] = embedded.ChinaCIDR()
	uid, gid := sudoOwner()
	for name, body := range files {
		p := filepath.Join(outDir, name)
		if err := os.WriteFile(p, body, 0o600); err != nil {
			return err
		}
		if uid >= 0 {
			if err := os.Chown(p, uid, gid); err != nil {
				return err
			}
		}
	}
	if uid >= 0 {
		_ = os.Chown(outDir, uid, gid)
	}
	fmt.Printf("wrote %d files to %s (server host %s)\n", len(files), outDir, expect["server_host"])
	return nil
}

// phonePolicy 是 bxkit.Explain 吃的那份意图:只有规则与 global,没有链接、没有具名出口。
type phonePolicy struct {
	Global bool     `json:"global"`
	Direct []string `json:"direct"`
	Proxy  []string `json:"proxy"`
}

func policyOf(cfg *config.Config) phonePolicy {
	p := phonePolicy{Global: cfg.Global, Direct: []string{}, Proxy: []string{}}
	for _, r := range cfg.Rules {
		p.Direct = append(p.Direct, r.Direct...)
		p.Proxy = append(p.Proxy, r.Proxy...)
	}
	return p
}

// deadServer 复制出站,只把地址换成必然不可达的那个;其余握手参数原样,所以失败只可能来自
// 「连不上」,不会是配置写错。
func deadServer(proxy map[string]any) map[string]any {
	out := make(map[string]any, len(proxy))
	for k, v := range proxy {
		out[k] = v
	}
	out["server"] = deadServerAddr
	out["server_port"] = 443
	return out
}

// sudoOwner 是调用 sudo 的那个人;不是经 sudo 跑的就不改属主。
func sudoOwner() (int, int) {
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil {
		return -1, -1
	}
	return uid, gid
}
