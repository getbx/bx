package sshpass

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(env []string) map[string]string {
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	return m
}

// The password never travels in the environment, argv, or a file: the env carries only where to
// ask and a one-time nonce; the answer comes over a socket in a 0700 directory.
func TestThePasswordIsNeverInTheEnvironment(t *testing.T) {
	s, err := Serve("hunter2-secret", "/usr/local/bin/bx")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, kv := range s.Env() {
		if strings.Contains(kv, "hunter2-secret") {
			t.Fatalf("the password leaked into the environment: %s", kv)
		}
	}
	env := envMap(s.Env())
	if env["SSH_ASKPASS"] != "/usr/local/bin/bx" || env["SSH_ASKPASS_REQUIRE"] != "force" {
		t.Fatalf("ssh is not told to use us: %v", env)
	}
	info, err := os.Stat(filepath.Dir(env[socketEnv]))
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("socket directory mode = %v (%v), want 0700", info.Mode().Perm(), err)
	}
}

func TestAPasswordPromptIsAnsweredOncePerSSHProcess(t *testing.T) {
	s, err := Serve("pw", "/x")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	env := envMap(s.Env())
	ask := func(ppid int, prompt, nonce string) (string, bool) {
		return askVia(env[socketEnv], nonce, ppid, prompt)
	}
	if got, ok := ask(100, "root@203.0.113.9's password: ", env[nonceEnv]); !ok || got != "pw" {
		t.Fatalf("first prompt: %q %v", got, ok)
	}
	// The same ssh asking again means the password was wrong: never retry it (lockouts, fail2ban).
	if _, ok := ask(100, "root@203.0.113.9's password: ", env[nonceEnv]); ok {
		t.Fatal("answered the same ssh process twice")
	}
	// A different ssh process (a reconnect) may ask once.
	if _, ok := ask(101, "Password:", env[nonceEnv]); !ok {
		t.Fatal("a second connection was refused")
	}
	// Not a password prompt (a key passphrase, a host-key question): refuse.
	if _, ok := ask(102, "Enter passphrase for key '/Users/x/.ssh/id_ed25519': ", env[nonceEnv]); ok {
		t.Fatal("answered a key passphrase prompt with the server password")
	}
	// Wrong nonce: someone else on this machine found the socket.
	if _, ok := ask(103, "Password:", "not-the-nonce"); ok {
		t.Fatal("answered a caller without the nonce")
	}
}

func TestTheAnswerStopsAfterClose(t *testing.T) {
	s, err := Serve("pw", "/x")
	if err != nil {
		t.Fatal(err)
	}
	env := envMap(s.Env())
	s.Close()
	if _, ok := askVia(env[socketEnv], env[nonceEnv], 1, "Password:"); ok {
		t.Fatal("still answering after Close")
	}
	if _, err := os.Stat(filepath.Dir(env[socketEnv])); !os.IsNotExist(err) {
		t.Fatalf("socket directory left behind: %v", err)
	}
}

func TestAskpassInvocationIsRecognisedOnlyWithOurEnvironment(t *testing.T) {
	t.Setenv(socketEnv, "")
	if IsAskpass() {
		t.Fatal("recognised without the socket variable")
	}
	t.Setenv(socketEnv, "/tmp/x/sock")
	t.Setenv(nonceEnv, "n")
	if !IsAskpass() {
		t.Fatal("not recognised with our environment")
	}
}
