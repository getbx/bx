package cli

import (
	"os"
	"regexp"
	"testing"

	"github.com/getbx/bx/internal/doctor"
	"github.com/getbx/bx/internal/guardian"
)

// 三个 DNS 状态常量必须与 guardian 那份逐字相同 —— doctor 不能 import guardian(成环),
// 只能各写一份,这条守卫让它们不漂。
func TestDoctorDNSStateConstantsMatchGuardian(t *testing.T) {
	for _, pair := range []struct{ got, want string }{
		{doctor.DNSStateUnknown, string(guardian.DNSUnknown)},
		{doctor.DNSStateManaged, string(guardian.DNSManaged)},
		{doctor.DNSStateNotNeeded, string(guardian.DNSNotNeeded)},
	} {
		if pair.got != pair.want {
			t.Fatalf("doctor 的 DNS 常量 %q ≠ guardian 的 %q", pair.got, pair.want)
		}
	}
}

// guardian.Status → GuardianFact 的转换是纯的、逐字段的。
func TestGuardianFactFromStatusCarriesEveryField(t *testing.T) {
	st := guardian.Status{
		DNSState: "managed", DNSManaged: true, DNSService: "Wi-Fi",
		Recovery: guardian.RecoverySnapshot{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"},
	}
	got := guardianFactFrom(st)
	if got.DNS != (doctor.DNSFact{State: "managed", Managed: true, Service: "Wi-Fi"}) {
		t.Fatalf("DNS = %+v", got.DNS)
	}
	if got.Recovery != (doctor.RecoveryFact{State: "failed", Stage: "verify", Attempt: 3, ErrorCode: "transport_unavailable"}) {
		t.Fatalf("Recovery = %+v", got.Recovery)
	}
}

// **判据只有一份。** cli 里除了薄壳,不许再有第二处产出 doctor check 的判断:
// collectClientDoctorWith 的函数体必须就是「采集 → doctor.Judge」。
func TestClientDoctorIsJudgedByTheDoctorPackage(t *testing.T) {
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	body := regexp.MustCompile(`(?s)func collectClientDoctorWith\([^)]*\) doctorReport \{(.*?)\n\}`).FindStringSubmatch(string(src))
	if body == nil {
		t.Fatal("找不到 collectClientDoctorWith —— 守卫读不懂现在的代码,先修守卫")
	}
	if !regexp.MustCompile(`return doctor\.Judge\(collectDoctorFacts\(`).MatchString(body[1]) {
		t.Fatalf("collectClientDoctorWith 不是「采集 → doctor.Judge」:\n%s", body[1])
	}
	if regexp.MustCompile(`AddCheck\(|addCheck\(`).MatchString(body[1]) {
		t.Fatal("collectClientDoctorWith 里仍在自己产出 check —— 判据长回了 cli")
	}
}
