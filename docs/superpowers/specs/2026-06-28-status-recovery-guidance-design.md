# `bx status` 恢复指引(④ 人类兼底 UX)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-28)。

## 背景与定位

④ = 人类友好兼底 UX(agent 不在驱动时,清晰状态/恢复/大白话)。现状:`bx status` 面板(`stats.Render`)只显**状态**(健康/延迟/连接/流量),不说**含义/怎么办**;`bx doctor` 有 hint,但人最常看的 status 不指引。daemon 未起时 `bx status` 返回原始 Go 错误("连接 bx 失败")。

用户定:先做 **status 恢复指引**,**只覆盖两态(不健康 / daemon 未起)**,daemon 未起**友好打印 + exit 0**(「没运行」是有效状态答案、非报错)。其余(高重连/armed)不做(YAGNI)。

## 目标 / 非目标

**目标**:① `bx status` 面板在隧道**不健康**时追加大白话恢复块——怎么了(可能被封/网络)+ kill-switch 正在保护真实 IP(暂不通是保护非故障)+ 具体下一步(等重连 / `bx doctor` / 让 agent 换隐写传输或 `sudo bx setup` 换链接)。② daemon **未起**时 `bx status`(非 json)友好打印「未运行 + 启动/体检」并 exit 0。**`--json` 路径完全不变**(机器面干净;指引仅人面)。

**非目标**:其余状态(高重连 flapping / mutation armed)指引;`bx doctor` 改动;TUI/菜单栏;`--json` 行为。

## 架构

### 恢复指引(stats 包)

```go
// recoveryHint:隧道不健康时返回大白话恢复块;健康返回 ""(面板不加噪音)。纯函数。
func recoveryHint(r Report) string {
    if r.TunnelHealthy {
        return ""
    }
    // 含:被封/网络可能 + kill-switch 保护说明 + 三条下一步(重连次数取 r.Restarts)
    return fmt.Sprintf(`
  ⚠ 隧道不健康:可能是服务器被封或网络波动。
    你的真实 IP 已被 kill-switch 保护(外网暂时不通是「保护」,不是故障)。
    可以试:
      · 稍等十几秒看是否自动重连(已重连 %d 次)
      · bx doctor                体检找原因
      · 让你的 agent 换隐写传输(brook→REALITY)绕过封锁,或 sudo bx setup 换新链接
`, r.Restarts)
}
```
`Render` 末尾追加 `recoveryHint(r)`(非空才有内容)。健康时面板与现状一字不差。

### daemon 未起友好面板(stats 包)

```go
// RenderNotRunning:bx status 连不上守护进程时的人面提示。
func RenderNotRunning() string {
    return "bx 未运行。\n  启动:sudo bx up        体检:bx doctor\n"
}
```

### statusAction(cli)

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
(仅 `if err != nil` 分支加 json/非 json 分流;其余不动。)

## 数据流 / 错误处理

`bx status`(非 json)→ 取 Report:成功 → `Render`(不健康则带恢复块);失败(daemon 未起/socket 不可达)→ `RenderNotRunning` + exit 0。`bx status --json` → 成功输出 JSON、失败返回错误(机器面与现状一致)。无新失败路径。

## 测试策略(全 Mac 原生)

- `recoveryHint`:健康 Report → `""`;不健康 Report → 含 "kill-switch"、"bx doctor"、"重连"(及 Restarts 数)、"换" 等关键字。
- `Render`:不健康 Report 输出含恢复块、健康 Report 不含(与现有 Render 测试并存,健康路径输出不变)。
- `RenderNotRunning`:含 "sudo bx up"、"未运行"。
- 回归:既有 stats/cli 测不受影响;`--json` 路径不变。

## 决策记录

- 只两态:不健康(恢复块)+ daemon 未起(友好面板 exit 0);其余 YAGNI。
- 恢复块第三条把人引向 agent(换传输)+ 手动兜底(setup),呼应 AI-native 兼底。
- `--json` 完全不变(机器面干净);指引仅人面。
- `recoveryHint`(unexported,纯函数)+ `RenderNotRunning`(exported,cli 调)放 stats 包,与 `Render` 同处、可测。

## 范围自检

单一小增量(`recoveryHint` + `Render` 追加 + `RenderNotRunning` + statusAction 分流 + 单测),全 Mac 可测、纯文本、`--json` 零变更。适合一份小 plan(1 任务)。
