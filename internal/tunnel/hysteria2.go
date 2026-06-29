package tunnel

import (
	"fmt"
	"os"
	"os/exec"
)

// hysteria2Factory 返回一个 RunnerFactory:从 hysteria2 链接生成 sing-box 配置写到 confPath,
// 再启动 `sing-box run -c confPath`。与 brook/reality 同构:bx 数据面只连本地 socks,不感知引擎。
func hysteria2Factory(singboxBin, link, confPath, httpAddr string) RunnerFactory {
	return func(socksAddr string) (Runner, error) {
		h, err := parseHysteria2Link(link)
		if err != nil {
			return nil, err
		}
		conf, err := h.singboxConfig(socksAddr, httpAddr)
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

// NewHysteria2 用 sing-box 二进制构造 hysteria2(QUIC)隧道,socks5 端口自动选取。
// 需内嵌 sing-box 含 with_quic tag(hysteria2 是 QUIC 协议)。probe/confPath/httpAddr 同 reality。
func NewHysteria2(singboxBin, link, probe, confPath, httpAddr string) (*Tunnel, error) {
	if _, err := parseHysteria2Link(link); err != nil {
		return nil, err // 早失败:链接非法不必等子进程
	}
	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return New(addr, hysteria2Factory(singboxBin, link, confPath, httpAddr), socks5Health(probe)), nil
}
