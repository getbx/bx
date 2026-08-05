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

	"github.com/getbx/bx/internal/install"
)

// 用户看到 "guardian operation failed" 时无从下手;必须把失败码和下一步给出来。
func TestGuardianHTTPErrorSurfacesFailureCode(t *testing.T) {
	err := guardianHTTPError("/v1/up", http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed","code":"core_ownership_uncertain"}`))

	msg := err.Error()
	if !strings.Contains(msg, "core_ownership_uncertain") {
		t.Errorf("必须展示失败码,实际:%s", msg)
	}
	if !strings.Contains(msg, "bx logs") && !strings.Contains(msg, "bx doctor") {
		t.Errorf("必须给出下一步排查动作,实际:%s", msg)
	}
}

// 旧版 Guardian 不回传 code 时不得崩溃或输出空码。
func TestGuardianHTTPErrorWithoutCodeStaysReadable(t *testing.T) {
	err := guardianHTTPError("/v1/up", http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed"}`))
	if strings.Contains(err.Error(), "code=") {
		t.Errorf("无 code 时不应输出空码,实际:%s", err.Error())
	}
	if !strings.Contains(err.Error(), "guardian operation failed") {
		t.Errorf("无 code 时应保留原文案,实际:%s", err.Error())
	}
}

// 响应体既非法 JSON 又无内容时,仍要保持可读、不崩溃。
func TestGuardianHTTPErrorWithUnparsableBodyStaysReadable(t *testing.T) {
	err := guardianHTTPError("/v1/up", http.StatusInternalServerError, nil)
	if strings.Contains(err.Error(), "code=") {
		t.Errorf("空 body 不应输出空码,实际:%s", err.Error())
	}
	if !strings.Contains(err.Error(), "returned 500") {
		t.Errorf("空 body 仍要保留状态码,实际:%s", err.Error())
	}
}

// client.Up()(经 c.request 共用路径)要把 Guardian 500 响应里的 code 透传给调用方。
func TestClientRequestSurfacesFailureCode(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: recoveryRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"guardian operation failed","code":"core_ownership_uncertain"}`)),
		}, nil
	})}}
	_, err := client.Up(context.Background())
	if err == nil || !strings.Contains(err.Error(), "core_ownership_uncertain") {
		t.Fatalf("Up() error = %v, want failure code surfaced", err)
	}
}

// client.Update() 走独立的解析路径(非 c.request),同样要透传 code。
func TestClientUpdateSurfacesFailureCode(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: recoveryRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"guardian operation failed","code":"core_ownership_uncertain"}`)),
		}, nil
	})}}
	_, err := client.Update(context.Background(), UpdateRequest{
		TransactionID: "tx-1", FromVersion: "v1", ToVersion: "v2",
		AssetSHA256: strings.Repeat("a", 64), PackagePath: "/var/lib/bx/update/staging/tx-1/package.tar.gz",
	})
	if err == nil || !strings.Contains(err.Error(), "core_ownership_uncertain") {
		t.Fatalf("Update() error = %v, want failure code surfaced", err)
	}
}

// client.RequestRecovery() 走第三条独立解析路径,同样要透传 code。
func TestClientRequestRecoverySurfacesFailureCode(t *testing.T) {
	client := &Client{HTTPClient: &http.Client{Transport: recoveryRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusInternalServerError,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"guardian operation failed","code":"core_ownership_uncertain"}`)),
		}, nil
	})}}
	_, err := client.RequestRecovery(context.Background(), RecoveryRequest{Reason: "manual"})
	if err == nil || !strings.Contains(err.Error(), "core_ownership_uncertain") {
		t.Fatalf("RequestRecovery() error = %v, want failure code surfaced", err)
	}
}

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

// 最需要排查指引的恰恰是那些没有 code 的 500(recoveryBlocked 之外的短路、
// 旧版 Guardian):指引不能以「有 code」为前提。
func TestGuardianHTTPError500AlwaysCarriesTroubleshooting(t *testing.T) {
	for _, body := range [][]byte{
		[]byte(`{"error":"guardian operation failed","code":"recovery_incomplete"}`),
		[]byte(`{"error":"guardian operation failed"}`),
		nil,
	} {
		msg := guardianHTTPError("/v1/up", http.StatusInternalServerError, body).Error()
		if !strings.Contains(msg, "bx doctor") {
			t.Errorf("500 必须附排查指引,实际:%s", msg)
		}
	}
}

// bx logs 读的是 Core 日志(/var/log/bx.log),而 Guardian 失败的完整原因写在
// Guardian 日志里——事故中「翻诊断包只拿到陈旧 Core 日志」就是这么来的。
// 指引必须点名 Guardian 日志路径。
func TestGuardianHTTPErrorPointsAtGuardianLog(t *testing.T) {
	msg := guardianHTTPError("/v1/up", http.StatusInternalServerError,
		[]byte(`{"error":"guardian operation failed","code":"recovery_incomplete"}`)).Error()
	if !strings.Contains(msg, install.GuardianStderrLogPath) {
		t.Errorf("排查指引必须点名 Guardian 日志 %s,实际:%s", install.GuardianStderrLogPath, msg)
	}
}

// 非 500(如关机中的 503)保持原样:那不是「Guardian 内部失败」,别拿排查
// 指引去噪扰正常的生命周期响应。
func TestGuardianHTTPErrorNon500StaysPlain(t *testing.T) {
	msg := guardianHTTPError("/v1/up", http.StatusServiceUnavailable,
		[]byte(`{"error":"guardian is shutting down"}`)).Error()
	if strings.Contains(msg, "bx doctor") {
		t.Errorf("非 500 不应附排查指引,实际:%s", msg)
	}
}
