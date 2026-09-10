package guardian

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/doctor"
)

func doctorTestConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	// **链接必须是真能解出 host:port 的形状**:brook 的链接把端点放在 server
	// 查询参数里(tunnel.ServerHost 的 brook 分支就认这一个),写成
	// `brook://example.com:9999?...` 解不出来 —— 那样探测那三条分支全会落进
	// 「读不出服务器地址」,测试看着绿而三种结局一条都没验到。
	body := "server: 'brook://server?server=example.com%3A9999&password=x'\nglobal: true\nrules:\n    - direct:\n        - '*.steamcontent.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// deadSock 给采集一个**必定拨不通**的控制 socket 路径。
//
// 少了它,这些测试会去拨 supervisor.SockPath —— 开发机上 bx 正跑着,那个
// socket 真的在,于是「测试环境没有 Core socket」那条断言在有人开着保护的
// 机器上转红、在别处转绿。**一个会偶发红的闸门比没有闸门更糟**,它训练人去
// 重跑;而这里要守的性质(拨不通就如实填 StatusSocketErr)与机器上有没有跑
// bx 毫无关系。
func deadSock(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "core.sock")
}

func fakeDoctorFacts(t *testing.T, calls *int) DoctorFactsFunc {
	t.Helper()
	return func(ctx context.Context, configPath string, status Status) doctor.Facts {
		*calls++
		return doctor.Facts{
			Version: "test", ConfigPath: configPath,
			Config: doctor.FileFact{ReadErr: "open " + configPath + ": no such file"},
		}
	}
}

// 与 /v1/rules、/v1/logs 同一道门。
func TestDoctorEndpointRequiresOwnerOrRoot(t *testing.T) {
	calls := 0
	handler := doctorHandler(fakeDoctorFacts(t, &calls), "/etc/bx/config.yaml", 501, func() Status { return Status{} }, doctorTimeout)
	for _, tc := range []struct {
		name string
		uid  uint32
		got  bool
		want int
	}{
		{"owner", 501, true, http.StatusOK},
		{"root", 0, true, http.StatusOK},
		{"别的用户", 502, true, http.StatusForbidden},
		{"拿不到对端凭据", 0, false, http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/doctor", nil), tc.uid, tc.got))
			if w.Code != tc.want {
				t.Fatalf("状态码 = %d, want %d", w.Code, tc.want)
			}
		})
	}
	if calls != 2 {
		t.Fatalf("被拒的请求不该触发采集(它会出网探测),实际采集了 %d 次", calls)
	}
}

// 应答就是 doctor.Report 的 JSON —— 与 bx doctor --json 同形状,菜单与 agent 按名字取。
func TestDoctorEndpointReturnsTheJudgedReport(t *testing.T) {
	calls := 0
	handler := doctorHandler(fakeDoctorFacts(t, &calls), "/etc/bx/config.yaml", 501, func() Status { return Status{} }, doctorTimeout)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/doctor", nil), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d %s", w.Code, w.Body.String())
	}
	var rep doctor.Report
	if err := json.Unmarshal(w.Body.Bytes(), &rep); err != nil {
		t.Fatalf("解不出 doctor.Report:%v %s", err, w.Body.String())
	}
	if rep.Kind != "client" || rep.OK {
		t.Fatalf("report = %+v(缺配置不该 ok)", rep)
	}
	var names []string
	for _, c := range rep.Checks {
		names = append(names, c.Name)
	}
	if !strings.Contains(strings.Join(names, " "), "config_readable") {
		t.Fatalf("checks = %v", names)
	}
	w = httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/doctor", nil), 501, true))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", w.Code)
	}
}

func TestDoctorEndpointReportsWhenNotWired(t *testing.T) {
	w := httptest.NewRecorder()
	doctorHandler(nil, "/etc/bx/config.yaml", 501, func() Status { return Status{} }, doctorTimeout)(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/doctor", nil), 501, true))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("未接线 = %d, want 501", w.Code)
	}
}

// 生产那份采集:读得到配置时走长路径(解析、体检、服务行、socket、Guardian 事实、平台);
// 读不到时 GuardianRules 明说「Guardian 自己也读不到」。**探测被注入的 nil 掉了**——
// 单测不出网。
func TestCollectDoctorFactsWalksTheLongPathWithoutProbing(t *testing.T) {
	path := doctorTestConfig(t)
	f := collectDoctorFactsWith(context.Background(), path, Status{DNSState: "managed", DNSManaged: true},
		doctorCollectorDeps{
			sock:     deadSock(t),
			probe:    nil,
			platform: func(context.Context) []doctor.Check { return []doctor.Check{{Name: "terminal_proxy", Status: "info"}} },
		})
	if f.ConfigPath != path || f.Config.ReadErr != "" || !f.Config.Mode0600 {
		t.Fatalf("配置事实 = %+v", f.Config)
	}
	if f.Parsed == nil || f.Parsed.Server == "" {
		t.Fatalf("要解析出 server:%+v", f.Parsed)
	}
	if f.RuleReview == nil {
		t.Fatal("读到配置就要算规则体检")
	}
	if f.Probe != nil {
		t.Fatal("注入 nil 探测器时不该有 probe 事实(单测不出网)")
	}
	if len(f.Service) != 3 {
		t.Fatalf("服务三行 = %+v", f.Service)
	}
	if f.Guardian == nil || f.Guardian.DNS.State != "managed" {
		t.Fatalf("Guardian 事实 = %+v", f.Guardian)
	}
	if len(f.Platform) != 1 || f.Platform[0].Name != "terminal_proxy" {
		t.Fatalf("平台检查 = %+v", f.Platform)
	}
	if f.StatusSocketErr == "" {
		t.Fatal("拨不通的 socket 要如实填 StatusSocketErr")
	}
	missing := collectDoctorFactsWith(context.Background(), filepath.Join(t.TempDir(), "nope.yaml"), Status{},
		doctorCollectorDeps{sock: deadSock(t)})
	if missing.Config.ReadErr == "" || missing.GuardianRules.Err == "" {
		t.Fatalf("配置不存在时要如实报 ReadErr 与 GuardianRules.Err:%+v %+v", missing.Config, missing.GuardianRules)
	}
}

// 探测器返回什么就是什么(通/不通/超时各成一条 check),名字固定 probe。
func TestCollectDoctorFactsProbeOutcomes(t *testing.T) {
	path := doctorTestConfig(t)
	ok := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
		sock: deadSock(t),
		probe: func(_ context.Context, host string, port int) (probeOutcome, error) {
			return probeOutcome{Reachable: true, RTTMS: 42}, nil
		},
	})
	if ok.Probe == nil || ok.Probe.Status != "ok" || !strings.Contains(ok.Probe.Detail, "42ms") {
		t.Fatalf("通 = %+v", ok.Probe)
	}
	bad := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
		sock: deadSock(t),
		probe: func(_ context.Context, host string, port int) (probeOutcome, error) {
			return probeOutcome{Reachable: false, Error: "connection refused"}, nil
		},
	})
	if bad.Probe == nil || bad.Probe.Status != "fail" || !strings.Contains(bad.Probe.Detail, "connection refused") {
		t.Fatalf("不通 = %+v", bad.Probe)
	}
	broken := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
		sock: deadSock(t),
		probe: func(_ context.Context, host string, port int) (probeOutcome, error) {
			return probeOutcome{}, context.DeadlineExceeded
		},
	})
	if broken.Probe == nil || broken.Probe.Status != "warn" {
		t.Fatalf("探不出来(不是不通)= %+v", broken.Probe)
	}
}

// 接线:NewLocalAPI 真的挂上 /v1/doctor,门是 owner,采集函数是 options 里那个。
func TestNewLocalAPIWiresDoctorEndpoint(t *testing.T) {
	calls := 0
	api := NewLocalAPI(&fakeController{}, LocalAPIOptions{OwnerUID: 501, ConfigPath: "/etc/bx/config.yaml", DoctorFacts: fakeDoctorFacts(t, &calls)})
	w := httptest.NewRecorder()
	api.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/doctor", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("无凭据 = %d", w.Code)
	}
	w = httptest.NewRecorder()
	api.ServeHTTP(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/doctor", nil), 501, true))
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("经 NewLocalAPI = %d calls=%d body=%s", w.Code, calls, w.Body.String())
	}
}

func TestDaemonWiresDoctorFacts(t *testing.T) {
	got := localAPIOptionsFor(DaemonOptions{ConfigPath: "/etc/bx/config.yaml", LocalAPIOwnerUID: 501})
	if got.DoctorFacts == nil {
		t.Fatal("LocalAPI 没接 doctor 采集")
	}
	if reflect.ValueOf(got.DoctorFacts).Pointer() != reflect.ValueOf(DoctorFactsFunc(collectDoctorFacts)).Pointer() {
		t.Fatal("接上的不是生产那份 collectDoctorFacts")
	}
}

func TestDoctorCapabilityIsDeclaredAndPinned(t *testing.T) {
	if CapabilityDoctor != "doctor" {
		t.Fatalf("CapabilityDoctor 的值变了(%q):菜单 DiagnosticsModel.swift 按字面量 \"doctor\" 门控", CapabilityDoctor)
	}
	for _, c := range GuardianCapabilities() {
		if c == CapabilityDoctor {
			return
		}
	}
	t.Fatalf("能力清单里没有 %q:%v", CapabilityDoctor, GuardianCapabilities())
}

// **一份预算,每一个会等的依赖都要吃到它。**
//
// 这条守的不是「handler 建了个 10 秒的 ctx」(那句话建一次就永远成立),而是
// 「采集里每一个原语真的拿到了它」—— 复审抓到的正是后者:ctx 建了,却只传给了
// 平台检查那一个,而探测(客户端超时 12 秒,比整轮预算还长)与问 launchd
// (`GuardianActive` 用 context.Background(),压根没有超时)各走各的钟。
// 于是那个常量只是一句注释,而一次卡住的 `launchctl` 会坐在 daemon 的关机路径上。
//
// 判据是**看到的截止时刻**而不是「有没有截止时刻」:后者对一个自己新建
// `context.WithTimeout` 的依赖照样成立,而那正是「各走各的钟」的写法。
func TestCollectDoctorFactsGivesEveryDepTheSameDeadline(t *testing.T) {
	seen := map[string]time.Time{}
	record := func(name string, ctx context.Context) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Errorf("%s 拿到的 ctx 没有截止时刻 —— 整轮那份预算没传到它这里", name)
			return
		}
		seen[name] = deadline
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	want, _ := ctx.Deadline()
	collectDoctorFactsWith(ctx, doctorTestConfig(t), Status{}, doctorCollectorDeps{
		sock: deadSock(t),
		probe: func(ctx context.Context, host string, port int) (probeOutcome, error) {
			record("probe", ctx)
			return probeOutcome{Reachable: true, RTTMS: 1}, nil
		},
		platform: func(ctx context.Context) []doctor.Check { record("platform", ctx); return nil },
		service:  func(ctx context.Context) []doctor.Check { record("service", ctx); return nil },
		dial: func(ctx context.Context, path string) error {
			record("dial", ctx)
			return errors.New("dead")
		},
	})
	for _, name := range []string{"probe", "platform", "service", "dial"} {
		got, ok := seen[name]
		if !ok {
			t.Fatalf("%s 这个依赖压根没被调用,守卫读不懂现在的采集流程", name)
		}
		if !got.Equal(want) {
			t.Errorf("%s 的截止时刻 %v ≠ 整轮那份 %v —— 它在用自己的钟", name, got, want)
		}
	}
}

// 卡住的依赖不许让 handler 活过它的预算 —— daemon 的 Shutdown 要等在飞的 handler。
func TestDoctorHandlerDoesNotOutliveItsBudget(t *testing.T) {
	collect := func(ctx context.Context, configPath string, status Status) doctor.Facts {
		<-ctx.Done() // 一个只会在预算到期时才回来的依赖
		return doctor.Facts{Version: "test", ConfigPath: configPath}
	}
	handler := doctorHandler(collect, "/etc/bx/config.yaml", 501, func() Status { return Status{} }, 50*time.Millisecond)
	done := make(chan int, 1)
	go func() {
		w := httptest.NewRecorder()
		handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/doctor", nil), 501, true))
		done <- w.Code
	}()
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("预算到期仍应给出一份如实的报告,状态码 = %d", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler 活过了它的预算 —— 采集没有吃到那个 ctx")
	}
}

// 生产接线给的是真预算,不是某个随手写的数(budget 是参数,写错不会有人报错)。
func TestNewLocalAPIGivesDoctorTheRealBudget(t *testing.T) {
	var left time.Duration
	api := NewLocalAPI(&fakeController{}, LocalAPIOptions{
		OwnerUID: 501, ConfigPath: "/etc/bx/config.yaml",
		DoctorFacts: func(ctx context.Context, configPath string, status Status) doctor.Facts {
			if deadline, ok := ctx.Deadline(); ok {
				left = time.Until(deadline)
			}
			return doctor.Facts{Version: "test", ConfigPath: configPath}
		},
	})
	api.ServeHTTP(httptest.NewRecorder(), withPeer(httptest.NewRequest(http.MethodGet, "/v1/doctor", nil), 501, true))
	if left <= doctorTimeout-2*time.Second || left > doctorTimeout {
		t.Fatalf("采集拿到的剩余预算 %v,与 doctorTimeout %v 对不上", left, doctorTimeout)
	}
}

// blackHoleControlSocket 起一个**会 accept、但从不应答**的 unix socket。
//
// 它比「一个根本不存在的路径」强的地方正是这条守卫要的:拨不通的路径让每个原语
// 都立刻返回,于是「吃不吃 ctx」在输出上完全一样 —— 那是「测试输入让待守属性不
// 可见」的老形状。连得上但永远不答,才逼得出「谁在用自己的钟」。
func blackHoleControlSocket(t *testing.T) string {
	t.Helper()
	// **不用 t.TempDir()**:macOS 上它给的路径接近 120 字节,而 unix socket 的
	// sun_path 只有 104 —— bind 会以 EINVAL 失败,于是这条守卫会以 SKIP 静默
	// 消失。一条在最需要它的时候恰好不可达的守卫,与没有这条守卫完全一样。
	// (deadSock 那个 helper 只造路径、从不 bind,所以不吃这个限制。)
	dir, err := os.MkdirTemp("", "bxdoc")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "core.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("起不了 unix socket:%v", err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		var held []net.Conn
		for {
			conn, err := ln.Accept()
			if err != nil {
				for _, c := range held {
					_ = c.Close()
				}
				return
			}
			held = append(held, conn) // 收下,不读不写 —— 对端只能等
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		<-done
	})
	return path
}

// liveDoctorDeps 那几个**生产**闭包也必须吃调用方给的 ctx。
//
// 既有的 TestCollectDoctorFactsGivesEveryDepTheSameDeadline 只证明采集**把** ctx
// 递了下去 —— 它注入的是测试自己的闭包,而生产那份接过 ctx 之后拿它做什么,
// 那条守卫一个字都没说。这正是「守卫钉住的是缺陷旁边的东西」:probe 那个闭包
// 一度把 ctx 收下、转手用 context.Background() 去拨,两条测试全绿,而 10 秒预算
// 在真机上被一个 12 秒的客户端时钟顶穿。
//
// 判据是**行为**:给一个 250 毫秒就到期的 ctx,对着一个会 accept 但永不应答的
// socket,闭包必须在这份预算(加一点余量)之内回来。用自己的钟就回不来。
func TestLiveDoctorDepsForwardTheCtxTheyAreHanded(t *testing.T) {
	sock := blackHoleControlSocket(t)
	deps := liveDoctorDeps(sock)
	if deps.sockPath() != sock {
		t.Fatalf("liveDoctorDeps 没把 sock 收下:%q", deps.sockPath())
	}

	const budget = 250 * time.Millisecond
	// 余量给得比预算大得多、又远小于每个原语自己的钟(探测 12 秒、拨号 500 毫秒
	// 之上还有 HTTP 那层):落在中间才既不假红也不假绿。
	const margin = 3 * time.Second

	t.Run("probe", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		started := time.Now()
		_, err := deps.probe(ctx, "203.0.113.9", 443)
		elapsed := time.Since(started)
		if elapsed > budget+margin {
			t.Fatalf("探测用了 %v(预算 %v)—— 它在用自己的钟,不是调用方给的那份", elapsed, budget)
		}
		// **「早早返回」还不够**:一个立刻返回一个假答案的实现也满足上一条。
		// 回来的必须是「预算到期」这件事本身。
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("探测返回的错误是 %v,而它该是那份预算到期", err)
		}
	})

	t.Run("service", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()
		started := time.Now()
		deps.service(ctx)
		if elapsed := time.Since(started); elapsed > budget+margin {
			t.Fatalf("问服务用了 %v(预算 %v)—— launchctl 那一跳没吃这份 ctx", elapsed, budget)
		}
	})

	t.Run("dial", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		cancel() // 已经到期:拨号必须当场认账,不许再自己等 500 毫秒
		started := time.Now()
		err := deps.dial(ctx, deps.sockPath())
		if elapsed := time.Since(started); elapsed > margin {
			t.Fatalf("拨控制 socket 用了 %v —— 它没看这份已经取消的 ctx", elapsed)
		}
		if err == nil {
			t.Fatal("ctx 已经取消,拨号却报成功")
		}
	})
}
