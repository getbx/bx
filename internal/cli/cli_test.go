package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
)

func TestAppHasVersion(t *testing.T) {
	app := New()
	if strings.TrimSpace(app.Version) == "" {
		t.Fatal("app version should not be empty")
	}
}

func TestBuildExecStart(t *testing.T) {
	got := buildExecStart("/usr/local/bin/bx", "/etc/bx/config.yaml")
	want := "/usr/local/bin/bx run -c /etc/bx/config.yaml"
	if got != want {
		t.Fatalf("ExecStart 应跑 run, got %q", got)
	}
}

func TestBlinkRoundTripThroughCLI(t *testing.T) {
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	enc := blink.Encode(link)
	dec, err := blink.Decode(enc)
	if err != nil || dec != link {
		t.Fatalf("round-trip 失败: %q err=%v", dec, err)
	}
}

func TestBXServerLink(t *testing.T) {
	link, err := bxServerLink("example.com", serverConfig{Listen: ":9999", Password: "pw"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := blink.Decode(link)
	if err != nil {
		t.Fatal(err)
	}
	want := "brook://server?server=example.com%3A9999&password=pw"
	if raw != want {
		t.Fatalf("raw link = %q, want %q", raw, want)
	}
}

func TestBXServerLinkRejectsHostWithPort(t *testing.T) {
	if _, err := bxServerLink("example.com:8443", serverConfig{Listen: ":9999", Password: "pw"}); err == nil {
		t.Fatal("host 带端口应报错,端口应来自 listen")
	}
}

func TestServerFirewallHint(t *testing.T) {
	got := serverFirewallHint(":9998")
	for _, want := range []string{"TCP 9998", "sudo ufw allow 9998/tcp"} {
		if !strings.Contains(got, want) {
			t.Fatalf("firewall hint = %q, want contains %q", got, want)
		}
	}
	if got := serverFirewallHint("bad-listen"); got != "" {
		t.Fatalf("bad listen should not produce hint, got %q", got)
	}
}

func TestDoctorHelpers(t *testing.T) {
	if got := boolStatus(true); got != "ok" {
		t.Fatalf("boolStatus(true)=%q", got)
	}
	if got := boolStatus(false); got != "fail" {
		t.Fatalf("boolStatus(false)=%q", got)
	}
	if got := redactLink("bx://secret"); got != "bx://<redacted>" {
		t.Fatalf("redact bx link = %q", got)
	}
	if got := redactLink("brook://server?password=pw"); got != "internal-link:<redacted>" {
		t.Fatalf("redact internal link = %q", got)
	}
}

func TestIsListening(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if !isListening(port) {
		t.Fatalf("port %s should be detected as listening", port)
	}
}

func TestWriteReadServerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	cfg := serverConfig{Listen: ":9999", Password: "pw"}
	if err := writeServerConfig(path, cfg, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
	got, err := readServerConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg {
		t.Fatalf("config = %+v, want %+v", got, cfg)
	}
}

func TestWriteServerConfigForceResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "pw"}, true); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestRotateServerConfigPreservesListenAndResetsPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := writeServerConfig(path, serverConfig{Listen: ":9999", Password: "old"}, false); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := rotateServerConfig(path, "new")
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":9999" || got.Password != "new" {
		t.Fatalf("rotated config = %+v", got)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("server config perm = %o, want 0600", fi.Mode().Perm())
	}
}

func TestShareHelpers(t *testing.T) {
	if got, err := cleanShareName("alice-1"); err != nil || got != "alice-1" {
		t.Fatalf("cleanShareName = %q, %v", got, err)
	}
	for _, bad := range []string{"", "../x", "a b", "x/y"} {
		if _, err := cleanShareName(bad); err == nil {
			t.Fatalf("bad share name %q should fail", bad)
		}
	}
	dir := t.TempDir()
	if got := shareConfigPath(dir, "alice"); got != filepath.Join(dir, "alice.yaml") {
		t.Fatalf("shareConfigPath = %q", got)
	}
}

func TestReadSharesSorted(t *testing.T) {
	dir := t.TempDir()
	for _, item := range []struct {
		name   string
		listen string
	}{
		{"bob", ":10002"},
		{"alice", ":10001"},
	} {
		if err := writeServerConfig(shareConfigPath(dir, item.name), serverConfig{Listen: item.listen, Password: "pw"}, false); err != nil {
			t.Fatal(err)
		}
	}
	got, err := readShares(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "alice" || got[1].Name != "bob" {
		t.Fatalf("shares = %+v", got)
	}
}

func TestNextShareListenSkipsExistingShares(t *testing.T) {
	dir := t.TempDir()
	if err := writeServerConfig(shareConfigPath(dir, "alice"), serverConfig{Listen: ":10000", Password: "pw"}, false); err != nil {
		t.Fatal(err)
	}
	got, err := nextShareListen(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != ":10001" {
		t.Fatalf("nextShareListen = %q, want :10001", got)
	}
}

func TestResolveConfigPathKeepsExplicitMissingPath(t *testing.T) {
	// 用户显式传入的不存在路径应原样返回(不偷偷回退),便于错误信息指向用户路径
	p := "/nonexistent/explicit/whoami-bx-test.yaml"
	if got := resolveConfigPath(p); got != p {
		t.Fatalf("显式缺失路径应原样返回, got %q", got)
	}
}
