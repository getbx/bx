# `bx status` 恢复指引(④ 人类兼底 UX)Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** `bx status` 在隧道不健康时追加大白话恢复块、daemon 未起时友好打印(exit 0),让人在出问题时知道怎么办;`--json` 完全不变。

**Architecture:** `stats` 包加 `recoveryHint(r) string`(不健康才有内容)由 `Render` 末尾追加,加 `RenderNotRunning()`;`statusAction` 在取 Report 失败时按 `--json` 分流(机器面返回错误、人面友好打印 + exit 0)。纯文本、人面专用、JSON 零变更。

**Tech Stack:** Go 1.26.3;改 `internal/stats/render.go`、`internal/stats/render_test.go`、`internal/cli/cli.go`。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3。TDD;提交中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`;master 直接提交。
- **只两态**:隧道不健康(恢复块)+ daemon 未起(友好面板)。其余(高重连/armed)不做。
- **daemon 未起 = 友好打印 + exit 0**(非 json);`bx status --json` 失败仍返回错误(机器面不变)。
- **`--json` 路径完全不变**;指引仅人面。
- 健康时 `Render` 输出与现状一字不差(`recoveryHint` 健康返回 `""`)。

---

### Task 1: recoveryHint + RenderNotRunning + Render 追加 + statusAction 分流

**Files:**
- Modify: `internal/stats/render.go`(`recoveryHint` 纯函数;`Render` 末尾追加;`RenderNotRunning`)
- Modify: `internal/stats/render_test.go`(3 个测试)
- Modify: `internal/cli/cli.go`(`statusAction` 取 Report 失败时按 json 分流)

**Interfaces:**
- Consumes: 现有 `stats.Report`/`Render`、`readStatusReport`/`writeJSON`(cli)。
- Produces:
  - `func recoveryHint(r Report) string`(unexported)
  - `func RenderNotRunning() string`(exported,cli 调)

- [ ] **Step 1: 写失败测试**

在 `internal/stats/render_test.go` 末尾追加(`strings`/`testing` 已 import):
```go
func TestRecoveryHint(t *testing.T) {
	if got := recoveryHint(Report{TunnelHealthy: true}); got != "" {
		t.Errorf("健康时 recoveryHint 应为空,实际:%q", got)
	}
	out := recoveryHint(Report{TunnelHealthy: false, Restarts: 3})
	for _, want := range []string{"kill-switch", "bx doctor", "重连 3", "换"} {
		if !strings.Contains(out, want) {
			t.Errorf("不健康 recoveryHint 应含 %q,实际:\n%s", want, out)
		}
	}
}

func TestRender_UnhealthyHasRecovery(t *testing.T) {
	out := Render(Report{TunnelHealthy: false, Restarts: 2})
	if !strings.Contains(out, "kill-switch") || !strings.Contains(out, "bx doctor") {
		t.Errorf("不健康面板应含恢复指引,实际:\n%s", out)
	}
	if strings.Contains(Render(Report{TunnelHealthy: true}), "kill-switch") {
		t.Error("健康面板不应含恢复块")
	}
}

func TestRenderNotRunning(t *testing.T) {
	out := RenderNotRunning()
	for _, want := range []string{"未运行", "sudo bx up"} {
		if !strings.Contains(out, want) {
			t.Errorf("RenderNotRunning 应含 %q,实际:%q", want, out)
		}
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/stats/ -run 'Recovery|NotRunning|UnhealthyHasRecovery' 2>&1 | head`
Expected: 编译失败(`undefined: recoveryHint` / `undefined: RenderNotRunning`)。

- [ ] **Step 3: 改 render.go**

(a) `Render` 末尾(现 `return b.String()` 之前)追加恢复块:
```go
	fmt.Fprint(&b, recoveryHint(r))
	return b.String()
```

(b) 在 `Render` 之后(`humanBytes` 之前或之后均可)加两函数:
```go
// recoveryHint:隧道不健康时返回大白话恢复块(怎么了 + kill-switch 保护说明 + 下一步);
// 健康返回 ""(面板不加噪音)。纯函数,人面专用。
func recoveryHint(r Report) string {
	if r.TunnelHealthy {
		return ""
	}
	return fmt.Sprintf(`
  ⚠ 隧道不健康:可能是服务器被封或网络波动。
    你的真实 IP 已被 kill-switch 保护(外网暂时不通是「保护」,不是故障)。
    可以试:
      · 稍等十几秒看是否自动重连(已重连 %d 次)
      · bx doctor                体检找原因
      · 让你的 agent 换隐写传输(brook→REALITY)绕过封锁,或 sudo bx setup 换新链接
`, r.Restarts)
}

// RenderNotRunning:bx status 连不上守护进程时的人面提示(daemon 未起)。
func RenderNotRunning() string {
	return "bx 未运行。\n  启动:sudo bx up        体检:bx doctor\n"
}
```

- [ ] **Step 4: 改 statusAction(cli.go)**

把 `statusAction`(现 `rep, err := readStatusReport(); if err != nil { return err } ...`)替换为按 json 分流版:
```go
func statusAction(c *cli.Context) error {
	rep, err := readStatusReport()
	if err != nil {
		if c.Bool("json") {
			return err // 机器面:不变(返回错误)
		}
		fmt.Print(stats.RenderNotRunning()) // 人面:友好 + exit 0
		return nil
	}
	if c.Bool("json") {
		return writeJSON(os.Stdout, rep)
	}
	fmt.Print(stats.Render(rep))
	return nil
}
```
(`stats`/`fmt`/`os` 在 cli.go 已 import。)

- [ ] **Step 5: 跑绿 + 全量**

Run:
```bash
go test ./internal/stats/ -run 'Recovery|NotRunning|UnhealthyHasRecovery|Render' -v
go build ./... && go vet ./... && go test ./...
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=linux go vet -tags integration ./internal/supervisor/
```
Expected: 新 3 测 + 既有 `TestRender_Unhealthy`/`TestRender_ContainsKeyInfo`(健康路径不变)全绿;全套件绿;两平台编译过。

- [ ] **Step 6: 提交**

```bash
git add internal/stats/render.go internal/stats/render_test.go internal/cli/cli.go
git commit -m "feat(stats): bx status 恢复指引(④ 人类兼底 UX)

隧道不健康时面板追加大白话恢复块(被封/网络可能 + kill-switch 保护说明 +
等重连/bx doctor/让 agent 换传输或 setup 换链接);daemon 未起时 status(非 json)
友好打印「未运行 + sudo bx up」并 exit 0。--json 完全不变;健康面板输出不变。

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- 不健康 → 恢复块(被封/网络 + kill-switch 保护 + 三条下一步含 Restarts)→ Step3b `recoveryHint` + Step3a `Render` 追加。
- daemon 未起 → 友好打印 exit 0(非 json)、json 仍返回错误 → Step3b `RenderNotRunning` + Step4 `statusAction` 分流。
- `--json` 不变、健康面板不变 → Step4(json 分支原样)+ Step3(健康 recoveryHint 返回 "")。
- 测试(recoveryHint 空/含关键字、Render 不健康含恢复块/健康不含、RenderNotRunning)→ Step1。

**占位扫描:** 无 TBD;每步完整代码/命令。

**类型一致性:** `recoveryHint(Report) string`(Step3b)与测试(Step1)、`Render` 调用(Step3a)一致;`RenderNotRunning() string`(Step3b)与测试(Step1)、`statusAction` 调用 `stats.RenderNotRunning()`(Step4)一致;`statusAction` 复用现有 `readStatusReport`/`writeJSON`/`stats.Render`,签名不变。
