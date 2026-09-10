package guardian

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

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
	handler := doctorHandler(fakeDoctorFacts(t, &calls), "/etc/bx/config.yaml", 501, func() Status { return Status{} })
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
	handler := doctorHandler(fakeDoctorFacts(t, &calls), "/etc/bx/config.yaml", 501, func() Status { return Status{} })
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
	doctorHandler(nil, "/etc/bx/config.yaml", 501, func() Status { return Status{} })(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/doctor", nil), 501, true))
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
		probe: func(host string, port int) (probeOutcome, error) {
			return probeOutcome{Reachable: true, RTTMS: 42}, nil
		},
	})
	if ok.Probe == nil || ok.Probe.Status != "ok" || !strings.Contains(ok.Probe.Detail, "42ms") {
		t.Fatalf("通 = %+v", ok.Probe)
	}
	bad := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
		sock: deadSock(t),
		probe: func(host string, port int) (probeOutcome, error) {
			return probeOutcome{Reachable: false, Error: "connection refused"}, nil
		},
	})
	if bad.Probe == nil || bad.Probe.Status != "fail" || !strings.Contains(bad.Probe.Detail, "connection refused") {
		t.Fatalf("不通 = %+v", bad.Probe)
	}
	broken := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
		sock:  deadSock(t),
		probe: func(host string, port int) (probeOutcome, error) { return probeOutcome{}, context.DeadlineExceeded },
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
