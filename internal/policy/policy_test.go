package policy

import (
	"strings"
	"testing"
)

func TestApplyMakesModesExclusive(t *testing.T) {
	in := []byte("server: brook://x\nrules:\n  - direct: [example.com]\n")
	out, changed, err := Apply(in, Request{Mode: "proxy", Add: []string{"example.com"}})
	if err != nil || !changed {
		t.Fatalf("Apply changed=%v err=%v", changed, err)
	}
	text := string(out)
	if !strings.Contains(text, "proxy:") || strings.Contains(text, "direct:") {
		t.Fatalf("modes not exclusive:\n%s", text)
	}
}

func TestApplyRejectsRiskyDirect(t *testing.T) {
	_, _, err := Apply([]byte("server: brook://x\n"), Request{Mode: "direct", Add: []string{"evil.workers.dev"}})
	if err == nil || !strings.Contains(err.Error(), "allow_risk") {
		t.Fatalf("err=%v", err)
	}
}

func TestApplyRemoveDoesNotLeaveEmptyPolicy(t *testing.T) {
	in := []byte("server: brook://x\nrules:\n  - direct: [example.com]\n")
	out, changed, err := Apply(in, Request{Mode: "direct", Remove: []string{"example.com"}})
	if err != nil || !changed {
		t.Fatalf("Apply changed=%v err=%v", changed, err)
	}
	if strings.Contains(string(out), "direct:") || strings.Contains(string(out), "- {}") {
		t.Fatalf("remove left empty policy:\n%s", out)
	}
}

// **危险的是「在任何人都能注册子域的平台上用通配符」,不是平台本身。**
//
// `*.s3.amazonaws.com`:攻击者注册 evil.s3.amazonaws.com 即命中你的白名单,
// 于是他能把你的真实 IP 钓出来。
// `mybucket.s3.amazonaws.com`:那个确切主机他拿不到,直连只对这一个主机暴露,
// 是一次窄而明确的选择。
//
// 旧的 DirectRisk 两者一律拦 —— 不是因为判据写错,而是因为它判的是「域名落在
// 哪个平台」。过宽的门会把人逼去用 --force,那正是门死掉的方式。
func TestDirectRuleHazardOnlyFlagsWildcardsOnOpenPlatforms(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		hazard  bool
		why     string
	}{
		{"*.s3.amazonaws.com", true, "开放平台 + 通配"},
		{"*.amazonaws.com", true, "开放平台顶级域 + 通配"},
		{"*.OSS-CN-SH.ALIYUNCS.COM", true, "大小写不敏感"},
		{"*.myqcloud.com.", true, "尾点要归一"},
		{"  *.github.io  ", true, "两边空白要去掉"},
		{"mybucket.s3.amazonaws.com", false, "确切主机:攻击者注册不到它"},
		{"amazonaws.com", false, "确切域名,没有通配"},
		{"*.apple.com", false, "品牌自控域,子域拿不到"},
		{"*.qq.com", false, "同上"},
		{"192.0.2.1", false, "IP 字面量与通配无关"},
		{"", false, "空串不判危险,交给既有的模式校验去报错"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			hazard, reason, suggestion := DirectRuleHazard(tc.pattern)
			if hazard != tc.hazard {
				t.Fatalf("hazard = %t, want %t(%s)", hazard, tc.hazard, tc.why)
			}
			if !tc.hazard {
				if reason != "" || suggestion != "" {
					t.Fatalf("不危险时不该有话说:reason=%q suggestion=%q", reason, suggestion)
				}
				return
			}
			// 说明必须回答两个问题:为什么危险、该怎么改。只说「危险」而不说
			// 怎么办,用户唯一的出路就是 --force,那等于没有这道门。
			if reason == "" || suggestion == "" {
				t.Fatalf("危险时 reason 与 suggestion 都要有:reason=%q suggestion=%q", reason, suggestion)
			}
			for _, w := range []string{"subdomain"} {
				if !containsFold(reason, w) {
					t.Errorf("reason 里没说清风险来自子域可被他人注册:%q", reason)
				}
			}
			if !containsFold(suggestion, "exact") {
				t.Errorf("suggestion 里没给出「改成确切主机」这条路:%q", suggestion)
			}
		})
	}
}

func containsFold(s, sub string) bool {
	return len(s) >= len(sub) && stringsContainsFold(s, sub)
}

func stringsContainsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}
