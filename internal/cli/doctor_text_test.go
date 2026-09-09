package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func TestRenderDoctorReportPrintsEveryCheckAndItsHint(t *testing.T) {
	rep := doctorReport{Checks: []checkReport{
		{Name: "config_readable", Status: "ok", Detail: "yes"},
		{Name: "udp_policy", Status: "warn", Detail: "non-DNS UDP blocked", Hint: "use sudo bx realtime on"},
	}}
	lines := renderDoctorReport(rep)
	want := []string{"ok|config readable|yes", "warn|udp policy|non-DNS UDP blocked", "hint|udp policy|use sudo bx realtime on"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("\n got %q\nwant %q", lines, want)
	}
}

// 文本路径不许再有自己的一份判据:doctorAction 里除了渲染与流量行,不许出现
// 「读配置 / 解析 / 拨 socket」这些采集动作。
func TestDoctorTextPathRendersTheSharedReport(t *testing.T) {
	src := menuGoSource(t, "cli.go")
	body := regexp.MustCompile(`(?s)func doctorAction\(c \*cli\.Context\) \(err error\) \{(.*?)\n\}`).FindStringSubmatch(src)
	if body == nil {
		t.Fatal("找不到 doctorAction")
	}
	if !strings.Contains(body[1], "renderDoctorReport(collectClientDoctor(") {
		t.Fatal("文本路径没有渲染共享的 Report")
	}
	for _, forbidden := range []string{"os.ReadFile(", "config.Parse(", "checkStatusSocket()", "readGuardianStatus()", "collectPlatformChecks("} {
		if strings.Contains(body[1], forbidden) {
			t.Fatalf("doctorAction 里仍有自己的采集 %s —— 判据又分叉了", forbidden)
		}
	}
}

func menuGoSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("读不到 %s: %v", name, err)
	}
	return string(b)
}
