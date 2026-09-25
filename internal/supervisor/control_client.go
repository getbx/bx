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
	"net/url"
	"time"

	"github.com/getbx/bx/internal/stats"
)

func controlHTTPClient(sockPath string) *http.Client {
	return controlHTTPClientWithTimeout(sockPath, 3*time.Second)
}

func controlHTTPClientForOperation(sockPath string) *http.Client {
	return controlHTTPClientWithTimeout(sockPath, 0)
}

func controlHTTPClientWithTimeout(sockPath string, timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: controlHTTPTransport(sockPath)}
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
	return FetchStatusReportContext(context.Background(), sockPath)
}

// FetchStatusReportContext 是 FetchStatusReport 的带 ctx 版本 —— 调用方需要
// 一个有界等待时用它(Guardian 把 Core 统计并进 /v1/status 时必须短超时,不能
// 让一个拨不通的 Core 拖住整个响应)。
func FetchStatusReportContext(ctx context.Context, sockPath string) (stats.Report, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local/v0/status", nil)
	if err != nil {
		return stats.Report{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return stats.Report{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return stats.Report{}, fmt.Errorf("the control plane /v0/status returned %d", resp.StatusCode)
	}
	var rep stats.Report
	if err := json.NewDecoder(resp.Body).Decode(&rep); err != nil {
		return stats.Report{}, err
	}
	return rep, nil
}

// FetchRuntimeState reads the non-secret Core handoff state over its unix socket.
func FetchRuntimeState(sockPath string) (RuntimeState, error) {
	return FetchRuntimeStateContext(context.Background(), sockPath)
}

// FetchRuntimeStateContext 是 FetchRuntimeState 的带 ctx 版本 —— 调用方**自己有
// 一份预算**时用它(与 ProbeControlContext、FetchStatusReportContext 同一条)。
//
// 不带 ctx 的那个版本自己还有两层时钟(1 秒拨号 + 3 秒客户端超时),加起来能把
// 一份 10 秒的预算撑破;而 Guardian 的 /v1/doctor 跑在 `Daemon.Shutdown` 要等的
// 那批 handler 里 —— **停止路径不许因为别的事没做完而变慢**。ctx 只会让它更早
// 返回,永远不会让它等更久。
func FetchRuntimeStateContext(ctx context.Context, sockPath string) (RuntimeState, error) {
	return fetchRuntimeState(ctx, sockPath)
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
		return RuntimeState{}, fmt.Errorf("the control plane /v0/runtime returned %d", resp.StatusCode)
	}
	var state RuntimeState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return RuntimeState{}, err
	}
	return state, nil
}

// FetchAppTraffic 经控制面 GET /v0/apps(HTTP over unix socket)取一份应用流量
// 归因报告。**总是带 `?subscribe=1`** —— apptraffic.go 的设计前提是「消费方每次
// 拉取都会带上它」(订阅同时兼具 30 秒 TTL 续期),不带这个参数会让采集在
// 两次拉取的间隙悄悄过期。
//
// 三态原样透传给调用方(subscribed/report/error),**不在这里合并或改写** ——
// AppTrafficResponse.Error 非空时 Report 是零值(Groups==nil),由调用方
// 自己先判 Error 再碰 Report,与控制面 handler 那侧同一条纪律。
// FetchExplain 问跑着的 Core:「现在向这个目标发一条连接会发生什么、为什么」。
//
// **501 单独成一句话。** 「这一版 Core 没接线」与「这个目标没有答案」是两件事,
// 压成一句「请求失败」会让人去查一个不存在的网络问题(与 handleApps 那条
// 「没接线不是没有应用」同源)。
func FetchExplain(sockPath, target string) (ExplainResponse, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://local/v0/explain?target="+url.QueryEscape(target), nil)
	if err != nil {
		return ExplainResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return ExplainResponse{}, err
	}
	defer resp.Body.Close()
	// **404 与 501 都是「这一版 Core 没有判定查询」。**
	//
	// 501 = 路由在、没接线;404 = **路由压根不存在**,也就是一个升级前的 Core。
	// 后者才是升级期最常见的那一种,而只认 501 会让它落进下面的通用错误 ——
	// 真机冒烟第一次就撞上了:CLI 报「bx 没在跑,先 bx up」,而 bx 跑得好好的。
	// 一句指向错误方向的诊断比没有诊断更糟。
	if resp.StatusCode == http.StatusNotImplemented || resp.StatusCode == http.StatusNotFound {
		return ExplainResponse{}, ErrExplainUnsupported
	}
	if resp.StatusCode != http.StatusOK {
		var out controlResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.Error != "" {
			return ExplainResponse{}, fmt.Errorf("the control plane /v0/explain returned %d: %s", resp.StatusCode, out.Error)
		}
		return ExplainResponse{}, fmt.Errorf("the control plane /v0/explain returned %d", resp.StatusCode)
	}
	var out ExplainResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ExplainResponse{}, err
	}
	return out, nil
}

func FetchAppTraffic(sockPath string) (AppTrafficResponse, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, "http://local/v0/apps?subscribe=1", nil)
	if err != nil {
		return AppTrafficResponse{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return AppTrafficResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return AppTrafficResponse{}, fmt.Errorf("the control plane /v0/apps returned %d", resp.StatusCode)
	}
	var out AppTrafficResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return AppTrafficResponse{}, err
	}
	return out, nil
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
			return fmt.Errorf("the control plane /v0/shutdown returned %d: %s", response.StatusCode, result.Error)
		}
		return fmt.Errorf("the control plane /v0/shutdown returned %d", response.StatusCode)
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
		return false, fmt.Errorf("the control plane /v0/capabilities returned %d", resp.StatusCode)
	}
	var out struct {
		SafeReconnect bool `json:"safe_reconnect"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, err
	}
	return out.SafeReconnect, nil
}

// decodeControlResponse 读一次控制面应答,**成败两条路都返回 State**。
//
// 此前 postControl / postControlBody / doControlRequest 里有三份逐字重复的
// 拷贝,而它们在错误路径上一律 `return "", err` —— 于是 409 带回来的 State
// (reverted / committed / idle)在这里被丢掉,而那正是「你的改动被死手自动
// 回滚了」与「你根本没武装过」的唯一区分。三份合成一份,漂移在构造上不可能。
//
// 解不开 body 时不覆盖状态码:一个返回 HTML 错误页的 409,报「控制面返回 409」
// 比报「invalid character '<'」有用。连不上 socket 那条路根本到不了这里,
// 于是 State 保持空串 —— **「没问出来」不许被读成任何一种状态**。
func decodeControlResponse(resp *http.Response, path string) (string, error) {
	var out controlResponse
	decodeErr := json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode != http.StatusOK {
		if out.Error != "" {
			return out.State, fmt.Errorf("the control plane %s returned %d: %s", path, resp.StatusCode, out.Error)
		}
		return out.State, fmt.Errorf("the control plane %s returned %d", path, resp.StatusCode)
	}
	if decodeErr != nil {
		return "", decodeErr
	}
	return out.State, nil
}

func postControl(sockPath, path string) (string, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	resp, err := client.Post("http://local"+path, "application/json", bytes.NewReader(nil))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	return decodeControlResponse(resp, path)
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
	return decodeControlResponse(resp, path)
}

func SetTransportControl(sockPath, link string) (string, error) {
	return postControlBody(sockPath, "/v0/transport", map[string]string{"link": link})
}

// SetServerControl 请 Core 把主传输与 UDP 传输一起换到目标服务器(commit-confirmed:
// 成功后调用方须 CommitControl,否则死手到点自动 revert)。
func SetServerControl(sockPath, link, udp string) (string, error) {
	return postControlBody(sockPath, "/v0/server", map[string]string{"link": link, "udp": udp})
}

func ReconnectControlContext(ctx context.Context, sockPath string) (string, error) {
	return reconnectControlContext(ctx, sockPath, controlHTTPClientForOperation)
}

// RecoverPathControl runs the Core's serialized path recovery operation. The caller
// supplies the operation deadline because transport health may exceed normal RPC timeouts.
func RecoverPathControl(ctx context.Context, sockPath string, in PathRecoveryRequest) (PathRecoverySnapshot, error) {
	body, err := json.Marshal(in)
	if err != nil {
		return PathRecoverySnapshot{}, err
	}
	client := controlHTTPClientForOperation(sockPath)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v0/path-recovery", bytes.NewReader(body))
	if err != nil {
		return PathRecoverySnapshot{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	return doPathRecoveryRequest(client, req)
}

// FetchPathRecovery returns the latest non-secret recovery snapshot without waiting
// for an in-flight POST operation.
func FetchPathRecovery(ctx context.Context, sockPath string) (PathRecoverySnapshot, error) {
	client := controlHTTPClient(sockPath)
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local/v0/path-recovery", nil)
	if err != nil {
		return PathRecoverySnapshot{}, err
	}
	return doPathRecoveryRequest(client, req)
}

func doPathRecoveryRequest(client *http.Client, req *http.Request) (PathRecoverySnapshot, error) {
	resp, err := client.Do(req)
	if err != nil {
		return PathRecoverySnapshot{}, err
	}
	defer resp.Body.Close()
	var snapshot PathRecoverySnapshot
	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return PathRecoverySnapshot{}, err
	}
	if resp.StatusCode != http.StatusOK {
		code := stablePathRecoveryCode(snapshot.ErrorCode)
		if code == "" {
			code = "recovery_failed"
		}
		snapshot.ErrorCode = code
		snapshot.Detail = ""
		return snapshot, &PathRecoveryError{Code: code}
	}
	return snapshot, nil
}

func reconnectControlContext(ctx context.Context, sockPath string, clientFactory func(string) *http.Client) (string, error) {
	client := clientFactory(sockPath)
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
	return decodeControlResponse(resp, path)
}

func RehijackControl(sockPath string) (string, error) {
	return postControlBody(sockPath, "/v0/rehijack", nil)
}

// ProbeControl 让 Core 用它的**直连**拨号器量一次到某台服务器的往返时间。
//
// 客户端超时必须比服务端的 probeTimeout(8 秒)长,否则拿到的永远是自己的超时,
// 而服务端那个上限一次都不会生效 —— 与菜单对 /v1/update-check 同一条纪律。
func ProbeControl(sockPath, host string, port int) (ProbeResult, error) {
	return ProbeControlContext(context.Background(), sockPath, host, port)
}

// ProbeControlContext 是 ProbeControl 的带 ctx 版本 —— 调用方**自己有一份预算**时
// 用它。上面那个 12 秒的客户端超时是「让服务端那 8 秒先生效」的下限,不是上限:
// Guardian 的 /v1/doctor 整轮只有 10 秒,光这一次探测就能把它撑破,而它跑在
// daemon 的 shutdown 要等的那批 handler 里 —— **停止路径不许因为别的事没做完
// 而变慢**。ctx 只会让它更早返回,永远不会让它等更久。
func ProbeControlContext(ctx context.Context, sockPath, host string, port int) (ProbeResult, error) {
	client := controlHTTPClient(sockPath)
	client.Timeout = probeTimeout + 4*time.Second
	defer client.CloseIdleConnections()
	body, err := json.Marshal(ProbeRequest{Host: host, Port: port})
	if err != nil {
		return ProbeResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v0/probe", bytes.NewReader(body))
	if err != nil {
		return ProbeResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return ProbeResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var out controlResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		if out.Error != "" {
			return ProbeResult{}, fmt.Errorf("the control plane /v0/probe returned %d: %s", resp.StatusCode, out.Error)
		}
		return ProbeResult{}, fmt.Errorf("the control plane /v0/probe returned %d", resp.StatusCode)
	}
	var result ProbeResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return ProbeResult{}, err
	}
	return result, nil
}
