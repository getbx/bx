package toolkeys

import (
	"bytes"
	"encoding/json"
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
	if !bytes.Contains(w.Body.Bytes(), []byte(`"id":"cred"`)) {
		t.Fatalf("catalog does not use stable JSON fields: %s", w.Body.String())
	}
}

func TestLocalAPICreatesSecretFreePendingRequest(t *testing.T) {
	s, err := OpenStore(t.TempDir() + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"origin":"https://API.Example.com","auth_hint":{"type":"bearer"},"reason":"create task"}`)
	r := httptest.NewRequest(http.MethodPost, "/v1/pending", bytes.NewReader(body))
	w := httptest.NewRecorder()
	NewLocalAPI(s, nil).ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var pending PendingRequest
	if err := json.Unmarshal(w.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Origin != "https://api.example.com" || pending.ID == "" {
		t.Fatalf("pending=%+v", pending)
	}
}

func TestLocalAPIPendingRejectsSecretFields(t *testing.T) {
	s, err := OpenStore(t.TempDir() + "/credentials.json")
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/v1/pending", bytes.NewBufferString(`{"origin":"https://api.example.com","auth_hint":{"type":"bearer"},"reason":"x","token":"do-not-accept"}`))
	w := httptest.NewRecorder()
	NewLocalAPI(s, nil).ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
