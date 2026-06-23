package tunnel

import (
	"fmt"
	"os"
	"os/exec"
)

// realityFactory 返回一个 RunnerFactory:从 vless 链接生成 sing-box 配置写到 confPath,
// 再启动 `sing-box run -c confPath`(socks 入站监听传入的 socksAddr)。
// 与 brookFactory 同构:bx 数据面只连本地 socks,不感知引擎。
func realityFactory(singboxBin, link, confPath, httpAddr string) RunnerFactory {
	return func(socksAddr string) (Runner, error) {
		v, err := parseVlessLink(link)
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

// NewReality 用 sing-box 二进制构造 REALITY 隧道,socks5 端口自动选取。
// probe 同 brook(如 "1.1.1.1:443");confPath 是生成的 sing-box 配置落盘路径;
// httpAddr 非空时额外开 HTTP 代理(如 127.0.0.1:7890,给 tailscaled 控制面)。
func NewReality(singboxBin, link, probe, confPath, httpAddr string) (*Tunnel, error) {
	if _, err := parseVlessLink(link); err != nil {
		return nil, err // 早失败:链接非法不必等子进程
	}
	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return New(addr, realityFactory(singboxBin, link, confPath, httpAddr), socks5Health(probe)), nil
}
