package guardian

import (
	"bytes"
	"context"
	"encoding/json"
	"reflect"
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
	// **解析器地址必须一路带出来。** 它是 install 层一直采着、却从没发布过的字段,
	// 而菜单在 DNS **没被接管**时要显示的正是它。只比 State/Service 时,
	// 中途把它掉了不会有任何测试转红。
	if !reflect.DeepEqual(managed, DNSStatus{State: DNSManaged, Service: "Wi-Fi", Servers: []string{"127.0.0.1"}}) {
		t.Fatalf("EnsureManaged() = %+v, want managed Wi-Fi", managed)
	}

	unmanaged, err := manager.Inspect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(unmanaged, DNSStatus{State: DNSUnmanaged, Service: "Wi-Fi", Servers: []string{"1.1.1.1"}}) {
		t.Fatalf("Inspect() = %+v, want unmanaged Wi-Fi", unmanaged)
	}

	restored, err := manager.Restore(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored, DNSStatus{State: DNSUnmanaged, Service: "Wi-Fi", Servers: []string{"1.1.1.1"}}) {
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
