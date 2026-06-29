package tunnel

import (
	"fmt"
	"os"
	"os/exec"
)

// vmessFactory 返回一个 RunnerFactory:从 vmess 链接生成 sing-box 配置写到 confPath,
// 再启动 `sing-box run -c confPath`。与其它引擎同构:bx 数据面只连本地 socks。
func vmessFactory(singboxBin, link, confPath, httpAddr string) RunnerFactory {
	return func(socksAddr string) (Runner, error) {
		v, err := parseVmessLink(link)
		if err != nil {
			return nil, err
		}
		conf, err := v.singboxConfig(socksAddr, httpAddr)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(confPath, conf, 0o600); err != nil {
			return nil, fmt.Errorf("写 sing-box 配置: %w", err)
		}
		cmd := exec.Command(singboxBin, "run", "-c", confPath)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("启动 sing-box: %w", err)
		}
		return &execRunner{cmd: cmd}, nil
	}
}

// VmessHost 从 vmess:// 链接解出服务器主机(供 supervisor 加路由 bypass 防环)。
// vmess 链接是 base64-JSON,url.Parse 取不到 host,故需走 parseVmessLink。
func VmessHost(link string) (string, error) {
	v, err := parseVmessLink(link)
	if err != nil {
		return "", err
	}
	return v.Add, nil
}

// NewVmess 用 sing-box 二进制构造 vmess 隧道,socks5 端口自动选取。
func NewVmess(singboxBin, link, probe, confPath, httpAddr string) (*Tunnel, error) {
	if _, err := parseVmessLink(link); err != nil {
		return nil, err
	}
	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return New(addr, vmessFactory(singboxBin, link, confPath, httpAddr), socks5Health(probe)), nil
}
