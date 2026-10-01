package mobileconfig

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/getbx/bx/internal/config"
)

func buildWith(t *testing.T, opts Options) map[string]any {
	t.Helper()
	cfg, err := config.Parse([]byte(baseYAML))
	if err != nil {
		t.Fatal(err)
	}
	f, err := BuildWithOptions(cfg, testLists, testProxy, opts)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(f.Config, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// Tailscale off is today's config, byte for byte: the option must not leak into the default.
func TestTailscaleOffIsTodaysConfig(t *testing.T) {
	cfg, err := config.Parse([]byte(baseYAML))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := Build(cfg, testLists, testProxy)
	if err != nil {
		t.Fatal(err)
	}
	off, err := BuildWithOptions(cfg, testLists, testProxy, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plain.Config, off.Config) {
		t.Fatal("Options{} changed the config")
	}
	if _, ok := buildWith(t, Options{})["endpoints"]; ok {
		t.Fatal("a Tailscale endpoint is present with Tailscale off")
	}
}

// iOS runs one VPN at a time, so bx carries the user's tailnet itself. Whatever the tailnet
// claims — its devices, the home and office subnets its routers advertise, MagicDNS names —
// must go to Tailscale before any other route: those subnets are private ranges, which bx
// otherwise sends direct onto whatever Wi-Fi the phone is on.
func TestTailscaleClaimsTailnetTrafficBeforeEveryOtherRoute(t *testing.T) {
	doc := buildWith(t, Options{Tailscale: true})

	eps, _ := doc["endpoints"].([]any)
	if len(eps) != 1 {
		t.Fatalf("endpoints = %v, want one Tailscale endpoint", doc["endpoints"])
	}
	ep := eps[0].(map[string]any)
	if ep["type"] != "tailscale" || ep["tag"] != TailscaleTag || ep["accept_routes"] != true || ep["state_directory"] == "" {
		t.Fatalf("endpoint = %v: want type tailscale, tag %q, accept_routes, a state directory", ep, TailscaleTag)
	}

	rules := at(doc, "route", "rules").([]any)
	claim := -1
	for i, r := range rules {
		m := r.(map[string]any)
		if pb, ok := m["preferred_by"].([]any); ok && len(pb) == 1 && pb[0] == TailscaleTag && m["outbound"] == TailscaleTag {
			claim = i
		}
	}
	if claim < 0 {
		t.Fatalf("no route rule sends what Tailscale claims to it: %v", rules)
	}
	for i, r := range rules[:claim] {
		m := r.(map[string]any)
		if m["action"] != "sniff" && m["action"] != "hijack-dns" && m["action"] != "reject" {
			t.Errorf("rule %d (%v) routes traffic before the Tailscale claim", i, m)
		}
	}

	servers := at(doc, "dns", "servers").([]any)
	var tsDNS string
	for _, s := range servers {
		m := s.(map[string]any)
		if m["type"] == "tailscale" && m["endpoint"] == TailscaleTag {
			tsDNS, _ = m["tag"].(string)
		}
	}
	if tsDNS == "" {
		t.Fatalf("no MagicDNS server: %v", servers)
	}
	first := at(doc, "dns", "rules").([]any)[0].(map[string]any)
	// In a DNS rule preferred_by names DNS servers, not endpoints (sing-box 1.14) — naming the
	// endpoint passes `sing-box check` and fails at start ("DNS server not found"), on device.
	if pb, _ := first["preferred_by"].([]any); len(pb) != 1 || pb[0] != tsDNS || first["server"] != tsDNS {
		t.Fatalf("first DNS rule = %v: tailnet names must be answered by MagicDNS (preferred_by %q) before fake-IP", first, tsDNS)
	}
}
