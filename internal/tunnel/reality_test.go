package tunnel

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The factory must (a) write a valid sing-box config to confPath derived from the
// link, and (b) build a command pointing sing-box at that config. We use a fake
// binary path and assert the config file is produced + references the socks addr.
func TestRealityFactoryWritesConfig(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "sing-box.json")
	link := "vless://uid@1.2.3.4:443?security=reality&pbk=P&sid=S&sni=www.microsoft.com&flow=xtls-rprx-vision&fp=chrome"
	f := realityFactory("/bin/true", link, conf) // /bin/true exits 0 immediately
	r, err := f("127.0.0.1:10811")
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	defer r.Kill()
	data, err := os.ReadFile(conf)
	if err != nil {
		t.Fatalf("config not written: %v", err)
	}
	if !strings.Contains(string(data), "10811") || !strings.Contains(string(data), "www.microsoft.com") {
		t.Errorf("config missing socks port or sni: %s", data)
	}
}

func TestRealityFactoryRejectsBadLink(t *testing.T) {
	f := realityFactory("/bin/true", "brook://x", filepath.Join(t.TempDir(), "c.json"))
	if _, err := f("127.0.0.1:1"); err == nil {
		t.Fatal("expected error for non-vless link")
	}
}
