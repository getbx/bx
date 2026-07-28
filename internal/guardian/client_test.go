package guardian

import (
	"context"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

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

type recoveryRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f recoveryRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
