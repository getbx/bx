package main

import "testing"

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
