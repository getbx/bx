package toolkeys

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrokerInjectsSecretAndStripsCallerAuth(t *testing.T) {
	const secret = "tool-secret-value"
	var auth, cookie string
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, cookie = r.Header.Get("Authorization"), r.Header.Get("Cookie")
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Set-Cookie", "session=private")
		_, _ = w.Write([]byte(`{"access_token":"new-secret"}`))
	}))
	defer ts.Close()
	b := testBroker(t, secret, testHTTPClient(ts))
	out, err := b.Do(context.Background(), APIRequest{CredentialID: "cred", Method: http.MethodGet, Path: "/v1/run", Headers: map[string]string{"Authorization": "Bearer attacker", "Cookie": "x=y"}}, "mcp")
	if err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer "+secret || cookie != "" {
		t.Fatalf("auth=%q cookie=%q", auth, cookie)
	}
	if string(out.JSONBody) == "" || out.Headers["Set-Cookie"] != "" {
		t.Fatalf("response=%+v", out)
	}
}

func TestBrokerDoesNotFollowRedirect(t *testing.T) {
	var followed bool
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/next" {
			followed = true
		}
		w.Header().Set("Location", "/next")
		w.WriteHeader(http.StatusFound)
	}))
	defer ts.Close()
	_, err := testBroker(t, "secret", testHTTPClient(ts)).Do(context.Background(), APIRequest{CredentialID: "cred", Method: http.MethodGet, Path: "/start"}, "mcp")
	if err == nil || !strings.Contains(err.Error(), string(CodeRedirectNotFollowed)) {
		t.Fatalf("err = %v", err)
	}
	if followed {
		t.Fatal("redirect target was requested")
	}
}

func testBroker(t *testing.T, secret string, client *http.Client) *Broker {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenStore(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(Credential{ID: "cred", Origin: "https://api.example.test", Secret: secret, AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	audit, err := OpenAudit(filepath.Join(dir, "audit.jsonl"), 30*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return NewBroker(store, audit, client)
}
func testHTTPClient(ts *httptest.Server) *http.Client {
	d := &net.Dialer{Timeout: time.Second}
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return d.DialContext(ctx, "tcp", ts.Listener.Addr().String())
	}, TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
}
