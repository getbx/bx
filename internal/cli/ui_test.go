package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsLoopbackListen(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:8787", "localhost:8787", "[::1]:8787"} {
		if !isLoopbackListen(addr) {
			t.Fatalf("%s should be loopback", addr)
		}
	}
	for _, addr := range []string{"0.0.0.0:8787", ":8787", "1.2.3.4:8787"} {
		if isLoopbackListen(addr) {
			t.Fatalf("%s should not be allowed", addr)
		}
	}
}

func TestUIIndex(t *testing.T) {
	s := uiServer{host: "example.com", sharesDir: t.TempDir()}
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	s.handleIndex(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "bx server") {
		t.Fatalf("index body missing title")
	}
	if !strings.Contains(rec.Body.String(), "example.com") {
		t.Fatalf("index body missing host")
	}
}

func TestUISharesAPI(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Listen: ":10001", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	s := uiServer{sharesDir: dir}
	req := httptest.NewRequest(http.MethodGet, "/api/shares", nil)
	rec := httptest.NewRecorder()
	s.handleShares(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"name":"alice"`) {
		t.Fatalf("shares response = %s", rec.Body.String())
	}
}
