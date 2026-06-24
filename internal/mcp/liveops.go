package mcp

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"time"

	"github.com/getbx/bx/internal/supervisor"
	mcpstats "github.com/getbx/bx/internal/stats"
)

// requireRoot は改动类操作の权限门控(纯函数,便于测试)。
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
	conn, err := net.DialTimeout("unix", supervisor.SockPath, 500*time.Millisecond)
	if err != nil {
		return StatusOut{}, ToolError{
			Code:        CodeTunnelUnhealthy,
			Message:     "bx 未运行或控制 socket 不可达",
			Remediation: "sudo bx up",
		}
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	var rep mcpstats.Report
	if err := json.NewDecoder(conn).Decode(&rep); err != nil {
		return StatusOut{}, ToolError{
			Code:    CodeTunnelUnhealthy,
			Message: fmt.Sprintf("读状态 socket 失败: %v", err),
		}
	}
	return StatusOut{
		TunnelHealthy: rep.TunnelHealthy,
		LatencyMS:     rep.LatencyMS,
		Mode:          "host",
		UDPMode:       rep.UDPMode,
	}, nil
}

// --- 以下为 honest stubs,待 Task 9 集成真实快照/supervisor 机器 ---

func (o *liveOps) Diagnose() (DiagnoseOut, error) {
	return DiagnoseOut{}, ToolError{
		Code:        CodeNotImplemented,
		Message:     "Diagnose 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "用 `bx doctor --json` 替代",
	}
}

func (o *liveOps) Logs(in LogsIn) (LogsOut, error) {
	return LogsOut{}, ToolError{
		Code:        CodeNotImplemented,
		Message:     "Logs 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "用 `bx logs` 替代",
	}
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
	return ToolError{
		Code:        CodeNotImplemented,
		Message:     "SetTransport 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "待 Task 9 实现",
	}
}

func (o *liveOps) RestartTunnel() error {
	if err := requireRoot(isRoot()); err != nil {
		return err
	}
	return ToolError{
		Code:        CodeNotImplemented,
		Message:     "RestartTunnel 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "用 `sudo bx down && sudo bx up` 替代",
	}
}

func (o *liveOps) Rehijack() error {
	if err := requireRoot(isRoot()); err != nil {
		return err
	}
	return ToolError{
		Code:        CodeNotImplemented,
		Message:     "Rehijack 尚未接线(待 Task 9 集成真实快照/supervisor 机器)",
		Remediation: "用 `sudo bx down && sudo bx up` 替代",
	}
}
