# bx_diagnose + bx_logs 接守护进程(mcp 只读诊断)设计

Status: APPROVED-FOR-PLANNING(brainstormed 2026-06-28)。

## 背景与定位

mcp 控制面有几个 stub(CodeNotImplemented):只读诊断 `bx_diagnose`/`bx_logs`/`bx_verify`/`bx_plan` + 改动类 `bx_setup`(特权安装,真需 root,非控制面 op)/`bx_restart_tunnel`(守护进程 op,需新端点)。三类不同活,用户定**先接只读诊断的 `bx_diagnose`+`bx_logs`**——agent 故障排查工具,价值最高、风险最低、复用现有逻辑、无 root/新端点/owner-auth 纠结。

关键约束:agent 跑 `bx mcp` 是**业主(非 root)**,能读守护进程 **status**(socket 0666),但读不到 root-owned `/etc/bx/config.yaml`(0600)或(常)journal。故 `bx_diagnose` 以**守护进程 status 为真相源**(不读 root 配置、不 shell `bx inspect`),是 **④ 人面恢复指引的机器版**(结构化 findings + 修复建议)。`Diagnose`/`Logs` 本就不在 `isRoot` 门控组(只 Setup/SetTransport/RestartTunnel/Rehijack 是),故**无鉴权改动**。

## 目标 / 非目标

**目标**:① `bx_diagnose` 真做:纯函数 `diagnoseFindings(rep StatusOut, reachable bool) []Finding` 从 status 推导结构化 findings(未运行/不健康+kill-switch/频繁重连/armed/健康),`liveOps.Diagnose` 经 `o.Status()` 取 status 后产出。② `bx_logs` 真做:`install.TailLogs(service, lines) (string, error)`(非 follow、返回末 N 行)+ 纯 `logsResultText(raw string, err error) string`(优雅降级:空/无权限→「无日志或无权限,试 sudo bx logs」)+ `liveOps.Logs` 包装。全 Mac 测纯逻辑;`TailLogs`(journalctl/tail)优雅降级。

**非目标**:`bx_setup`(特权安装,另议)/`bx_restart_tunnel`(守护进程端点,另片)/`bx_verify`/`bx_plan`;读 root 配置;`isRoot` 门控改动;`ShowLogs`(follow)行为变更。

## 架构

### bx_diagnose(mcp 包)

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
`o.Status()` 已存在(`FetchStatusReport` → `StatusOut`;daemon 不可达返 ToolError)。Diagnose 据 `err==nil` 定 reachable;**永不返错**(连不上也是一条 finding)。

### bx_logs(install + mcp 包)

```go
// install 包:TailLogs 返回服务末 N 行日志(非 follow)。linux journalctl、darwin tail;复用 ShowLogs 的源选择。
func TailLogs(service string, lines int) (string, error)
```
```go
// mcp 包:logsResultText 把 TailLogs 结果转成给 agent 的文本(优雅降级)。纯函数。
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
`Logs` 永不返错(降级为文本)。`in.Since` 暂不支持(YAGNI;只 lines)。

## 数据流 / 错误处理

`bx_diagnose` → `o.Status()`(socket)→ 可达则 status 推 findings、不可达则「未运行」finding;永不报错。`bx_logs` → `install.TailLogs`(journalctl/tail 子进程)→ `logsResultText` 降级 → 文本;永不报错(无权限/空→清晰提示)。两者只读、业主可跑、无 root。

## 测试策略(全 Mac 原生)

- `diagnoseFindings`:不可达→1 条 error「未运行」;不健康→含 error「不健康/kill-switch」;Restarts=5→含 warn「频繁重连」;MutationState="armed"→含 warn「待确认」;全健康→1 条 info「健康」;多态叠加(不健康+armed→2 条)。
- `logsResultText`:err→含「sudo bx logs」;空 raw→含「无日志」;正常 raw→原样返回。
- 回归:既有 mcp(Status/mutating 工具)测不受影响;`install.TailLogs` 子进程在 CI/真机跑(纯逻辑 `logsResultText` 已覆盖降级);两平台编译。

## 决策记录

- `bx_diagnose` 以守护进程 status 为源(业主可访)、是 ④ 的机器版结构化 findings;不读 root 配置、不 shell inspect。
- `bx_logs` 经 `install.TailLogs`(复用 ShowLogs 源选择)+ `logsResultText` 优雅降级(业主常读不到 journal)。
- 两者永不返错(降级为 finding/文本),只读、免 root、不动 isRoot 门控。
- `bx_verify`/`bx_plan`/`bx_setup`/`bx_restart_tunnel` 留后(各自不同活)。

## 范围自检

单一可实现增量(`diagnoseFindings`+`logsResultText` 纯函数 + `install.TailLogs` + `liveOps.Diagnose`/`Logs` 接线 + 单测),Mac 测纯逻辑、子进程优雅降级。适合一份小 plan(2 任务:① diagnose;② logs)。
