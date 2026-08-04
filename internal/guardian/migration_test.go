package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestManagerMigrationVerifiesDNSBeforeBarrierRelease(t *testing.T) {
	env := newManagerTestEnv(t)
	env.dns.record = true
	request := MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.10/32"},
	}
	if err := env.manager.Migrate(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"desired.on",
		"barrier.install",
		"legacy.stop",
		"barrier.reassert",
		"core.start",
		"dns.ensure",
		"dns.inspect",
		"barrier.release",
		"legacy.remove",
	}
	if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	status := env.manager.Status()
	if status.Protection != ProtectionProtected || status.DNSState != DNSManaged {
		t.Fatalf("status = %+v", status)
	}
}

func TestMigrationRequestJSONContainsOnlyNonSecretHandoffMetadata(t *testing.T) {
	request := MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.10/32", "2001:db8::10/128"},
	}
	b, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"gateway", "server_bypass", "198.51.100.10/32"} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("migration request missing %q: %s", want, b)
		}
	}
	for _, forbidden := range []string{"client_link", "server_link", "token", "password", "config"} {
		if bytes.Contains(bytes.ToLower(b), []byte(forbidden)) {
			t.Fatalf("migration request contains %q: %s", forbidden, b)
		}
	}
}

func TestValidateMigrationRequestRequiresExactBypassesAndIPv4(t *testing.T) {
	for _, request := range []MigrationRequest{
		{Gateway: "192.0.2.1", ServerBypass: []string{"198.51.100.0/24"}},
		{Gateway: "192.0.2.1", ServerBypass: []string{"2001:db8::10/128"}},
		{Gateway: "not-an-ip", ServerBypass: []string{"198.51.100.10/32"}},
	} {
		if _, err := ValidateMigrationRequest(request); err == nil {
			t.Fatalf("unsafe migration request accepted: %+v", request)
		}
	}
	request := MigrationRequest{
		Gateway:      "192.0.2.1",
		ServerBypass: []string{"198.51.100.10/32", "2001:db8::10/128", "198.51.100.10/32"},
	}
	got, err := ValidateMigrationRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.ServerBypass) != 2 {
		t.Fatalf("normalized bypasses = %#v", got.ServerBypass)
	}
}
