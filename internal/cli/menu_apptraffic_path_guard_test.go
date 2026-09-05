package cli

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/appattr"
)

// 应用流量窗口的 path 是一条跨语言契约:Go 的 appattr.Path 常量 ↔ 菜单
// AppTrafficModel.swift 里 `enum AppTrafficPath: String { case tunnel, direct, blocked }`。
// Swift 对枚举的解码是**严格**的 —— Go 侧多发一个值,整份报告解不出来,窗口报
// 「App traffic is not available」,与 2026-09-05 那次 chunked 事故在用户眼里一模一样,
// 而这里没有任何编译错误会提醒改 Swift。双向钉住:每个 Go 值 Swift 都认,每个 Swift
// 值 Go 都发得出。读不到 Swift 文件响亮失败。
func TestMenuAppTrafficPathEnumMatchesGoPaths(t *testing.T) {
	src, err := os.ReadFile("../../apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift")
	if err != nil {
		t.Fatalf("读不到菜单的 AppTrafficModel.swift,守卫失去意义: %v", err)
	}
	m := regexp.MustCompile(`enum AppTrafficPath: String[^{]*\{\s*case ([a-z, ]+)`).FindSubmatch(src)
	if m == nil {
		t.Fatal("找不到 enum AppTrafficPath 的 case 列表 —— 守卫读不懂现在的 Swift 了")
	}
	swift := map[string]bool{}
	for _, c := range strings.Split(string(m[1]), ",") {
		swift[strings.TrimSpace(c)] = true
	}
	goPaths := map[string]bool{}
	for _, p := range appattr.OrderedPaths() {
		goPaths[string(p)] = true
	}
	for p := range goPaths {
		if !swift[p] {
			t.Errorf("Go 会发 path=%q,而菜单的 AppTrafficPath 不认它 —— 整份应用流量报告会解不出来", p)
		}
	}
	for c := range swift {
		if !goPaths[c] {
			t.Errorf("菜单认 path=%q,而 Go 从不发它 —— 死枚举值,先问是不是 Go 那边删了", c)
		}
	}
}
