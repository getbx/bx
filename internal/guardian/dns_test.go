package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/getbx/bx/internal/install"
)

func TestDNSManagerMapsInstallEnabledStatus(t *testing.T) {
	manager := dnsManager{
		service: "Wi-Fi",
		ensure: func(context.Context, string) (install.DNSStatus, error) {
			return install.DNSStatus{Enabled: true, Service: "Wi-Fi", Servers: []string{"127.0.0.1"}}, nil
		},
		inspect: func(context.Context, string) (install.DNSStatus, error) {
			return install.DNSStatus{Enabled: false, Service: "Wi-Fi", Servers: []string{"1.1.1.1"}}, nil
		},
		restore: func(context.Context, string) (install.DNSStatus, error) {
			return install.DNSStatus{Enabled: false, Service: "Wi-Fi", Servers: []string{"1.1.1.1"}}, nil
		},
	}

	managed, err := manager.EnsureManaged(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if managed != (DNSStatus{State: DNSManaged, Service: "Wi-Fi"}) {
		t.Fatalf("EnsureManaged() = %+v, want managed Wi-Fi", managed)
	}

	unmanaged, err := manager.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if unmanaged != (DNSStatus{State: DNSUnmanaged, Service: "Wi-Fi"}) {
		t.Fatalf("Inspect() = %+v, want unmanaged Wi-Fi", unmanaged)
	}

	restored, err := manager.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if restored != (DNSStatus{State: DNSUnmanaged, Service: "Wi-Fi"}) {
		t.Fatalf("Restore() = %+v, want unmanaged Wi-Fi", restored)
	}
}

func TestGuardianDNSStatusJSONOmitsResolverAddresses(t *testing.T) {
	status := Status{DNSState: DNSManaged, DNSManaged: true, DNSService: "Wi-Fi"}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"1.1.1.1", "8.8.8.8", "servers"} {
		if bytes.Contains(data, []byte(forbidden)) {
			t.Fatalf("status leaked %q: %s", forbidden, data)
		}
	}
}
