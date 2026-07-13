package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getbx/bx/internal/config"
)

func TestGetPreset(t *testing.T) {
	p, err := getPreset("gaming")
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "gaming" || !containsString(p.Direct, "client-update.akamai.steamstatic.com") {
		t.Fatalf("gaming preset = %+v", p)
	}
	if _, err := getPreset("missing"); err == nil {
		t.Fatal("unknown preset should fail")
	}
}

func TestAppPresetsAvoidOpenCloudDomains(t *testing.T) {
	for name, preset := range appPresets {
		for _, domain := range preset.Direct {
			if risk := directRuleRisk(domain); risk != "" {
				t.Fatalf("preset %s contains risky direct domain %q: %s", name, domain, risk)
			}
		}
	}
}

func TestApplyPresetToConfigAddsDirectRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	in := `
server: brook://server?server=example.com%3A443&password=pw
rules:
  - proxy:
      - client-update.akamai.steamstatic.com
      - proxy.example.com
  - proxy:
      - "*.steamstatic.com"
`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := applyPresetToConfig(path, appPresets["gaming"])
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("expected preset to change config")
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	direct := ruleField(cfg, "direct")
	proxy := ruleField(cfg, "proxy")
	for _, domain := range appPresets["gaming"].Direct {
		if !containsString(direct, domain) {
			t.Fatalf("direct rules missing %q: %+v", domain, direct)
		}
		if containsString(proxy, domain) {
			t.Fatalf("preset should remove proxy conflict for %q: %+v", domain, proxy)
		}
	}
	if !containsString(proxy, "proxy.example.com") {
		t.Fatalf("non-conflicting proxy rule should be preserved: %+v", proxy)
	}
}

func TestPresetApplySuccessMessageDoesNotClaimRuleCount(t *testing.T) {
	if got, want := presetApplySuccessMessage("gaming"), "✅ preset gaming 已应用。"; got != want {
		t.Fatalf("preset success message = %q, want %q", got, want)
	}
}

func TestApplyPresetToConfigNoop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	in := `
server: brook://server?server=example.com%3A443&password=pw
rules:
  - direct:
      - client-update.akamai.steamstatic.com
      - steamcdn-a.akamaihd.net
      - media.steampowered.com
      - "*.steamcontent.com"
      - "*.steamstatic.com"
`
	if err := os.WriteFile(path, []byte(in), 0o600); err != nil {
		t.Fatal(err)
	}
	changed, err := applyPresetToConfig(path, appPresets["gaming"])
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Fatal("fully applied preset should be noop")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
