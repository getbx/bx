package cli

import (
	"os"
	"strings"
	"testing"
)

// `bx setup` 收尾那一句是「上报默认开」唯一的告知面(所有者定的无感默认:不弹窗、菜单不常驻)。
// 判据:它说出三件事 —— 发去哪(维护者、只经隧道)、留在哪(本地目录 + 看的命令)、怎么关。
func TestReportsNoticeSaysWhereItGoesWhereItStaysAndHowToTurnItOff(t *testing.T) {
	notice := reportsNotice()
	for _, want := range []string{"maintainer", "through the tunnel", reportsDir, "bx reports", "reports: off"} {
		if !strings.Contains(notice, want) {
			t.Fatalf("setup notice lacks %q:\n%s", want, notice)
		}
	}
	if strings.Contains(notice, "**") || strings.Contains(notice, "`") {
		t.Fatalf("setup notice carries markup the terminal will not render:\n%s", notice)
	}
}

// 那一句要真的跟在每一条「Next: sudo bx up」后面。setupAction 要 root 与真实文件系统,
// 读源码是这里够得着的办法;锚点找不到就响亮失败,不许安静地扫了零处。
func TestSetupPrintsTheReportsNoticeAfterEveryNextStep(t *testing.T) {
	src, err := os.ReadFile("cli.go")
	if err != nil {
		t.Fatal(err)
	}
	const next = `"bx up\n"`
	body := string(src)
	start := strings.Index(body, "func setupAction(")
	if start < 0 {
		t.Fatal("setupAction not found in cli.go")
	}
	end := strings.Index(body[start:], "\n}\n")
	if end < 0 {
		t.Fatal("setupAction has no closing brace")
	}
	fn := body[start : start+end]
	nexts := strings.Count(fn, next)
	if nexts == 0 {
		t.Fatalf("setupAction no longer prints a %s line; move this guard to wherever the closing step went", next)
	}
	notices := strings.Count(fn, "reportsNotice()")
	if notices != nexts {
		t.Fatalf("setupAction prints %d next-step lines but %d reports notices", nexts, notices)
	}
	rest := fn
	for i := 0; i < nexts; i++ {
		at := strings.Index(rest, next)
		after := rest[at+len(next):]
		nl := strings.Index(after, "\n")
		following := after[nl+1:]
		firstLine := strings.TrimSpace(strings.SplitN(following, "\n", 2)[0])
		if !strings.Contains(firstLine, "reportsNotice()") {
			t.Fatalf("next-step line #%d is not followed by the reports notice; next line is %q", i+1, firstLine)
		}
		rest = after
	}
}

// CLI 与 Guardian 各有一份目录常量(两个包,不共享);漂了 `bx reports` 就列一个空目录。
func TestReportsDirMatchesTheGuardiansStore(t *testing.T) {
	src, err := os.ReadFile("../guardian/reporter.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `reportsDir           = "`+reportsDir+`"`) {
		t.Fatalf("internal/guardian/reporter.go does not store reports in %s", reportsDir)
	}
}
