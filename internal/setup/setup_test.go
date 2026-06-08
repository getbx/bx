package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	if !cfg.Global {
		t.Error("global 应为 true")
	}
	if !cfg.Killswitch {
		t.Error("killswitch 应为 true")
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
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "brook://new") {
		t.Error("force 应写入新链接")
	}
}
