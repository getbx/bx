package guardian

import (
	"context"

	"github.com/getbx/bx/internal/install"
)

type dnsManager struct {
	service string
	ensure  func(context.Context, string) (install.DNSStatus, error)
	inspect func(context.Context, string) (install.DNSStatus, error)
	restore func(context.Context, string) (install.DNSStatus, error)
}

// NewDNSManager adapts the install package's context-aware macOS DNS API to
// Guardian's resolver-free DNS lifecycle contract.
func NewDNSManager(service string) DNSManager {
	return dnsManager{
		service: service,
		ensure:  install.EnableDNSContext,
		inspect: install.InspectDNSContext,
		restore: install.DisableDNSContext,
	}
}

func (m dnsManager) EnsureManaged(ctx context.Context) (DNSStatus, error) {
	status, err := m.ensure(ctx, m.service)
	return guardianDNSStatus(status, err), err
}

func (m dnsManager) Inspect(ctx context.Context) (DNSStatus, error) {
	status, err := m.inspect(ctx, m.service)
	return guardianDNSStatus(status, err), err
}

func (m dnsManager) Restore(ctx context.Context) (DNSStatus, error) {
	status, err := m.restore(ctx, m.service)
	return guardianDNSStatus(status, err), err
}

func guardianDNSStatus(status install.DNSStatus, err error) DNSStatus {
	if err != nil {
		return DNSStatus{State: DNSUnknown, Service: status.Service, Servers: status.Servers}
	}
	if status.Enabled {
		return DNSStatus{State: DNSManaged, Service: status.Service, Servers: status.Servers}
	}
	return DNSStatus{State: DNSUnmanaged, Service: status.Service, Servers: status.Servers}
}
