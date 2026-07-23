package tray

import (
	"encoding/json"
	"os"
	"os/exec"

	"github.com/getbx/bx/internal/install"
)

type StatusDetail struct {
	Healthy   bool
	LatencyMS int64
	Server    string
	Transport string
}

// parseStatusJSON 解析 `bx status --json` 输出。字段名与 internal/stats/render.go 的 json tag 一致。
func parseStatusJSON(b []byte) (StatusDetail, bool) {
	var raw struct {
		Server        string `json:"server"`
		TunnelHealthy bool   `json:"tunnel_healthy"`
		LatencyMS     int64  `json:"latency_ms"`
		Transport     string `json:"transport"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return StatusDetail{}, false
	}
	return StatusDetail{Healthy: raw.TunnelHealthy, LatencyMS: raw.LatencyMS, Server: raw.Server, Transport: raw.Transport}, true
}

// detectState 非提权合成托盘态 + 细节。updateAvailable 由调用方(轮询循环,节流)传入;
// detectState 自身不发起更新检查。
func detectState(exePath, configPath string, updateAvailable bool) (TrayState, StatusDetail) {
	svcRunning := install.ServiceState("is-active", "bx") == "active"
	_, statErr := os.Stat(configPath)
	configExists := statErr == nil
	var detail StatusDetail
	if out, err := exec.Command(exePath, "status", "--json").Output(); err == nil {
		if d, ok := parseStatusJSON(out); ok {
			detail = d
		}
	}
	return trayStateFrom(svcRunning, configExists, detail.Healthy, updateAvailable), detail
}
