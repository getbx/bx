package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/policy"
)

// **两份清单必须逐字相同。** 判定权在 Guardian(policy.DirectRuleHazard),
// Swift 那份只决定右键要不要把某个候选摆出来;但两份一旦漂开,菜单会把一个
// Guardian 会拒绝的候选摆在一键的位置上,用户点下去只看到一句失败。
func TestOpenPlatformListMatchesPolicy(t *testing.T) {
	src := readMenuSwiftSource(t, "AppTrafficModel.swift")
	block := regexp.MustCompile(`(?s)let openSubdomainPlatforms: \[String\] = \[(.*?)\]`).FindStringSubmatch(src)
	if block == nil {
		t.Fatal("读不出 openSubdomainPlatforms —— 守卫已失效,先修守卫")
	}
	var swift []string
	for _, raw := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(block[1], -1) {
		swift = append(swift, raw[1])
	}
	if len(swift) == 0 {
		t.Fatal("Swift 那份清单是空的")
	}
	for _, host := range swift {
		// **两种写法都问一遍**:判据判的是覆盖面,`*.` 只是写法 —— 只问通配
		// 那一种,一次「只拦通配」的收窄不会让这条守卫红。
		if hazard, _, _ := policy.DirectRuleHazard("*." + host); !hazard {
			t.Errorf("Swift 列了 %q,而 policy 不认为 *.%s 危险 —— 两份漂开了", host, host)
		}
		if hazard, _, _ := policy.DirectRuleHazard(host); !hazard {
			t.Errorf("Swift 列了 %q,而 policy 不认为裸写的 %s 危险 —— 判据又被收窄了", host, host)
		}
	}

	// 反向:不能只查一个已知成员就当作「Go 那份都在 Swift 里」——那样删掉 Swift
	// 清单里除了自检成员以外的任何一条都不会被抓到。改成真正的双向集合相等:
	// 从 policy.go 的**源码文本**里抽出 riskyDirectDomains 的字面量清单(与本文件读
	// Swift 源码用的是同一手法),与 Swift 那份逐项比对,两侧各自点名报错。
	goList := readOpenSubdomainPlatformsFromPolicySource(t)

	// 用一个已知成员做自检,防止正则改错之后这条守卫变成空转(两侧「抽出来的
	// 条数 > 0」的前置断言已经在上面/下面各自做了,这里再加一道具体成员的锚点)。
	if !strings.Contains(block[1], "amazonaws.com") {
		t.Error("Swift 清单里没有 amazonaws.com —— 要么漏了,要么守卫读错了地方")
	}

	swiftSet := make(map[string]bool, len(swift))
	for _, host := range swift {
		swiftSet[host] = true
	}
	goSet := make(map[string]bool, len(goList))
	for _, host := range goList {
		goSet[host] = true
	}
	for _, host := range goList {
		if !swiftSet[host] {
			t.Errorf("policy.riskyDirectDomains 里有 %q,而 Swift 的 openSubdomainPlatforms 里没有 —— 两份漂开了", host)
		}
	}
	for _, host := range swift {
		if !goSet[host] {
			t.Errorf("Swift 的 openSubdomainPlatforms 里有 %q,而 policy.riskyDirectDomains 里没有 —— 两份漂开了", host)
		}
	}
}

// readOpenSubdomainPlatformsFromPolicySource 从 internal/policy/policy.go 的
// 源码文本里抽出 riskyDirectDomains 的字面量清单。读不出来必须 t.Fatal —— 正则没
// 匹配到时若当成空集合,「两侧集合相等」的比较会静默通过,而那正是这条守卫
// 要挡住的失效形状。
func readOpenSubdomainPlatformsFromPolicySource(t *testing.T) []string {
	t.Helper()
	path := filepath.Join("..", "policy", "policy.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 守卫已经失效,先修守卫", path, err)
	}
	block := regexp.MustCompile(`(?s)var riskyDirectDomains = \[\]string\{(.*?)\}`).FindStringSubmatch(string(src))
	if block == nil {
		t.Fatal("读不出 policy.go 里的 riskyDirectDomains —— 守卫已失效,先修守卫")
	}
	var out []string
	for _, raw := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(block[1], -1) {
		out = append(out, raw[1])
	}
	if len(out) == 0 {
		t.Fatal("policy.go 里的 riskyDirectDomains 清单是空的 —— 守卫读错了地方")
	}
	return out
}
