package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/config"
)

func TestDeadServerOnlyMovesTheAddressAndLeavesTheCallerAlone(t *testing.T) {
	in := map[string]any{"type": "vless", "server": "203.0.113.9", "server_port": 8443, "uuid": "u"}
	out := deadServer(in)
	if out["server"] != deadServerAddr || out["server_port"] != 443 || out["uuid"] != "u" || out["type"] != "vless" {
		t.Fatalf("dead outbound = %v", out)
	}
	if in["server"] != "203.0.113.9" {
		t.Fatal("deadServer mutated the live outbound")
	}
}

// policy.json 进 App 包:它只许带规则与 global,链接(凭据)一个字都不许在里面。
func TestPhonePolicyCarriesRulesButNeverTheLink(t *testing.T) {
	const link = "vless://11111111-2222-3333-4444-555555555555@203.0.113.9:443?security=reality&sni=www.cloudflare.com&pbk=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA&sid=abcd"
	cfg, err := config.Parse([]byte("server: " + link + "\nglobal: true\nrules:\n  - direct: ['*.apple.com']\n  - proxy: ['x.com']\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(policyOf(cfg))
	got := string(raw)
	if got != `{"global":true,"direct":["*.apple.com"],"proxy":["x.com"]}` {
		t.Fatalf("policy = %s", got)
	}
	if strings.Contains(got, "11111111") || strings.Contains(got, "203.0.113.9") {
		t.Fatalf("policy leaks the server link: %s", got)
	}
}
