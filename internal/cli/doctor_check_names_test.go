package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// **仓库级守卫,推广 55ef8ea。** 那次事故的形状是:同一个 check name 被
// rep.addCheck 调了两次,--json 消费方(agent/MCP,这是这条路径唯一的读者)按
// 名字取,只会拿到其中一条,静默丢掉另一条的结论——那次撞的是
// risky_direct_rule(多条危险直连合并前),这条守卫钉住的是**任何**未来会撞
// 上同一种形状的 check,不只是那一条。
//
// 用一份能走到「config 已解析、规则体检算过、平台检查也跑过」这条最长路径的
// fixture,让 collectClientDoctorWith 把它今天能产出的 checks 尽量跑全
// (includePlatformChecks=true,与 doctorAction 的 --json 路径同一开关)。
//
// **读不到任何 check 就是守卫本身坏了,响亮失败,不是静默通过** —— 一个找不到
// 检查项就自动过的守卫等于没有守卫。
func TestDoctorReportHasNoDuplicateCheckNames(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := "server: brook://example.com:9999?password=x\n" +
		"rules:\n" +
		"  - direct:\n" +
		"      - '*.myqcloud.com'\n" +
		"      - '*.apple.com'\n" +
		"      - ocsp.apple.com\n" +
		"    proxy:\n" +
		"      - '*.google.com'\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	rep := collectClientDoctorWith(path, "", time.Second, true, true)
	if len(rep.Checks) == 0 {
		t.Fatal("一个 check 都没枚举到 —— 守卫读不懂现在的 doctor 输出,请连同它一起修")
	}

	seen := make(map[string]int, len(rep.Checks))
	for _, c := range rep.Checks {
		seen[c.Name]++
	}
	var dups []string
	for name, n := range seen {
		if n > 1 {
			dups = append(dups, fmt.Sprintf("%s×%d", name, n))
		}
	}
	if len(dups) > 0 {
		sort.Strings(dups)
		t.Fatalf("同名 check 出现了多次:%v —— 按名字取的消费方只会拿到其中一条,"+
			"静默丢掉其余结论(55ef8ea 就是这个形状,那次撞的是 risky_direct_rule)", dups)
	}
}
