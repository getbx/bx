# bx_diagnose + bx_logs 接守护进程(mcp 只读诊断)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 mcp 的 `bx_diagnose`/`bx_logs` 从 stub 接成真:diagnose 以守护进程 status 推导结构化 findings(④ 机器版),logs 经 `install.TailLogs` 返回文本并优雅降级。

**Architecture:** 纯函数 `diagnoseFindings(StatusOut, reachable) []Finding` + `logsResultText(raw, err) string`(Mac 可测);`liveOps.Diagnose` 经 `o.Status()` 取状态、`liveOps.Logs` 经新 `install.TailLogs`;`StatusOut` 补 `Restarts`(诊断需要)。两者只读、永不返错、免 root、不动 isRoot 门控。

**Tech Stack:** Go 1.26.3;改 `internal/mcp/ops.go`、`internal/mcp/liveops.go`、`internal/install/install.go`;测 `internal/mcp/diag_test.go`(新)。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **只读、永不返错**:Diagnose/Logs 不返 error——连不上/无权限降级为 finding/文本。免 root;**不动 `isRoot` 门控**(它们本不在门控组)。
- `bx_diagnose` 以守护进程 **status** 为源(业主可访),不读 root 配置、不 shell inspect。
- `bx_logs` 经 `install.TailLogs`(复用 ShowLogs 的 darwin tail / linux journalctl 源选择,但返回文本);无权限/空→清晰提示 `sudo bx logs`。

---

### Task 1: bx_diagnose(StatusOut.Restarts + diagnoseFindings + Diagnose 接线)

**Files:**
- Modify: `internal/mcp/ops.go`(`StatusOut` 加 `Restarts int`)
- Modify: `internal/mcp/liveops.go`(`Status()` 填 `Restarts`;加 `diagnoseFindings`;接 `Diagnose`;import `fmt`)
- Create: `internal/mcp/diag_test.go`(`TestDiagnoseFindings` + `hasFinding`)

**Interfaces:**
- Consumes: 现有 `o.Status() (StatusOut, error)`、`Finding`/`DiagnoseOut`、`supervisor.FetchStatusReport`(其 `stats.Report` 有 `Restarts`)。
- Produces:
  - `StatusOut.Restarts int`
  - `func diagnoseFindings(rep StatusOut, reachable bool) []Finding`

- [ ] **Step 1: 写失败测试**

新建 `internal/mcp/diag_test.go`:
```go
package mcp

import (
	"strings"
	"testing"
)

func hasFinding(fs []Finding, sev, substr string) bool {
	for _, f := range fs {
		if f.Severity == sev && strings.Contains(f.Title+f.Remediation, substr) {
			return true
		}
	}
	return false
}

func TestDiagnoseFindings(t *testing.T) {
	if fs := diagnoseFindings(StatusOut{}, false); len(fs) != 1 || fs[0].Severity != "error" || !strings.Contains(fs[0].Title, "未运行") {
		t.Fatalf("不可达应 1 条 error 未运行, got %+v", fs)
	}
	if fs := diagnoseFindings(StatusOut{TunnelHealthy: true}, true); len(fs) != 1 || fs[0].Severity != "info" {
		t.Fatalf("健康应 1 条 info, got %+v", fs)
	}
	if !hasFinding(diagnoseFindings(StatusOut{TunnelHealthy: false}, true), "error", "kill-switch") {
		t.Error("不健康应有 error kill-switch")
	}
	if !hasFinding(diagnoseFindings(StatusOut{TunnelHealthy: true, Restarts: 5}, true), "warn", "重连") {
		t.Error("Restarts=5 应有 warn 重连")
	}
	if !hasFinding(diagnoseFindings(StatusOut{TunnelHealthy: true, MutationState: "armed"}, true), "warn", "待确认") {
		t.Error("armed 应有 warn 待确认")
	}
	if got := diagnoseFindings(StatusOut{TunnelHealthy: false, MutationState: "armed"}, true); len(got) != 2 {
		t.Errorf("不健康+armed 应 2 条, got %d: %+v", len(got), got)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run DiagnoseFindings 2>&1 | head`
Expected: 编译失败(`diagnoseFindings` undefined / `StatusOut.Restarts` undefined)。

- [ ] **Step 3: 改 ops.go(StatusOut.Restarts)**

`StatusOut` 结构体加字段(`LatencyMS` 后):
```go
	Restarts      int    `json:"restarts"`
```

- [ ] **Step 4: 改 liveops.go(Status 填 Restarts + diagnoseFindings + Diagnose)**

(a) `Status()` 的 `return StatusOut{...}` 里加 `Restarts: rep.Restarts,`(`rep` 是 `stats.Report`,有 `Restarts`)。

(b) import 块加 `"fmt"`。

(c) 把 `Diagnose` 的 stub 整体替换为:
```go
// diagnoseFindings 从守护进程 status 推导结构化诊断(④ 人面恢复指引的机器版)。纯函数。
// reachable=false:守护进程连不上(rep 忽略)。
func diagnoseFindings(rep StatusOut, reachable bool) []Finding {
	if !reachable {
		return []Finding{{Severity: "error", Title: "bx 未运行(连不上守护进程)", Remediation: "sudo bx up"}}
	}
	var fs []Finding
	if !rep.TunnelHealthy {
		fs = append(fs, Finding{Severity: "error",
			Title:       "隧道不健康:可能服务器被封或网络波动;真实 IP 已被 kill-switch 保护",
			Remediation: "等十几秒看自动重连;不行用 bx_set_transport 换隐写传输(brook→REALITY),或 sudo bx setup 换新链接"})
	}
	if rep.Restarts > 3 {
		fs = append(fs, Finding{Severity: "warn",
			Title:       fmt.Sprintf("隧道频繁重连(%d 次,可能不稳定)", rep.Restarts),
			Remediation: "查 bx_logs / 检查服务器与网络"})
	}
	if rep.MutationState == "armed" {
		fs = append(fs, Finding{Severity: "warn",
			Title:       "有待确认的改动(armed),未 commit 将自动回滚",
			Remediation: "bx_verify 通过后 bx_commit;或 bx_rollback 立即还原"})
	}
	if len(fs) == 0 {
		fs = append(fs, Finding{Severity: "info", Title: "隧道健康,无异常"})
	}
	return fs
}

func (o *liveOps) Diagnose() (DiagnoseOut, error) {
	rep, err := o.Status()
	return DiagnoseOut{Findings: diagnoseFindings(rep, err == nil)}, nil
}
```

- [ ] **Step 5: 跑绿 + 全量**

Run:
```bash
go test ./internal/mcp/ -run DiagnoseFindings -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: `TestDiagnoseFindings` PASS;既有 mcp 测不受影响;全套件绿;两平台编译过。

- [ ] **Step 6: 提交**

```bash
git add internal/mcp/ops.go internal/mcp/liveops.go internal/mcp/diag_test.go
git commit -m "feat(mcp): bx_diagnose 接守护进程 status(④ 机器版结构化 findings)

diagnoseFindings 纯函数从守护进程 status 推导 findings(未运行/不健康+kill-switch/
频繁重连/armed/健康);liveOps.Diagnose 经 o.Status() 产出、永不返错;StatusOut 补 Restarts。
业主可访、不读 root 配置、免 root。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: bx_logs(install.TailLogs + logsResultText + Logs 接线)

**Files:**
- Modify: `internal/install/install.go`(加 `TailLogs`)
- Modify: `internal/mcp/liveops.go`(加 `logsResultText`;接 `Logs`;import `strings` + `internal/install`)
- Modify: `internal/mcp/diag_test.go`(追加 `TestLogsResultText`)

**Interfaces:**
- Consumes: 现有 `install.ServiceName`/`existingPaths`/`launchdStdoutPath`/`launchdStderrPath`(install 包内);`LogsIn`/`LogsOut`。
- Produces:
  - `func TailLogs(service string, lines int) (string, error)`(install 包)
  - `func logsResultText(raw string, err error) string`(mcp 包)

- [ ] **Step 1: 写失败测试**

在 `internal/mcp/diag_test.go` 追加(import 加 `"errors"`):
```go
func TestLogsResultText(t *testing.T) {
	if got := logsResultText("", errors.New("denied")); !strings.Contains(got, "sudo bx logs") {
		t.Errorf("err 应提示 sudo bx logs, got %q", got)
	}
	if got := logsResultText("   ", nil); !strings.Contains(got, "无日志") {
		t.Errorf("空应提示无日志, got %q", got)
	}
	if got := logsResultText("line1\nline2", nil); got != "line1\nline2" {
		t.Errorf("正常应原样返回, got %q", got)
	}
}
```
(把 diag_test.go 的 import 改为 `import ("errors"; "strings"; "testing")`。)

- [ ] **Step 2: 跑红**

Run: `go test ./internal/mcp/ -run LogsResultText 2>&1 | head`
Expected: 编译失败(`logsResultText` undefined)。

- [ ] **Step 3: 加 install.TailLogs(install.go)**

在 `ShowLogs` 之后追加(`exec`/`runtime`/`fmt`/`strings`/`os` 已 import):
```go
// TailLogs 返回服务末 lines 行日志(非 follow,供 mcp bx_logs 返回文本)。
// 与 ShowLogs 同源选择:darwin launchd 日志文件 tail、linux journalctl;返回合并输出。
func TailLogs(service string, lines int) (string, error) {
	if lines <= 0 {
		lines = 100
	}
	if runtime.GOOS == "darwin" && service == ServiceName {
		paths := existingPaths(launchdStdoutPath, launchdStderrPath)
		if len(paths) == 0 {
			return "", fmt.Errorf("未找到 bx 日志文件(服务可能尚未启动)")
		}
		args := append([]string{"-n", fmt.Sprint(lines)}, paths...)
		out, err := exec.Command("tail", args...).CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("tail: %w", err)
		}
		return string(out), nil
	}
	out, err := exec.Command("journalctl", "-u", service, "--no-pager", "-n", fmt.Sprint(lines)).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("journalctl: %w", err)
	}
	return string(out), nil
}
```

- [ ] **Step 4: 加 logsResultText + 接 Logs(liveops.go)**

(a) import 块加 `"strings"` 和 `"github.com/getbx/bx/internal/install"`。

(b) 把 `Logs` 的 stub 整体替换为:
```go
// logsResultText 把 TailLogs 结果转成给 agent 的文本(优雅降级)。纯函数。
func logsResultText(raw string, err error) string {
	if err != nil {
		return "取日志失败(可能无权限):" + err.Error() + "\n试 sudo bx logs"
	}
	if strings.TrimSpace(raw) == "" {
		return "无日志(或本用户无权限读 journal)。试 sudo bx logs"
	}
	return raw
}

func (o *liveOps) Logs(in LogsIn) (LogsOut, error) {
	lines := in.Lines
	if lines <= 0 {
		lines = 100
	}
	raw, err := install.TailLogs(install.ServiceName, lines)
	return LogsOut{Text: logsResultText(raw, err)}, nil
}
```
(`in.Since` 暂不支持,YAGNI。)

- [ ] **Step 5: 跑绿 + 全量**

Run:
```bash
go test ./internal/mcp/ -run 'LogsResultText|DiagnoseFindings' -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: `TestLogsResultText` + `TestDiagnoseFindings` PASS;全套件绿;两平台编译过。

- [ ] **Step 6: 提交**

```bash
git add internal/install/install.go internal/mcp/liveops.go internal/mcp/diag_test.go
git commit -m "feat(mcp): bx_logs 接 install.TailLogs(优雅降级)

install.TailLogs 返回服务末 N 行日志(复用 ShowLogs 源选择,返回文本);
liveOps.Logs 经 logsResultText 降级(无权限/空→提示 sudo bx logs)、永不返错。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- `diagnoseFindings`(5 态)+ `Diagnose` 经 status、永不返错 → Task1 Step4;`StatusOut.Restarts` + Status 填充 → Step3/4a。
- `install.TailLogs`(复用源选择、返回文本)→ Task2 Step3;`logsResultText` 降级 + `Logs` 接线、永不返错 → Step4。
- 不动 isRoot 门控(Diagnose/Logs 本不在门控组)→ 计划未触碰 isRoot。
- 测试:diagnoseFindings 多态 + logsResultText 降级 → Task1 Step1 + Task2 Step1。

**占位扫描:** 无 TBD;每步完整代码/命令。`install.TailLogs` 子进程(journalctl/tail)不单测(Mac 跑不了 linux journalctl;纯逻辑 `logsResultText` 已覆盖降级),CI/真机覆盖。

**类型一致性:** `diagnoseFindings(StatusOut, bool) []Finding`(Task1 S4)与测试(S1)一致;`StatusOut.Restarts int`(S3)与 Status 填充(S4a)、diagnoseFindings 用 `rep.Restarts`(S4)一致;`logsResultText(string, error) string`(Task2 S4)与测试(S1)、`Logs` 调用(S4)一致;`install.TailLogs(string, int)(string,error)`(S3)与 `Logs` 调用(S4)一致;复用 `Finding{Severity,Title,Remediation}`/`DiagnoseOut{Findings}`/`LogsOut{Text}`/`LogsIn{Lines}`(ops.go 现有)。
