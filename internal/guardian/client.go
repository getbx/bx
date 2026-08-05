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
