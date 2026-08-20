package supervisor

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/stats"
)

// GET /v0/apps 必须与改动类路由同一道 peer-cred 门。
//
// **理由是这个 GET 带副作用,而 socket 是 0666**(serveControlWithPathRecovery
// 里那句 os.Chmod(SockPath, 0o666))。没有门时,本机任何 uid 的任何进程一句
// curl --unix-socket 就能:① 读到实时的「哪个应用在走隧道/直连/被拦」清单
// (含应用名、连接数、字节量、命中的用户规则原文);② 靠 ?subscribe=1 无限期
// 地把采集打开(每次拉取都续 30 秒 TTL)。Guardian 那一层的 authorizeOwnerPeer
// 因此**不是真正的边界** —— 它下面一层是敞开的。
//
// 这条用 net.Pipe(不是 *net.UnixConn)构造「拿不到 peer-cred」那一态,
// 与 TestControlShutdownUsesMutationPeerAuthorization 同一手法:平台无关,
// 且正是非 darwin/linux 上 peerCredSupported=false 时的形状 —— 必须 fail-closed。
//
// **副作用那一半是承重的**:只断言 403 抓不到「先 Subscribe 再拒绝」这种写法,
// 而那种写法把本条要堵的第二个洞(无限期开采集)原样留着。
func TestControlAppsRequiresPeerAuthorization(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{}}
	at := NewAppTraffic(src, nil)
	h := newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, 501, at)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	request := httptest.NewRequest(http.MethodGet, "/v0/apps?subscribe=1", nil)
	request = request.WithContext(context.WithValue(request.Context(), ctxConnKey{}, serverConn))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("拿不到 peer-cred 的连接读 /v0/apps: status=%d,want %d;body=%s",
			response.Code, http.StatusForbidden, response.Body.String())
	}
	if _, subscribed, _ := at.Snapshot(); subscribed {
		t.Fatal("被拒的请求仍然把采集打开了 —— ?subscribe=1 的副作用必须发生在授权门之后")
	}
}

// 授权门必须在「没接线」之前:appTraffic == nil 时也不许把 501 这个事实
// 泄露给未授权的 peer(端点存在与否本身就是信息),更不许因为没接线就跳过门。
func TestControlAppsAuthorizesBeforeAnsweringUnwired(t *testing.T) {
	h := newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, 501, nil)

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	request := httptest.NewRequest(http.MethodGet, "/v0/apps", nil)
	request = request.WithContext(context.WithValue(request.Context(), ctxConnKey{}, serverConn))
	response := httptest.NewRecorder()
	h.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("未接线 + 未授权:status=%d,want %d", response.Code, http.StatusForbidden)
	}
}

// 走**真实 unix socket**(带生产那份 ConnContext)验两件事:
//   - 业主 uid(= 跑测试的这个 uid)照常 200 并真的订上 —— Guardian 是 root,
//     生产里走的正是这一支,门不能把唯一的消费方一起关在外面;
//   - 另一个 uid 配成业主时,本进程这个非 root、非业主的 peer 被拒。
//
// net.Pipe 那两条测不到这一半:它构造的是「拿不到 uid」,而这里要的是
// 「拿到了 uid、但不是业主」—— 两种拒绝的判据不同,合并会漏掉后者。
func TestControlAppsOverRealUnixSocketHonoursOwnerUID(t *testing.T) {
	if !peerCredSupported {
		t.Skip("本平台不取 peer-cred(恒 fail-closed),由 net.Pipe 那两条覆盖")
	}
	euid := uint32(os.Geteuid())

	t.Run("owner-allowed", func(t *testing.T) {
		src := &fakeAppSource{owners: map[appattr.PortKey]string{}}
		at := NewAppTraffic(src, nil)
		code := appsStatusOverUnixSocket(t, at, euid)
		if code != http.StatusOK {
			t.Fatalf("业主 uid=%d 读 /v0/apps: status=%d,want 200", euid, code)
		}
		if _, subscribed, _ := at.Snapshot(); !subscribed {
			t.Fatal("业主拉取带 subscribe=1 却没订上")
		}
	})

	t.Run("stranger-denied", func(t *testing.T) {
		if euid == 0 {
			t.Skip("以 root 跑测试:root 永远放行,构造不出「非 root 非业主」")
		}
		other := euid + 1
		if other == 0 {
			other = 1
		}
		src := &fakeAppSource{owners: map[appattr.PortKey]string{}}
		at := NewAppTraffic(src, nil)
		code := appsStatusOverUnixSocket(t, at, other)
		if code != http.StatusForbidden {
			t.Fatalf("uid=%d 冒充业主 %d 读 /v0/apps: status=%d,want 403", euid, other, code)
		}
		if _, subscribed, _ := at.Snapshot(); subscribed {
			t.Fatal("被拒的请求仍然把采集打开了")
		}
	})
}

// appsStatusOverUnixSocket 起一个临时 unix socket(ConnContext 与生产
// serveControlWithPathRecovery 逐字同款),GET /v0/apps?subscribe=1 并返回状态码。
func appsStatusOverUnixSocket(t *testing.T, at *AppTraffic, ownerUID uint32) int {
	t.Helper()
	// 不用 t.TempDir():它把测试名拼进路径,超过 unix socket 的 104 字节上限
	// (实测 bind: invalid argument)。短前缀的 MkdirTemp 才装得下。
	dir, err := os.MkdirTemp("", "bxapps")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sockPath := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: newControlMuxWithAppTraffic(&fakeControlEngine{}, func() stats.Report { return stats.Report{} }, nopMutator{}, ownerUID, at),
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return context.WithValue(ctx, ctxConnKey{}, conn)
		},
	}
	go srv.Serve(ln) //nolint:errcheck
	defer srv.Close()

	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://local/v0/apps?subscribe=1")
	if err != nil {
		t.Fatalf("GET /v0/apps: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}
