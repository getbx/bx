package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/srvgen"
)

func TestServerHardenPatchesOnceRestartsOnlyWhenItWroteAndKeepsKeys(t *testing.T) {
	rp := srvgen.RealityParams{UUID: "11111111-2222-3333-4444-555555555555", Port: 443, SNI: "www.cloudflare.com", PrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ShortID: "abcd"}
	fresh, _ := rp.ServerConfig()
	legacy := strings.Replace(string(fresh), `"route"`, `"route_removed_for_test"`, 1) // pre-2026-09-29 shape
	path := filepath.Join(t.TempDir(), "sbserver.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	restarts := 0
	restart := func() error { restarts++; return nil }
	msg, err := hardenServerConfig(path, restart)
	if err != nil || restarts != 1 || !strings.Contains(msg, "Hardened") {
		t.Fatalf("first run: msg=%q err=%v restarts=%d", msg, err, restarts)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), rp.UUID) || !strings.Contains(string(after), rp.PrivateKey) {
		t.Fatal("keys or users lost")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600 (the file holds the private key)", info.Mode().Perm())
	}
	msg, err = hardenServerConfig(path, restart)
	if err != nil || restarts != 1 || !strings.Contains(msg, "already hardened") {
		t.Fatalf("second run must not write or restart: msg=%q err=%v restarts=%d", msg, err, restarts)
	}
}

func TestServerHardenSaysWhatItCannotCover(t *testing.T) {
	_, err := hardenServerConfig(filepath.Join(t.TempDir(), "missing.json"), func() error { return nil })
	if err == nil || !strings.Contains(err.Error(), "brook") {
		t.Fatalf("missing config: err = %v, want a sentence that says brook servers are not covered", err)
	}
	path := filepath.Join(t.TempDir(), "sbserver.json")
	fresh, _ := srvgen.RealityParams{UUID: "u", Port: 443, SNI: "s", PrivateKey: "k", ShortID: "a"}.ServerConfig()
	legacy := strings.Replace(string(fresh), `"route"`, `"route_removed_for_test"`, 1)
	_ = os.WriteFile(path, []byte(legacy), 0o600)
	_, err = hardenServerConfig(path, func() error { return errors.New("systemctl: boom") })
	if err == nil || !strings.Contains(err.Error(), "next start") {
		t.Fatalf("restart failure: err = %v, want it to say the rule is written and applies on the next start", err)
	}
}
