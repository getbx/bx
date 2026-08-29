package guardian

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

// fake core:netns 集成台的 Core 替身(spec
// 2026-08-29-guardian-linux-adapter-design.md「集成台接法」)。
//
// 为什么不能用真 Core:supervisor 集成台靠 Options.BuildTunnel **进程内**注入
// 假隧道,而 Guardian 的 RunDaemon 走真 ExecCoreRunner spawn 一个**子进程**,
// 注入缝跨不过 exec —— netns 里没有外网,真 bx run 永远到不了 tunnel healthy。
// 替身因此要满足的是 Guardian 对 Core 的**全部观测面**,而且这份契约不许手写
// 镜像:TestFakeCoreSatisfiesTheRealHealthChecker 把它直接喂给生产的
// HealthChecker.Wait(真协议客户端 + 真 validateRuntimeState + 真 SOCKS 探针),
// 契约漂移在 darwin 单测里当场红,轮不到集成台。
//
// 观测面三件:
//   - 控制 socket(HTTP over unix):GET /v0/runtime 报健康 RuntimeState、
//     POST /v0/shutdown 触发退出(协作关闭路径靠它);
//   - loopback SOCKS5:健康检查的探针会真的 CONNECT 一次,应答成功即可,
//     不需要真的连出去(探针 CONNECT 完就 Close,零字节数据);
//   - 进程形状:集成台要把测试二进制拷成名为 `bx` 的文件、以 `bx run` spawn
//     (TestMain 钩子),让 scanRunningCores 的判据(basename=bx && argv[1]=run
//     && uid=0)认得出它 —— 否则「已有 Core 在跑 ⇒ Up 拒绝」那条断言无从谈起。

func fakeCoreRuntimeState(pid int, socksAddr string) supervisor.RuntimeState {
	return supervisor.RuntimeState{
		Version:         version.Version,
		PID:             pid,
		TunName:         "bxfake0",
		SocksAddr:       socksAddr,
		ServerBypass:    []string{"203.0.113.20/32"},
		TunnelHealthy:   true,
		DNSListening:    true,
		RoutesInstalled: true,
	}
}

// serveFakeCoreControl 在 sockPath 上服 Core 控制面的最小子集。
// 返回的 shutdown channel 在收到 /v0/shutdown 时关闭。
func serveFakeCoreControl(sockPath string, state func() supervisor.RuntimeState) (shutdown chan struct{}, stop func(), err error) {
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, nil, err
	}
	shutdown = make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("/v0/runtime", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(state())
	})
	var once bool
	mux.HandleFunc("/v0/shutdown", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		if !once {
			once = true
			close(shutdown)
		}
	})
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(listener) }()
	return shutdown, func() { _ = server.Close() }, nil
}

// serveFakeCoreSOCKSOn 在给定 listener 上服一个只会说「成功」的 SOCKS5:
// 读完 greeting 与 CONNECT 请求,应答 05 00(无认证)与 05 00 00 01
// 0.0.0.0:0(成功),然后挂住连接直到对端关闭。健康探针要的就是一次成功的
// CONNECT,不是真的通路。
func serveFakeCoreSOCKSOn(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			greeting := make([]byte, 2)
			if _, err := io.ReadFull(c, greeting); err != nil || greeting[0] != 5 {
				return
			}
			methods := make([]byte, int(greeting[1]))
			if _, err := io.ReadFull(c, methods); err != nil {
				return
			}
			if _, err := c.Write([]byte{5, 0}); err != nil {
				return
			}
			head := make([]byte, 4)
			if _, err := io.ReadFull(c, head); err != nil {
				return
			}
			var addrLen int
			switch head[3] {
			case 1:
				addrLen = 4
			case 3:
				one := make([]byte, 1)
				if _, err := io.ReadFull(c, one); err != nil {
					return
				}
				addrLen = int(one[0])
			case 4:
				addrLen = 16
			default:
				return
			}
			rest := make([]byte, addrLen+2)
			if _, err := io.ReadFull(c, rest); err != nil {
				return
			}
			if _, err := c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
				return
			}
			_, _ = io.Copy(io.Discard, c) // 挂住到对端关闭
		}(conn)
	}
}

func serveFakeCoreSOCKS(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go serveFakeCoreSOCKSOn(listener)
	return listener.Addr().String()
}

// 契约测试:假 Core 必须让**生产的** HealthChecker 判健康 —— 协议、字段、
// 探针三层都走真代码。这条绿着,集成台那边「Core 健康」就不可能因为替身
// 与观测面漂移而假红/假绿。
func TestFakeCoreSatisfiesTheRealHealthChecker(t *testing.T) {
	dir, err := os.MkdirTemp("", "bxfc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "core.sock")

	socksAddr := serveFakeCoreSOCKS(t)
	pid := os.Getpid()
	_, stop, err := serveFakeCoreControl(sockPath, func() supervisor.RuntimeState {
		return fakeCoreRuntimeState(pid, socksAddr)
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	checker := HealthChecker{SockPath: sockPath, PollInterval: 20 * time.Millisecond}
	state, err := checker.Wait(context.Background(), HealthTarget{
		Version: version.Version,
		PID:     pid,
		Timeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("真 HealthChecker 不认这个假 Core: %v", err)
	}
	if !state.TunnelHealthy || state.PID != pid {
		t.Fatalf("state = %+v", state)
	}
}

// 协作关闭那半的契约:/v0/shutdown 要真的触发退出信号 —— Down 路径靠它。
func TestFakeCoreShutdownEndpointFires(t *testing.T) {
	dir, err := os.MkdirTemp("", "bxfc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "core.sock")
	shutdown, stop, err := serveFakeCoreControl(sockPath, func() supervisor.RuntimeState {
		return fakeCoreRuntimeState(os.Getpid(), "127.0.0.1:1")
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)

	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sockPath)
		},
	}}
	resp, err := client.Post("http://local/v0/shutdown", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	select {
	case <-shutdown:
	case <-time.After(2 * time.Second):
		t.Fatal("/v0/shutdown 没有触发退出信号")
	}
}

// TestMain 的 fake-core 模式:集成台把本测试二进制拷成 `bx` 后以
// `bx run …` spawn,进程从这里进入替身主循环而不是跑测试。
//   - BX_GUARDIAN_FAKE_CORE=1 触发;
//   - sock 路径经 BX_GUARDIAN_FAKE_CORE_SOCK 传入(缺省用生产常量);
//   - /v0/shutdown 或 SIGTERM 之外,进程一直活着 —— 崩溃重启断言会直接 kill。
func TestMain(m *testing.M) {
	if os.Getenv("BX_GUARDIAN_FAKE_CORE") == "1" {
		runFakeCoreMain()
		return
	}
	os.Exit(m.Run())
}

func runFakeCoreMain() {
	sockPath := os.Getenv("BX_GUARDIAN_FAKE_CORE_SOCK")
	if sockPath == "" {
		sockPath = supervisor.SockPath
	}
	_ = os.Remove(sockPath)
	socksListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(3)
	}
	go serveFakeCoreSOCKSOn(socksListener)
	pid := os.Getpid()
	socksAddr := socksListener.Addr().String()
	shutdown, _, err := serveFakeCoreControl(sockPath, func() supervisor.RuntimeState {
		return fakeCoreRuntimeState(pid, socksAddr)
	})
	if err != nil {
		os.Exit(3)
	}
	<-shutdown // /v0/shutdown 到达即协作退出;崩溃重启断言直接 kill,不走这里
	os.Exit(0)
}
