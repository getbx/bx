package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/blink"
	"github.com/getbx/bx/internal/config"
)

func TestWriteConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	link := "brook://server?server=1.2.3.4%3A9999&password=pw"
	if err := WriteConfig(p, []string{link}, "", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != link {
		t.Errorf("server 应为 %q, got %q", link, cfg.Server)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("配置权限 = %o, want 0600", fi.Mode().Perm())
	}
	if !cfg.Global {
		t.Error("global 应为 true")
	}
	if !cfg.Killswitch {
		t.Error("killswitch 应为 true")
	}
}

func TestWriteConfigPreservesBXLink(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	raw := "brook://server?server=1.2.3.4%3A9999&password=pw"
	link := blink.Encode(raw)
	if err := WriteConfig(p, []string{link}, "", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "bx://") {
		t.Fatalf("config should preserve bx:// link, got:\n%s", b)
	}
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server != raw {
		t.Fatalf("parsed server = %q, want %q", cfg.Server, raw)
	}
}

func TestWriteConfigRefusesExistingWithoutForce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte("server: old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteConfig(p, []string{"brook://new"}, "", false); err == nil {
		t.Fatal("已存在且无 force 应报错")
	}
	if err := WriteConfig(p, []string{"brook://new"}, "", true); err != nil {
		t.Fatalf("force 应覆盖: %v", err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("force 覆盖后权限 = %o, want 0600", fi.Mode().Perm())
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "brook://new") {
		t.Error("force 应写入新链接")
	}
}

func TestOwnerUIDFromEnv(t *testing.T) {
	mk := func(v string) func(string) string { return func(string) string { return v } }
	cases := []struct {
		in   string
		want int
	}{
		{"1000", 1000},
		{"", 0},
		{"abc", 0},
		{"0", 0},
		{" 1001 ", 1001},
		{"-5", 0},
	}
	for _, c := range cases {
		if got := ownerUIDFromEnv(mk(c.in)); got != c.want {
			t.Errorf("ownerUIDFromEnv(%q)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestWriteConfigMultiTransports(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	links := []string{
		blink.Encode("vless://u@1.2.3.4:9998?security=reality&pbk=K&sid=ab&sni=www.apple.com"),
		blink.Encode("brook://server?server=1.2.3.4%3A9999&password=pw"),
	}
	if err := WriteConfig(p, links, "", false); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if strings.Contains(string(b), "\nserver:") {
		t.Fatalf("多传输不该写 server:, got:\n%s", b)
	}
	cfg, err := config.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Transports) != 2 {
		t.Fatalf("应解析出 2 个传输, got %d", len(cfg.Transports))
	}
	if cfg.Transports[0][:8] != "vless://" || cfg.Transports[1][:8] != "brook://" {
		t.Fatalf("传输顺序不对: %v", cfg.Transports)
	}
}

func TestWriteConfigEmpty(t *testing.T) {
	if err := WriteConfig(filepath.Join(t.TempDir(), "c.yaml"), nil, "", false); err == nil {
		t.Fatal("空链接列表应报错")
	}
}

func TestWriteConfigWithUDPTransport(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	main := "bx://" + "eyJ2IjoxLCJ0cmFuc3BvcnQiOiJyZWFsaXR5IiwibGluayI6InZsZXNzOi8vdWlkQDEuMi4zLjQ6NDQzP3NlY3VyaXR5PXJlYWxpdHkmcGJrPVAmc2lkPWFiJnNuaT13d3cuY2xvdWRmbGFyZS5jb20ifQ"
	udp := "hysteria2://pw@1.2.3.4:443?obfs=salamander&obfs-password=x"
	if err := WriteConfig(p, []string{main}, udp, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "udp:") || !strings.Contains(string(b), "transport:") {
		t.Errorf("配置应含 udp.transport:\n%s", b)
	}
}
