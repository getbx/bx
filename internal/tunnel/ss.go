package tunnel

import (
	"fmt"
	"os"
	"os/exec"
)

// ssFactory 返回一个 RunnerFactory:从 ss 链接生成 sing-box 配置写到 confPath,再启动
// `sing-box run -c confPath`。与其它引擎同构:bx 数据面只连本地 socks。
func ssFactory(singboxBin, link, confPath, httpAddr string) RunnerFactory {
	return func(socksAddr string) (Runner, error) {
		ss, err := parseSSLink(link)
		if err != nil {
			return nil, err
		}
		conf, err := ss.singboxConfig(socksAddr, httpAddr)
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

// SSHost 从 ss:// 链接解出服务器主机(供 supervisor 加路由 bypass 防环)。
// ss:// 的 authority 是 base64,url.Parse 取不到 host,故需走 parseSSLink。
func SSHost(link string) (string, error) {
	ss, err := parseSSLink(link)
	if err != nil {
		return "", err
	}
	return ss.Host, nil
}

// NewShadowsocks 用 sing-box 二进制构造 shadowsocks 隧道,socks5 端口自动选取。
func NewShadowsocks(singboxBin, link, probe, confPath, httpAddr string) (*Tunnel, error) {
	if _, err := parseSSLink(link); err != nil {
		return nil, err
	}
	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return New(addr, ssFactory(singboxBin, link, confPath, httpAddr), socks5Health(probe)), nil
}
