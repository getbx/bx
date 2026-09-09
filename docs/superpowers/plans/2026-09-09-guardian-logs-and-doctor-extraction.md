# Guardian 日志端点 + doctor 判据抽取(spec §8 的 ① 与 ②)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让菜单在出问题的那一刻能看见 Guardian 的完整原因(`/v1/logs` + 弹窗「Show Details」),并把 `bx doctor` 的判据抽成 Guardian 也能调的纯函数包 `internal/doctor`,为 `/v1/doctor` 铺路。

**Architecture:** Guardian 新增 owner 门的 `GET /v1/logs`(路径来自 `install.GuardianLogPaths()`,倒读尾部);菜单新增 Diagnostics 窗口(本期只有日志页),四处「See /var/log/bx-guard.err.log」改为带「Show Details」的弹窗。`internal/doctor` 是纯判据包(`Judge(Facts) Report`),`internal/cli` 的 `collectClientDoctorWith` 变成「采集 Facts → Judge」两段,`--json` 输出逐字节不变;`bx doctor` 文本路径改为渲染同一份 `Report`。

**Tech Stack:** Go 1.26(`net/http` unix socket、`httptest`)、Swift 5.9 AppKit(SwiftPM + `scripts/test-macos-menu.sh` 单文件套件)、Go 读源码守卫(`internal/cli` 里 `swiftFunctionBody` / `menuMainSwiftCode` 那套)。

**Spec:** `docs/superpowers/specs/2026-09-09-guardian-owns-diagnostics-and-config-design.md`(§1、§2、§3、§5、§6 日志页、§7、§8 ①②)。

## Global Constraints

- 验证一律 `bash scripts/verify.sh --quick`(改 Swift 时它会跑 `swift build` + 全部 Swift 套件),提交前跑一次全量 `bash scripts/verify.sh`。**判据是退出码,不是 grep。**
- Go 格式化用 `$(go env GOPATH)/bin/gofumpt -l <dir>` 必须无输出。
- 提交信息中文 conventional commits,结尾带 `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`。
- Guardian 端点:owner 门一律 `authorizeOwnerPeer(r.Context(), ownerUID)`;响应体只带失败类别 `{"code":"…"}`,完整原因只进 Guardian 日志(`log.Printf`);JSON 一律经 `writeGuardianJSON`(它带 Content-Length,菜单的手写 HTTP 读取器只认它)。
- 能力声明 `GuardianCapabilities()` 每次返回新切片;`Status.Capabilities` 无 omitempty。
- 新加的 Swift 测试文件必须登记进 `scripts/test-macos-menu.sh`(`TestEveryMacOSMenuTestSuiteIsRegistered` 守着);Swift 用户可见文案英文(`TestMacMenuUserFacingStringsAreEnglish` 守着)。
- `internal/doctor` 不得 import `net`、`net/http`、`os`、`os/exec`、`syscall`,不得 import `internal/guardian` / `internal/cli` / `internal/supervisor` / `internal/install`(前者会成环,后者是控制面)。允许 `internal/rulereview`、`internal/config`。
- `bx doctor --json` 的输出(check 的 Name / 顺序 / Status / Detail / Hint)**逐字节不变**。
- 绝不启动 bx、不改路由;真机验收交给项目所有者。

---

## 文件结构

**Phase ①(Task 1–4)**
- `internal/guardian/logtail.go`(新):`tailLines(path, n)` 倒读文件尾部。
- `internal/guardian/logs.go`(新):`LogSource`/`LogTail`/`LogsResponse`、`logsHandler`、`guardianLogSources()`、`CapabilityLogs`。
- `internal/guardian/localapi.go`(改):`LocalAPIOptions.LogSources`,挂 `/v1/logs`。
- `internal/guardian/daemon.go`(改):`localAPIOptionsFor` 接 `guardianLogSources()`。
- `internal/guardian/types.go`(改):`CapabilityLogs` 进 `GuardianCapabilities()`。
- `apps/macos/BxMenu/Sources/BxMenu/LogsModel.swift`(新,纯):解码、`logsAvailable`、`logLinesMatching`。
- `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`(改):`.logs(lines:)` 端点。
- `apps/macos/BxMenu/Sources/BxMenu/DiagnosticsWindow.swift`(新,AppKit):日志页。
- `apps/macos/BxMenu/Sources/BxMenu/main.swift`(改):`openLogs` 改开窗口、`showGuardianFailure` 取代四处弹窗。
- `apps/macos/BxMenu/Sources/BxMenu/RulesModel.swift`(改):`guardianFetchFailureInfo` 不再指向 root 文件。
- `internal/cli/macos_menu_logs_test.go`(新):接线守卫。

**Phase ②(Task 5–7)**
- `internal/doctor/doctor.go`(新):`Check`/`Report`/`Facts`/`Judge`。
- `internal/doctor/rulereview.go`(新):从 `internal/cli/rulereview.go` 搬来的纯判据(`RuleReviewLines` 等)。
- `internal/doctor/purity_test.go`、`internal/doctor/doctor_test.go`(新)。
- `internal/cli/cli.go`(改):`checkReport`/`doctorReport` 变成 `doctor` 类型的别名;`collectClientDoctorWith` = 采集 + `doctor.Judge`;`doctorAction` 文本路径渲染 `Report`。
- `internal/cli/rulereview.go`(改):判据函数变成对 `doctor` 的薄壳。
- `internal/cli/doctor_facts.go`(新):`collectDoctorFacts`。

---

## Phase ①:`/v1/logs` + Show Details

### Task 1: 倒读文件尾部 `tailLines`

**Files:**
- Create: `internal/guardian/logtail.go`
- Test: `internal/guardian/logtail_test.go`

**Interfaces:**
- Produces: `func tailLines(path string, n int) ([]string, error)` —— 返回文件最后 n 行(不含换行符,保持文件顺序);文件不存在/读不到返回 error;n ≤ 0 返回空切片;文件行数不足 n 时返回全部。

- [ ] **Step 1: 写失败测试**

```go
// internal/guardian/logtail_test.go
package guardian

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailLinesReturnsTheLastNInFileOrder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.log")
	if err := os.WriteFile(path, []byte("a\nb\nc\nd\ne\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := tailLines(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "c,d,e" {
		t.Fatalf("尾部 3 行 = %v, want c,d,e", got)
	}
}

func TestTailLinesHandlesShortFilesMissingNewlineAndEmpty(t *testing.T) {
	dir := t.TempDir()
	short := filepath.Join(dir, "short.log")
	_ = os.WriteFile(short, []byte("only\ntwo"), 0o600) // 末尾没有换行
	got, err := tailLines(short, 10)
	if err != nil || strings.Join(got, ",") != "only,two" {
		t.Fatalf("行数不足时要给全部且认得末尾无换行:%v %v", got, err)
	}
	empty := filepath.Join(dir, "empty.log")
	_ = os.WriteFile(empty, nil, 0o600)
	got, err = tailLines(empty, 10)
	if err != nil || len(got) != 0 {
		t.Fatalf("空文件 = %v %v, want 空切片无错", got, err)
	}
	if got, err := tailLines(short, 0); err != nil || len(got) != 0 {
		t.Fatalf("n=0 = %v %v", got, err)
	}
}

func TestTailLinesReadsOnlyTheTailOfABigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.log")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	// 200k 行、约 3MB:倒读实现不该把整个文件读进来才取最后 5 行。
	for i := 0; i < 200_000; i++ {
		_, _ = f.WriteString("0123456789\n")
	}
	_, _ = f.WriteString("last-1\nlast-2\nlast-3\nlast-4\nlast-5\n")
	_ = f.Close()
	got, err := tailLines(path, 5)
	if err != nil || strings.Join(got, ",") != "last-1,last-2,last-3,last-4,last-5" {
		t.Fatalf("大文件尾部 = %v %v", got, err)
	}
}

func TestTailLinesReportsMissingFile(t *testing.T) {
	if _, err := tailLines(filepath.Join(t.TempDir(), "nope.log"), 3); err == nil {
		t.Fatal("文件不存在要报错,不能悄悄给空切片(「没读到」≠「日志是空的」)")
	}
}
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./internal/guardian/ -run TestTailLines -v`
Expected: `undefined: tailLines`

- [ ] **Step 3: 实现**

```go
// internal/guardian/logtail.go
package guardian

import (
	"bytes"
	"io"
	"os"
)

// tailLines 返回 path 最后 n 行(文件顺序,不含换行符)。
//
// **从文件末尾按块倒读,不整文件读进内存**:/var/log/bx-guard.err.log 真的到过
// 100MB(2026-09-01),而调用方只要最后几百行。文件不存在 / 打不开如实返回 error
// —— 「没读到」与「日志是空的」必须分开(与 Tristate 同一条)。
func tailLines(path string, n int) ([]string, error) {
	if n <= 0 {
		return []string{}, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const block = 64 << 10
	var buf []byte
	offset := info.Size()
	for offset > 0 && bytes.Count(buf, []byte{'\n'}) <= n {
		size := int64(block)
		if offset < size {
			size = offset
		}
		offset -= size
		chunk := make([]byte, size)
		if _, err := f.ReadAt(chunk, offset); err != nil && err != io.EOF {
			return nil, err
		}
		buf = append(chunk, buf...)
	}
	// 去掉末尾的换行,再按行切;最后一行没有换行也算一行。
	buf = bytes.TrimRight(buf, "\n")
	if len(buf) == 0 {
		return []string{}, nil
	}
	parts := bytes.Split(buf, []byte{'\n'})
	if len(parts) > n {
		parts = parts[len(parts)-n:]
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, string(p))
	}
	return out, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian/ -run TestTailLines -v`
Expected: 4 个 PASS。

- [ ] **Step 5: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/guardian/
git add internal/guardian/logtail.go internal/guardian/logtail_test.go
git commit -m "feat(guardian): tailLines 倒读日志尾部 —— 不把 100MB 的日志整个读进内存

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: `GET /v1/logs` 端点、能力声明、组装根接线

**Files:**
- Create: `internal/guardian/logs.go`
- Modify: `internal/guardian/types.go`(`CapabilityLogs`、`GuardianCapabilities()`)
- Modify: `internal/guardian/localapi.go`(`LocalAPIOptions.LogSources`、`mux.HandleFunc("/v1/logs", …)`)
- Modify: `internal/guardian/daemon.go:817-835`(`localAPIOptionsFor` 加 `LogSources: guardianLogSources()`)
- Test: `internal/guardian/logs_test.go`

**Interfaces:**
- Consumes: `tailLines(path, n)`(Task 1)、`install.GuardianStdoutLogPath` / `install.GuardianStderrLogPath` / `install.CoreLogPath()` / `install.GuardianLogPaths()`。
- Produces:
  ```go
  type LogSource struct{ Name, Path string }
  type LogTail struct {
      Name        string   `json:"name"`
      Path        string   `json:"path"`
      Lines       []string `json:"lines"`               // 无 omitempty:空数组与缺席不同
      Unavailable string   `json:"unavailable,omitempty"`
  }
  type LogsResponse struct{ Logs []LogTail `json:"logs"` }
  const CapabilityLogs = "logs"
  func guardianLogSources() []LogSource   // darwin 三份,其它平台 nil
  func logsHandler(sources []LogSource, ownerUID uint32) http.HandlerFunc
  // LocalAPIOptions 新字段:LogSources []LogSource
  ```
  查询参数 `lines`:缺省 200,范围 1–2000,非数字或越界 → 400 `{"code":"logs_bad_request"}`;`sources` 为 nil → 501。

- [ ] **Step 1: 写失败测试**

```go
// internal/guardian/logs_test.go
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
```

- [ ] **Step 2: 跑测试确认编译失败**

Run: `go test ./internal/guardian/ -run 'Logs' -v`
Expected: `undefined: logsHandler` 等。

- [ ] **Step 3: 实现 `logs.go`**

```go
// internal/guardian/logs.go
package guardian

import (
	"log"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/getbx/bx/internal/install"
)

// CapabilityLogs:这一版 Guardian 会经 /v1/logs 发布自己与 Core 的日志尾部。
//
// 菜单只在声明了它时才画「Show Details」/ 日志页 —— 旧版 Guardian 对 /v1/logs
// 回 404,而 404 在菜单上表达不出来(**绝不「试着拨一下看看」**)。
const CapabilityLogs = "logs"

// LogSource 是一份要发布的日志:名字给界面,路径给 tailLines。
type LogSource struct {
	Name string
	Path string
}

// LogTail 是一份日志的尾部。**Lines 无 omitempty**:空数组是「这份日志是空的」,
// 键缺席会被菜单读成「这份没给」;Unavailable 非空表示没读到(路径也照给,让人
// 知道去哪找)。
type LogTail struct {
	Name        string   `json:"name"`
	Path        string   `json:"path"`
	Lines       []string `json:"lines"`
	Unavailable string   `json:"unavailable,omitempty"`
}

type LogsResponse struct {
	Logs []LogTail `json:"logs"`
}

const (
	logsDefaultLines = 200
	logsMaxLines     = 2000
)

// guardianLogSources 把 install 那份路径清单配上名字。**路径不在这里另抄一份**
// (TestGuardianLogsServeTheInstalledPaths 钉着)。非 darwin 为 nil ⇒ 端点 501。
func guardianLogSources() []LogSource {
	var out []LogSource
	for _, path := range install.GuardianLogPaths() {
		out = append(out, LogSource{Name: logSourceName(path), Path: path})
	}
	return out
}

func logSourceName(path string) string {
	switch path {
	case install.GuardianStdoutLogPath:
		return "guardian"
	case install.GuardianStderrLogPath:
		return "guardian-errors"
	case install.CoreLogPath():
		return "core"
	}
	return filepath.Base(path)
}

// logsHandler 服务 GET /v1/logs?lines=N。
//
// **发布面记录**:此前这些日志只有 root 读得到(SecureGuardianLogs 收成 0600),
// 现在 owner 也读得到 —— 门与 /v1/down 同一道:owner 本来就能关掉保护,能做
// 更坏的事;日志里的服务器 IP / bypass 网段与 `bx status --json` 已发布的同量级。
// 不做脱敏、不做过滤。
func logsHandler(sources []LogSource, ownerUID uint32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "logs require owner or root peer"})
			return
		}
		if sources == nil {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "logs unavailable on this platform"})
			return
		}
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		lines := logsDefaultLines
		if raw := r.URL.Query().Get("lines"); raw != "" {
			n, err := strconv.Atoi(raw)
			if err != nil || n < 1 || n > logsMaxLines {
				writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "logs_bad_request"})
				return
			}
			lines = n
		}
		resp := LogsResponse{Logs: make([]LogTail, 0, len(sources))}
		for _, src := range sources {
			tail := LogTail{Name: src.Name, Path: src.Path, Lines: []string{}}
			got, err := tailLines(src.Path, lines)
			if err != nil {
				// 完整原因进日志;响应里只说这份读不到 —— 与其它端点同一条纪律。
				log.Printf("guardian_logs_read_failed name=%s path=%s err=%v", src.Name, src.Path, err)
				tail.Unavailable = "could not read this log"
			} else {
				tail.Lines = got
			}
			resp.Logs = append(resp.Logs, tail)
		}
		writeGuardianJSON(w, http.StatusOK, resp)
	}
}
```

- [ ] **Step 4: 接线 —— `types.go`、`localapi.go`、`daemon.go`**

`internal/guardian/types.go`:把 `GuardianCapabilities()` 的返回改为

```go
return []string{CapabilityDiagnosticsArchive, CapabilityReconcileReport, CapabilityMaintenanceHold, CapabilityRules, CapabilityServers, CapabilityStatusWatch, CapabilityApps, CapabilityLogs}
```

`internal/guardian/localapi.go`:在 `LocalAPIOptions` 的 `AppsSockPath` 字段之后加

```go
	// LogSources backs /v1/logs. Nil means "not wired" (non-darwin, or a
	// caller that never set it) — the endpoint answers 501, never an empty
	// list that reads like "the logs are empty".
	LogSources []LogSource
```

并在 `mux.HandleFunc("/v1/apps", …)` 那一行之后加

```go
	mux.HandleFunc("/v1/logs", logsHandler(options.LogSources, options.OwnerUID))
```

`internal/guardian/daemon.go` 的 `localAPIOptionsFor`,在 `ReloadRules: reloadCoreRules,` 之后加

```go
		// /v1/logs:路径来自 install 那份清单,不在 Guardian 里再抄一份。
		LogSources: guardianLogSources(),
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/guardian/ -run 'Logs|Capabilit' -v && go test ./internal/guardian/`
Expected: 全部 PASS(含既有 `TestGuardianCapabilities*` 类守卫 —— 若有守卫钉着能力清单的精确内容,按它的错误信息把 `CapabilityLogs` 加进它的期望列表)。

- [ ] **Step 6: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/guardian/
git add internal/guardian/
git commit -m "feat(guardian): GET /v1/logs 经 owner 门发布 Guardian 与 Core 日志尾部 —— 路径来自 install.GuardianLogPaths,能力声明 logs

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: Swift 侧:`LogsModel.swift`(纯)+ `GuardianClient.fetchLogs`

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/LogsModel.swift`
- Create: `apps/macos/BxMenu/Tests/LogsModelTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`(`GuardianEndpoint.logs(lines:)`、`expectedStatus`、`timeout`、`guardianRequest`、`fetchLogs`)
- Modify: `scripts/test-macos-menu.sh`(登记 `logs-model` 套件;`guardian-client*` 与 `guardian-status*` 那几个套件的源文件清单要加 `LogsModel.swift`,因为 GuardianClient.swift 现在引用 `LogsReport`)

**Interfaces:**
- Produces:
  ```swift
  struct LogTail: Decodable, Equatable { let name: String; let path: String; let lines: [String]; let unavailable: String }
  struct LogsReport: Decodable, Equatable { let logs: [LogTail] }
  func logsAvailable(capabilities: [String]?) -> Bool         // 能力键缺席 = 旧版 ⇒ false
  func logLinesMatching(_ lines: [String], code: String?) -> Set<Int>  // 含失败码的行下标;code 空 ⇒ 空集
  let logsDefaultLineCount = 200
  // GuardianClient
  func fetchLogs(lines: Int = logsDefaultLineCount) throws -> LogsReport
  ```

- [ ] **Step 1: 写失败测试**

```swift
// apps/macos/BxMenu/Tests/LogsModelTests.swift
import Foundation

@main
struct LogsModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func decode(_ json: String) -> LogsReport? {
        do {
            return try JSONDecoder().decode(LogsReport.self, from: Data(json.utf8))
        } catch {
            expect(false, "解码失败:\(error)")
            return nil
        }
    }

    // 线上形状:lines 恒在场(可能是空数组),unavailable 只在读不到时出现。
    static func testDecodesTailsAndToleratesMissingUnavailable() {
        let json = """
        {"logs":[{"name":"guardian-errors","path":"/var/log/bx-guard.err.log","lines":["a","b"]},
                 {"name":"core","path":"/var/log/bx.log","lines":[],"unavailable":"could not read this log"}]}
        """
        guard let report = decode(json) else { return }
        expect(report.logs.count == 2, "两份来源")
        expect(report.logs[0].lines == ["a", "b"] && report.logs[0].unavailable.isEmpty, "第一份:\(report.logs[0])")
        expect(report.logs[1].lines.isEmpty && !report.logs[1].unavailable.isEmpty, "第二份要带 unavailable:\(report.logs[1])")
    }

    // 能力键缺席 = 旧版 Guardian,不是「不支持」的同义反复 —— 判据与 rulesEditingAvailable 同款。
    static func testLogsAvailableIsGatedByCapability() {
        expect(!logsAvailable(capabilities: nil), "nil ⇒ 旧版,不可用")
        expect(!logsAvailable(capabilities: ["rules"]), "没声明 logs ⇒ 不可用")
        expect(logsAvailable(capabilities: ["rules", "logs"]), "声明了 ⇒ 可用")
    }

    // 失败码定位:Guardian 日志的形状是 `guardian_xxx … code=… err=…`,按子串找。
    static func testLogLinesMatchingFindsTheFailureCode() {
        let lines = ["guardian_mutation_requested endpoint=/v1/up uid=501",
                     "guardian_mutation_result outcome=failed code=core_ownership_uncertain",
                     "guardian_core_scan enumerated=766 readable=765 cores=1"]
        expect(logLinesMatching(lines, code: "core_ownership_uncertain") == [1], "该命中第 1 行")
        expect(logLinesMatching(lines, code: nil).isEmpty, "没有码就不高亮任何行")
        expect(logLinesMatching(lines, code: "").isEmpty, "空码同上")
        expect(logLinesMatching(lines, code: "nope").isEmpty, "没命中就是空集,不是「全部」")
    }

    static func main() {
        testDecodesTailsAndToleratesMissingUnavailable()
        testLogsAvailableIsGatedByCapability()
        testLogLinesMatchingFindsTheFailureCode()
        if failures == 0 {
            print("LogsModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
```

- [ ] **Step 2: 跑确认编译失败**

Run: `M=apps/macos/BxMenu; swiftc $M/Sources/BxMenu/LogsModel.swift $M/Tests/LogsModelTests.swift -o /tmp/bx-logs-test 2>&1 | head -3`
Expected: `no such file` / `cannot find 'LogsReport'`。

- [ ] **Step 3: 实现 `LogsModel.swift`**

```swift
// apps/macos/BxMenu/Sources/BxMenu/LogsModel.swift
import Foundation

/// GET /v1/logs 的应答。**判据都在这个文件里**(AppKit 那半编不进套件)。
struct LogTail: Decodable, Equatable {
    let name: String
    let path: String
    /// 空数组 = 这份日志是空的;读不到时服务端给空数组 + unavailable。
    let lines: [String]
    /// 非空 = 没读到;原因不在这里(它在 Guardian 自己的日志里),这里只说没读到。
    let unavailable: String

    enum CodingKeys: String, CodingKey { case name, path, lines, unavailable }

    /// **必须手写。** Swift 合成的解码器不用属性默认值,而 `unavailable` 是 omitempty。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        path = try c.decodeIfPresent(String.self, forKey: .path) ?? ""
        lines = try c.decodeIfPresent([String].self, forKey: .lines) ?? []
        unavailable = try c.decodeIfPresent(String.self, forKey: .unavailable) ?? ""
    }

    init(name: String, path: String, lines: [String], unavailable: String = "") {
        self.name = name
        self.path = path
        self.lines = lines
        self.unavailable = unavailable
    }
}

struct LogsReport: Decodable, Equatable {
    let logs: [LogTail]

    enum CodingKeys: String, CodingKey { case logs }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        logs = try c.decodeIfPresent([LogTail].self, forKey: .logs) ?? []
    }

    init(logs: [LogTail]) { self.logs = logs }
}

/// 缺省向 Guardian 要多少行(服务端上限 2000)。
let logsDefaultLineCount = 200

/// 这一版 Guardian 支不支持 /v1/logs。**能力键缺席 = 旧版**,不是「不支持」的
/// 同义反复:少了这个判断菜单会对着一个每次点都 404 的按钮。
func logsAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("logs")
}

/// 日志里含失败码的行下标。Guardian 日志的形状是 `guardian_xxx … code=… err=…`,
/// 按子串找就够;没有码(或没命中)是**空集**,不是「全部」—— 高亮全部等于没高亮。
func logLinesMatching(_ lines: [String], code: String?) -> Set<Int> {
    guard let code, !code.isEmpty else { return [] }
    var out = Set<Int>()
    for (index, line) in lines.enumerated() where line.contains(code) {
        out.insert(index)
    }
    return out
}
```

- [ ] **Step 4: `GuardianClient.swift` 加端点**

在 `enum GuardianEndpoint` 里 `case appTraffic` 之后加:

```swift
    /// Guardian 与 Core 两份日志的尾部。**只有 `logsAvailable(capabilities:)` 判定
    /// 这一版 Guardian 支持时才该调用它**(理由同 appTraffic)。
    case logs(lines: Int)
```

`expectedStatus` 的 200 那一组加 `.logs`;`timeout` 的 `switch` 里把 `.logs` 归到与 `.listRules` 相同的那一档(找到含 `.listRules` 的 `case` 行,加上 `, .logs`)。

`guardianRequest(for:)` 的 `switch` 里加:

```swift
    case let .logs(lines):
        method = "GET"
        // lines 是 Int,插值不引入注入面(与 statusWatch 同理)。
        path = "/v1/logs?lines=\(lines)"
        body = nil
```

`GuardianClient` 的方法区(`func appTraffic()` 之后)加:

```swift
    /// 取两份日志的尾部。**调用前必须过 `logsAvailable(capabilities:)` 那道能力门。**
    func fetchLogs(lines: Int = logsDefaultLineCount) throws -> LogsReport {
        try perform(endpoint: .logs(lines: lines), as: LogsReport.self)
    }
```

- [ ] **Step 5: 登记套件并把 LogsModel.swift 加进依赖 GuardianClient.swift 的套件**

`scripts/test-macos-menu.sh`:在 `run_test transition-notice` 之前加

```bash
run_test logs-model \
  "$MENU/Sources/BxMenu/LogsModel.swift" \
  "$MENU/Tests/LogsModelTests.swift"
```

再用 sed 给每一个已包含 `GuardianClient.swift` 的 `run_test` 块加上 `LogsModel.swift`(它们是 guardian-client、guardian-status、guardian-client-timeout、guardian-failure-code、guardian-status-fields 五处):

```bash
perl -0pi -e 's|(  "\$MENU/Sources/BxMenu/AppTrafficModel.swift" \\\n  "\$MENU/Sources/BxMenu/GuardianClient.swift" \\\n)|  "\$MENU/Sources/BxMenu/AppTrafficModel.swift" \\\n  "\$MENU/Sources/BxMenu/LogsModel.swift" \\\n  "\$MENU/Sources/BxMenu/GuardianClient.swift" \\\n|g' scripts/test-macos-menu.sh
grep -c "LogsModel.swift" scripts/test-macos-menu.sh
```
Expected: `6`(5 处 + logs-model 自己)。

- [ ] **Step 6: 跑 Swift 套件与构建**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -2`
Expected: `Build complete!`,`LogsModelTests passed` 与 `macOS menu tests passed`。

- [ ] **Step 7: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/LogsModel.swift apps/macos/BxMenu/Tests/LogsModelTests.swift apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift scripts/test-macos-menu.sh
git commit -m "feat(menu): /v1/logs 的客户端与纯模型 —— 解码、能力门控、失败码定位

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: Diagnostics 窗口(日志页)+ 弹窗「Show Details」

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/DiagnosticsWindow.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(`openLogs`、四处弹窗、新增 `showGuardianFailure` / `openDiagnosticsLogs` / `diagnosticsWindow`)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/RulesModel.swift:25`(`guardianFetchFailureInfo`)
- Modify: `apps/macos/BxMenu/Tests/RulesModelTests.swift:281`(那条断言)
- Create: `internal/cli/macos_menu_logs_test.go`(接线守卫)

**Interfaces:**
- Consumes: `LogsReport`、`logLinesMatching`、`logsAvailable`、`GuardianClient.fetchLogs` (Task 3)。
- Produces(AppKit):
  ```swift
  final class DiagnosticsWindowController: NSObject, NSWindowDelegate {
      func showLogs(_ report: LogsReport, highlightingCode code: String?)
      var onExportDiagnostics: (() -> Void)?    // 底部「Export Diagnostics…」→ 走原 runDoctor 的终端归档
  }
  ```
  `main.swift`:
  ```swift
  private func showGuardianFailure(title: String, error: Error)   // 弹窗;logsAvailable 时带「Show Details」
  private func openDiagnosticsLogs(highlighting code: String?)     // 拉 /v1/logs 后 showLogs
  ```

- [ ] **Step 1: 先写 Go 接线守卫(红)**

```go
// internal/cli/macos_menu_logs_test.go
package cli

import (
	"strings"
	"testing"
)

// 失败弹窗不再把用户指向一个 root 0600 的文件;完整原因由 Guardian 经 /v1/logs
// 发布,弹窗带「Show Details」打开日志页。四处弹窗都走同一个 showGuardianFailure。
func TestMacMenuFailureAlertsNeverPointAtRootOnlyLogs(t *testing.T) {
	main := stripSwiftComments(menuMainSwiftSource(t))
	rules := stripSwiftComments(readMenuSwiftSource(t, "RulesModel.swift"))
	for name, src := range map[string]string{"main.swift": main, "RulesModel.swift": rules} {
		if strings.Contains(src, "/var/log/bx-guard") {
			t.Fatalf("%s 里仍有指向 root 日志文件的路径 —— 普通用户打开就是 Permission denied", name)
		}
	}
	if n := strings.Count(main, "showGuardianFailure(title:"); n < 4 {
		t.Fatalf("showGuardianFailure 只有 %d 个调用方,四处失败弹窗(apps / switch server / rule group / add rule)都要走它", n)
	}
	body, ok := swiftFunctionBody(main, "private func showGuardianFailure(title: String, error: Error)")
	if !ok {
		t.Fatal("读不出 showGuardianFailure 的函数体")
	}
	gate := strings.Index(body, "logsAvailable(capabilities: maintenanceReport?.capabilities)")
	details := strings.Index(body, `"Show Details"`)
	open := strings.Index(body, "openDiagnosticsLogs(highlighting: guardianFailureCode(of: error))")
	if gate < 0 || details < 0 || open < 0 || gate > details || details > open {
		t.Fatalf("Show Details 必须在能力门之后出现、并把失败码交给 openDiagnosticsLogs(gate=%d details=%d open=%d)", gate, details, open)
	}
}

// Troubleshoot ▸ Open Logs:这一版 Guardian 支持时开日志页,不支持时退回原来的文件夹。
func TestMacMenuOpenLogsPrefersTheGuardianLogPage(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "@objc private func openLogs()")
	if !ok {
		t.Fatal("读不出 openLogs 的函数体")
	}
	gate := strings.Index(body, "logsAvailable(capabilities: maintenanceReport?.capabilities)")
	page := strings.Index(body, "openDiagnosticsLogs(highlighting: nil)")
	folder := strings.Index(body, "NSWorkspace.shared.open(")
	if gate < 0 || page < 0 || folder < 0 || gate > page || page > folder {
		t.Fatalf("openLogs 要先按能力开日志页、旧版才退回文件夹(gate=%d page=%d folder=%d)", gate, page, folder)
	}
}

// 窗口只摆:高亮哪些行由纯函数 logLinesMatching 决定。
func TestMacMenuDiagnosticsWindowHighlightsByThePureJudgement(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "DiagnosticsWindow.swift")))
	if !strings.Contains(window, "logLinesMatching(") {
		t.Fatal("DiagnosticsWindow 没有用 logLinesMatching 决定高亮 —— 判据落进了 AppKit 那半")
	}
	if !strings.Contains(window, "onExportDiagnostics?()") {
		t.Fatal("日志页底部没有 Export Diagnostics 的出口 —— 归档那条终端路是 spec §1 表里保留的")
	}
}
```

Run: `go test ./internal/cli/ -run 'TestMacMenuFailureAlerts|TestMacMenuOpenLogs|TestMacMenuDiagnosticsWindow' -v 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: 三条 FAIL。

- [ ] **Step 2: 实现 `DiagnosticsWindow.swift`**

```swift
// apps/macos/BxMenu/Sources/BxMenu/DiagnosticsWindow.swift
import AppKit

/// 「Diagnostics」窗口。本期只有日志页;spec §6 的 Checks 页在 /v1/doctor 落地后加。
///
/// **这个文件只做摆放。** 哪些行要高亮(失败码定位)由 LogsModel 的纯函数
/// `logLinesMatching` 决定;这里连一次字符串比较都不做。
final class DiagnosticsWindowController: NSObject, NSWindowDelegate {
    private var window: NSWindow?
    private var stack: NSStackView?

    /// 用户点了底部「Export Diagnostics…」—— 走原来那条终端归档路(spec §1 表里保留的)。
    var onExportDiagnostics: (() -> Void)?

    func showLogs(_ report: LogsReport, highlightingCode code: String?) {
        let window = ensureWindow()
        render(report, code: code)
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    private func ensureWindow() -> NSWindow {
        if let window { return window }
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 760, height: 480),
            styleMask: [.titled, .closable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Diagnostics"
        window.isReleasedWhenClosed = false
        window.center()
        window.delegate = self

        let stack = NSStackView()
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 8
        stack.edgeInsets = NSEdgeInsets(top: 16, left: 18, bottom: 16, right: 18)
        stack.translatesAutoresizingMaskIntoConstraints = false

        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true
        scroll.drawsBackground = false
        scroll.translatesAutoresizingMaskIntoConstraints = false
        let clip = FlippedView()
        clip.translatesAutoresizingMaskIntoConstraints = false
        clip.addSubview(stack)
        scroll.documentView = clip

        guard let content = window.contentView else { return window }
        content.addSubview(scroll)
        NSLayoutConstraint.activate([
            scroll.leadingAnchor.constraint(equalTo: content.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: content.trailingAnchor),
            scroll.topAnchor.constraint(equalTo: content.topAnchor),
            scroll.bottomAnchor.constraint(equalTo: content.bottomAnchor),
            stack.leadingAnchor.constraint(equalTo: clip.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: clip.trailingAnchor),
            stack.topAnchor.constraint(equalTo: clip.topAnchor),
            stack.bottomAnchor.constraint(equalTo: clip.bottomAnchor),
            clip.widthAnchor.constraint(equalTo: scroll.widthAnchor),
        ])
        self.stack = stack
        self.window = window
        return window
    }

    private func render(_ report: LogsReport, code: String?) {
        guard let stack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
        if let code, !code.isEmpty {
            let banner = NSTextField(labelWithString: "Highlighting lines that mention \(code).")
            banner.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
            banner.textColor = .secondaryLabelColor
            stack.addArrangedSubview(banner)
        }
        for tail in report.logs {
            stack.addArrangedSubview(header("\(tail.name)  ·  \(tail.path)"))
            if !tail.unavailable.isEmpty {
                stack.addArrangedSubview(hint("Not available: \(tail.unavailable)"))
                continue
            }
            if tail.lines.isEmpty {
                stack.addArrangedSubview(hint("This log is empty."))
                continue
            }
            let marks = logLinesMatching(tail.lines, code: code)
            let text = NSTextView()
            text.isEditable = false
            text.isSelectable = true
            text.drawsBackground = false
            text.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
            text.textContainer?.widthTracksTextView = true
            text.translatesAutoresizingMaskIntoConstraints = false
            let body = NSMutableAttributedString()
            for (index, line) in tail.lines.enumerated() {
                var attributes: [NSAttributedString.Key: Any] = [
                    .font: NSFont.monospacedSystemFont(ofSize: 11, weight: .regular),
                    .foregroundColor: NSColor.labelColor,
                ]
                if marks.contains(index) {
                    attributes[.backgroundColor] = NSColor.systemYellow.withAlphaComponent(0.35)
                }
                body.append(NSAttributedString(string: line + "\n", attributes: attributes))
            }
            text.textStorage?.setAttributedString(body)
            text.widthAnchor.constraint(greaterThanOrEqualToConstant: 700).isActive = true
            stack.addArrangedSubview(text)
        }
        let export = NSButton(title: "Export Diagnostics…", target: self, action: #selector(exportDiagnostics))
        export.bezelStyle = .rounded
        export.controlSize = .small
        export.toolTip = "Runs bx doctor in Terminal and collects a diagnostics folder you can share."
        stack.addArrangedSubview(export)
    }

    @objc private func exportDiagnostics() {
        onExportDiagnostics?()
    }

    private func header(_ title: String) -> NSTextField {
        let label = NSTextField(labelWithString: title)
        label.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        return label
    }

    private func hint(_ text: String) -> NSTextField {
        let label = NSTextField(labelWithString: text)
        label.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        label.textColor = .secondaryLabelColor
        return label
    }
}
```

(`FlippedView` 已在 ServersWindow/AppTrafficWindow 那半定义过,同 target 内可直接用;若编译报重定义,说明它是 `private`,把 `AppTrafficWindow.swift` 里那份改成非 private。)

- [ ] **Step 3: `main.swift` 接线**

在 `private lazy var appTrafficWindow` 之后加:

```swift
    /// Diagnostics 窗口(本期只有日志页)。归档出口接回原来那条终端路。
    private lazy var diagnosticsWindow: DiagnosticsWindowController = {
        let controller = DiagnosticsWindowController()
        controller.onExportDiagnostics = { [weak self] in
            self?.exportDiagnostics()
        }
        return controller
    }()

    /// 拉一次 /v1/logs 再开窗口;拉不到就明说(这条路本身就是「看失败原因」的路,
    /// 它自己失败时不能再指向别的什么)。
    private func openDiagnosticsLogs(highlighting code: String?) {
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().fetchLogs() }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let report):
                    self.diagnosticsWindow.showLogs(report, highlightingCode: code)
                case .failure(let error):
                    self.showMessage("Logs are not available", "bx could not read its logs: \(error.localizedDescription)")
                }
            }
        }
    }

    /// 失败弹窗的唯一出口。**完整原因由 Guardian 经 /v1/logs 发布**,弹窗只带失败码
    /// 与一个「Show Details」;旧 Guardian(没声明 logs)只说失败码。
    /// 措辞纪律:只说发生了什么与下一步,不断言原因。
    private func showGuardianFailure(title: String, error: Error) {
        let alert = NSAlert()
        alert.messageText = title
        alert.informativeText = error.localizedDescription
        alert.addButton(withTitle: "OK")
        let canShow = logsAvailable(capabilities: maintenanceReport?.capabilities)
        if canShow {
            alert.addButton(withTitle: "Show Details")
        }
        NSApp.activate(ignoringOtherApps: true)
        let response = alert.runModal()
        if canShow, response == .alertSecondButtonReturn {
            openDiagnosticsLogs(highlighting: guardianFailureCode(of: error))
        }
    }
```

把原来的 `@objc private func runDoctor()` 改名为 `private func exportDiagnostics()`(函数体不动:仍是那条 `openTerminal("… sudo … doctor …")`),并把 Troubleshoot 子菜单里的

```swift
        troubleshoot.addAction("Check for Problems", symbol: "stethoscope", target: self, action: #selector(runDoctor))
```

改成

```swift
        troubleshoot.addAction("Check for Problems", symbol: "stethoscope", target: self, action: #selector(runDoctorFromMenu))
```

并加一个薄壳(③ 期会把它换成打开 Checks 页):

```swift
    @objc private func runDoctorFromMenu() {
        exportDiagnostics()
    }
```

`openLogs` 改成:

```swift
    @objc private func openLogs() {
        // 这一版 Guardian 会发布日志就开日志页;旧版退回原来的文件夹(那是诊断包的落点)。
        if logsAvailable(capabilities: maintenanceReport?.capabilities) {
            openDiagnosticsLogs(highlighting: nil)
            return
        }
        let url = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Library")
            .appendingPathComponent("Logs")
            .appendingPathComponent("bx")
        do {
            try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true)
            NSWorkspace.shared.open(url)
        } catch {
            showMessage("Logs Unavailable", error.localizedDescription)
        }
    }
```

四处弹窗(按 `grep -n "bx-guard.err.log" main.swift` 找):
- `fetchAppTrafficOnDemand` 里那段(`"App traffic is not available"`):那里没有 `error` 对象(`try?` 吞掉了)。把 `let fetched = try? GuardianClient().appTraffic()` 改成 `let outcome = Result { try GuardianClient().appTraffic() }`,`guard let fetched` 改成 `guard case .success(let fetched) = outcome else { … }`,失败分支里把整个 `NSAlert` 段换成 `if case .failure(let error) = outcome { self.showGuardianFailure(title: "App traffic is not available", error: error) }`。
- `"Could not switch server"`、`"Could not change that group"`、`"Could not add that rule"` 三处:把 `let alert = NSAlert()` 到 `alert.runModal()` 整段换成 `self.showGuardianFailure(title: "<原 messageText>", error: error)`。

- [ ] **Step 4: `RulesModel.swift` 与它的测试**

`guardianFetchFailureInfo` 的 500 分支把 `info += "). See /var/log/bx-guard.err.log for the reason."` 改成 `info += "). Use Show Details for the reason."`;头注释里提到路径的那句改成「指路 Show Details **只在 HTTP 500 时成立**」。
`RulesModelTests.swift:281` 那条 `expect(server.contains("bx-guard.err.log"), …)` 改成 `expect(server.contains("Show Details"), "500 要指向 Show Details")`;288 与 295 两条改成 `!denied.contains("Show Details")` / `!unreachable.contains("Show Details")`(判据不变:只有 500 才指路)。

- [ ] **Step 5: 构建、跑套件、跑守卫**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -1; go test ./internal/cli/ 2>&1 | tail -2`
Expected: `Build complete!`、`macOS menu tests passed`、`ok`。若 `TestMacMenuTroubleshootSubmenuHoldsTheRareActions` 红(它钉 `troubleshoot.addAction("Check for Problems"` 字面),确认那一行仍在;若 `TestMacMenuPutsConstructiveActionBeforeDiagnostics` 红,检查 `"Open Logs"` 仍在 Troubleshoot 块里。

- [ ] **Step 6: 变异验证三条守卫**

```bash
M=apps/macos/BxMenu/Sources/BxMenu
cp $M/main.swift /tmp/bx-main.bak
sed -i '' 's|openDiagnosticsLogs(highlighting: guardianFailureCode(of: error))|openDiagnosticsLogs(highlighting: nil)|' $M/main.swift
go test ./internal/cli/ -run TestMacMenuFailureAlertsNeverPointAtRootOnlyLogs >/dev/null 2>&1 && echo "❌ still green" || echo "✅ red"
cp /tmp/bx-main.bak $M/main.swift
```
Expected: `✅ red`。再对 `openLogs` 去掉能力门(把 `if logsAvailable(...)` 改成 `if true`)重复一次,`TestMacMenuOpenLogsPrefersTheGuardianLogPage` 应红;恢复。

- [ ] **Step 7: 全量 verify 并提交**

```bash
bash scripts/verify.sh --quick && git add -A apps/macos/BxMenu internal/cli/macos_menu_logs_test.go && git commit -m "feat(menu): 失败弹窗带 Show Details 打开 Guardian 发布的日志页 —— 不再指向 root 0600 的文件

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

**Phase ① 真机验收(交给项目所有者)**:重装菜单后,在按应用窗口右键加一条非法规则(例如空的通配 `*.`)→ 弹窗应有「Show Details」→ 点开看到 Guardian 日志里 `guardian_rules_change_failed` 那行被高亮;Troubleshoot ▸ Open Logs 直接开日志页。

---

## Phase ②:`internal/doctor` 抽取

### Task 5: `internal/doctor` 纯判据包

**Files:**
- Create: `internal/doctor/doctor.go`
- Create: `internal/doctor/rulereview.go`(从 `internal/cli/rulereview.go:19-345` 搬来的纯判据)
- Test: `internal/doctor/purity_test.go`、`internal/doctor/doctor_test.go`

**Interfaces:**
- Produces:
  ```go
  package doctor
  type Check struct { Name string `json:"name"`; Status string `json:"status"`; Detail string `json:"detail,omitempty"`; Hint string `json:"hint,omitempty"` }
  type Report struct { OK bool `json:"ok"`; Kind string `json:"kind"`; Version string `json:"version"`; SecretsRedacted bool `json:"secrets_redacted"`; ChangesSystem bool `json:"changes_system"`; ChangesNetwork bool `json:"changes_network"`; RequiresRoot bool `json:"requires_root"`; Checks []Check `json:"checks"` }
  func (r *Report) AddCheck(name, status, detail, hint string)
  func (r *Report) AddReport(c Check)
  func (r Report) HasFail() bool

  type FileFact struct { Bytes []byte; ReadErr string; PermissionDenied bool; Mode0600 bool }
  type GuardianRulesFact struct { Review *rulereview.Report; ConfigPath string; Err string }
  type DNSFact struct { State string; Managed bool; Service string }
  type RecoveryFact struct { State, Stage string; Attempt int; ErrorCode string }
  type Facts struct {
      Version     string
      ConfigPath  string
      Config      FileFact
      Parsed      *config.Config      // 读到且解析成功时非 nil
      ParseErr    string
      GuardianRules GuardianRulesFact // 只在 Config.ReadErr != "" 时被看
      RuleReview  *rulereview.Report  // 读到、解析成功、Server 非空时由采集方算好
      Probe       *Check              // nil = 没探
      Service     []Check
      StatusSocketErr string
      Darwin      bool
      Guardian    *struct{ DNS DNSFact; Recovery RecoveryFact } // nil = 没问到 ⇒ 用 darwin 退路
      Platform    []Check
  }
  func Judge(f Facts) Report
  func RedactLink(link string) string
  func UDPPolicy(mode string) (status, detail, hint string)
  func DNSCheck(d DNSFact) Check
  func RecoveryCheck(r RecoveryFact) Check
  // rulereview.go
  type Finding struct { Status, Key, Value, Hint string }
  const DeadRulesCheckName = "dead rules"
  func RuleReviewLines(rep rulereview.Report) []Finding
  func RuleReviewCheckName(key string) string
  const DNSStateUnknown = "unknown"; DNSStateManaged = "managed"; DNSStateNotNeeded = "<与 guardian.DNSNotNeeded 相同的值>"
  ```
  `DNSStateNotNeeded` 的值:执行时 `grep -n "DNSNotNeeded" internal/guardian/types.go` 抄那个字面量;Task 6 的跨包守卫会钉住三者相等。

- [ ] **Step 1: 纯度守卫(红,因为包还不存在)**

```go
// internal/doctor/purity_test.go
package doctor

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本包只允许依赖两个纯计算的叶子:rulereview(规则体检判据)与 config(配置类型)。
// 依赖 guardian 会成环(guardian 要调本包),依赖 cli/supervisor/install 会把判据拖回
// 「只能靠人读」的位置。
var allowedInternalDeps = map[string]struct{}{
	"github.com/getbx/bx/internal/rulereview": {},
	"github.com/getbx/bx/internal/config":     {},
}

func TestDoctorPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]string{
		"os": "读文件/读环境是采集方的事", "os/exec": "跑命令属于组装层",
		"net": "判据不许联网", "net/http": "判据不许联网", "syscall": "判据不碰内核",
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if why, bad := banned[path]; bad {
				t.Errorf("%s import 了 %q —— %s", name, path, why)
			}
			if strings.HasPrefix(path, "github.com/getbx/bx/internal/") {
				if _, ok := allowedInternalDeps[path]; !ok {
					t.Errorf("%s import 了 %q:纯判据只允许依赖 %v", name, path, allowedInternalDeps)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个源文件都没检查到 —— 守卫读不懂目录结构")
	}
}
```

- [ ] **Step 2: 判据的行为测试(红)**

```go
// internal/doctor/doctor_test.go
package doctor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/rulereview"
)

func names(r Report) string {
	var out []string
	for _, c := range r.Checks {
		out = append(out, c.Name+":"+c.Status)
	}
	return strings.Join(out, " ")
}

func find(r Report, name string) Check {
	for _, c := range r.Checks {
		if c.Name == name {
			return c
		}
	}
	return Check{}
}

// 配置不存在(不是权限):config_readable fail + setup 提示,udp_policy 按缺省 proxy。
func TestJudgeMissingConfig(t *testing.T) {
	r := Judge(Facts{Version: "v", ConfigPath: "/x/config.yaml",
		Config: FileFact{ReadErr: "open /x/config.yaml: no such file or directory"},
		Service: []Check{{Name: "service_installed", Status: "fail"}}})
	if r.OK {
		t.Fatal("缺配置不该 ok")
	}
	if got := names(r); got != "config:info config_readable:fail service_installed:fail status_socket:ok udp_policy:ok" {
		t.Fatalf("顺序/名字 = %q", got)
	}
	if c := find(r, "config_readable"); c.Hint != "sudo bx setup <client-link>" || !strings.Contains(c.Detail, "no such file") {
		t.Fatalf("config_readable = %+v", c)
	}
	if c := find(r, "udp_policy"); !strings.Contains(c.Detail, "relayed through bx tunnel") {
		t.Fatalf("udp_policy 缺省要按 proxy:%+v", c)
	}
}

// 权限读不到 + Guardian 退路成功:info + Guardian 算的体检行;Guardian 读的不是同一个文件则退路作废。
func TestJudgePermissionDeniedUsesGuardianReviewOnlyForTheSameFile(t *testing.T) {
	review := &rulereview.Report{}
	same := Judge(Facts{ConfigPath: "/etc/bx/config.yaml",
		Config:        FileFact{ReadErr: "permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{Review: review, ConfigPath: "/etc/bx/config.yaml"}})
	if c := find(same, "config_readable"); c.Status != "info" || !strings.Contains(c.Detail, "规则已改经 Guardian 读取") {
		t.Fatalf("同一文件的退路 = %+v", c)
	}
	other := Judge(Facts{ConfigPath: "/tmp/other.yaml",
		Config:        FileFact{ReadErr: "permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{Review: review, ConfigPath: "/etc/bx/config.yaml"}})
	if c := find(other, "config_readable"); c.Status != "fail" {
		t.Fatalf("Guardian 读的是别的文件,退路必须作废:%+v", c)
	}
	nilReview := Judge(Facts{ConfigPath: "/etc/bx/config.yaml",
		Config:        FileFact{ReadErr: "permission denied", PermissionDenied: true},
		GuardianRules: GuardianRulesFact{Review: nil, ConfigPath: "/etc/bx/config.yaml"}})
	if c := find(nilReview, "config_readable"); c.Status != "fail" {
		t.Fatalf("Guardian 没发布体检时不许当成「都很健康」:%+v", c)
	}
}

// 读到且解析成功:权限、解析、server_link、transports、udp_transport、probe、体检行、udp_policy 按配置。
func TestJudgeParsedConfigProducesTheFullLadder(t *testing.T) {
	cfg := &config.Config{Server: "bx://abc", Transports: []string{"bx://abc", "bx://def"}}
	cfg.UDP.Mode = "direct-realtime"
	cfg.UDP.Transport = "hysteria2://x"
	probe := Check{Name: "probe", Status: "ok", Detail: "366ms"}
	r := Judge(Facts{ConfigPath: "/etc/bx/config.yaml",
		Config: FileFact{Bytes: []byte("server: x"), Mode0600: false}, Parsed: cfg,
		RuleReview: &rulereview.Report{}, Probe: &probe,
		Service: []Check{{Name: "guardian_installed", Status: "ok"}},
		StatusSocketErr: "dial unix /var/run/bx/core.sock: connect: no such file"})
	want := "config:info config_readable:ok config_permissions:warn config_parse:ok server_link:ok transports:ok udp_transport:ok probe:ok guardian_installed:ok status_socket:warn udp_policy:warn"
	if got := names(r); got != want {
		t.Fatalf("\n got %q\nwant %q", got, want)
	}
	if c := find(r, "server_link"); c.Detail != "bx://<redacted>" {
		t.Fatalf("链接必须脱敏:%+v", c)
	}
	if c := find(r, "config_permissions"); c.Hint != "chmod 600 /etc/bx/config.yaml" {
		t.Fatalf("权限提示 = %+v", c)
	}
	if c := find(r, "status_socket"); c.Hint != "bx logs" {
		t.Fatalf("status_socket 提示 = %+v", c)
	}
	if c := find(r, "udp_policy"); !strings.Contains(c.Detail, "may expose real network path") {
		t.Fatalf("udp_policy 要按配置里的 direct-realtime:%+v", c)
	}
	if !r.OK {
		t.Fatal("没有 fail 就该 ok")
	}
}

func TestJudgeParseFailureAndEmptyServer(t *testing.T) {
	bad := Judge(Facts{Config: FileFact{Bytes: []byte("x"), Mode0600: true}, ParseErr: "yaml: boom"})
	if got := names(bad); got != "config:info config_readable:ok config_permissions:ok config_parse:fail status_socket:ok udp_policy:ok" {
		t.Fatalf("解析失败阶梯 = %q", got)
	}
	empty := Judge(Facts{Config: FileFact{Bytes: []byte("x"), Mode0600: true}, Parsed: &config.Config{}})
	if c := find(empty, "server_link"); c.Status != "fail" || c.Hint != "sudo bx setup <client-link>" {
		t.Fatalf("server 为空 = %+v", c)
	}
}

// darwin:Guardian 没问到走退路(DNS unknown ⇒ fail;recovery failed/unknown/recovery_unavailable ⇒ warn)。
func TestJudgeDarwinGuardianChecksWithAndWithoutGuardian(t *testing.T) {
	noGuardian := Judge(Facts{Config: FileFact{ReadErr: "x"}, Darwin: true})
	if c := find(noGuardian, "guardian_dns"); c.Status != "fail" || c.Detail != "state=unknown managed=false" {
		t.Fatalf("没问到 Guardian 的 DNS 行 = %+v", c)
	}
	if c := find(noGuardian, "network_recovery"); c.Status != "warn" || !strings.Contains(c.Detail, "error_code=recovery_unavailable") {
		t.Fatalf("没问到 Guardian 的恢复行 = %+v", c)
	}
	healthy := Judge(Facts{Config: FileFact{ReadErr: "x"}, Darwin: true,
		Guardian: &GuardianFact{DNS: DNSFact{State: DNSStateManaged, Managed: true, Service: "Wi-Fi"},
			Recovery: RecoveryFact{State: "idle", Stage: "idle"}}})
	if c := find(healthy, "guardian_dns"); c.Status != "ok" || c.Detail != "state=managed managed=true service=Wi-Fi" {
		t.Fatalf("健康 DNS 行 = %+v", c)
	}
	if c := find(healthy, "network_recovery"); c.Status != "ok" || c.Detail != "state=idle stage=idle attempt=0" {
		t.Fatalf("空闲恢复行 = %+v", c)
	}
	notNeeded := Judge(Facts{Config: FileFact{ReadErr: "x"}, Darwin: true,
		Guardian: &GuardianFact{DNS: DNSFact{State: DNSStateNotNeeded}}})
	if c := find(notNeeded, "guardian_dns"); c.Status != "ok" {
		t.Fatalf("NotNeeded 是健康态:%+v", c)
	}
	linux := Judge(Facts{Config: FileFact{ReadErr: "x"}, Darwin: false})
	if find(linux, "guardian_dns").Name != "" {
		t.Fatal("非 darwin 不产出 guardian_dns")
	}
}

func TestJudgeAppendsPlatformChecksLastAndComputesOK(t *testing.T) {
	r := Judge(Facts{Config: FileFact{ReadErr: "x"}, Platform: []Check{{Name: "terminal_proxy", Status: "ok"}}})
	if !strings.HasSuffix(names(r), " terminal_proxy:ok") {
		t.Fatalf("平台检查要排最后:%q", names(r))
	}
	if r.OK {
		t.Fatal("config_readable fail ⇒ ok=false")
	}
}
```

- [ ] **Step 3: 实现 `doctor.go`**

```go
// internal/doctor/doctor.go
package doctor

import (
	"fmt"
	"strings"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/rulereview"
)

// Check 与 Report 的 JSON 形状是 `bx doctor --json` 的契约(agent / MCP 按名字取),
// 逐字段与迁移前的 cli.checkReport / cli.doctorReport 相同。
type Check struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type Report struct {
	OK              bool    `json:"ok"`
	Kind            string  `json:"kind"`
	Version         string  `json:"version"`
	SecretsRedacted bool    `json:"secrets_redacted"`
	ChangesSystem   bool    `json:"changes_system"`
	ChangesNetwork  bool    `json:"changes_network"`
	RequiresRoot    bool    `json:"requires_root"`
	Checks          []Check `json:"checks"`
}

func (r *Report) AddCheck(name, status, detail, hint string) {
	r.Checks = append(r.Checks, Check{Name: name, Status: status, Detail: detail, Hint: hint})
}

func (r *Report) AddReport(c Check) { r.Checks = append(r.Checks, c) }

func (r Report) HasFail() bool {
	for _, c := range r.Checks {
		if c.Status == "fail" {
			return true
		}
	}
	return false
}

// FileFact 是「读配置文件」这一步的事实。ReadErr 非空 = 没读到;PermissionDenied
// 单列是因为只有权限失败才走 Guardian 退路(文件不存在是「没 setup 过」,真问题)。
type FileFact struct {
	Bytes            []byte
	ReadErr          string
	PermissionDenied bool
	Mode0600         bool
}

// GuardianRulesFact 是 Guardian /v1/rules 退路拿到的东西:Review 为 nil 表示这一版
// Guardian 没发布体检;ConfigPath 是它读的文件,与要问的不是同一个就作废。
type GuardianRulesFact struct {
	Review     *rulereview.Report
	ConfigPath string
	Err        string
}

// DNS 状态的三个值与 guardian.DNSState 的常量逐字相同(跨包守卫钉在 internal/cli)。
// 本包不能 import guardian —— 它要被 guardian 调,成环。
const (
	DNSStateUnknown   = "unknown"
	DNSStateManaged   = "managed"
	DNSStateNotNeeded = "not_needed"
)

type DNSFact struct {
	State   string
	Managed bool
	Service string
}

type RecoveryFact struct {
	State     string
	Stage     string
	Attempt   int
	ErrorCode string
}

type GuardianFact struct {
	DNS      DNSFact
	Recovery RecoveryFact
}

// Facts 是判据的全部输入:每一项都是采到的事实或「没采到 + 原因」,没有一项是判断。
// **两份采集方**(internal/cli 与 internal/guardian)各自填它,喂进同一个 Judge。
type Facts struct {
	Version       string
	ConfigPath    string
	Config        FileFact
	Parsed        *config.Config
	ParseErr      string
	GuardianRules GuardianRulesFact
	RuleReview    *rulereview.Report
	Probe         *Check
	Service       []Check
	// StatusSocketErr 非空 = Core 控制 socket 没应答。
	StatusSocketErr string
	Darwin          bool
	// Guardian 为 nil = 没问到 ⇒ darwin 上走「保守退路」:DNS unknown、恢复 failed/unknown。
	Guardian *GuardianFact
	Platform []Check
}

// Judge 把事实折成报告。**check 的名字、顺序、措辞是 --json 契约**,与迁移前的
// collectClientDoctorWith 逐字节相同(internal/cli 的 TestClientDoctorJSONReport 等守着)。
func Judge(f Facts) Report {
	rep := Report{Kind: "client", Version: f.Version, SecretsRedacted: true}
	udpMode := "proxy"
	rep.AddCheck("config", "info", f.ConfigPath, "")
	if f.Config.ReadErr != "" {
		rulesErr := f.GuardianRules.Err
		if rulesErr == "" && !f.Config.PermissionDenied {
			rulesErr = "配置不是因为权限读不到,不走 Guardian 退路"
		}
		if rulesErr == "" && f.GuardianRules.ConfigPath != f.ConfigPath {
			rulesErr = fmt.Sprintf("Guardian 读的是 %s,与要问的 %s 不是同一个文件", f.GuardianRules.ConfigPath, f.ConfigPath)
		}
		if rulesErr == "" && f.GuardianRules.Review == nil {
			rulesErr = "这一版 Guardian 没有发布规则体检"
		}
		if rulesErr != "" {
			rep.AddCheck("config_readable", "fail", f.Config.ReadErr, "sudo bx setup <client-link>")
		} else {
			rep.AddCheck("config_readable", "info",
				f.Config.ReadErr+";规则已改经 Guardian 读取(业主授权,无需 root);"+
					"其余依赖配置的检查(权限/解析/server link/udp 策略)本次缺席,要它们请用 sudo", "")
			for _, l := range RuleReviewLines(*f.GuardianRules.Review) {
				rep.AddCheck(RuleReviewCheckName(l.Key), l.Status, l.Value, l.Hint)
			}
		}
	} else {
		rep.AddCheck("config_readable", "ok", "yes", "")
		if f.Config.Mode0600 {
			rep.AddCheck("config_permissions", "ok", "0600", "")
		} else {
			rep.AddCheck("config_permissions", "warn", "not 0600", "chmod 600 "+f.ConfigPath)
		}
		if f.ParseErr != "" || f.Parsed == nil {
			rep.AddCheck("config_parse", "fail", f.ParseErr, "")
		} else {
			cfg := f.Parsed
			rep.AddCheck("config_parse", "ok", "yes", "")
			udpMode = cfg.UDP.Mode
			if cfg.Server == "" {
				rep.AddCheck("server_link", "fail", "empty", "sudo bx setup <client-link>")
			} else {
				rep.AddCheck("server_link", "ok", RedactLink(cfg.Server), "")
				if len(cfg.Transports) > 1 {
					rep.AddCheck("transports", "ok", fmt.Sprintf("%d 个传输(自动容灾)", len(cfg.Transports)), "")
				}
				if cfg.UDP.Transport != "" {
					rep.AddCheck("udp_transport", "ok", RedactLink(cfg.UDP.Transport), "")
				}
				if f.Probe != nil {
					rep.AddReport(*f.Probe)
				}
				if f.RuleReview != nil {
					for _, l := range RuleReviewLines(*f.RuleReview) {
						rep.AddCheck(RuleReviewCheckName(l.Key), l.Status, l.Value, l.Hint)
					}
				}
			}
		}
	}
	for _, c := range f.Service {
		rep.AddReport(c)
	}
	if f.StatusSocketErr != "" {
		rep.AddCheck("status_socket", "warn", f.StatusSocketErr, "bx logs")
	} else {
		rep.AddCheck("status_socket", "ok", "reachable", "")
	}
	status, detail, hint := UDPPolicy(udpMode)
	rep.AddCheck("udp_policy", status, detail, hint)
	if f.Darwin {
		g := f.Guardian
		if g == nil {
			// 与迁移前 guardianStatusFallback(darwin) 相同:Protection NeedsAttention 不进
			// doctor,进的只有 DNS(空 ⇒ unknown)与 Recovery(failed/unknown/recovery_unavailable)。
			g = &GuardianFact{Recovery: RecoveryFact{State: "failed", Stage: "unknown", ErrorCode: "recovery_unavailable"}}
		}
		rep.AddReport(DNSCheck(g.DNS))
		rep.AddReport(RecoveryCheck(g.Recovery))
	}
	for _, c := range f.Platform {
		rep.AddReport(c)
	}
	rep.OK = !rep.HasFail()
	return rep
}

func RedactLink(link string) string {
	switch {
	case strings.HasPrefix(link, "bx://"):
		return "bx://<redacted>"
	case strings.HasPrefix(link, "blink://"):
		return "blink://<legacy-redacted>"
	case strings.HasPrefix(link, "brook://"):
		return "internal-link:<redacted>"
	default:
		return "<redacted>"
	}
}

func UDPPolicy(mode string) (status, detail, hint string) {
	switch mode {
	case "proxy":
		return "ok", "non-DNS UDP relayed through bx tunnel", ""
	case "direct-realtime":
		return "warn", "non-DNS UDP direct; may expose real network path", "Use sudo bx realtime on to relay UDP through bx, or sudo bx realtime off to block it"
	default:
		return "warn", "non-DNS UDP blocked", "Google Meet/WebRTC may stutter; use sudo bx realtime on"
	}
}

func DNSCheck(d DNSFact) Check {
	state := d.State
	if state == "" {
		state = DNSStateUnknown
	}
	detail := fmt.Sprintf("state=%s managed=%t", state, d.Managed)
	if d.Service != "" {
		detail += " service=" + d.Service
	}
	if state == DNSStateManaged && d.Managed {
		return Check{Name: "guardian_dns", Status: "ok", Detail: detail}
	}
	// NotNeeded 是健康态(linux:数据面自己管,dns_managed 如实为 false)。
	if state == DNSStateNotNeeded {
		return Check{Name: "guardian_dns", Status: "ok", Detail: detail}
	}
	return Check{Name: "guardian_dns", Status: "fail", Detail: detail, Hint: "sudo bx up; bx logs"}
}

func RecoveryCheck(r RecoveryFact) Check {
	status := "ok"
	hint := ""
	switch r.State {
	case "accepted", "running":
		status = "info"
	case "failed":
		status = "warn"
		hint = "bx logs --json; bx reconnect (troubleshooting only)"
	}
	detail := fmt.Sprintf("state=%s stage=%s attempt=%d", r.State, r.Stage, r.Attempt)
	if r.ErrorCode != "" {
		detail += " error_code=" + r.ErrorCode
	}
	return Check{Name: "network_recovery", Status: status, Detail: detail, Hint: hint}
}
```

**`DNSStateNotNeeded` 的字面量**:执行前 `grep -n "DNSNotNeeded" internal/guardian/types.go`,把常量值抄成与 `guardian.DNSNotNeeded` 完全相同的字符串(上面的 `"not_needed"` 是占位猜测,**以 grep 结果为准**)。

- [ ] **Step 4: 把规则体检的判据搬到 `internal/doctor/rulereview.go`**

从 `internal/cli/rulereview.go` **剪切**以下定义到新文件 `internal/doctor/rulereview.go`(包名 `doctor`),并按下表改名(导出):

| 原名(cli) | 新名(doctor) |
|---|---|
| `type doctorFinding` | `type Finding` |
| `const deadRulesCheckName` | `const DeadRulesCheckName` |
| `func ruleReviewDoctorLines` | `func RuleReviewLines` |
| `func riskyRuleFinding` | `func riskyRuleFinding`(不导出) |
| `func summarizeClass` | `func summarizeClass` |
| `func builtinListLines` | `func builtinListLines` |
| `func classFindingsExcludingKinds` | 同名 |
| `func builtinListSourceSuffix` | 同名 |
| `func classKindFindings` | 同名 |
| `func summarizeFindings` | 同名 |
| `func ruleReviewCheckName` | `func RuleReviewCheckName` |

`guardianRulesForDoctor`(它拨 Guardian,是采集)与 `buildRuleReviewInput`(组装)**留在 cli**。搬完后在 `internal/cli/rulereview.go` 留薄壳,让既有调用点与测试不动:

```go
type doctorFinding = doctor.Finding

const deadRulesCheckName = doctor.DeadRulesCheckName

func ruleReviewDoctorLines(rep rulereview.Report) []doctorFinding { return doctor.RuleReviewLines(rep) }

func ruleReviewCheckName(key string) string { return doctor.RuleReviewCheckName(key) }
```

若 `internal/cli` 的测试直接调了 `riskyRuleFinding`/`summarizeClass` 等未导出函数(`grep -n "riskyRuleFinding\|summarizeClass\|builtinListLines" internal/cli/*_test.go`),把那些测试**整体搬到** `internal/doctor/rulereview_test.go`(改包名、改成调新名);它们是判据的测试,该跟判据走。

- [ ] **Step 5: 跑本包测试**

Run: `go test ./internal/doctor/ -v 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: 全部 PASS(含纯度守卫)。若 `config.Config` 的 `UDP` 字段名与上面测试里的 `cfg.UDP.Mode` / `cfg.UDP.Transport` 不符,以 `internal/config` 的真实字段名为准改测试,**不改判据的语义**。

- [ ] **Step 6: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/doctor/ internal/cli/
go build ./... && git add internal/doctor/ internal/cli/rulereview.go
git commit -m "feat(doctor): internal/doctor 纯判据包 —— Judge(Facts) 与规则体检判据从 cli 搬出,供 CLI 与 Guardian 共用

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 6: `internal/cli` 改为「采集 Facts → doctor.Judge」,JSON 逐字节不变

**Files:**
- Create: `internal/cli/doctor_facts.go`
- Modify: `internal/cli/cli.go`(`checkReport`/`doctorReport` 别名、`collectClientDoctorWith`、删除 `udpPolicyDoctor`/`redactLink`/`guardianDNSDoctorCheck`/`recoveryDoctorCheck` 的本体改为薄壳)
- Test: `internal/cli/doctor_facts_test.go`

**Interfaces:**
- Consumes: `doctor.Facts`、`doctor.Judge`、`doctor.DNSFact`、`doctor.RecoveryFact`、`doctor.GuardianFact`、`doctor.Check`(Task 5)。
- Produces:
  ```go
  type checkReport = doctor.Check
  type doctorReport = doctor.Report
  func collectDoctorFacts(configPath, target string, timeout time.Duration, skipProbe, includePlatformChecks bool) doctor.Facts
  func collectClientDoctorWith(...) doctorReport   // = doctor.Judge(collectDoctorFacts(...))
  func guardianFactFrom(status guardian.Status) doctor.GuardianFact   // 纯转换
  ```

- [ ] **Step 1: 先写守卫(红)**

```go
// internal/cli/doctor_facts_test.go
package cli

import (
	"os"
	"regexp"
	"testing"

	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/guardian"
)

// 三个 DNS 状态常量必须与 guardian 那份逐字相同 —— doctor 不能 import guardian(成环),
// 只能各写一份,这条守卫让它们不漂。
func TestDoctorDNSStateConstantsMatchGuardian(t *testing.T) {
	for _, pair := range []struct{ got, want string }{
		{doctor.DNSStateUnknown, string(guardian.DNSUnknown)},
		{doctor.DNSStateManaged, string(guardian.DNSManaged)},
		{doctor.DNSStateNotNeeded, string(guardian.DNSNotNeeded)},
	} {
		if pair.got != pair.want {
			t.Fatalf("doctor 的 DNS 常量 %q ≠ guardian 的 %q", pair.got, pair.want)
		}
	}
}

// guardian.Status → GuardianFact 的转换是纯的、逐字段的。
func TestGuardianFactFromStatusCarriesEveryField(t *testing.T) {
	st := guardian.Status{DNSState: "managed", DNSManaged: true, DNSService: "Wi-Fi",
		Recovery: guardian.RecoverySnapshot{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"}}
	got := guardianFactFrom(st)
	if got.DNS != (doctor.DNSFact{State: "managed", Managed: true, Service: "Wi-Fi"}) {
		t.Fatalf("DNS = %+v", got.DNS)
	}
	if got.Recovery != (doctor.RecoveryFact{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"}) {
		t.Fatalf("Recovery = %+v", got.Recovery)
	}
}

// **判据只有一份。** cli 里除了薄壳,不许再有第二处产出 doctor check 的判断:
// collectClientDoctorWith 的函数体必须就是「采集 → doctor.Judge」。
func TestClientDoctorIsJudgedByTheDoctorPackage(t *testing.T) {
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)func collectClientDoctorWith\([^)]*\) doctorReport \{(.*?)\n\}`).FindStringSubmatch(string(src))
	if body == nil {
		t.Fatal("找不到 collectClientDoctorWith —— 守卫读不懂现在的代码,先修守卫")
	}
	if !regexp.MustCompile(`return doctor\.Judge\(collectDoctorFacts\(`).MatchString(body[1]) {
		t.Fatalf("collectClientDoctorWith 不是「采集 → doctor.Judge」:\n%s", body[1])
	}
	if regexp.MustCompile(`AddCheck\(|addCheck\(`).MatchString(body[1]) {
		t.Fatal("collectClientDoctorWith 里仍在自己产出 check —— 判据长回了 cli")
	}
}
```

Run: `go test ./internal/cli/ -run 'TestDoctorDNSState|TestGuardianFactFrom|TestClientDoctorIsJudged' 2>&1 | tail -3`
Expected: 编译失败(`guardianFactFrom` 未定义)。

- [ ] **Step 2: 别名与采集**

`internal/cli/cli.go`:把 `type checkReport struct {…}` 与 `type doctorReport struct {…}` 两个定义(约 198–214 行)整体换成

```go
// checkReport / doctorReport 是 internal/doctor 那两个类型的别名:判据搬去了那边,
// 这里 80 处使用(server doctor、inspect、MCP 渲染)一个不用改。
type checkReport = doctor.Check

type doctorReport = doctor.Report
```

并删除 `func (r *doctorReport) addCheck`、`addReport`、`hasFail` 三个方法(别名类型不能再定义方法),把全文件对 `.addCheck(` / `.addReport(` / `.hasFail()` 的调用改成 `.AddCheck(` / `.AddReport(` / `.HasFail()`:

```bash
sed -i '' 's/\.addCheck(/.AddCheck(/g; s/\.addReport(/.AddReport(/g; s/\.hasFail()/.HasFail()/g' internal/cli/*.go
```

`udpPolicyDoctor`、`redactLink`、`guardianDNSDoctorCheck`、`recoveryDoctorCheck` 四个函数的本体改成薄壳:

```go
func udpPolicyDoctor(mode string) (status, detail, hint string) { return doctor.UDPPolicy(mode) }

func redactLink(link string) string { return doctor.RedactLink(link) }

func guardianDNSDoctorCheck(status guardian.Status) checkReport { return doctor.DNSCheck(guardianFactFrom(status).DNS) }

func recoveryDoctorCheck(snapshot guardian.RecoverySnapshot) checkReport {
	return doctor.RecoveryCheck(doctor.RecoveryFact{State: snapshot.State, Stage: snapshot.Stage, Attempt: snapshot.Attempt, ErrorCode: snapshot.ErrorCode})
}
```

新文件 `internal/cli/doctor_facts.go`:

```go
package cli

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/embedded"
	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/rulereview"
	"github.com/getbx/bx/internal/stats"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

// guardianFactFrom 把 Guardian 的状态折成判据要的两个事实。纯转换,逐字段。
func guardianFactFrom(st guardian.Status) doctor.GuardianFact {
	return doctor.GuardianFact{
		DNS:      doctor.DNSFact{State: st.DNSState, Managed: st.DNSManaged, Service: st.DNSService},
		Recovery: doctor.RecoveryFact{State: st.Recovery.State, Stage: st.Recovery.Stage, Attempt: st.Recovery.Attempt, ErrorCode: st.Recovery.ErrorCode},
	}
}

// collectDoctorFacts 是 CLI 这一侧的事实采集:读文件、拨 Core / Guardian、探测、平台检查。
// **这里没有一句判断** —— 判断全在 doctor.Judge。每一步「没采到」都带原因进 Facts。
func collectDoctorFacts(configPath, target string, timeout time.Duration, skipProbe, includePlatformChecks bool) doctor.Facts {
	cfgPath := resolveConfigPath(configPath)
	f := doctor.Facts{Version: version.String(), ConfigPath: cfgPath, Darwin: runtime.GOOS == "darwin"}
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		f.Config.ReadErr = err.Error()
		f.Config.PermissionDenied = errors.Is(err, fs.ErrPermission)
		review, guardianPath, rulesErr := guardianRulesForDoctor()
		f.GuardianRules = doctor.GuardianRulesFact{Review: review, ConfigPath: guardianPath}
		if rulesErr != nil {
			f.GuardianRules.Err = rulesErr.Error()
		}
	} else {
		f.Config.Bytes = b
		f.Config.Mode0600 = modeCheck(cfgPath, 0o600)
		cfg, perr := config.Parse(b)
		if perr != nil {
			f.ParseErr = perr.Error()
		} else {
			f.Parsed = cfg
			if cfg.Server != "" {
				if !skipProbe {
					probe := probeCheck(cfg.Server, target, timeout)
					f.Probe = &probe
				}
				review := rulereview.Review(buildRuleReviewInput(cfg, embedded.ChinaDomain(), func() (stats.Report, error) {
					return supervisor.FetchStatusReport(statusSocketPath())
				}))
				f.RuleReview = &review
			}
		}
	}
	f.Service = serviceDoctorChecks(runtime.GOOS, guardianServiceChecks, systemdServiceChecks)
	if err := checkStatusSocket(); err != nil {
		f.StatusSocketErr = err.Error()
	}
	if runtime.GOOS == "darwin" {
		if st, err := readGuardianStatus(); err == nil {
			g := guardianFactFrom(st)
			f.Guardian = &g
		}
	}
	if includePlatformChecks {
		f.Platform = collectPlatformChecks(context.Background())
	}
	return f
}
```

`collectClientDoctorWith` 的整个函数体换成一行:

```go
func collectClientDoctorWith(configPath, target string, timeout time.Duration, skipProbe, includePlatformChecks bool) doctorReport {
	return doctor.Judge(collectDoctorFacts(configPath, target, timeout, skipProbe, includePlatformChecks))
}
```

**注意一处等价性**:迁移前 `readGuardianStatus()` 失败时走 `guardianStatusFallback(stats.Report{}, "darwin")`,它给的 DNS 是空串(⇒ unknown)、Recovery 是 failed/unknown/recovery_unavailable;`Judge` 在 `Guardian == nil` 时造的退路与之逐字相同(Task 5 的 `TestJudgeDarwinGuardianChecksWithAndWithoutGuardian` 钉着)。`rulereview.Review` 返回的是值还是指针,以其真实签名为准(`grep -n "^func Review" internal/rulereview/*.go`),对应改 `f.RuleReview` 的取址。

- [ ] **Step 3: 跑 cli 全部测试(既有的 doctor 测试是等价性的回归网)**

Run: `go build ./... && go test ./internal/cli/ 2>&1 | tail -3`
Expected: `ok`。特别关注 `TestClientDoctorJSONReport`、`TestClientDoctorIncludesPlatformChecks`、`TestCollectClientDoctorWithIncludePlatformChecksToggle`、`TestDoctorReportHasNoDuplicateCheckNames`、`doctor_outcomes_test.go`、`doctor_service_test.go` 全绿;若哪条红,**是 Facts→Judge 的转写漏了某个分支**,修 Judge/采集,不改测试。

- [ ] **Step 4: 逐字节对比(在本机跑一次,不要求 root)**

用两个 worktree 对比(**不要用 `git stash`**,本仓库明令禁止,它会卷走未提交的工作):

```bash
git worktree add /tmp/bx-before HEAD~2 >/dev/null 2>&1 || git worktree add /tmp/bx-before HEAD~1
(cd /tmp/bx-before && go run ./cmd/bx doctor --json --skip-probe > /tmp/doctor-before.json 2>/dev/null)
go run ./cmd/bx doctor --json --skip-probe > /tmp/doctor-after.json 2>/dev/null
diff /tmp/doctor-before.json /tmp/doctor-after.json && echo "✅ --json 逐字节相同"
git worktree remove /tmp/bx-before --force
```
Expected: `✅ --json 逐字节相同`(`HEAD~N` 取到 Task 5 之前那个提交;两次都是非 root、同一台机器、`--skip-probe`,输出应完全一致)。若 `version` 字段因 `go run` 的构建信息不同而不同,用 `jq 'del(.version)'` 两边都去掉再 diff。

- [ ] **Step 5: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/cli/
git add internal/cli/
git commit -m "refactor(cli): bx doctor --json 改为「采集 Facts → doctor.Judge」,输出逐字节不变;check 类型改为 doctor 的别名

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: `bx doctor` 文本路径渲染同一份 Report

**Files:**
- Modify: `internal/cli/cli.go:1562-1658`(`doctorAction`)
- Test: `internal/cli/doctor_text_test.go`

**Interfaces:**
- Consumes: `collectClientDoctorWith`(Task 6)、`doctorLine(status, key, value)`(既有)、`doctorOutcomeChecks(doctorTrafficFacts(ctx))`(既有,文本路径独有)。
- Produces: `func renderDoctorReport(rep doctorReport) []string`(纯渲染:每条 check 一行 `doctorLine` 的 (status, key, value),hint 非空再来一行 `hint`;key = `strings.ReplaceAll(name, "_", " ")`)。

**刻意的行为变化(写进提交信息)**:文本输出此前是一份独立手写的清单(与 `--json` 不同:没有 `udp_policy` / `guardian_dns`,有 `doctorOutcomeChecks` 的流量行)。现在文本 = 同一份 `Report` 逐条渲染 + 流量行(文本路径独有,保留)。文本是给人看的,不是契约;`--json` 不变。

- [ ] **Step 1: 写失败测试**

```go
// internal/cli/doctor_text_test.go
package cli

import (
	"regexp"
	"strings"
	"testing"
)

func TestRenderDoctorReportPrintsEveryCheckAndItsHint(t *testing.T) {
	rep := doctorReport{Checks: []checkReport{
		{Name: "config_readable", Status: "ok", Detail: "yes"},
		{Name: "udp_policy", Status: "warn", Detail: "non-DNS UDP blocked", Hint: "use sudo bx realtime on"},
	}}
	lines := renderDoctorReport(rep)
	want := []string{"ok|config readable|yes", "warn|udp policy|non-DNS UDP blocked", "hint|udp policy|use sudo bx realtime on"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("\n got %q\nwant %q", lines, want)
	}
}

// 文本路径不许再有自己的一份判据:doctorAction 里除了渲染与流量行,不许出现
// 「读配置 / 解析 / 拨 socket」这些采集动作。
func TestDoctorTextPathRendersTheSharedReport(t *testing.T) {
	src := menuGoSource(t, "cli.go")
	body := regexp.MustCompile(`(?s)func doctorAction\(c \*cli\.Context\) \(err error\) \{(.*?)\n\}`).FindStringSubmatch(src)
	if body == nil {
		t.Fatal("找不到 doctorAction")
	}
	if !strings.Contains(body[1], "renderDoctorReport(collectClientDoctor(") {
		t.Fatal("文本路径没有渲染共享的 Report")
	}
	for _, forbidden := range []string{"os.ReadFile(", "config.Parse(", "checkStatusSocket()", "readGuardianStatus()", "collectPlatformChecks("} {
		if strings.Contains(body[1], forbidden) {
			t.Fatalf("doctorAction 里仍有自己的采集 %s —— 判据又分叉了", forbidden)
		}
	}
}
```

`menuGoSource` 不存在的话,在同文件加:

```go
func menuGoSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读不到 %s: %v", name, err)
	}
	return string(b)
}
```
(并 import `os`。)

- [ ] **Step 2: 跑确认红**

Run: `go test ./internal/cli/ -run 'TestRenderDoctorReport|TestDoctorTextPath' 2>&1 | tail -3`
Expected: `undefined: renderDoctorReport`。

- [ ] **Step 3: 实现**

在 `internal/cli/cli.go` 加:

```go
// renderDoctorReport 把共享的 Report 渲染成文本路径的行:每条 check 一行,
// hint 非空再来一行。**文本与 --json 从此是同一份判据的两种渲染。**
// 返回 "status|key|value" 三段,供 doctorAction 逐行交给 doctorLine。
func renderDoctorReport(rep doctorReport) []string {
	var out []string
	for _, c := range rep.Checks {
		key := strings.ReplaceAll(c.Name, "_", " ")
		out = append(out, c.Status+"|"+key+"|"+c.Detail)
		if c.Hint != "" {
			out = append(out, "hint|"+key+"|"+c.Hint)
		}
	}
	return out
}
```

`doctorAction` 从 `fmt.Println("bx doctor")` 起到函数末尾,换成:

```go
	fmt.Println("bx doctor")
	doctorLine("ok", "version", version.String())
	for _, line := range renderDoctorReport(collectClientDoctor(c.String("config"), c.String("target"), c.Duration("timeout"), c.Bool("skip-probe"))) {
		parts := strings.SplitN(line, "|", 3)
		doctorLine(parts[0], parts[1], parts[2])
	}
	// 流量成败那几行是文本路径独有的(它们不在 --json 契约里,加进去会改契约)。
	for _, check := range doctorOutcomeChecks(doctorTrafficFacts(c.Context)) {
		doctorLine(check.Status, check.Name, check.Detail)
		if check.Hint != "" {
			doctorLine("hint", check.Name, check.Hint)
		}
	}
	return nil
```

注意:`collectClientDoctor`(不带 With)已经是 `includePlatformChecks=true`(看它的定义 `cli.go:2278`),与原文本路径末尾那段 `collectPlatformChecks` 等价。删掉 `doctorAction` 里因此不再被引用的 `checkFileMode`、`doctorProbe`、`darwinServiceDoctorLines`、`boolStatus`/`serviceState`(文本专用)等函数**前先 grep 它们还有没有别的调用方**(`grep -n "doctorProbe(\|darwinServiceDoctorLines(\|checkFileMode(" internal/cli/*.go`),有就留,没有就删连同它们的测试。

- [ ] **Step 4: 跑全部 + 手看一次文本输出**

Run: `go test ./internal/cli/ 2>&1 | tail -2 && go run ./cmd/bx doctor --skip-probe 2>&1 | head -30`
Expected: `ok`;文本里每条 check 一行、hint 紧随其后、末尾是流量行。若有既有测试断言文本路径的具体行(`grep -n '"bx doctor"' internal/cli/*_test.go`),按新渲染更新它的期望 —— 那是渲染改变,不是判据改变。

- [ ] **Step 5: 全量 verify、提交、记档**

```bash
bash scripts/verify.sh && git add -A internal/cli && git commit -m "refactor(cli): bx doctor 文本路径渲染共享的 Report —— 文本与 --json 从此是同一份判据的两种渲染

文本输出因此多了 udp_policy / guardian_dns 等此前只在 --json 里的行,流量行保留;
文本是给人看的,不是契约。

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

在 `CLAUDE.md` 的「AI-native 诊断面」一节之后加一段(≤ 15 行):`internal/doctor` 是纯判据、`bx doctor` 两条路径与将来的 `/v1/doctor` 共用 `Judge`、DNS 常量跨包守卫、`/v1/logs` 的发布面记录与能力名、菜单 Show Details 取代 root 路径;真机未验清单:Show Details 高亮、Open Logs 日志页。提交:`docs: 记档 /v1/logs 与 internal/doctor`。

---

## 自审

- **Spec 覆盖**:§2 → Task 1–2;§5 → Task 4;§6 日志页 + Export 出口 → Task 4;§3 判据/采集分离 + JSON 不变 → Task 5–7;§7 守卫:纯度(Task 5)、`/v1/logs` 路径来源与 NewLocalAPI 接线(Task 2)、root 路径字面量消失与四处弹窗走同一出口(Task 4)、判据只有一份(Task 6、7)、能力声明(Task 2/3)。**未在本计划内**:§3 的 `/v1/doctor` 与 Guardian 侧采集、§6 Checks 页、§4 Add Server、§7 shell-out 白名单守卫 —— 属 spec §8 的 ③④,下一份计划。
- **占位符**:`DNSStateNotNeeded` 的字面量标为「以 grep 为准」,守卫钉住;`rulereview.Review` 返回值/指针以真实签名为准 —— 两处都是执行时一眼能定的事实,不是设计缺口。
- **类型一致性**:`LogSource`/`LogTail`/`LogsResponse`(Go)↔ `LogTail`/`LogsReport`(Swift)字段名逐一对应;`doctor.Check` 字段与 `checkReport` 逐字段相同(别名);`guardianFactFrom` 在 Task 6 定义、Task 5 的 `GuardianFact` 类型在 doctor.go 里已定义(测试里用到的 `GuardianFact` 名字与实现一致)。
