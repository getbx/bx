package tray

import (
	"encoding/json"
	"os"
	"os/exec"
	"time"

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

// parseUpdateCheckJSON 解析 `bx update --check --json` 输出(字段对齐 internal/cli/update.go
// 的 updateCheckReport)。坏 JSON → ok=false。
func parseUpdateCheckJSON(b []byte) (available bool, ok bool) {
	var raw struct {
		Available bool `json:"available"`
		Verified  bool `json:"verified"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return false, false
	}
	return raw.Available, true
}

// shouldCheckUpdate 报告是否到期该查更新(节流)。lastChecked 零值=从未查过→查。
func shouldCheckUpdate(lastChecked, now time.Time, interval time.Duration) bool {
	if lastChecked.IsZero() {
		return true
	}
	return now.Sub(lastChecked) >= interval
}

// checkUpdateAvailable spawn `bx update --check --json` 判有无更新。非提权只读;
// 任何失败(网络/MITM/坏输出)→ false,绝不连累主状态判定。
func checkUpdateAvailable(exePath string) bool {
	out, err := exec.Command(exePath, "update", "--check", "--json").Output()
	if err != nil {
		return false
	}
	avail, ok := parseUpdateCheckJSON(out)
	return ok && avail
}
