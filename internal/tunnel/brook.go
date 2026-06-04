package tunnel

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"time"

	"golang.org/x/net/proxy"
)

// execRunner 用 os/exec 跑 brook。
type execRunner struct{ cmd *exec.Cmd }

func (e *execRunner) Wait() error { return e.cmd.Wait() }
func (e *execRunner) Kill() error {
	if e.cmd.Process != nil {
		return e.cmd.Process.Kill()
	}
	return nil
}

// brookFactory 返回一个 RunnerFactory:启动 `brook connect -l link --socks5 addr`。
func brookFactory(brookBin, link string) RunnerFactory {
	return func(socksAddr string) (Runner, error) {
		cmd := exec.Command(brookBin, "connect", "-l", link, "--socks5", socksAddr)
		if err := cmd.Start(); err != nil {
			return nil, fmt.Errorf("启动 brook: %w", err)
		}
		return &execRunner{cmd: cmd}, nil
	}
}

// socks5Health 经 socks5 拨号到 probe 目标,返回连接耗时(毫秒)。
func socks5Health(probe string) HealthCheck {
	return func(socksAddr string) (int64, error) {
		d, err := proxy.SOCKS5("tcp", socksAddr, nil, &net.Dialer{Timeout: 5 * time.Second})
		if err != nil {
			return 0, err
		}
		ctxDialer, ok := d.(proxy.ContextDialer)
		if !ok {
			return 0, fmt.Errorf("dialer 不支持 context")
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		conn, err := ctxDialer.DialContext(ctx, "tcp", probe)
		if err != nil {
			return 0, err
		}
		conn.Close()
		return time.Since(start).Milliseconds(), nil
	}
}

// NewBrook 用真实 brook 二进制构造隧道,socks5 端口自动选取。
// probe 是健康检查目标(如 "1.1.1.1:443")。
func NewBrook(brookBin, link, probe string) (*Tunnel, error) {
	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return New(addr, brookFactory(brookBin, link), socks5Health(probe)), nil
}
