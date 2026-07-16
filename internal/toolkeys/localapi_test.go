package toolkeys

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLocalAPIListNeverReturnsSecret(t *testing.T) {
	s, err := OpenStore(t.TempDir() + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Credential{ID: "cred", Label: "Example", Origin: "https://api.example.com", Secret: "localapi-secret", AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/credentials", nil)
	w := httptest.NewRecorder()
	NewLocalAPI(s, nil).ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("localapi-secret")) {
		t.Fatalf("secret leaked: %s", w.Body.String())
	}
}
