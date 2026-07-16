package toolkeys

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreNeverExposesSecretInMeta(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	c := Credential{ID: "cred-1", Label: "Example", Origin: "https://api.example.com", Secret: "bx-secret-123", AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}
	if err := s.Put(c); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s.List())
	if bytes.Contains(b, []byte(c.Secret)) {
		t.Fatalf("meta leaked secret: %s", b)
	}
	got, err := s.Resolve(c.ID)
	if err != nil || got.Secret != c.Secret {
		t.Fatalf("Resolve = %+v, %v", got, err)
	}
}

func TestStorePersistsSecretOnlyInPrivateDiskRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	c := Credential{ID: "cred", Origin: "https://api.example.com", Secret: "disk-only-secret", AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}
	if err := s.Put(c); err != nil {
		t.Fatal(err)
	}
	public, _ := json.Marshal(c)
	if bytes.Contains(public, []byte(c.Secret)) {
		t.Fatalf("credential JSON leaked secret: %s", public)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Resolve(c.ID)
	if err != nil || got.Secret != c.Secret {
		t.Fatalf("reopened = %+v, %v", got, err)
	}
}

func TestStoreFileModeAndAtomicRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.json")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Credential{ID: "c", Origin: "https://api.example.com", Secret: "old", AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceSecret("c", "new"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%v err=%v", info, err)
	}
	got, _ := s.Resolve("c")
	if got.Secret != "new" {
		t.Fatalf("secret = %q", got.Secret)
	}
}

func TestPendingExpiresAndCompletionConsumesIt(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	p, err := s.CreatePending("https://api.example.com", AuthHint{Type: AuthBearer}, "create task", "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := s.CompletePending(p.ID, "secret")
	if err != nil || c.Origin != p.Origin {
		t.Fatalf("CompletePending = %+v, %v", c, err)
	}
	if _, err := s.CompletePending(p.ID, "again"); err == nil {
		t.Fatal("pending reused")
	}
}

func TestStoreCanPauseAndDeleteCredential(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "credentials.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(Credential{ID: "cred", Origin: "https://api.example.com", Secret: "secret", AuthHint: AuthHint{Type: AuthBearer}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetEnabled("cred", false); err != nil {
		t.Fatal(err)
	}
	got, err := s.Resolve("cred")
	if err != nil || got.Enabled {
		t.Fatalf("credential = %+v, %v", got, err)
	}
	if err := s.Delete("cred"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve("cred"); err == nil {
		t.Fatal("deleted credential resolved")
	}
}
