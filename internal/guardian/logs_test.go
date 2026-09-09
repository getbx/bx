package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/install"
)

func logsFixture(t *testing.T) []LogSource {
	t.Helper()
	dir := t.TempDir()
	guard := filepath.Join(dir, "guard.err.log")
	core := filepath.Join(dir, "core.log")
	_ = os.WriteFile(guard, []byte("g1\ng2\ng3\n"), 0o600)
	_ = os.WriteFile(core, []byte("c1\n"), 0o600)
	return []LogSource{
		{Name: "guardian-errors", Path: guard},
		{Name: "core", Path: core},
		{Name: "missing", Path: filepath.Join(dir, "nope.log")},
	}
}

// 与 /v1/rules、/v1/servers 同一道门:owner 或 root。
func TestLogsEndpointRequiresOwnerOrRoot(t *testing.T) {
	handler := logsHandler(logsFixture(t), 501)
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
			handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/logs", nil), tc.uid, tc.got))
			if w.Code != tc.want {
				t.Fatalf("状态码 = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

// 尾部按 lines 截、读不到的那份如实 unavailable、顺序与来源一致。
func TestLogsEndpointServesTailsAndReportsUnavailable(t *testing.T) {
	handler := logsHandler(logsFixture(t), 501)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/logs?lines=2", nil), 501, true))
	if w.Code != http.StatusOK {
		t.Fatalf("状态码 = %d %s", w.Code, w.Body.String())
	}
	var got LogsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Logs) != 3 {
		t.Fatalf("要三份来源原样带出,实际 %d", len(got.Logs))
	}
	if got.Logs[0].Name != "guardian-errors" || strings.Join(got.Logs[0].Lines, ",") != "g2,g3" {
		t.Fatalf("第一份 = %+v", got.Logs[0])
	}
	if strings.Join(got.Logs[1].Lines, ",") != "c1" || got.Logs[1].Unavailable != "" {
		t.Fatalf("第二份 = %+v", got.Logs[1])
	}
	if got.Logs[2].Unavailable == "" || len(got.Logs[2].Lines) != 0 {
		t.Fatalf("读不到的那份要 unavailable 非空、lines 为空:%+v", got.Logs[2])
	}
	// 「lines」键必须在场(空数组),缺席会被菜单读成「这份没给」。
	if !strings.Contains(w.Body.String(), `"lines":[]`) {
		t.Fatalf("空 lines 要序列化成 []:%s", w.Body.String())
	}
}

func TestLogsEndpointValidatesLinesAndMethod(t *testing.T) {
	handler := logsHandler(logsFixture(t), 501)
	for _, q := range []string{"lines=0", "lines=2001", "lines=abc"} {
		w := httptest.NewRecorder()
		handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/logs?"+q, nil), 501, true))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "logs_bad_request") {
			t.Fatalf("%s → %d %s", q, w.Code, w.Body.String())
		}
	}
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/logs", nil), 501, true))
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST = %d", w.Code)
	}
	// 缺省 200 行:文件只有 3 行,给全部。
	w = httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/logs", nil), 501, true))
	var got LogsResponse
	_ = json.Unmarshal(w.Body.Bytes(), &got)
	if len(got.Logs[0].Lines) != 3 {
		t.Fatalf("缺省行数下应给全部 3 行,实际 %d", len(got.Logs[0].Lines))
	}
}

// 「没接线」回 501,不是一份看起来正常的空清单。
func TestLogsEndpointReportsWhenNotWired(t *testing.T) {
	w := httptest.NewRecorder()
	logsHandler(nil, 501)(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/logs", nil), 501, true))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("未接线 = %d, want 501", w.Code)
	}
}

// 接线守卫:NewLocalAPI 真的挂上 /v1/logs,门是 owner。
func TestNewLocalAPIWiresLogsEndpoint(t *testing.T) {
	handler := NewLocalAPI(&fakeController{}, LocalAPIOptions{OwnerUID: 501, LogSources: logsFixture(t)})
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/v1/logs", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("无凭据访问 /v1/logs = %d, want 403", w.Code)
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, withPeer(httptest.NewRequest(http.MethodGet, "/v1/logs?lines=1", nil), 501, true))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"g3"`) {
		t.Fatalf("经 NewLocalAPI = %d %s", w.Code, w.Body.String())
	}
}

// 路径必须来自 install 那份常量,不许在 Guardian 里再抄一份。
func TestGuardianLogsServeTheInstalledPaths(t *testing.T) {
	sources := guardianLogSources()
	want := install.GuardianLogPaths()
	if len(sources) != len(want) {
		t.Fatalf("来源数 %d ≠ install.GuardianLogPaths() 的 %d", len(sources), len(want))
	}
	for i := range want {
		if sources[i].Path != want[i] {
			t.Fatalf("第 %d 份路径 %q ≠ %q", i, sources[i].Path, want[i])
		}
		if sources[i].Name == "" {
			t.Fatalf("第 %d 份没有名字", i)
		}
	}
	got := localAPIOptionsFor(DaemonOptions{ConfigPath: "/etc/bx/config.yaml", LocalAPIOwnerUID: 501})
	if !reflect.DeepEqual(got.LogSources, sources) {
		t.Fatalf("daemon 组装的 LogSources = %+v, want %+v", got.LogSources, sources)
	}
}

func TestLogsCapabilityIsDeclared(t *testing.T) {
	for _, c := range GuardianCapabilities() {
		if c == CapabilityLogs {
			return
		}
	}
	t.Fatalf("能力清单里没有 %q:%v", CapabilityLogs, GuardianCapabilities())
}
