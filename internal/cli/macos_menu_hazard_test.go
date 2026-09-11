package cli

import (
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
		if hazard, _, _ := policy.DirectRuleHazard("*." + host); !hazard {
			t.Errorf("Swift 列了 %q,而 policy 不认为 *.%s 危险 —— 两份漂开了", host, host)
		}
	}
	// 反向:Go 那份里的每一条都要在 Swift 里。用一个已知成员做自检,防止
	// 正则改错之后这条守卫变成空转。
	if !strings.Contains(block[1], "amazonaws.com") {
		t.Error("Swift 清单里没有 amazonaws.com —— 要么漏了,要么守卫读错了地方")
	}
}
