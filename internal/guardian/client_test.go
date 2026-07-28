package guardian

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStatusClientRedactsNestedRecoveryBeforePersistence(t *testing.T) {
	secret := "vless://user:password@example.test?token=secret"
	client := &Client{HTTPClient: &http.Client{Transport: recoveryRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		body, err := json.Marshal(Status{
			SchemaVersion: 1,
			Protection:    ProtectionBlocked,
			Recovery: RecoverySnapshot{
				ID:        "recovery-8",
				State:     "failed",
				Stage:     "transport_health",
				ErrorCode: "future_secret_error",
				Detail:    secret,
			},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(body))),
		}, nil
	})}}

	status, err := client.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Recovery.Detail != "" || status.Recovery.ErrorCode != "recovery_failed" {
		t.Fatalf("status recovery = %+v, want redacted stable failure", status.Recovery)
	}
}

func TestRecoveryClientTypesOnlyTransportFailureAsGuardianUnavailable(t *testing.T) {
	client := NewClient(filepath.Join(t.TempDir(), "missing.sock"))
	_, err := client.RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"})
	var unavailable *UnavailableError
	if !errors.As(err, &unavailable) {
		t.Fatalf("RequestRecovery error = %v, want typed Guardian unavailable", err)
	}

	client = &Client{HTTPClient: &http.Client{Transport: recoveryRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"business failure"}`)),
		}, nil
	})}}
	_, err = client.RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"})
	if err == nil || !strings.Contains(err.Error(), "returned 500") {
		t.Fatalf("business failure = %v, want HTTP status error", err)
	}
	if errors.As(err, &unavailable) {
		t.Fatalf("business failure was typed as Guardian unavailable: %v", err)
	}
}

func TestRecoveryClientFailsClosedWhenPOSTIsAcceptedThenResponseDrops(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "bxg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "g.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	accepted := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			accepted <- err
			return
		}
		defer conn.Close()
		request, err := http.ReadRequest(bufio.NewReader(conn))
		if err == nil {
			_, err = io.ReadAll(request.Body)
			_ = request.Body.Close()
		}
		accepted <- err
		// Close without a response after the complete POST was observable.
	}()

	client := NewClient(socketPath)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, requestErr := client.RequestRecovery(ctx, RecoveryRequest{Reason: "manual"})
	if err := <-accepted; err != nil {
		t.Fatalf("Guardian did not receive complete POST: %v", err)
	}
	if requestErr == nil {
		t.Fatal("response drop should fail closed")
	}
	var unavailable *UnavailableError
	if errors.As(requestErr, &unavailable) {
		t.Fatalf("possibly accepted POST was typed unavailable: %v", requestErr)
	}
	var ambiguous *AmbiguousRecoveryError
	if !errors.As(requestErr, &ambiguous) {
		t.Fatalf("response drop error = %T %v, want typed ambiguous recovery error", requestErr, requestErr)
	}
}

func TestRecoveryClientTypesDroppedAcceptedResponseBodyAsAmbiguous(t *testing.T) {
	socketDir, err := os.MkdirTemp("/tmp", "bxg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "g.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		request, readErr := http.ReadRequest(bufio.NewReader(conn))
		if readErr == nil {
			_, _ = io.ReadAll(request.Body)
			_ = request.Body.Close()
		}
		_, _ = io.WriteString(conn, "HTTP/1.1 202 Accepted\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"recovery_id\":\"accepted")
	}()

	_, requestErr := NewClient(socketPath).RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"})
	if requestErr == nil {
		t.Fatal("partial accepted response body should fail closed")
	}
	var unavailable *UnavailableError
	if errors.As(requestErr, &unavailable) {
		t.Fatalf("partial accepted response was typed unavailable: %v", requestErr)
	}
	var ambiguous *AmbiguousRecoveryError
	if !errors.As(requestErr, &ambiguous) {
		t.Fatalf("partial accepted response error = %T %v, want typed ambiguous", requestErr, requestErr)
	}
}

type recoveryRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f recoveryRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
