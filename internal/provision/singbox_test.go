package provision

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestEnsureSingboxDownloadsAndVerifies(t *testing.T) {
	payload := []byte("#!/bin/sh\necho fake-singbox\n")
	sum := sha256.Sum256(payload)
	hexsum := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	p, err := EnsureSingbox(dir, "", srv.URL, hexsum)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	got, _ := os.ReadFile(p)
	if string(got) != string(payload) {
		t.Fatalf("content mismatch")
	}
	// second call uses cache (server can be down): close server, call again
	srv.Close()
	if _, err := EnsureSingbox(dir, "", srv.URL, hexsum); err != nil {
		t.Fatalf("cached ensure failed: %v", err)
	}
}

func TestEnsureSingboxRejectsBadHash(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("tampered"))
	}))
	defer srv.Close()
	if _, err := EnsureSingbox(t.TempDir(), "", srv.URL, "deadbeef"); err == nil {
		t.Fatal("expected sha256 mismatch error")
	}
}

func TestEnsureSingboxOverride(t *testing.T) {
	f := t.TempDir() + "/mybin"
	os.WriteFile(f, []byte("x"), 0o755)
	p, err := EnsureSingbox(t.TempDir(), f, "", "")
	if err != nil || p != f {
		t.Fatalf("override p=%q err=%v", p, err)
	}
}

func TestEnsureSingboxNoSource(t *testing.T) {
	if _, err := EnsureSingbox(t.TempDir(), "", "", ""); err == nil {
		t.Fatal("expected error when neither override nor url given")
	}
}
