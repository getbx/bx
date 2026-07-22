//go:build darwin

package supervisor

import (
	"context"
	"errors"
	"net/netip"
	"reflect"
	"strings"
	"testing"
)

func TestDarwinUnderlayObserveCanonicalizesPhysicalPath(t *testing.T) {
	var observedInterface string
	manager := &darwinUnderlayManager{
		defaultRoute: func(context.Context) (string, string, error) {
			return "::ffff:192.168.50.2", "en0", nil
		},
		interfacePrefixes: func(interfaceName string) ([]netip.Prefix, error) {
			observedInterface = interfaceName
			return []netip.Prefix{
				netip.MustParsePrefix("192.168.50.27/24"),
				netip.MustParsePrefix("2001:db8::7/64"),
			}, nil
		},
	}

	got, err := manager.Observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.0/24", "2001:db8::/64")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("underlay snapshot = %#v, want %#v", got, want)
	}
	if observedInterface != "en0" {
		t.Fatalf("observed interface = %q, want en0", observedInterface)
	}
}

func TestDarwinUnderlayValidateCaptureRequiresBothIPv4HalvesAndIPv6Rejects(t *testing.T) {
	lookups := map[string]darwinRouteSelection{
		"1.1.1.1":              {Interface: "utun9"},
		"129.1.1.1":            {Interface: "utun9"},
		"2001:4860:4860::8888": {Gateway: "::1", Reject: true},
		"9000::1":              {Gateway: "::1", Reject: true},
	}
	var calls []string
	manager := &darwinUnderlayManager{
		routeLookup: func(_ context.Context, destination string, ipv6 bool) (darwinRouteSelection, error) {
			calls = append(calls, destination)
			if !ipv6 && strings.Contains(destination, ":") {
				t.Fatalf("IPv6 destination %q was queried as IPv4", destination)
			}
			return lookups[destination], nil
		},
		ipv6Enabled: func() bool { return true },
	}

	if err := manager.ValidateCapture(context.Background(), tunHandle{Name: "utun9"}); err != nil {
		t.Fatal(err)
	}
	wantCalls := []string{"1.1.1.1", "129.1.1.1", "2001:4860:4860::8888", "9000::1"}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("capture queries = %#v, want %#v", calls, wantCalls)
	}
}

func TestDarwinUnderlayValidateCaptureReturnsCaptureMissing(t *testing.T) {
	manager := &darwinUnderlayManager{
		routeLookup: func(_ context.Context, destination string, _ bool) (darwinRouteSelection, error) {
			if destination == "129.1.1.1" {
				return darwinRouteSelection{Interface: "en0"}, nil
			}
			return darwinRouteSelection{Interface: "utun9"}, nil
		},
		ipv6Enabled: func() bool { return false },
	}
	err := manager.ValidateCapture(context.Background(), tunHandle{Name: "utun9"})
	if err == nil || !strings.Contains(err.Error(), "capture_missing") {
		t.Fatalf("validation error = %v, want capture_missing", err)
	}
}

func TestDarwinUnderlayValidateCaptureRejectsWrongIPv6RejectGateway(t *testing.T) {
	manager := &darwinUnderlayManager{
		routeLookup: func(_ context.Context, destination string, ipv6 bool) (darwinRouteSelection, error) {
			if !ipv6 {
				return darwinRouteSelection{Interface: "utun9"}, nil
			}
			if destination == "9000::1" {
				return darwinRouteSelection{Gateway: "fe80::1", Reject: true}, nil
			}
			return darwinRouteSelection{Gateway: "::1", Reject: true}, nil
		},
		ipv6Enabled: func() bool { return true },
	}
	err := manager.ValidateCapture(context.Background(), tunHandle{Name: "utun9"})
	if err == nil || !strings.Contains(err.Error(), "capture_missing") {
		t.Fatalf("validation error = %v, want capture_missing", err)
	}
}

func TestDarwinUnderlayRebindUsesFakeRunnerAndNeverTouchesCapture(t *testing.T) {
	old := mustUnderlaySnapshot(t, "en0", "192.168.50.2", "192.168.50.27/24")
	next := mustUnderlaySnapshot(t, "en1", "192.168.1.1", "192.168.1.42/24")
	runner := &recordingCommandRunner{errors: map[string]error{
		"route -n change -net 203.0.113.9/32 192.168.1.1": errors.New("route: not in table"),
		"route -n delete -net 203.0.113.9/32":             errors.New("route: not in table"),
	}}
	manager := &darwinUnderlayManager{runner: runner}

	if err := manager.Rebind(context.Background(), tunHandle{Name: "utun9"}, old, next, []string{"203.0.113.9/32"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, command := range runner.calls {
		for _, forbidden := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
			if strings.Contains(command, forbidden) {
				t.Fatalf("capture mutation: %s", command)
			}
		}
	}
	if got, want := runner.commandsFor("203.0.113.9/32"), []string{
		"route -n change -net 203.0.113.9/32 192.168.1.1",
		"route -n delete -net 203.0.113.9/32",
		"route -n add -net 203.0.113.9/32 192.168.1.1",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("server rebind commands = %#v, want %#v", got, want)
	}
}

func TestParseDarwinRouteSelectionRecognizesReject(t *testing.T) {
	selection, err := parseDarwinRouteSelection([]byte(`
   route to: 9000::1
destination: default
   gateway: ::1
     flags: <UP,GATEWAY,REJECT,STATIC>
 interface: utun9
`))
	if err != nil {
		t.Fatal(err)
	}
	want := darwinRouteSelection{Gateway: "::1", Interface: "utun9", Reject: true}
	if !reflect.DeepEqual(selection, want) {
		t.Fatalf("selection = %#v, want %#v", selection, want)
	}
}
