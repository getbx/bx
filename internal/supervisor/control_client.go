// control_client.go — 控制面 HTTP 客户端(unix socket over HTTP)。
// 供 CLI 与 MCP 共用,避免重复实现 unix dial + JSON 解码。
package supervisor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/getbx/bx/internal/stats"
)

func controlHTTPClient(sockPath string) *http.Client {
	return &http.Client{
		Timeout:   3 * time.Second,
		Transport: controlHTTPTransport(sockPath),
	}
}

func controlHTTPClientForOperation(sockPath string) *http.Client {
	return &http.Client{Transport: controlHTTPTransport(sockPath)}
}

func controlHTTPTransport(sockPath string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 1 * time.Second}).DialContext(ctx, "unix", sockPath)
		},
	}
}

// FetchStatusReport 经控制面 GET /v0/status(HTTP over unix socket)取一份 Report。
// sockPath 通常为 SockPath;测试时可传临时 socket 路径。
func FetchStatusReport(sockPath string) (stats.Report, error) {
	client := controlHTTPClient(sockPath)
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

// FetchRuntimeState reads the non-secret Core handoff state over its unix socket.
func FetchRuntimeState(sockPath string) (RuntimeState, error) {
	return fetchRuntimeState(context.Background(), sockPath)
}

func fetchRuntimeState(ctx context.Context, sockPath string) (RuntimeState, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local/v0/runtime", nil)
	if err != nil {
		return RuntimeState{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return RuntimeState{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return RuntimeState{}, fmt.Errorf("控制面 /v0/runtime 返回 %d", resp.StatusCode)
	}
	var state RuntimeState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return RuntimeState{}, err
	}
	return state, nil
}

// ShutdownControl asks the matching Core process to cancel its own Run context.
func ShutdownControl(ctx context.Context, sockPath string, expectedPID int) error {
	if expectedPID <= 0 {
		return fmt.Errorf("expected Core PID must be positive")
	}
	body, err := json.Marshal(shutdownRequest{ExpectedPID: expectedPID})
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v0/shutdown", bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var result controlResponse
	decodeErr := json.NewDecoder(response.Body).Decode(&result)
	if response.StatusCode != http.StatusOK {
		if result.Error != "" {
			return fmt.Errorf("控制面 /v0/shutdown 返回 %d: %s", response.StatusCode, result.Error)
		}
		return fmt.Errorf("控制面 /v0/shutdown 返回 %d", response.StatusCode)
	}
	if decodeErr != nil {
		return decodeErr
	}
	return nil
}

func SupportsSafeReconnect(sockPath string) (bool, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://local/v0/capabilities")
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("控制面 /v0/capabilities 返回 %d", resp.StatusCode)
	}
	var out struct {
		SafeReconnect bool `json:"safe_reconnect"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.SafeReconnect, nil
}

func postControl(sockPath, path string) (string, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	resp, err := client.Post("http://local"+path, "application/json", bytes.NewReader(nil))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out controlResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", fmt.Errorf("控制面 %s 返回 %d: %s", path, resp.StatusCode, out.Error)
		}
		return "", fmt.Errorf("控制面 %s 返回 %d", path, resp.StatusCode)
	}
	return out.State, nil
}

func CommitControl(sockPath string) (string, error) {
	return postControl(sockPath, "/v0/commit")
}

func RollbackControl(sockPath string) (string, error) {
	return postControl(sockPath, "/v0/rollback")
}

// ReloadControl 触发路由规则热重载(bx direct/proxy 改配置后):控制面重读配置、
// 重建 router 原子换入,不断隧道。同步返回成败。
func ReloadControl(sockPath string) (string, error) {
	return postControl(sockPath, "/v0/reload")
}

// postControlBody POST path,带可选 JSON body;返回 controlResponse.State,非 2xx → error(含 Error)。
func postControlBody(sockPath, path string, body any) (string, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return "", err
		}
		rd = bytes.NewReader(b)
	}
	resp, err := client.Post("http://local"+path, "application/json", rd)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out controlResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil && resp.StatusCode == http.StatusOK {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", fmt.Errorf("控制面 %s 返回 %d: %s", path, resp.StatusCode, out.Error)
		}
		return "", fmt.Errorf("控制面 %s 返回 %d", path, resp.StatusCode)
	}
	return out.State, nil
}

func SetTransportControl(sockPath, link string) (string, error) {
	return postControlBody(sockPath, "/v0/transport", map[string]string{"link": link})
}

// ReconnectControl 让守护进程安全重建当前传输。TUN、路由和 DNS 保持不变。
func ReconnectControl(sockPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	return ReconnectControlContext(ctx, sockPath)
}

func ReconnectControlContext(ctx context.Context, sockPath string) (string, error) {
	client := controlHTTPClientForOperation(sockPath)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v0/reconnect", nil)
	if err != nil {
		return "", err
	}
	return doControlRequest(client, req, "/v0/reconnect")
}

func doControlRequest(client *http.Client, req *http.Request, path string) (string, error) {
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var out controlResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return "", fmt.Errorf("控制面 %s 返回 %d: %s", path, resp.StatusCode, out.Error)
		}
		return "", fmt.Errorf("控制面 %s 返回 %d", path, resp.StatusCode)
	}
	return out.State, nil
}

func RehijackControl(sockPath string) (string, error) {
	return postControlBody(sockPath, "/v0/rehijack", nil)
}
