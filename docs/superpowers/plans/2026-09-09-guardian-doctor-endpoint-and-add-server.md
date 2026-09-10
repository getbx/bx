# Guardian `/v1/doctor` + Diagnostics Checks 页 + Add Server(spec §8 的 ③ 与 ④)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 「Check for Problems」不再开终端(Guardian 在进程内采集事实、喂同一个 `doctor.Judge`,菜单渲染 Checks 页);「Replace Configuration…」变成 Servers 窗口里的「Add Server…」(加进清单并热切换,不弹密码、不断网);并把 spec §7 的 shell-out 白名单守卫补上。

**Architecture:** 判据仍只有一份(`internal/doctor.Judge`),本计划只加**第二个采集方**(Guardian 进程内)。为此把两块只有 cli 有的事实采集/判据下沉到两边都能引的位置:平台检查 → 新叶子包 `internal/platformcheck`;darwin 服务三行 → `internal/doctor.DarwinServiceChecks`。菜单侧新增纯模型 `DiagnosticsModel.swift`(解码/排序/合计),`DiagnosticsWindow` 加 Checks 页;Add Server 复用已有的 `/v1/servers add` + 热切换,Guardian 侧补「同名 409」与「名字可省略」。

**Tech Stack:** Go 1.26(`net/http` unix socket、`httptest`)、Swift 5.9 AppKit(SwiftPM + `scripts/test-macos-menu.sh` 单文件套件)、Go 读源码守卫(`internal/cli` 的 `swiftFunctionBody` / `swiftFunctionDefs` / `menuMainSwiftCode`)。

**Spec:** `docs/superpowers/specs/2026-09-09-guardian-owns-diagnostics-and-config-design.md`(§1、§3、§4、§6、§7、§8 ③④)。前置:`docs/superpowers/plans/2026-09-09-guardian-logs-and-doctor-extraction.md` 已落地(`0ba8392..c9e42ca`)。

## Global Constraints

- 验证一律 `bash scripts/verify.sh --quick`,提交前跑一次全量 `bash scripts/verify.sh`。**判据是退出码。**
- Go 格式化 `$(go env GOPATH)/bin/gofumpt -l <dir>` 必须无输出(`internal/winfw/rules.go` 是 vendored 的既有 drift,忽略)。
- 提交信息中文 conventional commits,结尾带 `Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>`。
- **绝不 `git stash`;绝不用 `git checkout <path>` 还原未提交的工作;变异验证用 `cp` 到 /tmp 再 `cp` 回来。**
- Guardian 端点:owner 门一律 `authorizeOwnerPeer(r.Context(), ownerUID)`;响应体只带失败类别 `{"code":"…"}`,完整原因只进 Guardian 日志;JSON 一律经 `writeGuardianJSON`。能力声明 `GuardianCapabilities()` 每次返回新切片。菜单按能力决定画不画,**绝不试着拨一下看看**。
- `bx doctor --json` 的输出**逐字节不变**(`internal/doctor/testdata/judge_golden.json` + `TestJudgeGolden` 守着;本计划不改 `Judge` 的判据,只加输入)。
- `internal/doctor` 纯度(`purity_test.go`):本包文件不 import `net`/`net/http`/`os`/`os/exec`/`syscall`,不 import `internal/guardian`/`cli`/`supervisor`/`install`。`internal/platformcheck` 允许 `os`/`os/exec`(它就是采集),但不得 import `guardian`/`cli`。
- Swift 用户可见文案英文(`TestMacMenuUserFacingStringsAreEnglish`);`main.swift` 不得新增 `Process`/spawn、`Timer.scheduledTimer`;新增 Swift 测试文件必须登记进 `scripts/test-macos-menu.sh`。
- 既有守卫可重新锚定,不许改弱;变异验证「全绿」先查变异落没落上。
- `/v1/doctor` 的探测是 root 出网:只在用户显式点了才发生,循环里绝不自动跑;整轮封顶 10 秒。
- 绝不启动 bx、不改路由;真机验收交给项目所有者。

---

## 文件结构

**Task 1** `internal/platformcheck/`(新包:从 `internal/cli` 搬来的平台检查)· `internal/cli/platform_check.go`(薄壳)
**Task 2** `internal/doctor/service.go`(`DarwinServiceChecks` 等纯判据)· `internal/guardian/doctorfact.go`(`DoctorGuardianFact`)· `internal/cli/cli.go`(薄壳)
**Task 3** `internal/guardian/doctor.go`(采集 + `/v1/doctor` handler + `CapabilityDoctor`)· `localapi.go` / `daemon.go` / `types.go`(接线)
**Task 4** `apps/macos/BxMenu/Sources/BxMenu/DiagnosticsModel.swift`(纯)· `Tests/DiagnosticsModelTests.swift` · `GuardianClient.swift`(`.doctor`)· `scripts/test-macos-menu.sh`
**Task 5** `DiagnosticsWindow.swift`(Checks 页)· `main.swift`(`runDoctorFromMenu` → Checks 页)· `internal/cli/macos_menu_doctor_test.go`
**Task 6** `internal/guardian/servers.go`(add:同名 409、名字可省略、应答带 `added`)· `servers_test.go`
**Task 7** `ServersWindow.swift`(Add Server… 取代 Replace Configuration…)· `ServersModel.swift`(`added` 解码、`addServerPlan`)· `GuardianClient.swift`(`.addServer`)· `main.swift`(add+switch 流)· `internal/cli/macos_menu_addserver_test.go`
**Task 8** `internal/cli/macos_menu_shellout_allowlist_test.go`(§7 白名单)· `CLAUDE.md`

---

### Task 1: 平台检查下沉到 `internal/platformcheck`

**Files:**
- Create: `internal/platformcheck/platformcheck.go`(`Collect`、`TerminalProxyChecks`,原 `platform_check.go` + `platform_check_common.go`)
- Create: `internal/platformcheck/darwin.go`(原 `internal/cli/platform_check_darwin.go`,`//go:build darwin`)
- Create: `internal/platformcheck/tunnel_claims_darwin.go`(原 `internal/cli/tunnel_claims_darwin.go`)
- Create: `internal/platformcheck/darwin_test.go`、`tunnel_claims_darwin_test.go`(原两份测试搬家)
- Delete: `internal/cli/platform_check_darwin.go`、`platform_check_common.go`、`tunnel_claims_darwin.go`、`platform_check_darwin_test.go`、`tunnel_claims_darwin_test.go`
- Modify: `internal/cli/platform_check.go` → 只剩两个薄壳
- Test: `internal/platformcheck/purity_test.go`(新)

**Interfaces:**
- Produces:
  ```go
  package platformcheck
  type Check = doctor.Check
  func Collect(ctx context.Context) []Check           // 原 cli.collectPlatformChecks
  func TerminalProxyChecks() []Check                  // 原 cli.collectTerminalProxyChecks(cli_test.go 在用)
  ```
  cli 侧保留 `func collectPlatformChecks(ctx context.Context) []checkReport { return platformcheck.Collect(ctx) }` 与 `func collectTerminalProxyChecks() []checkReport { return platformcheck.TerminalProxyChecks() }`。

- [ ] **Step 1: 写纯度/边界守卫(红:包不存在)**

```go
// internal/platformcheck/purity_test.go
package platformcheck

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本包是**采集**(允许 os/exec),但它要被 Guardian 与 CLI 两边引,所以不许
// 反向依赖任一控制面:import guardian 会成环,import cli 把它拖回单一消费方。
func TestPlatformcheckDoesNotDependOnControlPlanes(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录: %v", err)
	}
	banned := map[string]string{
		"github.com/getbx/bx/internal/guardian": "guardian 要调本包,反向 import 成环",
		"github.com/getbx/bx/internal/cli":      "cli 是消费方之一,不许反向依赖",
		"github.com/getbx/bx/internal/install":  "装机层不该被诊断采集拖进来",
	}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, spec := range f.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if why, bad := banned[path]; bad {
				t.Errorf("%s import 了 %q —— %s", name, path, why)
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个源文件都没检查到 —— 守卫读不懂目录结构")
	}
}

// TerminalProxyChecks 在没有任何代理环境变量时必须给一条 info(不是空切片):
// 「查了、没有」与「没查」要分得开。
func TestTerminalProxyChecksReportsUnsetAsInfo(t *testing.T) {
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		t.Setenv(k, "")
	}
	got := TerminalProxyChecks()
	if len(got) != 1 || got[0].Name != "terminal_proxy" || got[0].Status != "info" || got[0].Detail != "not set" {
		t.Fatalf("未设置代理时 = %+v", got)
	}
	t.Setenv("HTTPS_PROXY", "http://user:pw@proxy.local:3128")
	got = TerminalProxyChecks()
	if len(got) != 1 || got[0].Status != "ok" || strings.Contains(got[0].Detail, "pw@") || !strings.Contains(got[0].Detail, "<redacted>@proxy.local") {
		t.Fatalf("凭据必须脱敏:%+v", got)
	}
}
```

- [ ] **Step 2: 跑确认红**

Run: `go test ./internal/platformcheck/ 2>&1 | head -3`
Expected: `no Go files` / build failed。

- [ ] **Step 3: 搬文件(`git mv`,保住历史),改包名与类型名**

```bash
mkdir -p internal/platformcheck
git mv internal/cli/platform_check_darwin.go        internal/platformcheck/darwin.go
git mv internal/cli/platform_check_darwin_test.go   internal/platformcheck/darwin_test.go
git mv internal/cli/tunnel_claims_darwin.go         internal/platformcheck/tunnel_claims_darwin.go
git mv internal/cli/tunnel_claims_darwin_test.go    internal/platformcheck/tunnel_claims_darwin_test.go
git mv internal/cli/platform_check_common.go        internal/platformcheck/platformcheck.go
# 包名与类型名
sed -i '' 's/^package cli$/package platformcheck/' internal/platformcheck/*.go
sed -i '' 's/\bcheckReport\b/Check/g' internal/platformcheck/*.go
# 导出两个入口
sed -i '' 's/^func collectPlatformChecks(/func Collect(/; s/\bcollectPlatformChecks(/Collect(/g' internal/platformcheck/*.go
sed -i '' 's/^func collectTerminalProxyChecks(/func TerminalProxyChecks(/; s/\bcollectTerminalProxyChecks(/TerminalProxyChecks(/g' internal/platformcheck/*.go
```

在 `internal/platformcheck/platformcheck.go` 顶部(`package platformcheck` 之后)加:

```go
import (
	"context"
	"os"
	"strings"

	"github.com/getbx/bx/internal/doctor"
)

// Check 与 doctor.Check 是同一个类型:平台检查的产物直接进 doctor.Facts.Platform,
// 两边(CLI / Guardian)不必各转一次。
type Check = doctor.Check
```

`Collect` 的非 darwin 版本(原 `internal/cli/platform_check.go` 的内容)搬进 `platformcheck.go` 末尾并加 build tag 文件:新建 `internal/platformcheck/other.go`:

```go
//go:build !darwin

package platformcheck

import "context"

func Collect(_ context.Context) []Check { return TerminalProxyChecks() }
```

(darwin 的 `Collect` 已在 `darwin.go` 里由 sed 改名。)`darwin.go` 的 import 块把 `checkReport` 所在的 cli 依赖清干净;`tunnel_claims_darwin.go` 的 `leakcheck`/`supervisor` import 原样保留。

- [ ] **Step 4: cli 侧薄壳**

`internal/cli/platform_check.go` 整个改成:

```go
package cli

import (
	"context"

	"github.com/getbx/bx/internal/platformcheck"
)

// 平台检查下沉到 internal/platformcheck(2026-09-09):它要被 Guardian 的
// /v1/doctor 采集与这里的 bx doctor 两边引。这两个壳只为既有调用点与测试保名字。
func collectPlatformChecks(ctx context.Context) []checkReport { return platformcheck.Collect(ctx) }

func collectTerminalProxyChecks() []checkReport { return platformcheck.TerminalProxyChecks() }
```

搬走的文件里若有被 cli 别处引用的辅助函数(`oneLine`、`truncateDetail`、`redactProxyValue` —— 控制器 grep 过:cli 里**没有**别的调用方),不必留壳。`go build ./... && go vet ./internal/cli/ ./internal/platformcheck/` 必须过;编不过就说明还有 cli 引用被搬走的未导出名 —— 按编译错误补壳或改调用,**不搬回来**。

- [ ] **Step 5: 跑测试**

Run: `go test ./internal/platformcheck/ ./internal/cli/ 2>&1 | tail -3`
Expected: 两个 `ok`(搬过去的两份 darwin 测试在 darwin 上照跑;`cli_test.go` 里那一处 `collectTerminalProxyChecks` 经壳仍绿)。

- [ ] **Step 6: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/platformcheck/ internal/cli/
git add -A internal/platformcheck internal/cli
git commit -m "refactor(platformcheck): 平台检查从 cli 下沉成叶子包 —— Guardian 的 /v1/doctor 与 bx doctor 共用同一份采集

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 2: `doctor.DarwinServiceChecks` + `guardian.DoctorGuardianFact`

**Files:**
- Create: `internal/doctor/service.go`
- Create: `internal/doctor/service_test.go`
- Create: `internal/guardian/doctorfact.go`
- Modify: `internal/cli/cli.go`(`darwinServiceChecks`、`boolStatus`、`serviceStatusFromState`、`hintForState`、`darwinGuardianServiceName` 变薄壳/别名)
- Modify: `internal/cli/doctor_facts.go`(`guardianFactFrom` 变薄壳)

**Interfaces:**
- Produces:
  ```go
  package doctor
  const DarwinGuardianServiceName = "com.getbx.bx.guard"
  func BoolStatus(ok bool) string                                  // "ok"/"fail"
  func ServiceStatusFromState(action, state string) string         // 原 cli.serviceStatusFromState,原样
  func HintForState(state, primary, logs string) string            // 原 cli.hintForState,原样
  func DarwinServiceChecks(installed, active bool) []Check         // 原 cli.darwinServiceChecks,原样
  package guardian
  func DoctorGuardianFact(st Status) doctor.GuardianFact           // 原 cli.guardianFactFrom
  ```

- [ ] **Step 1: 写失败测试**

```go
// internal/doctor/service_test.go
package doctor

import "testing"

// 三行服务检查是 --json 契约的一部分(名字/状态/detail/hint),搬家不许改一个字。
func TestDarwinServiceChecksThreeRows(t *testing.T) {
	up := DarwinServiceChecks(true, true)
	if len(up) != 3 {
		t.Fatalf("要三行,实际 %d", len(up))
	}
	if up[0] != (Check{Name: "service_installed", Status: "ok", Detail: DarwinGuardianServiceName}) {
		t.Fatalf("installed = %+v", up[0])
	}
	if up[1] != (Check{Name: "service_active", Status: "ok", Detail: "active", Hint: "sudo bx up"}) {
		t.Fatalf("active = %+v", up[1])
	}
	if up[2] != (Check{Name: "service_enabled", Status: "ok", Detail: "enabled", Hint: "sudo bx up"}) {
		t.Fatalf("enabled = %+v", up[2])
	}
	down := DarwinServiceChecks(false, false)
	if down[0].Status != "fail" || down[0].Hint != "sudo bx setup <client-link>" {
		t.Fatalf("没装 = %+v", down[0])
	}
	if down[1].Status != "fail" || down[1].Detail != "inactive" || down[1].Hint != "bx logs" {
		t.Fatalf("没跑 = %+v", down[1])
	}
	if down[2].Status != "fail" || down[2].Detail != "disabled" {
		t.Fatalf("没启用 = %+v", down[2])
	}
}
```

**注意**:上面的期望值是按 `internal/cli/cli.go` 里 `darwinServiceChecks`/`serviceStatusFromState`/`hintForState` 的**当前**实现推的;实现时先读它们(`grep -n "^func darwinServiceChecks\|^func serviceStatusFromState\|^func hintForState\|^func boolStatus" internal/cli/cli.go`),**若期望值与真实输出不符,改测试的期望不改判据**(搬家零漂移是本任务的全部要点)。

```go
// internal/guardian/doctorfact_test.go
package guardian

import (
	"testing"

	"github.com/getbx/bx/internal/doctor"
)

func TestDoctorGuardianFactCarriesEveryField(t *testing.T) {
	st := Status{DNSState: "managed", DNSManaged: true, DNSService: "Wi-Fi",
		Recovery: RecoverySnapshot{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"}}
	got := DoctorGuardianFact(st)
	if got.DNS != (doctor.DNSFact{State: "managed", Managed: true, Service: "Wi-Fi"}) {
		t.Fatalf("DNS = %+v", got.DNS)
	}
	if got.Recovery != (doctor.RecoveryFact{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"}) {
		t.Fatalf("Recovery = %+v", got.Recovery)
	}
}
```

- [ ] **Step 2: 跑确认红**

Run: `go test ./internal/doctor/ -run TestDarwinServiceChecks 2>&1 | head -3; go test ./internal/guardian/ -run TestDoctorGuardianFact 2>&1 | head -3`
Expected: 两处 `undefined`。

- [ ] **Step 3: 实现**

`internal/doctor/service.go`:把 `internal/cli/cli.go` 里 `const darwinGuardianServiceName`、`func darwinServiceChecks`、`func boolStatus`、`func serviceStatusFromState`、`func hintForState` 四个函数**原样剪切**过来(函数体一个字不改),改名为导出名(见 Interfaces),`checkReport{…}` 改成 `Check{…}`,包名 `doctor`。文件头注释:

```go
// 服务三行的判据(darwin 上 Guardian 是 launchd 服务)。**从 internal/cli 原样搬来**:
// Guardian 的 /v1/doctor 采集要用同一份,而 cli 不能被 guardian import。
```

`internal/cli/cli.go` 对应位置改成薄壳(保住既有调用点与测试):

```go
const darwinGuardianServiceName = doctor.DarwinGuardianServiceName

func darwinServiceChecks(installed, active bool) []checkReport {
	return doctor.DarwinServiceChecks(installed, active)
}

func boolStatus(ok bool) string { return doctor.BoolStatus(ok) }

func serviceStatusFromState(action, state string) string { return doctor.ServiceStatusFromState(action, state) }

func hintForState(state, primary, logs string) string { return doctor.HintForState(state, primary, logs) }
```

`internal/guardian/doctorfact.go`:

```go
package guardian

import "github.com/getbx/bx/internal/doctor"

// DoctorGuardianFact 把 Guardian 的状态折成 doctor 判据要的两个事实。纯转换、逐字段。
// 住在 guardian 而不是 doctor:doctor 不能 import guardian(会成环),而 cli 与
// guardian 自己的 /v1/doctor 采集都要这一份 —— 两份拷贝就是漂移的起点。
func DoctorGuardianFact(st Status) doctor.GuardianFact {
	return doctor.GuardianFact{
		DNS:      doctor.DNSFact{State: string(st.DNSState), Managed: st.DNSManaged, Service: st.DNSService},
		Recovery: doctor.RecoveryFact{State: st.Recovery.State, Stage: st.Recovery.Stage, Attempt: st.Recovery.Attempt, ErrorCode: st.Recovery.ErrorCode},
	}
}
```

(`st.DNSState` 若已是 `string` 类型就不加 `string(...)`,以编译器为准。)`internal/cli/doctor_facts.go` 的 `guardianFactFrom` 改成 `return guardian.DoctorGuardianFact(st)`;`TestGuardianFactFromStatusCarriesEveryField` 原样保留(它现在守的是壳仍然委托)。

- [ ] **Step 4: 跑测试 + golden 不动**

Run: `go build ./... && go test ./internal/doctor/ ./internal/guardian/ ./internal/cli/ 2>&1 | tail -4`
Expected: 三个 `ok`;`TestJudgeGolden` 与 `TestClientDoctorJSONReport` 仍绿(服务行的措辞一个字没变)。

- [ ] **Step 5: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/doctor/ internal/guardian/ internal/cli/
git add internal/doctor/service.go internal/doctor/service_test.go internal/guardian/doctorfact.go internal/guardian/doctorfact_test.go internal/cli/cli.go internal/cli/doctor_facts.go
git commit -m "refactor(doctor): 服务三行判据与 Guardian 状态→事实的转换各只留一份 —— 给 Guardian 侧采集铺路

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 3: Guardian 进程内采集 + `GET /v1/doctor`

**Files:**
- Create: `internal/guardian/doctor.go`
- Create: `internal/guardian/doctor_test.go`
- Modify: `internal/guardian/types.go`(`CapabilityDoctor` 进 `GuardianCapabilities()`)
- Modify: `internal/guardian/localapi.go`(`LocalAPIOptions.DoctorFacts`、`mux.HandleFunc("/v1/doctor", …)`)
- Modify: `internal/guardian/daemon.go`(`localAPIOptionsFor` 接 `collectDoctorFacts`)

**Interfaces:**
- Consumes: `doctor.Judge`、`doctor.Facts`、`doctor.DarwinServiceChecks`(Task 2)、`DoctorGuardianFact`(Task 2)、`platformcheck.Collect`(Task 1)、既有 `reviewRulesAt(configPath, nil)`、`liveServerProbe(host, port)`、`setup.LinkHost`/`setup.LinkPort`、`install.GuardianInstalled/GuardianActive`、`supervisor.SockPath`、`observableStatus(controller, recoveries, options)`。
- Produces:
  ```go
  const CapabilityDoctor = "doctor"
  // DoctorFactsFunc 是 Guardian 侧的事实采集:status 是这一刻 Guardian 自己的状态(handler 算好传进来)。
  type DoctorFactsFunc func(ctx context.Context, configPath string, status Status) doctor.Facts
  func collectDoctorFacts(ctx context.Context, configPath string, status Status) doctor.Facts   // 生产那份
  func doctorHandler(collect DoctorFactsFunc, configPath string, ownerUID uint32, status func() Status) http.HandlerFunc
  // LocalAPIOptions 新字段:DoctorFacts DoctorFactsFunc(nil = 没接线 ⇒ 501)
  ```
  `GET /v1/doctor` → 200 + `doctor.Report` JSON(与 `bx doctor --json` 同形状);非 GET 405;未接线 501;整轮 `context.WithTimeout(10s)`,超时的探测那一条如实 `warn`。

- [ ] **Step 1: 写失败测试**

```go
// internal/guardian/doctor_test.go
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
	body := "server: brook://example.com:9999?password=x\nglobal: true\nrules:\n    - direct:\n        - '*.steamcontent.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fakeDoctorFacts(t *testing.T, calls *int) DoctorFactsFunc {
	t.Helper()
	return func(ctx context.Context, configPath string, status Status) doctor.Facts {
		*calls++
		return doctor.Facts{Version: "test", ConfigPath: configPath,
			Config: doctor.FileFact{ReadErr: "open " + configPath + ": no such file"}}
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

// 生产那份采集:读得到配置时走长路径(解析、体检、服务行、socket、darwin 两行、平台);
// 读不到时 GuardianRules 明说「Guardian 自己也读不到」。**探测被 skipProbe 掉了**——
// 单测不出网。
func TestCollectDoctorFactsWalksTheLongPathWithoutProbing(t *testing.T) {
	path := doctorTestConfig(t)
	f := collectDoctorFactsWith(context.Background(), path, Status{DNSState: "managed", DNSManaged: true},
		doctorCollectorDeps{probe: nil, platform: func(context.Context) []doctor.Check { return []doctor.Check{{Name: "terminal_proxy", Status: "info"}} }})
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
		t.Fatal("测试环境没有 Core socket,StatusSocketErr 应非空")
	}
	missing := collectDoctorFactsWith(context.Background(), filepath.Join(t.TempDir(), "nope.yaml"), Status{}, doctorCollectorDeps{})
	if missing.Config.ReadErr == "" || missing.GuardianRules.Err == "" {
		t.Fatalf("配置不存在时要如实报 ReadErr 与 GuardianRules.Err:%+v %+v", missing.Config, missing.GuardianRules)
	}
}

// 探测器返回什么就是什么(通/不通/超时各成一条 check),名字固定 probe。
func TestCollectDoctorFactsProbeOutcomes(t *testing.T) {
	path := doctorTestConfig(t)
	ok := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
		probe: func(host string, port int) (probeOutcome, error) { return probeOutcome{Reachable: true, RTTMS: 42}, nil },
	})
	if ok.Probe == nil || ok.Probe.Status != "ok" || !strings.Contains(ok.Probe.Detail, "42ms") {
		t.Fatalf("通 = %+v", ok.Probe)
	}
	bad := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
		probe: func(host string, port int) (probeOutcome, error) { return probeOutcome{Reachable: false, Error: "connection refused"}, nil },
	})
	if bad.Probe == nil || bad.Probe.Status != "fail" || !strings.Contains(bad.Probe.Detail, "connection refused") {
		t.Fatalf("不通 = %+v", bad.Probe)
	}
	broken := collectDoctorFactsWith(context.Background(), path, Status{}, doctorCollectorDeps{
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
```

- [ ] **Step 2: 跑确认红**

Run: `go test ./internal/guardian/ -run 'Doctor' 2>&1 | head -5`
Expected: `undefined: doctorHandler` 等。

- [ ] **Step 3: 实现 `internal/guardian/doctor.go`**

```go
package guardian

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/config"
	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/platformcheck"
	"github.com/getbx/bx/internal/setup"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/getbx/bx/internal/version"
)

// CapabilityDoctor:这一版 Guardian 会经 /v1/doctor 发布一份 doctor.Report。
// 菜单只在声明了它时才把「Check for Problems」指向 Checks 页,否则退回终端那条路。
const CapabilityDoctor = "doctor"

// DoctorFactsFunc 是 Guardian 侧的事实采集。**判断一句都不在这里**——全在
// doctor.Judge;这一层与 internal/cli 的 collectDoctorFacts 是同一份判据的第二个
// 采集方(spec §3)。status 由 handler 算好传进来(它要 controller,采集函数不该
// 自己去拿)。
type DoctorFactsFunc func(ctx context.Context, configPath string, status Status) doctor.Facts

// doctorTimeout 是整轮采集的上限:探测 5 秒 + 其余。bx status 的观测是 5 秒,
// doctor 多一次出网探测。超时的项如实 warn,绝不让整份应答失败。
const doctorTimeout = 10 * time.Second

// probeOutcome 是探测的原始结果(与 supervisor.ProbeResult 同形,单独定义是为了
// 让单测不依赖 supervisor 的构造)。
type probeOutcome struct {
	Reachable bool
	RTTMS     int64
	Error     string
}

// doctorCollectorDeps 是采集的可注入原语:单测里 probe=nil ⇒ 不探(不出网),
// platform=nil ⇒ 不采平台检查。生产用 liveDoctorDeps()。
type doctorCollectorDeps struct {
	probe    func(host string, port int) (probeOutcome, error)
	platform func(context.Context) []doctor.Check
}

func liveDoctorDeps() doctorCollectorDeps {
	return doctorCollectorDeps{
		probe: func(host string, port int) (probeOutcome, error) {
			r, err := liveServerProbe(host, port)
			return probeOutcome{Reachable: r.Reachable, RTTMS: r.RTTMS, Error: r.Error}, err
		},
		platform: platformcheck.Collect,
	}
}

// collectDoctorFacts 是生产那份采集(localAPIOptionsFor 接的就是它)。
func collectDoctorFacts(ctx context.Context, configPath string, status Status) doctor.Facts {
	return collectDoctorFactsWith(ctx, configPath, status, liveDoctorDeps())
}

func collectDoctorFactsWith(ctx context.Context, configPath string, status Status, deps doctorCollectorDeps) doctor.Facts {
	f := doctor.Facts{Version: version.String(), ConfigPath: configPath, Darwin: runtime.GOOS == "darwin"}
	b, err := os.ReadFile(configPath)
	if err != nil {
		f.Config.ReadErr = err.Error()
		f.Config.PermissionDenied = errors.Is(err, fs.ErrPermission)
		// Guardian 自己就是 root:它读不到就没有第二条路,如实说。
		f.GuardianRules = doctor.GuardianRulesFact{ConfigPath: configPath, Err: "guardian 自己也读不到这份配置:" + err.Error()}
	} else {
		if info, serr := os.Stat(configPath); serr == nil {
			f.Config.Mode0600 = info.Mode().Perm() == 0o600
		}
		cfg, perr := config.Parse(b)
		if perr != nil {
			f.ParseErr = perr.Error()
		} else {
			f.Parsed = cfg
			if cfg.Server != "" {
				if deps.probe != nil {
					f.Probe = doctorProbeCheck(cfg.Server, deps.probe)
				}
				f.RuleReview = reviewRulesAt(configPath, nil)
			}
		}
	}
	f.Service = doctor.DarwinServiceChecks(install.GuardianInstalled(), install.GuardianActive())
	if err := dialControlSocket(supervisor.SockPath); err != nil {
		f.StatusSocketErr = err.Error()
	}
	if runtime.GOOS == "darwin" {
		g := DoctorGuardianFact(status)
		f.Guardian = &g
	}
	if deps.platform != nil {
		f.Platform = deps.platform(ctx)
	}
	return f
}

// doctorProbeCheck 把一次 TCP 探测折成 probe 那一行。**这里的 probe 与 bx doctor
// 那条不是同一种探测**:CLI 做的是完整传输握手(setup.ProbeServer),Guardian 做的
// 是 Core 代发的 TCP 往返(/v0/probe)。名字同为 probe(它是「服务器够不够得着」
// 这一格),detail 里写明是 tcp,读的人分得开。
func doctorProbeCheck(link string, probe func(host string, port int) (probeOutcome, error)) *doctor.Check {
	host, ok := setup.LinkHost(link)
	port := setup.LinkPort(link)
	if !ok || host == "" || port == 0 {
		return &doctor.Check{Name: "probe", Status: "warn", Detail: "could not read the server address from the link"}
	}
	r, err := probe(host, port)
	target := fmt.Sprintf("tcp %s:%d", host, port)
	switch {
	case err != nil:
		return &doctor.Check{Name: "probe", Status: "warn", Detail: target + " not probed: " + err.Error()}
	case !r.Reachable:
		return &doctor.Check{Name: "probe", Status: "fail", Detail: target + " " + r.Error}
	default:
		return &doctor.Check{Name: "probe", Status: "ok", Detail: fmt.Sprintf("%s %dms", target, r.RTTMS)}
	}
}

func dialControlSocket(path string) error {
	conn, err := net.DialTimeout("unix", path, 500*time.Millisecond)
	if err != nil {
		return err
	}
	return conn.Close()
}

// doctorHandler 服务 GET /v1/doctor。owner 门(与 /v1/rules、/v1/logs 同一道);
// **门之后才采集**——采集会出网探测,被拒的请求不许触发它。
func doctorHandler(collect DoctorFactsFunc, configPath string, ownerUID uint32, status func() Status) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "doctor requires owner or root peer"})
			return
		}
		if collect == nil || configPath == "" {
			writeGuardianJSON(w, http.StatusNotImplemented, map[string]string{"error": "doctor unavailable: not wired"})
			return
		}
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		uid, _ := peerUIDFrom(r.Context())
		log.Printf("guardian_doctor_requested uid=%d", uid)
		ctx, cancel := context.WithTimeout(r.Context(), doctorTimeout)
		defer cancel()
		rep := doctor.Judge(collect(ctx, configPath, status()))
		writeGuardianJSON(w, http.StatusOK, rep)
	}
}
```

`setup.LinkPort` 若不存在(控制器只看到 `serverEntries` 里用了它),以 `grep -n "^func LinkPort" internal/setup/*.go` 为准;没有就用 `serverEntries` 里同一种取法。

- [ ] **Step 4: 接线**

`types.go`:`GuardianCapabilities()` 末尾加 `CapabilityDoctor`。
`localapi.go`:`LocalAPIOptions` 加

```go
	// DoctorFacts backs /v1/doctor: Guardian 自己采集事实、喂 doctor.Judge。nil = 没接线 ⇒ 501。
	DoctorFacts DoctorFactsFunc
```

并在 `/v1/logs` 那行之后加

```go
	mux.HandleFunc("/v1/doctor", doctorHandler(options.DoctorFacts, options.ConfigPath, options.OwnerUID, func() Status {
		return observableStatus(controller, pathRecoveryControllerFor(controller), options)
	}))
```

`daemon.go` 的 `localAPIOptionsFor`:加 `DoctorFacts: collectDoctorFacts,`。

- [ ] **Step 5: 跑测试**

Run: `go test ./internal/guardian/ 2>&1 | tail -2`
Expected: `ok`。若 `fakeController` 的 `Status()` 让 `observableStatus` 在测试里 panic,看 `apps_test.go`/`logs_test.go` 里同样用 `&fakeController{}` 的先例是怎么过的(它们没碰 status);必要时给 `TestNewLocalAPIWiresDoctorEndpoint` 用 `newFakeManagerEnv` 一类既有替身。

- [ ] **Step 6: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/guardian/
git add internal/guardian/
git commit -m "feat(guardian): GET /v1/doctor —— Guardian 进程内采集事实、喂同一个 doctor.Judge,能力声明 doctor

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 4: Swift 纯模型 `DiagnosticsModel` + `GuardianClient.fetchDoctor`

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/DiagnosticsModel.swift`
- Create: `apps/macos/BxMenu/Tests/DiagnosticsModelTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`(`.doctor` 端点 + `fetchDoctor()`)
- Modify: `scripts/test-macos-menu.sh`(登记 `diagnostics-model`;含 `GuardianClient.swift` 的五个套件加 `DiagnosticsModel.swift`)

**Interfaces:**
- Produces:
  ```swift
  struct DoctorCheck: Decodable, Equatable { let name, status, detail, hint: String }
  struct DoctorReport: Decodable, Equatable { let ok: Bool; let version: String; let checks: [DoctorCheck] }
  func doctorAvailable(capabilities: [String]?) -> Bool
  func sortedDoctorChecks(_ checks: [DoctorCheck]) -> [DoctorCheck]   // fail > warn > info > ok,同档保持原序
  func doctorSummaryLine(_ checks: [DoctorCheck]) -> String            // "N failed · M warning(s)";两数各自计,不合成
  func doctorCheckTitle(_ name: String) -> String                      // "config_readable" → "config readable"
  let guardianDoctorTimeout: TimeInterval = 20   // 放在 GuardianClient.swift 的常量区
  // GuardianClient
  func fetchDoctor() throws -> DoctorReport
  ```

- [ ] **Step 1: 写失败测试**

```swift
// apps/macos/BxMenu/Tests/DiagnosticsModelTests.swift
import Foundation

@main
struct DiagnosticsModelTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func decode(_ json: String) -> DoctorReport? {
        do {
            return try JSONDecoder().decode(DoctorReport.self, from: Data(json.utf8))
        } catch {
            expect(false, "解码失败:\(error)")
            return nil
        }
    }

    // 线上形状 = bx doctor --json:detail/hint 是 omitempty,缺席不许抛。
    static func testDecodesReportWithOptionalDetailAndHint() {
        let json = """
        {"ok":false,"kind":"client","version":"v1","secrets_redacted":true,"changes_system":false,
         "changes_network":false,"requires_root":false,
         "checks":[{"name":"config","status":"info","detail":"/etc/bx/config.yaml"},
                   {"name":"config_readable","status":"fail","detail":"permission denied","hint":"sudo bx setup <client-link>"},
                   {"name":"status_socket","status":"ok"}]}
        """
        guard let report = decode(json) else { return }
        expect(!report.ok, "ok 要解出来")
        expect(report.checks.count == 3, "三条 check")
        expect(report.checks[2].detail.isEmpty && report.checks[2].hint.isEmpty, "缺席的 detail/hint 落成空串")
        expect(report.checks[1].hint == "sudo bx setup <client-link>", "hint 原样")
    }

    // 能力键缺席 = 旧版 Guardian ⇒ 不可用(与 logsAvailable / rulesEditingAvailable 同款)。
    static func testDoctorAvailableIsGatedByCapability() {
        expect(!doctorAvailable(capabilities: nil), "nil ⇒ 旧版")
        expect(!doctorAvailable(capabilities: ["logs"]), "没声明 doctor")
        expect(doctorAvailable(capabilities: ["logs", "doctor"]), "声明了")
    }

    // 坏的排前:fail > warn > info > ok;同一档保持服务端顺序(那是 --json 契约的顺序)。
    static func testSortedChecksPutBadFirstAndKeepOrderWithinATier() {
        let checks = [
            DoctorCheck(name: "a", status: "ok", detail: "", hint: ""),
            DoctorCheck(name: "b", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "c", status: "info", detail: "", hint: ""),
            DoctorCheck(name: "d", status: "fail", detail: "", hint: ""),
            DoctorCheck(name: "e", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "f", status: "weird", detail: "", hint: ""),
        ]
        let names = sortedDoctorChecks(checks).map(\.name)
        expect(names == ["d", "b", "e", "c", "f", "a"], "排序 = \(names)(认不出的状态与 info 同档,不丢)")
    }

    // 合计句两数各自计,**永远不合成一个总数**(与 leakcheck 的三段计数同一条纪律)。
    static func testSummaryLineCountsFailuresAndWarningsSeparately() {
        let checks = [
            DoctorCheck(name: "a", status: "fail", detail: "", hint: ""),
            DoctorCheck(name: "b", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "c", status: "warn", detail: "", hint: ""),
            DoctorCheck(name: "d", status: "ok", detail: "", hint: ""),
        ]
        expect(doctorSummaryLine(checks) == "1 failed · 2 warnings", "合计 = \(doctorSummaryLine(checks))")
        expect(doctorSummaryLine([checks[3]]) == "0 failed · 0 warnings", "全绿也要说清是 0/0,不说「没问题」")
        expect(doctorSummaryLine([checks[1]]) == "0 failed · 1 warning", "单数")
    }

    static func testCheckTitleReadsLikeProse() {
        expect(doctorCheckTitle("config_readable") == "config readable", "下划线换空格")
        expect(doctorCheckTitle("rule_rules_never_in_effect") == "rule rules never in effect", "全部下划线")
    }

    static func main() {
        testDecodesReportWithOptionalDetailAndHint()
        testDoctorAvailableIsGatedByCapability()
        testSortedChecksPutBadFirstAndKeepOrderWithinATier()
        testSummaryLineCountsFailuresAndWarningsSeparately()
        testCheckTitleReadsLikeProse()
        if failures == 0 {
            print("DiagnosticsModelTests passed")
        }
        if failures > 0 {
            FileHandle.standardError.write(Data("\(failures) failure(s)\n".utf8))
            exit(1)
        }
    }
}
```

- [ ] **Step 2: 跑确认红**

Run: `M=apps/macos/BxMenu; swiftc $M/Sources/BxMenu/DiagnosticsModel.swift $M/Tests/DiagnosticsModelTests.swift -o /tmp/bx-diag 2>&1 | head -3`
Expected: 找不到文件 / `DoctorReport` 不存在。

- [ ] **Step 3: 实现 `DiagnosticsModel.swift`**

```swift
import Foundation

/// /v1/doctor 应答(与 `bx doctor --json` 同形状)的纯模型:解码、排序、合计。
/// **判据都在这里**,AppKit 那半只摆。

struct DoctorCheck: Decodable, Equatable {
    let name: String
    let status: String
    let detail: String
    let hint: String

    enum CodingKeys: String, CodingKey { case name, status, detail, hint }

    /// 手写:detail/hint 是 omitempty,合成解码器对缺键会抛。
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        status = try c.decodeIfPresent(String.self, forKey: .status) ?? ""
        detail = try c.decodeIfPresent(String.self, forKey: .detail) ?? ""
        hint = try c.decodeIfPresent(String.self, forKey: .hint) ?? ""
    }

    init(name: String, status: String, detail: String, hint: String) {
        self.name = name
        self.status = status
        self.detail = detail
        self.hint = hint
    }
}

struct DoctorReport: Decodable, Equatable {
    let ok: Bool
    let version: String
    let checks: [DoctorCheck]

    enum CodingKeys: String, CodingKey { case ok, version, checks }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        ok = try c.decodeIfPresent(Bool.self, forKey: .ok) ?? false
        version = try c.decodeIfPresent(String.self, forKey: .version) ?? ""
        checks = try c.decodeIfPresent([DoctorCheck].self, forKey: .checks) ?? []
    }

    init(ok: Bool, version: String, checks: [DoctorCheck]) {
        self.ok = ok
        self.version = version
        self.checks = checks
    }
}

/// 这一版 Guardian 有没有 /v1/doctor。**能力键缺席 = 旧版**,那时「Check for
/// Problems」退回终端那条路,不画一个每次点都 404 的页。
func doctorAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("doctor")
}

/// 坏的排前(fail > warn > info > ok);同一档保持服务端顺序 —— 那是 --json 契约的
/// 顺序,用户在 CLI 里看到的就是它。认不出的状态与 info 同档,**不丢**。
func sortedDoctorChecks(_ checks: [DoctorCheck]) -> [DoctorCheck] {
    func rank(_ status: String) -> Int {
        switch status {
        case "fail": return 0
        case "warn": return 1
        case "ok": return 3
        default: return 2
        }
    }
    return checks.enumerated()
        .sorted { a, b in
            let ra = rank(a.element.status), rb = rank(b.element.status)
            return ra != rb ? ra < rb : a.offset < b.offset
        }
        .map(\.element)
}

/// 「N failed · M warning(s)」。**两数各自计,永远不合成一个总数**:合成的数在任何
/// 成熟配置上都不为零,会被训练成噪声,把真正的 fail 一起淹掉。
func doctorSummaryLine(_ checks: [DoctorCheck]) -> String {
    let failed = checks.filter { $0.status == "fail" }.count
    let warned = checks.filter { $0.status == "warn" }.count
    return "\(failed) failed · \(warned) warning\(warned == 1 ? "" : "s")"
}

/// check 名转成人话:与 `bx doctor` 文本路径同一个规则(下划线换空格)。
func doctorCheckTitle(_ name: String) -> String {
    name.replacingOccurrences(of: "_", with: " ")
}
```

- [ ] **Step 4: `GuardianClient.swift`**

常量区加 `private let guardianDoctorTimeout: TimeInterval = 20`(探测 5 秒 + Guardian 整轮 10 秒上限,再留余量)。`GuardianEndpoint` 加 `case doctor`;`expectedStatus` 200 组加 `.doctor`;`timeout` 的 `switch` 加 `case .doctor: return guardianDoctorTimeout`;`guardianRequest` 加

```swift
    case .doctor:
        method = "GET"
        path = "/v1/doctor"
        body = nil
```

方法区加

```swift
    /// 一份 Guardian 进程内算出的 doctor 报告。**调用前必须过 `doctorAvailable(capabilities:)`。**
    /// 它会让 Guardian 出网探测一次服务器,所以只在用户显式点了才调,绝不放进任何定时器。
    func fetchDoctor() throws -> DoctorReport {
        try perform(endpoint: .doctor, as: DoctorReport.self)
    }
```

- [ ] **Step 5: 脚本登记**

`scripts/test-macos-menu.sh`:在 `run_test transition-notice` 之前加

```bash
run_test diagnostics-model \
  "$MENU/Sources/BxMenu/DiagnosticsModel.swift" \
  "$MENU/Tests/DiagnosticsModelTests.swift"
```

并给每个含 `GuardianClient.swift` 的 `run_test` 块加上 `DiagnosticsModel.swift`(`GuardianClient.swift` 会引用 `DoctorReport`,少一处那个套件就编不过)。这五个块里 `LogsModel.swift` 都紧挨在 `GuardianClient.swift` 之前,按它锚定:

```bash
perl -0pi -e 's|(  "\$MENU/Sources/BxMenu/LogsModel.swift" \\\n)|$1  "\$MENU/Sources/BxMenu/DiagnosticsModel.swift" \\\n|g' scripts/test-macos-menu.sh
grep -c "DiagnosticsModel.swift" scripts/test-macos-menu.sh   # 期望 7(五个含 GuardianClient 的块 + logs-model 块(多编一个文件无害)+ 新的 diagnostics-model 块)
```

- [ ] **Step 6: 构建 + 套件 + 登记守卫**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -1; go test ./internal/cli/ -run 'TestEveryMacOSMenuTestSuiteIsRegistered|TestMacMenuUserFacingStringsAreEnglish'`
Expected: `Build complete!`、`macOS menu tests passed`、`ok`。

- [ ] **Step 7: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/DiagnosticsModel.swift apps/macos/BxMenu/Tests/DiagnosticsModelTests.swift apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift scripts/test-macos-menu.sh
git commit -m "feat(menu): /v1/doctor 的客户端与纯模型 —— 解码、能力门控、坏的排前、两数各计的合计句

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 5: Diagnostics 窗口 Checks 页 + 「Check for Problems」不再开终端

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/DiagnosticsWindow.swift`(`NSTabView` 两页:Checks / Logs;`showChecks(_:)`;`onRunAgain`)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(`openDiagnosticsChecks()`、`runDoctorFromMenu` 按能力分流、`onRunAgain` 接线)
- Create: `internal/cli/macos_menu_doctor_test.go`

**Interfaces:**
- Consumes: `DoctorReport`、`sortedDoctorChecks`、`doctorSummaryLine`、`doctorCheckTitle`、`doctorAvailable`、`GuardianClient.fetchDoctor()`(Task 4);既有 `showLogs(_:highlightingCode:)`、`onExportDiagnostics`、`exportDiagnostics()`、`diagnosticsFetchInFlight`、`showMessage`。
- Produces(AppKit):
  ```swift
  // DiagnosticsWindowController
  var onRunAgain: (() -> Void)?
  func showChecks(_ report: DoctorReport)          // 选中 Checks 页并重画
  func showLogs(_ report: LogsReport, highlightingCode code: String?)   // 现在选中 Logs 页
  // main.swift
  private func openDiagnosticsChecks()
  @objc private func runDoctorFromMenu()          // doctorAvailable ⇒ openDiagnosticsChecks();否则 exportDiagnostics()
  ```

- [ ] **Step 1: 先写 Go 接线守卫(红)**

```go
// internal/cli/macos_menu_doctor_test.go
package cli

import (
	"strings"
	"testing"
)

// 「Check for Problems」在这一版 Guardian 有 doctor 能力时开 Checks 页,旧版才退回
// 终端那条路(exportDiagnostics)。顺序判据:能力门在前,两条路都在。
func TestMacMenuCheckForProblemsPrefersTheDoctorPage(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "@objc private func runDoctorFromMenu()")
	if !ok {
		t.Fatal("读不出 runDoctorFromMenu 的函数体 —— 守卫已失效,先修守卫")
	}
	gate := strings.Index(body, "doctorAvailable(capabilities: maintenanceReport?.capabilities)")
	page := strings.Index(body, "openDiagnosticsChecks()")
	terminal := strings.Index(body, "exportDiagnostics()")
	if gate < 0 || page < 0 || terminal < 0 || gate > page || page > terminal {
		t.Fatalf("要先按能力开 Checks 页、旧版才退回终端(gate=%d page=%d terminal=%d)", gate, page, terminal)
	}
}

// Checks 页由 fetchDoctor 喂,结果经 showChecks 摆;拉不到就明说,不摆空页。
func TestMacMenuDoctorPageIsFedByFetchDoctor(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func openDiagnosticsChecks()")
	if !ok {
		t.Fatal("读不出 openDiagnosticsChecks 的函数体")
	}
	for _, want := range []string{"diagnosticsFetchInFlight", "GuardianClient().fetchDoctor()", "self.diagnosticsWindow.showChecks(report)", "showMessage("} {
		if !strings.Contains(body, want) {
			t.Fatalf("openDiagnosticsChecks 缺 %s", want)
		}
	}
	if !strings.Contains(code, "controller.onRunAgain = ") || !strings.Contains(code, "self?.openDiagnosticsChecks()") {
		t.Fatal("窗口的 Run again 没接回 openDiagnosticsChecks")
	}
}

// 窗口只摆:排序、合计、标题全由纯模型给。
func TestMacMenuDiagnosticsWindowRendersChecksByThePureModel(t *testing.T) {
	window := blankSwiftStringLiterals(stripSwiftComments(readMenuSwiftSource(t, "DiagnosticsWindow.swift")))
	body, ok := swiftFunctionBody(window, "private func renderChecks(_ report: DoctorReport)")
	if !ok {
		t.Fatal("读不出 renderChecks 的函数体")
	}
	for _, want := range []string{"sortedDoctorChecks(report.checks)", "doctorSummaryLine(report.checks)", "doctorCheckTitle(", "onRunAgain?()"} {
		if !strings.Contains(body, want) && !strings.Contains(window, want) {
			t.Fatalf("Checks 页缺 %s —— 判据落进了 AppKit 那半,或 Run again 没出口", want)
		}
	}
	if !strings.Contains(window, "NSTabView") {
		t.Fatal("两页要用 NSTabView(Checks / Logs),不要两个窗口")
	}
}
```

Run: `go test ./internal/cli/ -run 'TestMacMenuCheckForProblems|TestMacMenuDoctorPage|TestMacMenuDiagnosticsWindowRendersChecks' 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: 三条 FAIL。

- [ ] **Step 2: `DiagnosticsWindow.swift` 改成两页**

把 `ensureWindow()` 改为:窗口 contentView 放一个 `NSTabView`(两个 `NSTabViewItem`:`label = "Checks"`、`label = "Logs"`),每页各自一个 scroll+FlippedView+stack(把现有 scroll/stack 的构造抽成 `private func makeScrollingStack() -> (NSScrollView, NSStackView)`,调两次)。存 `private var checksStack: NSStackView?`、`private var logsStack: NSStackView?`、`private var tabs: NSTabView?`。

```swift
    /// 用户点了 Checks 页的「Run again」。
    var onRunAgain: (() -> Void)?

    func showChecks(_ report: DoctorReport) {
        let window = ensureWindow()
        renderChecks(report)
        tabs?.selectTabViewItem(at: 0)
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    func showLogs(_ report: LogsReport, highlightingCode code: String?) {
        let window = ensureWindow()
        renderLogs(report, code: code)   // 原 render(_:code:) 改名,目标栈改成 logsStack
        tabs?.selectTabViewItem(at: 1)
        NSApp.activate(ignoringOtherApps: true)
        window.makeKeyAndOrderFront(nil)
    }

    /// Checks 页:合计一句在顶,坏的排前,每条 = 状态标签 + 名字 + detail,hint 另起一行暗色小字。
    /// **排序、合计、标题全由纯模型给**(DiagnosticsModel),这里只摆。
    private func renderChecks(_ report: DoctorReport) {
        guard let stack = checksStack else { return }
        for view in stack.arrangedSubviews {
            stack.removeArrangedSubview(view)
            view.removeFromSuperview()
        }
        let summary = NSTextField(labelWithString: doctorSummaryLine(report.checks))
        summary.font = .boldSystemFont(ofSize: NSFont.systemFontSize)
        stack.addArrangedSubview(summary)
        if !report.version.isEmpty {
            stack.addArrangedSubview(hint("bx \(report.version)"))
        }
        stack.addArrangedSubview(gap())
        for check in sortedDoctorChecks(report.checks) {
            let row = NSStackView()
            row.orientation = .horizontal
            row.alignment = .firstBaseline
            row.spacing = 8
            let badge = NSTextField(labelWithString: check.status.uppercased())
            badge.font = .monospacedSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
            badge.textColor = statusColor(check.status)
            badge.setContentHuggingPriority(.required, for: .horizontal)
            row.addArrangedSubview(badge)
            let title = NSTextField(labelWithString: doctorCheckTitle(check.name))
            title.setContentHuggingPriority(.required, for: .horizontal)
            row.addArrangedSubview(title)
            if !check.detail.isEmpty {
                let detail = hint(check.detail)
                detail.lineBreakMode = .byTruncatingTail
                detail.toolTip = check.detail
                row.addArrangedSubview(detail)
            }
            stack.addArrangedSubview(row)
            if !check.hint.isEmpty {
                let h = hint("→ " + check.hint)
                h.textColor = .tertiaryLabelColor
                stack.addArrangedSubview(h)
            }
        }
        stack.addArrangedSubview(gap())
        let again = NSButton(title: "Run again", target: self, action: #selector(runAgain))
        again.bezelStyle = .rounded
        again.controlSize = .small
        again.toolTip = "Asks bx to check again. This probes your server once, outside the tunnel."
        stack.addArrangedSubview(again)
    }

    @objc private func runAgain() {
        onRunAgain?()
    }

    private func statusColor(_ status: String) -> NSColor {
        switch status {
        case "fail": return .systemRed
        case "warn": return .systemOrange
        case "ok": return .systemGreen
        default: return .secondaryLabelColor
        }
    }

    private func gap() -> NSView {
        let spacer = NSView()
        spacer.translatesAutoresizingMaskIntoConstraints = false
        spacer.heightAnchor.constraint(equalToConstant: 6).isActive = true
        return spacer
    }
```

原 `render(_:code:)` 改名 `renderLogs(_:code:)`、目标栈 `logsStack`,其余不动(Export Diagnostics 按钮留在 Logs 页底部)。`makeScrollingStack()` 里的约束与现在完全一样,只是 `content` 换成对应 `NSTabViewItem.view`。

- [ ] **Step 3: `main.swift` 接线**

在 `diagnosticsWindow` 的 lazy 初始化里加 `controller.onRunAgain = { [weak self] in self?.openDiagnosticsChecks() }`。`openDiagnosticsLogs` 之后加:

```swift
    /// 拉一次 /v1/doctor 再开 Checks 页。**它让 Guardian 出网探测一次服务器**,所以只
    /// 由用户点击触发(菜单项与 Run again),绝不放进任何定时器或刷新路径。
    private func openDiagnosticsChecks() {
        guard !diagnosticsFetchInFlight else { return }
        diagnosticsFetchInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().fetchDoctor() }
            DispatchQueue.main.async {
                guard let self else { return }
                self.diagnosticsFetchInFlight = false
                switch result {
                case .success(let report):
                    self.diagnosticsWindow.showChecks(report)
                case .failure(let error):
                    self.showMessage("Checks are not available", "bx could not run its checks: \(error.localizedDescription)")
                }
            }
        }
    }
```

`runDoctorFromMenu` 改成:

```swift
    @objc private func runDoctorFromMenu() {
        // 这一版 Guardian 会自己算 doctor 就开 Checks 页;旧版退回终端那条归档路。
        if doctorAvailable(capabilities: maintenanceReport?.capabilities) {
            openDiagnosticsChecks()
            return
        }
        exportDiagnostics()
    }
```

- [ ] **Step 4: 构建、套件、守卫、变异**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -1; go test ./internal/cli/ 2>&1 | tail -2`
Expected: 全绿。变异:把 `runDoctorFromMenu` 里的 `if doctorAvailable(...)` 改成 `if true`(cp 备份)→ `TestMacMenuCheckForProblemsPrefersTheDoctorPage` 红;恢复。把 `sortedDoctorChecks(report.checks)` 改成 `report.checks` → `TestMacMenuDiagnosticsWindowRendersChecksByThePureModel` 红;恢复。

- [ ] **Step 5: 提交**

```bash
bash scripts/verify.sh --quick && git add -A apps/macos/BxMenu internal/cli/macos_menu_doctor_test.go && git commit -m "feat(menu): Check for Problems 开 Diagnostics 的 Checks 页 —— 不再开终端;旧 Guardian 退回归档路

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

**Phase ③ 真机验收**:Troubleshoot ▸ Check for Problems → 不开终端,Diagnostics 窗口 Checks 页,顶部 `N failed · M warnings`,坏的排前;与同一时刻 `sudo bx doctor --json --skip-probe` 的 check 名与状态逐条对上(`probe` 那条允许不同:Guardian 的是 TCP 往返)。

---

### Task 6: `/v1/servers add`:同名 409、名字可省略、应答带 `added`

**Files:**
- Modify: `internal/guardian/servers.go`(`addServerEntry`、`ServerListResponse.Added`)
- Test: `internal/guardian/servers_add_test.go`(新)

**Interfaces:**
- Produces:`POST /v1/servers {"action":"add","name":"<可空>","link":…,"udp":…}` → 200 `ServerListResponse{…, Added: "<最终名字>"}`;名字为空时用 `config.DeriveServerName(link)`;名字(大小写不敏感)已存在 → 409 `{"code":"servers_name_exists"}` **且盘上一个字节不动**;链接无效 → 400 `servers_add_failed`。

- [ ] **Step 1: 写失败测试**

```go
// internal/guardian/servers_add_test.go
package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// serversTestConfig(tokyo / osaka,current=tokyo)与 noSwitch 都是 servers_test.go 里既有的
// 替身,这里直接复用 —— 别再定义一份同名的,同包会撞名。
func postServers(t *testing.T, path, body string) (int, ServerListResponse, string) {
	t.Helper()
	handler := serversHandler(path, 501, noSwitch(t), nil, nil)
	w := httptest.NewRecorder()
	handler(w, withPeer(httptest.NewRequest(http.MethodPost, "/v1/servers", strings.NewReader(body)), 501, true))
	var resp ServerListResponse
	if w.Code == http.StatusOK {
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
	}
	return w.Code, resp, w.Body.String()
}

// 同名不许静默覆盖:setup.AddServer 对已存在的名字会**改写链接**,那对用户是「我加了
// 一台,结果把原来那台换掉了」。Guardian 先查重、409,盘上不动。
func TestAddServerRefusesAnExistingName(t *testing.T) {
	path := serversTestConfig(t)
	before, _ := os.ReadFile(path)
	code, _, body := postServers(t, path, `{"action":"add","name":"Tokyo","link":"brook://other.example.com:9999?password=y"}`)
	if code != http.StatusConflict || !strings.Contains(body, "servers_name_exists") {
		t.Fatalf("同名 = %d %s", code, body)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("拒绝之后盘上的配置动了")
	}
}

// 名字可省略:按链接推导(config.DeriveServerName),应答里 added 告诉界面最终叫什么。
func TestAddServerDerivesTheNameWhenOmitted(t *testing.T) {
	path := serversTestConfig(t)
	code, resp, body := postServers(t, path, `{"action":"add","link":"brook://vps2.example.com:9999?password=y"}`)
	if code != http.StatusOK {
		t.Fatalf("加 = %d %s", code, body)
	}
	if resp.Added != "vps2.example.com" {
		t.Fatalf("added = %q, want 按链接推导的名字", resp.Added)
	}
	if resp.Current != "tokyo" {
		t.Fatalf("add 不许动 current:%q", resp.Current)
	}
	if len(resp.Servers) != 3 {
		t.Fatalf("清单 = %+v", resp.Servers)
	}
}

func TestAddServerEchoesTheGivenName(t *testing.T) {
	path := serversTestConfig(t)
	code, resp, _ := postServers(t, path, `{"action":"add","name":"office","link":"brook://o.example.com:9999?password=y"}`)
	if code != http.StatusOK || resp.Added != "office" {
		t.Fatalf("= %d added=%q", code, resp.Added)
	}
}

func TestAddServerRejectsAnEmptyLink(t *testing.T) {
	path := serversTestConfig(t)
	code, _, body := postServers(t, path, `{"action":"add","name":"x","link":""}`)
	if code != http.StatusBadRequest || !strings.Contains(body, "servers_add_failed") {
		t.Fatalf("空链接 = %d %s", code, body)
	}
}
```

- [ ] **Step 2: 跑确认红**

Run: `go test ./internal/guardian/ -run 'TestAddServer' 2>&1 | grep -E "^(--- |FAIL|ok)" | head`
Expected: `resp.Added` 编译失败或同名那条 FAIL。

- [ ] **Step 3: 实现**

`ServerListResponse` 加字段:

```go
	// Added 只在 add 应答里出现:最终写进清单的名字(用户给的,或按链接推导的)。
	// 界面靠它知道接下来该切换到哪一台 —— 自己再推一遍推导规则就是第二份判据。
	Added string `json:"added,omitempty"`
```

`addServerEntry` 改成:

```go
func addServerEntry(w http.ResponseWriter, req serversRequest, configPath string, uid uint32) {
	name := strings.TrimSpace(req.Name)
	link := strings.TrimSpace(req.Link)
	log.Printf("guardian_server_add_requested name=%q uid=%d has_udp=%t", name, uid, strings.TrimSpace(req.UDP) != "")
	if name == "" {
		derived, err := config.DeriveServerName(link)
		if err != nil {
			log.Printf("guardian_server_add_failed reason=derive_name err=%v", err)
			writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
			return
		}
		name = derived
	}
	// **先查重,再写。** setup.AddServer 对已存在的名字是「改写那一台的链接」——
	// 对用户那是「我加了一台,结果把原来那台换掉了」,而界面上看不出任何异常。
	existing, _, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	for _, s := range existing {
		if strings.EqualFold(strings.TrimSpace(s.Name), name) {
			log.Printf("guardian_server_add_rejected reason=name_exists name=%q", name)
			writeGuardianJSON(w, http.StatusConflict, map[string]string{"code": "servers_name_exists"})
			return
		}
	}
	if _, err := setup.AddServer(configPath, name, link, strings.TrimSpace(req.UDP)); err != nil {
		log.Printf("guardian_server_add_failed name=%q err=%v", name, err)
		writeGuardianJSON(w, http.StatusBadRequest, map[string]string{"code": "servers_add_failed"})
		return
	}
	list, current, err := setup.ListServers(configPath)
	if err != nil {
		log.Printf("guardian_servers_read_failed path=%s err=%v", configPath, err)
		writeGuardianJSON(w, http.StatusInternalServerError, map[string]string{"code": "servers_read_failed"})
		return
	}
	log.Printf("guardian_server_added name=%q current=%q", name, current)
	writeGuardianJSON(w, http.StatusOK, ServerListResponse{
		Servers: serverEntries(list, current), Current: current, ConfigPath: configPath, Added: name,
	})
}
```

(`config` 已被 servers.go import;没有就加。)`config.DeriveServerName` 对空链接会报错 → 400,符合「空链接拒绝」。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/guardian/ 2>&1 | tail -2`
Expected: `ok`(既有 `servers_test.go` 的 add 用例若断言 `added` 键缺席,按新契约更新那一处断言)。

- [ ] **Step 5: 提交**

```bash
$(go env GOPATH)/bin/gofumpt -l internal/guardian/
git add internal/guardian/servers.go internal/guardian/servers_add_test.go internal/guardian/servers_test.go
git commit -m "feat(guardian): /v1/servers add 同名 409、名字可省略、应答带 added —— 菜单加服务器不会静默覆盖既有那台

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

### Task 7: Servers 窗口「Add Server…」= 加进清单并切换;「Replace Configuration…」从窗口退场

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/ServersModel.swift`(`ServerList.added`、`addServerOutcomeMessage`)
- Modify: `apps/macos/BxMenu/Tests/ServersModelTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`(`.addServer(name:link:)` + `addServer(name:link:)`)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/ServersWindow.swift`(`Replace Configuration…` 按钮 → `Add Server…`;`onAddServer`)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(`addServerFromWindow()`;`confirmAndSwitchServer` 拆出 `switchServer(name:)`;lazy 里接 `onAddServer`)
- Create: `internal/cli/macos_menu_addserver_test.go`

**Interfaces:**
- Consumes: Task 6 的 `added`;既有 `promptForClientLink(title:hint:confirmTitle:)`、`clipboardCandidateLink`、`showGuardianFailure(title:error:)`、`serverSwitchOutcomeMessage`、`refresh(userInitiated:)`、`switchInFlight`。
- Produces:
  ```swift
  // ServersModel.swift
  struct ServerList { …; var added: String = "" }            // 手写解码,缺席 = ""
  func addServerOutcomeMessage(added: String, switched: ServerSwitchResult?) -> String
  // GuardianClient
  func addServer(name: String, link: String) throws -> ServerList   // POST /v1/servers {"action":"add",…}
  // ServersWindow
  var onAddServer: (() -> Void)?
  // main.swift
  private func addServerFromWindow()
  private func switchServer(name: String, completion: ((ServerSwitchResult?) -> Void)? = nil)   // 无确认框的那半,confirmAndSwitchServer 调它
  ```
  「Replace Configuration…」按钮从窗口删除(它的 `onReplaceConfiguration` 回调一并删);菜单里那条旧 Guardian 降级路(`replaceConfigurationLivesInMenu`)**不动**。

- [ ] **Step 1: 写失败测试(Swift 纯模型 + Go 守卫)**

`ServersModelTests.swift` 加:

```swift
    // add 应答里的 added 缺席读作空串(旧 Guardian),不抛。
    static func testServerListDecodesAddedAndToleratesItsAbsence() {
        let with = try! JSONDecoder().decode(ServerList.self, from: Data(#"{"servers":[],"current":"","added":"vps2"}"#.utf8))
        expect(with.added == "vps2", "added 没解出来")
        let without = try! JSONDecoder().decode(ServerList.self, from: Data(#"{"servers":[],"current":""}"#.utf8))
        expect(without.added.isEmpty, "缺席要落成空串")
    }

    // 加完之后那句话:切成功说流量已从新那台出去;切没成说已加进清单但没切;
    // 切换那一步压根没做(add 成功、switch 抛错)说「已加进清单,可以在窗口里 Use」。
    static func testAddServerOutcomeMessageDistinguishesTheThreeEndings() {
        let applied = addServerOutcomeMessage(added: "vps2", switched: ServerSwitchResult(name: "vps2", host: "vps2.example.com", applied: true))
        expect(applied.contains("now leaves from vps2"), "切成功:\(applied)")
        let saved = addServerOutcomeMessage(added: "vps2", switched: ServerSwitchResult(name: "vps2", host: "", applied: false))
        expect(saved.contains("did not switch"), "切没成:\(saved)")
        let onlyAdded = addServerOutcomeMessage(added: "vps2", switched: nil)
        expect(onlyAdded.contains("Added vps2") && onlyAdded.contains("Use"), "只加了:\(onlyAdded)")
    }
```

并在 `main()` 里调用这两条。

```go
// internal/cli/macos_menu_addserver_test.go
package cli

import (
	"strings"
	"testing"
)

// 窗口里的按钮是 Add Server…,Replace Configuration… 已从窗口退场;点了走 onAddServer。
func TestMacMenuServersWindowOffersAddServerNotReplace(t *testing.T) {
	window := stripSwiftComments(readMenuSwiftSource(t, "ServersWindow.swift"))
	if !strings.Contains(window, `NSButton(title: "Add Server…"`) {
		t.Fatal("Servers 窗口没有 Add Server… 按钮")
	}
	if strings.Contains(window, "Replace Configuration") || strings.Contains(window, "onReplaceConfiguration") {
		t.Fatal("Replace Configuration 还在窗口里 —— 它被 Add Server 取代了(spec §4)")
	}
	if !strings.Contains(window, "onAddServer?()") {
		t.Fatal("Add Server… 没有回调出口")
	}
}

// 流程:贴链接 → 名字(可空)→ /v1/servers add → 用应答里的 added 切换 → 一句结果。
// 中间不弹密码、不开终端;失败走 showGuardianFailure。
func TestMacMenuAddServerAddsThenSwitchesWithoutPrivilege(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func addServerFromWindow()")
	if !ok {
		t.Fatal("读不出 addServerFromWindow 的函数体")
	}
	link := strings.Index(body, "promptForClientLink(")
	add := strings.Index(body, "GuardianClient().addServer(name:")
	sw := strings.Index(body, "self.switchServer(name: list.added")
	if link < 0 || add < 0 || sw < 0 || link > add || add > sw {
		t.Fatalf("顺序要是 链接 → add → 用 added 切换(link=%d add=%d switch=%d)", link, add, sw)
	}
	for _, forbidden := range []string{"runPrivileged(", "openTerminal(", "bxPath"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("Add Server 不许提权/开终端(出现了 %s)", forbidden)
		}
	}
	if !strings.Contains(body, "showGuardianFailure(title:") {
		t.Fatal("add 失败要走同一个失败漏斗")
	}
	if !strings.Contains(code, "controller.onAddServer = ") || !strings.Contains(code, "self?.addServerFromWindow()") {
		t.Fatal("窗口的 onAddServer 没接到 addServerFromWindow")
	}
	// 确认框那一半仍然只在 confirmAndSwitchServer 里;无确认的 switchServer 供 add 流复用。
	confirm, ok := swiftFunctionBody(code, "private func confirmAndSwitchServer(name: String, host: String)")
	if !ok || !strings.Contains(confirm, "switchServer(name: name") {
		t.Fatal("confirmAndSwitchServer 要经无确认的 switchServer(name:) 落地,而不是第二份切换逻辑")
	}
}
```

Run: `go test ./internal/cli/ -run 'TestMacMenuServersWindowOffersAddServer|TestMacMenuAddServerAddsThen' 2>&1 | grep -E "^(--- |FAIL|ok)"`
Expected: 两条 FAIL。Swift 套件那两条:`bash scripts/test-macos-menu.sh 2>&1 | grep -E "error:|FAIL" | head -3` 应报编译错误(`added`/`addServerOutcomeMessage` 不存在)。

- [ ] **Step 2: `ServersModel.swift`**

`ServerList` 加 `var added: String = ""`,`CodingKeys` 加 `added`,手写 `init(from:)` 里 `added = try c.decodeIfPresent(String.self, forKey: .added) ?? ""`,memberwise init 加参数(默认 `""`)。加:

```swift
/// 「Add Server…」做完之后那句话。三种结局分开说,**绝不合成「已添加并切换」**:
/// add 成功 + switch 生效 / add 成功 + switch 没生效(已回滚,原样在旧那台)/
/// add 成功 + switch 那一步根本没成(抛错)—— 第三种要告诉他清单里已经有了、可以手动 Use。
func addServerOutcomeMessage(added: String, switched: ServerSwitchResult?) -> String {
    guard let switched else {
        return "Added \(added) to your servers, but could not switch to it. Open Servers… and press Use to try again."
    }
    if switched.applied {
        return "Added \(added). Your traffic now leaves from \(added)."
    }
    return "Added \(added), but the running tunnel did not switch (it stayed on the previous server). "
        + "Press Use in Servers… to try again, or turn bx off and on."
}
```

- [ ] **Step 3: `GuardianClient.swift`**

`GuardianEndpoint` 加 `case addServer(name: String, link: String)`;`expectedStatus` 200 组加 `.addServer`;`timeout` 归到 `.listRules` 那一档;`guardianRequest`:

```swift
    case let .addServer(name, link):
        method = "POST"
        path = "/v1/servers"
        // 名字与链接都是用户输入 —— 用 JSONSerialization,不手拼。
        var payload: [String: String] = ["action": "add", "link": link]
        if !name.isEmpty { payload["name"] = name }
        body = (try? JSONSerialization.data(withJSONObject: payload)) ?? Data("{}".utf8)
```

方法:

```swift
    /// 把一台加进清单(不切换)。名字为空由 Guardian 按链接推导;应答里 `added` 是最终名字。
    /// 同名会被 Guardian 拒(409 servers_name_exists),**不会静默覆盖**。
    func addServer(name: String, link: String) throws -> ServerList {
        try perform(endpoint: .addServer(name: name, link: link), as: ServerList.self)
    }
```

- [ ] **Step 4: `ServersWindow.swift`**

把 `onReplaceConfiguration` 属性、`replaceConfiguration()` 方法和那个 `replace` 按钮整段删掉,换成:

```swift
    /// 用户点了「Add Server…」—— 贴一条链接加进清单并切换过去(spec §4)。
    var onAddServer: (() -> Void)?
```

按钮:

```swift
        let add = NSButton(title: "Add Server…", target: self, action: #selector(addServer))
        add.bezelStyle = .rounded
        add.controlSize = .small
        add.toolTip = "Paste a bx link to add a server and switch to it. The previous server stays in the list."
        buttons.addArrangedSubview(add)
```

```swift
    @objc private func addServer() {
        onAddServer?()
    }
```

- [ ] **Step 5: `main.swift`**

lazy `serversWindow` 里把 `controller.onReplaceConfiguration = …` 换成 `controller.onAddServer = { [weak self] in self?.addServerFromWindow() }`。把 `confirmAndSwitchServer` 拆成两半:确认框之后调 `switchServer(name: name)`;新函数:

```swift
    /// 无确认框的切换(确认在 confirmAndSwitchServer;Add Server 那条路的确认是它自己的第一步)。
    /// completion 收到 nil 表示请求本身失败(已弹过失败漏斗)。
    private func switchServer(name: String, completion: ((ServerSwitchResult?) -> Void)? = nil) {
        guard !switchInFlight else { completion?(nil); return }
        exitIPProbe = .unknown
        switchInFlight = true
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().switchServer(name: name) }
            DispatchQueue.main.async {
                guard let self else { return }
                self.switchInFlight = false
                switch result {
                case .success(let outcome):
                    completion?(outcome)
                case .failure(let error):
                    self.showGuardianFailure(title: "Could not switch server", error: error)
                    completion?(nil)
                }
                self.refresh(userInitiated: true)
            }
        }
    }
```

`confirmAndSwitchServer` 的后半改成 `switchServer(name: name) { outcome in guard let outcome else { return }; <原来那个 Switched / Saved 弹窗> }`。

```swift
    /// Servers 窗口的「Add Server…」:贴链接 → 起名(可空)→ Guardian add(同名 409)→ 用
    /// 应答里的 added 热切换 → 一句结果。**全程不提权、不开终端**:两步都是 owner 门
    /// 的 Guardian 端点(spec §4)。旧的那台留在清单里,随时能 Use 回去。
    private func addServerFromWindow() {
        guard let link = promptForClientLink(
            title: "Add Server",
            hint: "Paste the bx link for the new server. It will be added to your list and used right away.",
            confirmTitle: "Add and Switch"
        ) else { return }
        let name = promptForServerName()
        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let result = Result { try GuardianClient().addServer(name: name, link: link) }
            DispatchQueue.main.async {
                guard let self else { return }
                switch result {
                case .success(let list):
                    self.lastServers = list
                    self.switchServer(name: list.added) { outcome in
                        let alert = NSAlert()
                        alert.messageText = outcome?.applied == true ? "Switched" : "Added"
                        alert.informativeText = addServerOutcomeMessage(added: list.added, switched: outcome)
                        NSApp.activate(ignoringOtherApps: true)
                        alert.runModal()
                    }
                case .failure(let error):
                    self.showGuardianFailure(title: "Could not add that server", error: error)
                }
            }
        }
    }

    /// 名字可空:空就让 Guardian 按链接推导。
    private func promptForServerName() -> String {
        let alert = NSAlert()
        alert.messageText = "Name this server"
        alert.informativeText = "Leave it empty to name it after the server's address."
        let field = NSTextField(frame: NSRect(x: 0, y: 0, width: 260, height: 24))
        field.placeholderString = "e.g. tokyo"
        alert.accessoryView = field
        alert.addButton(withTitle: "Continue")
        NSApp.activate(ignoringOtherApps: true)
        alert.runModal()
        return field.stringValue.trimmingCharacters(in: .whitespacesAndNewlines)
    }
```

`promptForClientLink(title:hint:confirmTitle:)` 已存在(`replaceConfiguration` 在用);签名不同就以文件为准。**`replaceConfiguration()` 与它的菜单项一个字不动** —— 那是旧 Guardian 的降级路(`replaceConfigurationLivesInMenu`)。

- [ ] **Step 6: 构建、套件、守卫、变异**

Run: `swift build --package-path apps/macos/BxMenu 2>&1 | grep -E "error|Build complete"; bash scripts/test-macos-menu.sh 2>&1 | tail -1; go test ./internal/cli/ 2>&1 | tail -2`
Expected: 全绿(既有 `TestMacMenuMovesServerActionsIntoTheServersWindow` 钉着 `onReplaceConfiguration` —— 它现在要**改锚点**:窗口不再有 Replace,菜单里的降级路仍在;把那条守卫里对 `onReplaceConfiguration`/`replaceConfiguration?()` 的断言改成对 `onAddServer`/`onAddServer?()`,对 `replaceConfigurationLivesInMenu(` 门控的断言**保留**)。变异:把 `self.switchServer(name: list.added` 改成 `self.switchServer(name: name` → `TestMacMenuAddServerAddsThenSwitchesWithoutPrivilege` 红;恢复。

- [ ] **Step 7: 提交**

```bash
bash scripts/verify.sh --quick && git add -A apps/macos/BxMenu internal/cli && git commit -m "feat(menu): Servers 窗口 Add Server… —— 贴链接加进清单并热切换,不弹密码、不断网;Replace Configuration 从窗口退场

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

**Phase ④ 真机验收**:Servers… → Add Server… → 贴一条链接、名字留空 → 不弹密码、不断网,窗口里多一台(名字 = 服务器地址)且已切换、出口变了;再贴同一条 → 「Could not add that server」(409,清单不变);贴一条坏链接 → add 成功但 switch 回滚,那句「did not switch」出现,旧那台仍在用。

---

### Task 8: spec §7 的 shell-out 白名单守卫 + 记档

**Files:**
- Create: `internal/cli/macos_menu_shellout_allowlist_test.go`
- Modify: `CLAUDE.md`(「internal/doctor」那段之后加一段 ≤ 15 行)

**Interfaces:**
- Consumes: `swiftFunctionDefs(source) []swiftFunc`(`name`、`start`、`end`;`start/end` 是函数体不含花括号的字节区间,按体积升序)、`menuMainSwiftCode`。
- 现状(执行前 grep 过):调用点落在 exportDiagnostics(1945)、replaceConfiguration(2039/2046)、beginSetup(2063/2069)、uninstallBx(2108)、runEmbeddedInstaller(2141)、updateBx(2410,闭包内)、performToggle(2572),恰好是 spec §7 那七个。

- [ ] **Step 1: 写守卫(应当直接绿 —— 它钉的是现状;然后用变异证明它会红)**

```go
// internal/cli/macos_menu_shellout_allowlist_test.go
package cli

import (
	"sort"
	"strings"
	"testing"
)

// **这是 spec §1 那张表的不变量。** 菜单上只有两类动作允许提权 / 开终端:
// Guardian 不存在时也必须能跑的(install / repair / uninstall / update / 逃生口),
// 与必须走终端归档的(Export Diagnostics)。加进来可以,悄悄加不行 —— 与
// destPublicationAllowlist 同款:每一条写理由。
var menuShellOutAllowlist = map[string]string{
	"beginSetup":           "首次 setup 要写 /etc/bx 与装 unit,Guardian 还不存在",
	"runEmbeddedInstaller": "install / repair 要换掉 Guardian 自己",
	"uninstallBx":          "卸载要停掉并删掉 Guardian",
	"updateBx":             "更新要停掉 Guardian 换二进制",
	"exportDiagnostics":    "诊断包归档写用户目录并 chown,一年一次,走终端(spec §1)",
	"performToggle":        "Turn Off 的逃生口:Guardian 死了也要能关掉保护(2026-08-04 那 71 分钟)",
	"replaceConfiguration": "旧 Guardian(没有 servers 能力)的降级路;新 Guardian 上菜单不画它",
}

func TestMacMenuShellOutsStayOnTheAllowlist(t *testing.T) {
	code := menuMainSwiftCode(t)
	defs := swiftFunctionDefs(code)
	if len(defs) == 0 {
		t.Fatal("一个函数定义都没枚举到 —— 守卫读不懂现在的 main.swift")
	}
	enclosing := func(offset int) string {
		best := ""
		bestSize := -1
		for _, d := range defs {
			if d.start <= offset && offset <= d.end {
				if size := d.end - d.start; bestSize < 0 || size < bestSize {
					best, bestSize = d.name, size
				}
			}
		}
		return best
	}
	var offenders []string
	seen := map[string]bool{}
	for _, needle := range []string{"runPrivileged(", "openTerminal(", "runPrivilegedScriptOffMainThread("} {
		from := 0
		for {
			i := strings.Index(code[from:], needle)
			if i < 0 {
				break
			}
			at := from + i
			from = at + len(needle)
			// `private func runPrivileged(` 是定义不是调用点:签名落在自己的函数体之外,
			// enclosing 会把它算到外层(<top-level>)。按前面紧挨的 `func ` 跳过。
			if strings.HasSuffix(strings.TrimRight(code[:at], " \t"), "func") {
				continue
			}
			fn := enclosing(at)
			if fn == "" {
				fn = "<top-level>"
			}
			seen[fn] = true
			if _, ok := menuShellOutAllowlist[fn]; !ok {
				offenders = append(offenders, fn+" → "+needle)
			}
		}
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("main.swift 里有白名单之外的提权/开终端调用:%v —— 菜单的动作应经 Guardian 的 owner 门;"+
			"确实必须留在 CLI 的(Guardian 不存在时也要能跑 / 必须走终端归档)把函数名连同理由加进 menuShellOutAllowlist",
			offenders)
	}
	// 反向:白名单里不许有陈旧条目(函数已不存在或已不再 shell-out)。
	for fn := range menuShellOutAllowlist {
		if !seen[fn] {
			t.Errorf("白名单条目 %q 已经没有任何 shell-out 落在它里面 —— 删掉它,别让清单说假话", fn)
		}
	}
}
```

Run: `go test ./internal/cli/ -run TestMacMenuShellOutsStayOnTheAllowlist -v 2>&1 | tail -3`
Expected: PASS(若某个名字对不上,先 `grep -n "runPrivileged(\|openTerminal(\|runPrivilegedScriptOffMainThread(" apps/macos/BxMenu/Sources/BxMenu/main.swift` 看真实的包围函数,**按事实改白名单的名字,不改判据**)。变异:在 `openDiagnosticsChecks` 里加一行 `_ = runPrivileged("true")`(cp 备份)→ 守卫红并点名 `openDiagnosticsChecks`;恢复。再变异:白名单里多加一条不存在的 `"nope": "x"` → 反向断言红;恢复。

- [ ] **Step 2: CLAUDE.md**

在「internal/doctor」那段之后加一段(≤ 15 行):`/v1/doctor`(Guardian 进程内采集、同一个 `Judge`、能力 `doctor`、probe 那条是 TCP 往返不是完整握手、探测只在用户点了才发生)、`internal/platformcheck` 下沉、Diagnostics 两页、Add Server 取代 Replace(同名 409、名字可省略、旧 Guardian 降级路仍在菜单)、shell-out 白名单守卫 `TestMacMenuShellOutsStayOnTheAllowlist`;真机未验清单(Checks 页与 `bx doctor --json` 逐条对上、Add Server 三种结局)。**只点名存在的测试与路径**(`TestEveryTestNameMentionedInProseExists` / `TestDocumentedFilePathsExist`)。

- [ ] **Step 3: 全量 verify、提交**

```bash
bash scripts/verify.sh && git add internal/cli/macos_menu_shellout_allowlist_test.go CLAUDE.md && git commit -m "test(menu): shell-out 白名单守卫 —— 菜单提权/开终端只许落在 spec §1 那七个函数里;记档 ③④ 期

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>"
```

---

## 自审

- **Spec 覆盖**:§3 `/v1/doctor` + Guardian 采集 → Task 1–3;§6 Checks 页 + 能力门控 + Run again → Task 4–5;§4 Add Server(add + 切换、同名不静默、旧 Guardian 降级路保留、`bx setup` 不动)→ Task 6–7;§7 shell-out 白名单 → Task 8;§7 其它守卫(纯度、接线、能力钉字面量)分布在各任务。§1 的「Export Diagnostics 留终端」由 Task 5 保留 `exportDiagnostics()` 并进白名单。
- **占位符**:Task 3 的 `setup.LinkPort` 与 Task 7 的 `promptForClientLink` 签名标了「以文件为准」;Task 2 的服务三行期望值标了「以当前实现为准、只改期望不改判据」—— 都是执行时一眼能定的事实。
- **类型一致性**:`DoctorFactsFunc(ctx, configPath, status)` 在 Task 3 的定义、handler、`localAPIOptionsFor` 三处一致;`ServerList.added` ↔ Go `Added json:"added,omitempty"`;`switchServer(name:completion:)` 在 Task 7 的两个调用点一致;`doctorAvailable` 的字面量 `"doctor"` 由 Task 3 的 `TestDoctorCapabilityIsDeclaredAndPinned` 钉住。
- **一条刻意的偏离**:spec §4 说名字「默认取 `setup.LinkHost`」由菜单推导;本计划改由 Guardian 用 `config.DeriveServerName` 推导并经 `added` 回传 —— `bx://` 是 base64 信封,Swift 端解它是第二份判据。
