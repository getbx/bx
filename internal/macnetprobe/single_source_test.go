package macnetprobe_test

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// 这三条判据全仓只许有**一份实现**。
//
// 2026-09-18 真机:同样的解析在 internal/supervisor(喂 `bx status`)与
// internal/platformcheck(喂 `bx doctor` 与菜单的 Checks 页)里各有一份逐字拷贝。
// 修了前者的措辞而后者没跟上,于是同一台机器上两个面对**同一个事实**说了两句不一样
// 的话,**而两边的测试都绿** —— 因为两边各测各的那一份。
//
// 判据是「除了本包,没有别处再写一遍那个正则」:薄壳(调本包)可以有很多个,
// 自己解析的不许有第二个。
func TestTheseJudgementsExistOnlyHere(t *testing.T) {
	out, err := exec.Command("git", "-C", "../..", "grep", "-n", "-E",
		`regexp\.MustCompile\(.*(Connected\|Connecting|100\\\.64|HTTPSEnable)`, "--", "*.go").Output()
	if err != nil && len(out) == 0 {
		// git grep 没命中时退出码非 0 —— 那正是我们要的(只剩本包那一份也会命中)。
		out = nil
	}
	var offenders []string
	for _, line := range strings.Split(string(out), "\n") {
		if line == "" || strings.HasPrefix(line, "internal/macnetprobe/") {
			continue
		}
		offenders = append(offenders, line)
	}
	if len(offenders) > 0 {
		t.Fatalf("本包之外又出现了一份同样的解析 —— 两份判据必然漂,而漂的后果是两个面\n"+
			"对同一个事实说两句不一样的话,且两边测试都绿:\n  %s",
			strings.Join(offenders, "\n  "))
	}

	// 下限:本包自己那几个正则要真的在。读不出来时响亮失败,而不是「没有第二份」。
	self, err := exec.Command("git", "-C", "../..", "grep", "-c", "regexp.MustCompile",
		"--", "internal/macnetprobe/macnetprobe.go").Output()
	if err != nil {
		t.Fatalf("读不到本包的正则定义 —— 这条守卫读不懂现在的代码了:%v", err)
	}
	if n := regexp.MustCompile(`\d+`).FindString(string(self)); n == "" || n == "0" {
		t.Fatalf("本包一个正则都没有了(got %q)—— 判据搬走了就把这条守卫也搬走", string(self))
	}
}
