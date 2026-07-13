package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
)

// requireRoot 是改动类操作的权限门控(纯函数,便于测试)。
func requireRoot(isRoot bool) error {
	if !isRoot {
		return ToolError{
			Code:        CodePrivilegeRequired,
			Message:     "改动类操作需 root",
			Remediation: "用 `sudo bx mcp` 或 `ssh root@host bx mcp` 启动 server",
		}
	}
	return nil
}

func isRoot() bool { return os.Geteuid() == 0 }

// liveOps 把 Ops 绑到现有 internal 逻辑。
type liveOps struct {
	configPath string
}

// NewLiveOps 构造绑定现有逻辑的 Ops。
func NewLiveOps(configPath string) Ops { return &liveOps{configPath: configPath} }

// Capabilities 返回平台能力:platform from runtime.GOOS,transports,Installed = config 文件存在。
func (o *liveOps) Capabilities() (CapabilitiesOut, error) {
	_, err := os.Stat(o.configPath)
	installed := err == nil
	return CapabilitiesOut{
		Platform:   runtime.GOOS,
		Transports: []string{"brook", "reality"},
		Installed:  installed,
	}, nil
}

// Status 读 supervisor 控制 socket 并映射到 StatusOut。
// 若 socket 不可达(bx 未运行),返回 ToolError{CodeTunnelUnhealthy}。
func (o *liveOps) Status() (StatusOut, error) {
	rep, err := supervisor.FetchStatusReport(supervisor.SockPath)
	if err != nil {
		return StatusOut{}, ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     "bx 未运行或控制 socket 不可达",
			Remediation: "sudo bx up",
		}
	}
	return StatusOut{
		TunnelHealthy: rep.TunnelHealthy,
		LatencyMS:     rep.LatencyMS,
		Restarts:      rep.Restarts,
		Mode:          rep.Mode, // 分流模式 split/global/router(经 control socket 上报)
		UDPMode:       rep.UDPMode,
		MutationState: rep.MutationState,
	}, nil
}

// --- 以下为 honest stubs,待 Task 9 集成真实快照/supervisor 机器 ---

// diagnoseFindings 从守护进程 status 推导结构化诊断(④ 人面恢复指引的机器版)。纯函数。
// reachable=false:守护进程连不上(rep 忽略)。
func diagnoseFindings(rep StatusOut, reachable bool) []Finding {
	if !reachable {
		return []Finding{{Severity: "error", Title: "bx 未运行(连不上守护进程)", Remediation: "sudo bx up"}}
	}
	var fs []Finding
	if !rep.TunnelHealthy {
		fs = append(fs, Finding{
			Severity:    "error",
			Title:       "隧道不健康:可能服务器被封或网络波动;真实 IP 已被 kill-switch 保护",
			Remediation: "等十几秒看自动重连;不行用 bx_set_transport 换隐写传输(brook→REALITY),或 sudo bx setup 换新链接",
		})
	}
	if rep.Restarts > 3 {
		fs = append(fs, Finding{
			Severity:    "warn",
			Title:       fmt.Sprintf("隧道频繁重连(%d 次,可能不稳定)", rep.Restarts),
			Remediation: "查 bx_logs / 检查服务器与网络",
		})
	}
	if rep.MutationState == "armed" {
		fs = append(fs, Finding{
			Severity:    "warn",
			Title:       "有待确认的改动(armed),未 commit 将自动回滚",
			Remediation: "bx_verify 通过后 bx_commit;或 bx_rollback 立即还原",
		})
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

func (o *liveOps) Inspect(in InspectIn) (JSONCommandOut, error) {
	return runBXJSONCommand(inspectArgs(o.configPath, in))
}

func (o *liveOps) LeakCheck(in LeakCheckIn) (JSONCommandOut, error) {
	if out, blocked := browserConfirmationRequired(in); blocked {
		return out, nil
	}
	return runBXJSONCommand(leakCheckArgs(o.configPath, in))
}

func (o *liveOps) Observe(in ObserveIn) (JSONCommandOut, error) {
	return runBXJSONCommand(observeArgs(in))
}

func inspectArgs(configPath string, in InspectIn) []string {
	args := []string{"inspect", "--json", "--config", configPath}
	if !in.Probe || in.SkipProbe {
		args = append(args, "--skip-probe")
	}
	if strings.TrimSpace(in.Target) != "" {
		args = append(args, "--target", in.Target)
	}
	if strings.TrimSpace(in.Timeout) != "" {
		args = append(args, "--timeout", in.Timeout)
	}
	return args
}

func leakCheckArgs(configPath string, in LeakCheckIn) []string {
	args := []string{"leak-check", "--json", "--config", configPath}
	if in.Network {
		args = append(args, "--network")
	}
	if strings.TrimSpace(in.NetworkTimeout) != "" {
		args = append(args, "--network-timeout", in.NetworkTimeout)
	}
	if in.Browser {
		args = append(args, "--browser")
	}
	if strings.TrimSpace(in.BrowserTimeout) != "" {
		args = append(args, "--browser-timeout", in.BrowserTimeout)
	}
	for _, ip := range in.ExpectedIPs {
		if strings.TrimSpace(ip) != "" {
			args = append(args, "--expected-ip", ip)
		}
	}
	return args
}

func observeArgs(in ObserveIn) []string {
	args := []string{"observe", "--json"}
	if strings.TrimSpace(in.Duration) != "" {
		args = append(args, "--duration", in.Duration)
	}
	if strings.TrimSpace(in.Interval) != "" {
		args = append(args, "--interval", in.Interval)
	}
	if strings.TrimSpace(in.Scenario) != "" {
		args = append(args, "--scenario", in.Scenario)
	}
	return args
}

func browserConfirmationRequired(in LeakCheckIn) (JSONCommandOut, bool) {
	if !in.Browser || in.BrowserConfirmed {
		return JSONCommandOut{}, false
	}
	return JSONCommandOut{
		OK:    false,
		Error: "browser WebRTC leak check requires user confirmation before opening a local browser page",
		Hint:  "ask the user to confirm, then call bx_leak_check with browser=true and browser_confirmed=true",
		TestSteps: []string{
			"Tell the user bx will open a local 127.0.0.1 WebRTC test page.",
			"Ask the user to confirm this visible browser action.",
			"Call bx_leak_check with browser=true, browser_confirmed=true, and expected_ips set to acceptable proxy/VPS exits.",
			"Compare json.webrtc.leak_proof and json.checks against the expected exit IPs.",
		},
		Recommendations: []string{
			"Use network=true first for a non-browser exit-path check.",
			"Pass every acceptable proxy/VPS public IP in expected_ips to avoid false positives.",
			"If WebRTC reports unexpected_public_ip_detected, inspect bx_logs and active VPN/network-extension paths before changing routes.",
		},
	}, true
}

func runBXJSONCommand(args []string) (JSONCommandOut, error) {
	exe, err := os.Executable()
	if err != nil {
		return JSONCommandOut{OK: false, Command: append([]string{"bx"}, args...), Error: err.Error(), Hint: "run bx from an installed binary"}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, args...)
	out, err := cmd.CombinedOutput()
	command := append([]string{exe}, args...)
	if !json.Valid(out) {
		result := JSONCommandOut{OK: false, Command: command, Error: strings.TrimSpace(string(out))}
		if err != nil {
			result.Error = strings.TrimSpace(result.Error + "\n" + err.Error())
		}
		result.Hint = "run the matching bx CLI command directly for more detail"
		return result, nil
	}
	var report map[string]any
	if err := json.Unmarshal(out, &report); err != nil {
		return JSONCommandOut{OK: false, Command: command, Error: err.Error(), Hint: "run the matching bx CLI command directly for more detail"}, nil
	}
	result := JSONCommandOut{OK: true, Command: command, JSON: report}
	if okValue, ok := report["ok"].(bool); ok {
		result.OK = okValue
	}
	if err != nil {
		result.OK = false
		result.Error = err.Error()
	}
	return result, nil
}

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

func logsResultReport(raw string, err error) LogsOut {
	out := LogsOut{OK: err == nil, Text: raw}
	if err != nil {
		out.Error = err.Error()
		out.Hint = "try sudo bx logs"
	}
	if err == nil && strings.TrimSpace(raw) == "" {
		out.OK = false
		out.Hint = "try sudo bx logs"
	}
	return out
}

func (o *liveOps) Logs(in LogsIn) (LogsOut, error) {
	lines := in.Lines
	if lines <= 0 {
		lines = 100
	}
	raw, err := install.TailLogs(install.ServiceName, lines)
	out := logsResultReport(raw, err)
	if out.Text == "" {
		out.Text = logsResultText(raw, err)
	}
	return out, nil
}

func (o *liveOps) Plan(in PlanIn) (PlanOut, error) {
	return PlanOut{}, ToolError{
		Code:        CodeNotImplemented,
		Message:     "Plan 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "用 `bx darwin-plan` 或 `bx router-plan` 替代",
	}
}

func (o *liveOps) Verify() (VerifyOut, error) {
	return VerifyOut{}, ToolError{
		Code:        CodeNotImplemented,
		Message:     "Verify 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "待 Task 9 实现",
	}
}

// 以下 4 个改动类方法:先 requireRoot,再返回 NotImplemented。

func (o *liveOps) Setup(in SetupIn) error {
	if err := requireRoot(isRoot()); err != nil {
		return err
	}
	return ToolError{
		Code:        CodeNotImplemented,
		Message:     "Setup 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "用 `sudo bx setup <link>` 替代",
	}
}

func (o *liveOps) SetTransport(in SetTransportIn) error {
	if err := requireRoot(isRoot()); err != nil {
		return err
	}
	if _, err := supervisor.SetTransportControl(supervisor.SockPath, in.Link); err != nil {
		return ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     "set_transport 失败: " + err.Error(),
			Remediation: "确认 bx 守护进程在跑(bx up)且本机有权限",
		}
	}
	return nil
}

func (o *liveOps) RestartTunnel() error {
	if err := requireRoot(isRoot()); err != nil {
		return err
	}
	if _, err := supervisor.ReconnectControl(supervisor.SockPath); err != nil {
		return ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     "safe reconnect 失败: " + err.Error(),
			Remediation: "确认 bx 守护进程在跑(bx up),然后查 bx_logs",
		}
	}
	return nil
}

func (o *liveOps) Rehijack() error {
	if err := requireRoot(isRoot()); err != nil {
		return err
	}
	if _, err := supervisor.RehijackControl(supervisor.SockPath); err != nil {
		return ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     "rehijack 失败: " + err.Error(),
			Remediation: "确认 bx 守护进程在跑(bx up)",
		}
	}
	return nil
}

func (o *liveOps) Commit() error {
	if _, err := supervisor.CommitControl(supervisor.SockPath); err != nil {
		return ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     "commit 控制 socket 调用失败: " + err.Error(),
			Remediation: "确认 bx 正在运行;必要时查 bx status / bx logs",
		}
	}
	return nil
}

func (o *liveOps) Rollback() error {
	if _, err := supervisor.RollbackControl(supervisor.SockPath); err != nil {
		return ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     "rollback 控制 socket 调用失败: " + err.Error(),
			Remediation: "确认 bx 正在运行;必要时查 bx status / bx logs",
		}
	}
	return nil
}
