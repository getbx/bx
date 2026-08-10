package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/getbx/bx/internal/install"
)

type Client struct {
	SocketPath string
	HTTPClient *http.Client
}

type UnavailableError struct {
	Err error
}

func (e *UnavailableError) Error() string {
	return fmt.Sprintf("Guardian unavailable: %v", e.Err)
}

func (e *UnavailableError) Unwrap() error {
	return e.Err
}

type AmbiguousRecoveryError struct {
	Err error
}

func (e *AmbiguousRecoveryError) Error() string {
	return fmt.Sprintf("Guardian recovery request may have been accepted: %v", e.Err)
}

func (e *AmbiguousRecoveryError) Unwrap() error {
	return e.Err
}

type guardianDialError struct {
	err error
}

func (e *guardianDialError) Error() string {
	return e.err.Error()
}

func (e *guardianDialError) Unwrap() error {
	return e.err
}

func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath}
}

// NewClientWithTimeout 与 NewClient 同,但 HTTPClient 超时为给定值:update 事务
// 服务端上限 guardianMutationTimeout(60s),调用方按需给出余量(如 90s)以覆盖
// 一次完整 barrier/activate/commit 往返而不提前掐断连接。
func NewClientWithTimeout(socketPath string, timeout time.Duration) *Client {
	client := guardianHTTPClient(socketPath)
	client.Timeout = timeout
	return &Client{SocketPath: socketPath, HTTPClient: client}
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodGet, "/v1/status", nil)
}

func (c *Client) Up(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodPost, "/v1/up", nil)
}

func (c *Client) Down(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodPost, "/v1/down", nil)
}

// DownForUpgrade 停保护,但把这一跳标记成**维护**而不是「用户不要保护了」。
//
// 两个后果,缺一不可:desired **不被改写**(用户想要保护,只是此刻不能有),
// 以及前一秒才武装的那张维护挂起**不被销掉**(它正是拦住新 Guardian 在二进制
// 换到一半时把 Core 起回来的东西)。普通的 Down 两件都会做。
//
// 调用方只有一个:sudo bx app-install 的停保护步骤。用户明确说 off 的每一条路
// (bx down、菜单 Turn Off)都必须继续用 Down,那才是销挂起的正确时机。
func (c *Client) DownForUpgrade(ctx context.Context) (Status, error) {
	return c.request(ctx, http.MethodPost, downForUpgradePath, nil)
}

func (c *Client) Migrate(ctx context.Context, request MigrationRequest) (Status, error) {
	normalized, err := ValidateMigrationRequest(request)
	if err != nil {
		return Status{}, err
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return Status{}, err
	}
	return c.request(ctx, http.MethodPost, "/v1/migrate", bytes.NewReader(body))
}

func (c *Client) Update(ctx context.Context, request UpdateRequest) (UpdateResult, error) {
	normalized, err := ValidateUpdateRequest(request)
	if err != nil {
		return UpdateResult{}, err
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return UpdateResult{}, err
	}
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v1/update", bytes.NewReader(body))
	if err != nil {
		return UpdateResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return UpdateResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return UpdateResult{}, guardianHTTPError("/v1/update", response.StatusCode, body)
	}
	var result UpdateResult
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		return UpdateResult{}, err
	}
	return result, nil
}

func (c *Client) RequestRecovery(ctx context.Context, in RecoveryRequest) (RecoverySnapshot, error) {
	normalized, err := ValidateRecoveryRequest(in)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	body, err := json.Marshal(normalized)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	return c.recoveryRequest(ctx, http.MethodPost, "/v1/recoveries", bytes.NewReader(body), http.StatusAccepted)
}

func (c *Client) CurrentRecovery(ctx context.Context) (RecoverySnapshot, error) {
	return c.recoveryRequest(ctx, http.MethodGet, "/v1/recoveries/current", nil, http.StatusOK)
}

func (c *Client) recoveryRequest(ctx context.Context, method, path string, body io.Reader, expectedStatus int) (RecoverySnapshot, error) {
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local"+path, body)
	if err != nil {
		return RecoverySnapshot{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return RecoverySnapshot{}, ctxErr
		}
		var dialErr *guardianDialError
		if errors.As(err, &dialErr) {
			return RecoverySnapshot{}, &UnavailableError{Err: dialErr.err}
		}
		return RecoverySnapshot{}, &AmbiguousRecoveryError{Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		body, _ := io.ReadAll(response.Body)
		return RecoverySnapshot{}, guardianHTTPError(path, response.StatusCode, body)
	}
	var snapshot RecoverySnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
		if method == http.MethodPost {
			return RecoverySnapshot{}, &AmbiguousRecoveryError{Err: err}
		}
		return RecoverySnapshot{}, err
	}
	return redactRecoverySnapshot(snapshot), nil
}

func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (Status, error) {
	client := c.HTTPClient
	if client == nil {
		client = guardianHTTPClient(c.SocketPath)
	}
	if transport, ok := client.Transport.(*http.Transport); ok {
		defer transport.CloseIdleConnections()
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://local"+path, body)
	if err != nil {
		return Status{}, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return Status{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return Status{}, guardianHTTPError(path, response.StatusCode, body)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return Status{}, err
	}
	status.Recovery = redactRecoverySnapshot(status.Recovery)
	if status.NetworkGeneration == "" {
		status.NetworkGeneration = status.Recovery.Generation
	}
	return status, nil
}

// guardianFailureBody mirrors failureResponseBody in localapi.go: the JSON
// shape Guardian's four mutation handlers (mutation/update/migration/
// recoveryRequest) write on a 500 response. "code" is present when the
// failure is one of the named short circuits (recovery_incomplete /
// guardian_busy) or when it actually set a fresh LastError this call — see
// failureResponseBody's comment for why a missing code must stay missing
// rather than replay a stale one.
type guardianFailureBody struct {
	Error string `json:"error"`
	Code  string `json:"code"`
}

// guardianTroubleshootingHint names the Guardian log explicitly: `bx logs`
// reads the *Core* launchd logs (/var/log/bx.log), while the full reason for
// a Guardian failure — the raw error the response deliberately withholds —
// is only ever written to the Guardian log. During the 2026-08-05 incident
// the diagnostics archive was consulted and yielded stale Core logs, which
// is exactly what pointing at the wrong log produces.
const guardianTroubleshootingHint = "排查:sudo bx doctor;完整原因见 Guardian 日志 sudo tail -50 " +
	install.GuardianStderrLogPath + "(bx logs 看的是 Core 日志,不含 Guardian 失败原因)"

// guardianCodeHints carries the per-code next step for failures the generic
// hint cannot resolve.
//
// core_ownership_uncertain is **latched**: Manager.upLocked/Migrate check
// m.current.Uncertain before Existing() is ever called again, so a scan
// observation taken at one instant becomes a permanent refusal — if the
// third-party Core later disappears, `bx up` still fails and never re-scans.
// Down() clears m.current, which is the only escape. The latch is deliberately
// left in place (rewiring manager.go's lifecycle state machine is higher risk
// than the payoff, and the case is rare), so the failure has to explain itself
// instead. The Guardian response body deliberately withholds the raw error
// (it may carry paths/links/credentials), which is why the wording lives here
// on the CLI side rather than in the error text the daemon produces.
var guardianCodeHints = map[string]string{
	"core_ownership_uncertain": "若确认没有第二个 Core 在跑,执行 sudo bx down 再 sudo bx up 可清除这条已锁存的判定" +
		"(Guardian 把「所有权不确定」记在内存里,只有 down 会清)",
	// 维护挂起读不出来:Guardian 一律 fail-closed(不起 Core),而保护**不会**
	// 自己恢复。挂起只是一次升级留下的临时标记,内容读不懂时直接删掉即可 ——
	// 这条出路必须写在 CLI 侧:响应体刻意不外传原始错误串,daemon 那边写的
	// 错误文本用户根本看不到。
	"intent_unreadable": "维护挂起文件读不出来,保护不会自动恢复:检查 " + defaultMaintenanceHoldPath +
		"(它只是一次升级的临时标记,可直接删除),再 sudo bx up",
	// 挂起删不掉(多半是 /var/lib/bx 或那个文件本身不可写)。Guardian **拒绝**
	// 在这种情况下打开保护:挂起还武装着而保护开着,意味着 Core 一退出就既不
	// 重启也不装屏障,保护会在用户以为开着的时候悄悄退回明文直连。
	maintenanceHoldClearFailedCode: "维护挂起删不掉,保护因此没有打开(挂起还在就等于 Core 退出后不会被拉回来):" +
		"检查 " + defaultMaintenanceHoldPath + " 及其目录是否可写(ls -l /var/lib/bx)," +
		"必要时 sudo rm -f " + defaultMaintenanceHoldPath + " 后重试 sudo bx up",
	// recoveryBlocked 是**锁存**的:Up 的第一句就短路,而那句检查排在销挂起
	// 之前 —— 于是 `bx up` 既不会清挂起也不会启动。Down 会清掉这个状态
	// (它自己的 defer 也会销挂起),所以出路只有一条,必须写出来。
	"recovery_incomplete": "上一次启动恢复没能完成,Guardian 把后续操作锁住了(bx up 会在第一句就返回):" +
		"先 sudo bx down 再 sudo bx up —— 只有 down 会清掉这个锁存状态",
}

// guardianHTTPError renders a Guardian error response. Every 500 carries the
// troubleshooting hint, with or without a code: the failures that arrive
// without one (short circuits that never reach needsAttention, or an older
// Guardian) are precisely the ones a user cannot act on unaided, so gating
// the hint on "has a code" withheld it from the cases that needed it most.
func guardianHTTPError(path string, statusCode int, body []byte) error {
	var failure guardianFailureBody
	_ = json.Unmarshal(body, &failure)
	message := fmt.Sprintf("Guardian %s returned %d", path, statusCode)
	if failure.Error != "" {
		message += ": " + failure.Error
	}
	if failure.Code != "" {
		message += fmt.Sprintf("(code=%s)", failure.Code)
	}
	if statusCode == http.StatusInternalServerError {
		// 专用指引在前、通用排查在后:前者是这一类失败的直接出路。
		// 与通用指引一样只挂在 500 上(非 500 保持素净,见
		// TestGuardianHTTPErrorNon500StaysPlain)。
		if hint := guardianCodeHints[failure.Code]; hint != "" {
			message += "。" + hint
		}
		message += "。" + guardianTroubleshootingHint
	}
	return errors.New(message)
}

func guardianHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath)
			if err != nil {
				return nil, &guardianDialError{err: err}
			}
			return conn, nil
		}},
	}
}
