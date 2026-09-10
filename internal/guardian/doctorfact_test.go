package guardian

import (
	"testing"

	"github.com/getbx/bx/internal/doctor"
)

func TestDoctorGuardianFactCarriesEveryField(t *testing.T) {
	st := Status{
		DNSState: "managed", DNSManaged: true, DNSService: "Wi-Fi",
		Recovery: RecoverySnapshot{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"},
	}
	got := DoctorGuardianFact(st)
	if got.DNS != (doctor.DNSFact{State: "managed", Managed: true, Service: "Wi-Fi"}) {
		t.Fatalf("DNS = %+v", got.DNS)
	}
	if got.Recovery != (doctor.RecoveryFact{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"}) {
		t.Fatalf("Recovery = %+v", got.Recovery)
	}
}
