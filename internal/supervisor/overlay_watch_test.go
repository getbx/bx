package supervisor

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/overlay"
)

func tenantsNamed(names ...string) []overlay.Tenant {
	var out []overlay.Tenant
	for _, n := range names {
		out = append(out, overlay.Tenant{Name: n})
	}
	return out
}

// **晚于 bx 启动的 overlay 认得出来,但只有一半能自愈。**
//
// 中继旁路那一半会自己跟上(重试循环每轮现测),而 DNS split 不行:dns.Server 的
// SetSplit 是无锁赋值,今天安全只因为它只在启动时调一次 —— 运行期再调是 data race。
// 所以这条告警的用途不是「提示有个新玩意」,而是**明确告诉用户还差什么、怎么补**。
func TestTenantsThatAppearedAfterStartup(t *testing.T) {
	for _, tc := range []struct {
		name    string
		startup []overlay.Tenant
		now     []overlay.Tenant
		want    []string
	}{
		{
			name:    "启动后才起来的租户要报出来",
			startup: tenantsNamed("tailscale"),
			now:     tenantsNamed("tailscale", "zerotier"),
			want:    []string{"zerotier"},
		},
		{
			name:    "启动时就在的不报",
			startup: tenantsNamed("tailscale"),
			now:     tenantsNamed("tailscale"),
			want:    nil,
		},
		{
			// **消失不报。** 用户关掉 Tailscale 不是异常,而一条「它不见了」的告警
			// 会在每次关掉它之后常驻,把这一行训练成噪声。
			name:    "租户消失不是异常",
			startup: tenantsNamed("tailscale", "zerotier"),
			now:     tenantsNamed("tailscale"),
			want:    nil,
		},
		{
			name:    "启动时一个都没有,后来起了一个",
			startup: nil,
			now:     tenantsNamed("tailscale"),
			want:    []string{"tailscale"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tenantsAppearedSince(tc.startup, tc.now)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("= %v, want %v", got, tc.want)
			}
		})
	}
}

// 告警必须**说清楚还差什么**,而不是只说「检测到 X」。
//
// 用户看到「检测到 ZeroTier」除了困惑什么也得不到;他需要知道的是:旁路会自己跟上,
// 而 DNS 那一半要重启 bx。一条不给出路的告警只是噪声。
func TestLateTenantWarningSaysWhatIsStillMissing(t *testing.T) {
	w := lateTenantWarning([]string{"zerotier"})
	if w.Name == "" {
		t.Fatal("有晚到的租户却没有告警")
	}
	if w.Severity != "warn" {
		t.Errorf("共存告警不该是 error 级(它不阻断保护):%q", w.Severity)
	}
	joined := w.Detail + " " + w.Hint
	if !strings.Contains(joined, "zerotier") {
		t.Errorf("没点名是谁:%q", joined)
	}
	if !strings.Contains(strings.ToLower(joined), "restart") {
		t.Errorf("没给出路(DNS 那一半要重启 bx 才生效):%q", joined)
	}
	if lateTenantWarning(nil).Name != "" {
		t.Error("没有晚到的租户时必须一个字都不说")
	}
}
