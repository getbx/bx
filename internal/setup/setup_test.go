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
	if err := WriteConfig(p, link, false); err != nil {
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
	if err := WriteConfig(p, link, false); err != nil {
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
	if err := WriteConfig(p, "brook://new", false); err == nil {
		t.Fatal("已存在且无 force 应报错")
	}
	if err := WriteConfig(p, "brook://new", true); err != nil {
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
