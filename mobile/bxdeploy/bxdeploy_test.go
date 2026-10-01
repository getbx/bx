package bxdeploy

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func hostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

var addr = &net.TCPAddr{IP: net.ParseIP("203.0.113.9"), Port: 22}

// A server seen for the first time is remembered (a VPS you just bought is always new); a server
// whose identity changed is refused — that is the one check against someone in between, and the
// person is told so in the words the categories know (host_key_changed).
func TestFingerprintsAreRememberedAndAChangeIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	first, other := hostKey(t), hostKey(t)
	cb, err := hostKeyCallback(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := cb("203.0.113.9:22", addr, first); err != nil {
		t.Fatalf("a new server was refused: %v", err)
	}
	cb, _ = hostKeyCallback(path)
	if err := cb("203.0.113.9:22", addr, first); err != nil {
		t.Fatalf("the remembered server was refused: %v", err)
	}
	cb, _ = hostKeyCallback(path)
	err = cb("203.0.113.9:22", addr, other)
	if err == nil || !strings.Contains(err.Error(), "REMOTE HOST IDENTIFICATION HAS CHANGED") {
		t.Fatalf("a changed identity: err = %v", err)
	}
	// "I reinstalled it": forget, then the new identity is accepted.
	if err := forgetHost(path, "203.0.113.9", 22); err != nil {
		t.Fatal(err)
	}
	cb, _ = hostKeyCallback(path)
	if err := cb("203.0.113.9:22", addr, other); err != nil {
		t.Fatalf("after forgetting: %v", err)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("known_hosts mode %v, want 0600", info.Mode().Perm())
	}
}

// Like bxkit: nothing that spawns processes, no tunnel, no embedded binaries in the iPhone app.
func TestBxdeployPullsInNoPlumbing(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOOS=ios", "GOARCH=arm64", "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed: %v", err)
	}
	deps := strings.Split(string(out), "\n")
	if len(deps) < 10 {
		t.Fatal("go list listed almost nothing; this guard no longer reads it")
	}
	for _, d := range deps {
		d = strings.TrimSpace(d)
		if d == "os/exec" {
			t.Error("bxdeploy links os/exec")
		}
		for _, banned := range []string{"/internal/supervisor", "/internal/tunnel", "/internal/embedded", "/internal/provision", "/internal/guardian", "/internal/cli", "/internal/install", "/internal/update"} {
			if strings.HasSuffix(d, banned) {
				t.Errorf("bxdeploy depends on %s", d)
			}
		}
	}
}
