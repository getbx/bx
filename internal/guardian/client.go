package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

type Client struct {
	SocketPath string
	HTTPClient *http.Client
}

func NewClient(socketPath string) *Client {
	return &Client{SocketPath: socketPath}
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
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)
		if failure.Error != "" {
			return UpdateResult{}, fmt.Errorf("Guardian /v1/update returned %d: %s", response.StatusCode, failure.Error)
		}
		return UpdateResult{}, fmt.Errorf("Guardian /v1/update returned %d", response.StatusCode)
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
		return RecoverySnapshot{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != expectedStatus {
		return RecoverySnapshot{}, fmt.Errorf("Guardian %s returned %d", path, response.StatusCode)
	}
	var snapshot RecoverySnapshot
	if err := json.NewDecoder(response.Body).Decode(&snapshot); err != nil {
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
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(response.Body).Decode(&failure)
		if failure.Error != "" {
			return Status{}, fmt.Errorf("Guardian %s returned %d: %s", path, response.StatusCode, failure.Error)
		}
		return Status{}, fmt.Errorf("Guardian %s returned %d", path, response.StatusCode)
	}
	var status Status
	if err := json.NewDecoder(response.Body).Decode(&status); err != nil {
		return Status{}, err
	}
	return status, nil
}

func guardianHTTPClient(socketPath string) *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "unix", socketPath)
		}},
	}
}
