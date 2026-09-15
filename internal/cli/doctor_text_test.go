package cli

import (
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/elevate"
)

func TestRenderDoctorReportPrintsEveryCheckAndItsHint(t *testing.T) {
	rep := doctorReport{Checks: []checkReport{
		{Name: "config_readable", Status: "ok", Detail: "yes"},
		{Name: "udp_policy", Status: "warn", Detail: "non-DNS UDP blocked", Hint: "use " + elevate.Prefix + "bx realtime on"},
		// detail 里带 `|` —— 规则原文与错误文本里真的会有。三段是结构体而不是
		// 一个串,正是为了让这一行不会被切错;写成串再 SplitN 时这条会打出半句话。
		{Name: "rule_risky_direct_rule", Status: "warn", Detail: "1 条:a|b", Hint: "" + elevate.Prefix + "bx direct rm 'a|b'"},
	}}
	want := []doctorLineSpec{
		{Status: "ok", Key: "config readable", Value: "yes"},
		{Status: "warn", Key: "udp policy", Value: "non-DNS UDP blocked"},
		{Status: "hint", Key: "udp policy", Value: "use " + elevate.Prefix + "bx realtime on"},
		{Status: "warn", Key: "rule risky direct rule", Value: "1 条:a|b"},
		{Status: "hint", Key: "rule risky direct rule", Value: "" + elevate.Prefix + "bx direct rm 'a|b'"},
	}
	got := renderDoctorReport(rep)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("\n got %+v\nwant %+v", got, want)
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
