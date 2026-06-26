// control_client.go — 控制面 HTTP 客户端(unix socket over HTTP)。
// 供 CLI 与 MCP 共用,避免重复实现 unix dial + JSON 解码。
package supervisor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/getbx/bx/internal/stats"
)

// FetchStatusReport 经控制面 GET /v0/status(HTTP over unix socket)取一份 Report。
// sockPath 通常为 SockPath;测试时可传临时 socket 路径。
func FetchStatusReport(sockPath string) (stats.Report, error) {
	client := &http.Client{
		Timeout: 3 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return (&net.Dialer{Timeout: 1 * time.Second}).DialContext(ctx, "unix", sockPath)
			},
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://local/v0/status")
	if err != nil {
		return stats.Report{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return stats.Report{}, fmt.Errorf("控制面 /v0/status 返回 %d", resp.StatusCode)
	}
	var rep stats.Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return stats.Report{}, err
	}
	return rep, nil
}
