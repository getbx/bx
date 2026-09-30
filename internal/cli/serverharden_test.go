package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/srvgen"
)

// fakeHost answers what the real one reads from ss, /proc/<pid>/cgroup and systemctl.
type fakeHost struct {
	holders      map[string][]portHolder // key: listener string, e.g. "tcp/8444"
	afterRestart map[string][]portHolder // holders once Restart was called (nil = unchanged)
	others       []string
	restarts     int
	restartErr   error // returned by the first Restart only
}

func (h *fakeHost) Holders(l srvgen.Listener) ([]portHolder, error) {
	if h.restarts > 0 && h.afterRestart != nil {
		return h.afterRestart[l.String()], nil
	}
	return h.holders[l.String()], nil
}
func (h *fakeHost) RetryingSingboxUnits() ([]string, error) { return h.others, nil }
func (h *fakeHost) Restart() error {
	h.restarts++
	if h.restarts == 1 {
		return h.restartErr
	}
	return nil
}
func (h *fakeHost) Sleep(time.Duration) {}

var bxOwn = []portHolder{{PID: 100, Process: "sing-box", Unit: "bx-server.service"}}

func legacyServer(t *testing.T) (string, string) {
	t.Helper()
	fresh, _ := srvgen.RealityParams{UUID: "11111111-2222-3333-4444-555555555555", Port: 8444, SNI: "www.cloudflare.com", PrivateKey: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", ShortID: "abcd"}.ServerConfig()
	legacy := strings.Replace(string(fresh), `"route"`, `"route_removed_for_test"`, 1)
	path := filepath.Join(t.TempDir(), "sbserver.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, legacy
}

func TestServerHardenSucceedsOnlyOnceBxOwnsItsPortsAgain(t *testing.T) {
	path, _ := legacyServer(t)
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}}
	msg, err := hardenServerConfig(path, host)
	if err != nil || host.restarts != 1 || !strings.Contains(msg, "Hardened") {
		t.Fatalf("msg=%q err=%v restarts=%d", msg, err, host.restarts)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), `"route"`) {
		t.Fatal("route not written")
	}
	if !strings.Contains(string(after), "11111111-2222-3333-4444-555555555555") || !strings.Contains(string(after), strings.Repeat("A", 43)) {
		t.Fatal("keys or users lost")
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
	msg, err = hardenServerConfig(path, host)
	if err != nil || host.restarts != 1 || !strings.Contains(msg, "already hardened") {
		t.Fatalf("second run must not write or restart: msg=%q err=%v restarts=%d", msg, err, host.restarts)
	}
}

// 2026-09-30, the owner's VPS: a hand-made sing-box.service kept retrying; in the restart window it
// took bx's port and bx-server crash-looped while the command had printed success. Refuse up front.
func TestServerHardenRefusesWhileAnotherSingboxServiceIsRetrying(t *testing.T) {
	path, legacy := legacyServer(t)
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}, others: []string{"sing-box.service"}}
	_, err := hardenServerConfig(path, host)
	if err == nil || !strings.Contains(err.Error(), "sing-box.service") || !strings.Contains(err.Error(), "systemctl disable --now") {
		t.Fatalf("err = %v, want it to name the unit and how to stop it", err)
	}
	assertUntouched(t, path, legacy, host)
}

func TestServerHardenRefusesWhenSomethingElseHoldsBxsPort(t *testing.T) {
	path, legacy := legacyServer(t)
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": {{PID: 42, Process: "nginx", Unit: "nginx.service"}}}}
	_, err := hardenServerConfig(path, host)
	if err == nil || !strings.Contains(err.Error(), "nginx") || !strings.Contains(err.Error(), "tcp/8444") {
		t.Fatalf("err = %v, want it to name the process and the port", err)
	}
	assertUntouched(t, path, legacy, host)
}

// If bx does not own its ports after the restart, put the previous config back and restart with it —
// a server left down after a command that said "✓" is the worst outcome.
func TestServerHardenRestoresThePreviousConfigWhenBxDoesNotComeBack(t *testing.T) {
	path, legacy := legacyServer(t)
	stolen := []portHolder{{PID: 7, Process: "sing-box", Unit: "sing-box.service"}}
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}, afterRestart: map[string][]portHolder{"tcp/8444": stolen}}
	_, err := hardenServerConfig(path, host)
	if err == nil || !strings.Contains(err.Error(), "restored") || !strings.Contains(err.Error(), "sing-box.service") {
		t.Fatalf("err = %v, want it to say the previous config was restored and who held the port", err)
	}
	after, _ := os.ReadFile(path)
	if string(after) != legacy {
		t.Fatal("the previous config was not restored")
	}
	if host.restarts != 2 {
		t.Fatalf("restarts = %d, want 2 (the attempt, then the restore)", host.restarts)
	}
}

func TestServerHardenRestoresThePreviousConfigWhenTheRestartFails(t *testing.T) {
	path, legacy := legacyServer(t)
	host := &fakeHost{holders: map[string][]portHolder{"tcp/8444": bxOwn}, restartErr: errors.New("systemctl: boom")}
	_, err := hardenServerConfig(path, host)
	if err == nil || !strings.Contains(err.Error(), "restored") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want the restart error and that the previous config was restored", err)
	}
	if after, _ := os.ReadFile(path); string(after) != legacy {
		t.Fatal("the previous config was not restored")
	}
}

func TestServerHardenSaysWhatItCannotCover(t *testing.T) {
	_, err := hardenServerConfig(filepath.Join(t.TempDir(), "missing.json"), &fakeHost{})
	if err == nil || !strings.Contains(err.Error(), "brook") {
		t.Fatalf("missing config: err = %v, want a sentence that says brook servers are not covered", err)
	}
}

func assertUntouched(t *testing.T, path, legacy string, host *fakeHost) {
	t.Helper()
	after, _ := os.ReadFile(path)
	if string(after) != legacy {
		t.Fatal("the config was written although the command refused")
	}
	if host.restarts != 0 {
		t.Fatal("the server was restarted although the command refused")
	}
}

// Parsers for what the real host reads.
func TestSSAndCgroupParsing(t *testing.T) {
	out := `tcp LISTEN 0 4096 127.0.0.1:8444 0.0.0.0:* users:(("sing-box",pid=3665725,fd=7))
tcp LISTEN 0 511 203.0.113.92:443 0.0.0.0:* users:(("nginx",pid=3117982,fd=9),("nginx",pid=3117981,fd=9))
udp UNCONN 0 0 *:443 *:* users:(("sing-box",pid=1082,fd=8))`
	got := parseSSListeners(out, 8444)
	if len(got) != 1 || got[0].PID != 3665725 || got[0].Process != "sing-box" {
		t.Fatalf("8444 = %+v", got)
	}
	if got := parseSSListeners(out, 443); len(got) != 3 {
		t.Fatalf("443 = %+v, want both nginx workers and sing-box", got)
	}
	if got := parseSSListeners(out, 44); len(got) != 0 {
		t.Fatalf("port 44 matched a :443 line: %+v", got)
	}
	if u := unitFromCgroup("0::/system.slice/bx-server.service\n"); u != "bx-server.service" {
		t.Fatalf("unit = %q", u)
	}
	if u := unitFromCgroup("0::/user.slice/user-1000.slice/session-3.scope\n"); u != "" {
		t.Fatalf("a login session is not a service: %q", u)
	}
}

// enable-sync must not install anything when the restart check refuses: "Nothing was changed" has to be true.
func TestEnableSyncInstallsNothingWhenTheServerCheckRefuses(t *testing.T) {
	installed := false
	_, err := enableSync(
		func() (string, error) { return "", errors.New("tcp/8444 is held by nginx. Nothing was changed") },
		func() error { installed = true; return nil },
	)
	if err == nil || installed {
		t.Fatalf("err=%v installed=%v: the store was installed although the server check refused", err, installed)
	}
	msg, err := enableSync(func() (string, error) { return "✓ Hardened", nil }, func() error { installed = true; return nil })
	if err != nil || !installed || !strings.Contains(msg, "Rule sync is on") {
		t.Fatalf("happy path: msg=%q err=%v installed=%v", msg, err, installed)
	}
}
