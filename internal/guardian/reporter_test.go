package guardian

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/report"
)

func testReporter(t *testing.T, endpoint string, protected *atomic.Bool) *Reporter {
	t.Helper()
	dir := t.TempDir()
	r := NewReporter(ReporterOptions{
		Enabled:   true,
		Endpoint:  endpoint,
		Dir:       filepath.Join(dir, "reports"),
		InstallID: strings.Repeat("ab", 16),
		Version:   "v0.4.16",
		OS:        "darwin",
		Protected: func() bool { return protected.Load() },
		Doctor: func(ctx context.Context) doctor.Report {
			return doctor.Report{Checks: []doctor.Check{{Name: "tunnel", Status: "fail", Detail: "dial 203.0.113.92:443 timeout"}}}
		},
		LogTail:     func(n int) []string { return []string{"guardian_probe ip=203.0.113.92"} },
		HTTPTimeout: 2 * time.Second,
	})
	return r
}

// Record 攒包、脱敏、落盘;**保护没开就只入队不发**(直连 workers.dev 的 SNI 就是一条「这台机器
// 装了 bx」的明文证据);保护开着时 202 标 sent、4xx 标 rejected、5xx 留着重试。
func TestReporterSendsOnlyWhileProtectedAndHonoursTheCollectorsAnswer(t *testing.T) {
	var code atomic.Int32
	code.Store(202)
	var got atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.Add(1)
		if strings.Contains(r.Header.Get("Content-Type"), "json") == false {
			t.Errorf("content-type = %q", r.Header.Get("Content-Type"))
		}
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()
	var protected atomic.Bool
	r := testReporter(t, srv.URL+"/v1/reports", &protected)

	r.Record("attention:core_health_failed", report.Failure{Code: "core_health_failed"})
	pending, _ := r.store.Pending()
	if len(pending) != 1 {
		t.Fatalf("one pending report expected, got %v", pending)
	}
	raw, _ := r.store.Read(pending[0].Name)
	if strings.Contains(string(raw), "203.0.113.92") {
		t.Fatalf("stored report must be redacted:\n%s", raw)
	}
	if !strings.Contains(string(raw), `"signature": "attention:core_health_failed"`) {
		t.Fatalf("stored report must carry the signature:\n%s", raw)
	}

	// 保护关着:flush 一次,一个请求都不发。
	r.flush(context.Background())
	if got.Load() != 0 {
		t.Fatal("must not send while protection is off")
	}
	// 保护开着:202 → sent。
	protected.Store(true)
	r.flush(context.Background())
	if got.Load() != 1 {
		t.Fatalf("expected one send, got %d", got.Load())
	}
	if st := stateOf(t, r, "attention_core_health_failed"); st != "sent" {
		t.Fatalf("202 must mark sent, got %q", st)
	}
	// 4xx → rejected,不重试。
	code.Store(400)
	r.Record("update:rolled_back", report.Failure{Code: "rolled_back"})
	r.flush(context.Background())
	if st := stateOf(t, r, "update_rolled_back"); st != "rejected" {
		t.Fatalf("4xx must mark rejected, got %q", st)
	}
	// 5xx → 留着。
	code.Store(503)
	r.Record("recovery:transport_health:transport_unavailable", report.Failure{Code: "recovery_failed"})
	r.flush(context.Background())
	if st := stateOf(t, r, "recovery_transport_health_transport_unavailable"); st != "pending" {
		t.Fatalf("5xx must keep the report pending, got %q", st)
	}
}

// 关掉(reports: off)时 Record 什么都不做,目录里也不留东西。
func TestReporterDisabledStoresNothing(t *testing.T) {
	r := NewReporter(ReporterOptions{Enabled: false, Dir: filepath.Join(t.TempDir(), "reports"), InstallID: strings.Repeat("ab", 16)})
	r.Record("attention:x", report.Failure{Code: "x"})
	if all, _ := r.store.List(); len(all) != 0 {
		t.Fatalf("disabled reporter must store nothing, got %v", all)
	}
}

// 同签名 6 小时内只报一次(记账在 Store 里,这里只钉 Record 真的问了它)。
func TestReporterRateLimitsRepeats(t *testing.T) {
	var protected atomic.Bool
	r := testReporter(t, "http://127.0.0.1:1/never", &protected)
	r.Record("attention:x", report.Failure{Code: "x"})
	r.Record("attention:x", report.Failure{Code: "x"})
	if all, _ := r.store.List(); len(all) != 1 {
		t.Fatalf("second identical report within 6h must be suppressed, got %d", len(all))
	}
}

// InstallID:首次生成 32 位 hex 并落盘,之后读同一份;读不动就生成新的(不许为空)。
func TestInstallIDIsStableAcrossCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-id")
	a := loadOrCreateInstallID(path)
	b := loadOrCreateInstallID(path)
	if len(a) != 32 || a != b {
		t.Fatalf("install id = %q / %q", a, b)
	}
}

// stateOf 按签名(文件名里那一段)找一份报告的状态;List 按文件名排序,同一秒里的三份
// 顺序是字典序,不是写入序。
func stateOf(t *testing.T, r *Reporter, sig string) string {
	t.Helper()
	all, err := r.store.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range all {
		if strings.Contains(e.Name, "-"+sig+".") {
			return e.State
		}
	}
	t.Fatalf("no report for %s in %v", sig, all)
	return ""
}

// RunDaemon 把配置折成 ReporterOptions 的那一跳(纯函数):reports: off ⇒ Enabled=false;
// reports_endpoint 覆盖;目录与安装 ID 都在 /var/lib/bx 下(与日志同权限纪律)。
func TestReporterOptionsForFollowsTheConfig(t *testing.T) {
	on := reporterOptionsFor(&config.Config{}, "v0.4.16")
	if !on.Enabled || on.Endpoint != DefaultReportsEndpoint || on.Dir != "/var/lib/bx/reports" || on.Version != "v0.4.16" {
		t.Fatalf("defaults: %+v", on)
	}
	off := reporterOptionsFor(&config.Config{Reports: "off", ReportsEndpoint: "https://c.example/v1/reports"}, "v0.4.16")
	if off.Enabled || off.Endpoint != "https://c.example/v1/reports" {
		t.Fatalf("off + override: %+v", off)
	}
	if reportsInstallIDPath != "/var/lib/bx/install-id" {
		t.Fatalf("install id path = %q", reportsInstallIDPath)
	}
}
