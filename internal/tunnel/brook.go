package tunnel

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"os/exec"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// execRunner 用 os/exec 跑 brook。
type execRunner struct {
	cmd    *exec.Cmd
	stderr *stderrSink
}

func (e *execRunner) Wait() error { return e.cmd.Wait() }

// RecentStderr 实现 stderrTailer:把子进程最近吐的几行交给诊断路径。
func (e *execRunner) RecentStderr() []string {
	if e.stderr == nil {
		return nil
	}
	return e.stderr.RecentStderr()
}

func (e *execRunner) Kill() error {
	if e.cmd.Process != nil {
		return e.cmd.Process.Kill()
	}
	return nil
}

// brookFactory 返回一个 RunnerFactory:启动 `brook connect -l link --socks5 addr`。
// httpAddr 非空时额外开一个 HTTP 代理(给只认 HTTP_PROXY 的应用用,如 tailscaled
// 的控制面——公司网封 controlplane 直连,它需经代理出去;mihomo 旧用 7890)。
func brookFactory(brookBin, link, httpAddr string) RunnerFactory {
	return func(socksAddr string) (Runner, error) {
		args := []string{"connect", "-l", link, "--socks5", socksAddr}
		if httpAddr != "" {
			args = append(args, "--http", httpAddr)
		}
		cmd := exec.Command(brookBin, args...)
		// link 就在 argv 上,brook 有可能把它回显进自己的日志——必须登记为 secret。
		runner, err := startWithStderr(cmd, "brook", link)
		if err != nil {
			return nil, fmt.Errorf("启动传输进程: %w", err)
		}
		return runner, nil
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
		ctx, cancel := context.WithTimeout(context.Background(), healthConnectBudget)
		defer cancel()
		conn, err := ctxDialer.DialContext(ctx, "tcp", probe)
		if err != nil {
			return 0, err
		}
		defer conn.Close()
		return proveBytesFlow(conn, probe, start)
	}
}

const (
	// healthConnectBudget 是「连得上」那一半的预算(与改动前同值)。
	healthConnectBudget = 8 * time.Second
	// healthFlowBudget 是「字节回得来」那一半的预算。
	//
	// 单独给一份而不是共用 8 秒:真机见过 4091ms 的链路(reality 跨境),
	// 连接就要 4 秒,再从同一份预算里挤一个来回必然抖。两段各 8 秒,最坏
	// 一次探测 16 秒 —— 健康检查 5 秒一拍,慢链路上就是「下一拍晚点到」,
	// 而不是「判成不健康」。
	healthFlowBudget = 8 * time.Second
)

// proveBytesFlow 证明**字节真的能从隧道对面回来**,并把「回来第一个字节」
// 的耗时作为延迟。
//
// **改这段之前先读这个**:改动前的判据是「SOCKS CONNECT 成功」,而那严格弱于
// 「数据流得动」。真机 2026-09-03 给了一个干净的对照 —— 同一台机器、同一时刻、
// 同一台服务器:
//
//	reality  (纯 TCP) → 4091ms   ← 真 RTT
//	hysteria2(QUIC)  → 0ms      ← 5/5 次
//
// **QUIC 开一条流是纯本地动作**:sing-box 流一建好就回 SOCKS 成功,服务器一个
// 字节都还没回。于是那台机器同时显示「隧道健康 0ms」与「UDP 加速档 152/152
// 回落(那台 UDP 服务器不健康)」—— 同一个端点,两条腿判出相反的结论。
// 更早还有两次同形状的记录(2026-06-30 的「health 绿、curl exit 28」、
// 2026-08-14 的「必定关闭的端口经 SOCKS5 一样通」),都只当排查技巧记下了。
//
// **判据是「有没有字节回来」,不是「TLS 握手成不成功」。** 后者会把一个不说 TLS
// 的探测目标判成隧道故障;而我们要证明的只有一件事:一个来回真的穿过了隧道。
// 所以握手的返回值被**刻意丢弃**,只看计数器。InsecureSkipVerify 同理 ——
// 这里不认证任何东西,证书对不对与「字节能不能过来」无关。
func proveBytesFlow(conn net.Conn, probe string, start time.Time) (int64, error) {
	if err := conn.SetDeadline(time.Now().Add(healthFlowBudget)); err != nil {
		return 0, err
	}
	counted := &firstByteConn{Conn: conn}
	host, _, err := net.SplitHostPort(probe)
	if err != nil {
		host = probe
	}
	//nolint:gosec // 不认证任何东西:这里只测「字节过不过得来」,见函数注释。
	_ = tls.Client(counted, &tls.Config{InsecureSkipVerify: true, ServerName: host}).
		HandshakeContext(context.Background())
	at, ok := counted.first()
	if !ok {
		return 0, fmt.Errorf("连得上 %s,但 %s 内没有任何字节从隧道回来 —— 隧道多半载不动数据"+
			"(若探测目标不会应答任何数据,换一个,默认 1.1.1.1:443)", probe, healthFlowBudget)
	}
	return at.Sub(start).Milliseconds(), nil
}

// firstByteConn 记下**第一个字节回来的时刻**。
//
// 延迟从此是「到第一个字节」而不是「到 CONNECT 返回」—— 后者在 QUIC 上量的是
// 本地开流,那正是 0ms 的来源。这会让健康链路上的读数比改动前大一个来回,
// 那是它本来就该有的数。
type firstByteConn struct {
	net.Conn
	mu   sync.Mutex
	at   time.Time
	seen bool
}

func (c *firstByteConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.mu.Lock()
		if !c.seen {
			c.at, c.seen = time.Now(), true
		}
		c.mu.Unlock()
	}
	return n, err
}

func (c *firstByteConn) first() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at, c.seen
}

// NewBrook 用真实 brook 二进制构造隧道,socks5 端口自动选取。
// probe 是健康检查目标(如 "1.1.1.1:443");httpAddr 非空时额外开 HTTP 代理(如 127.0.0.1:7890)。
func NewBrook(brookBin, link, probe, httpAddr string) (*Tunnel, error) {
	port, err := pickFreePort()
	if err != nil {
		return nil, err
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	return newWithHTTP(addr, httpAddr, brookFactory(brookBin, link, httpAddr), socks5Health(probe)), nil
}
