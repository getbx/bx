package policy

import (
	"strings"
	"testing"

	"github.com/getbx/bx/internal/route"
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

// **危险的判据是「这条规则覆盖到哪里」,不是「它写成什么样」。**
//
// bx 的匹配器是后缀集(route.NewDomainSet 去掉 `*.` 存后缀,Match 逐级往父域
// 找),所以配置里**没有「确切主机规则」这种东西**:`bucket.s3.amazonaws.com`
// 与 `*.bucket.s3.amazonaws.com` 覆盖的子树完全一样,`evil.bucket.s3.amazonaws.com`
// 两种写法都命中。2026-09-11 曾把判据收窄成「只拦带 `*.` 的」,那等于在裸写的
// 形式上完全不设防 —— 已撤回,好用改由 --force / Add Anyway 这条逃生口买单。
func TestDirectRuleHazardJudgesCoverageNotSpelling(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		hazard  bool
		why     string
	}{
		{"*.s3.amazonaws.com", true, "开放平台 + 通配"},
		{"*.amazonaws.com", true, "开放平台顶级域 + 通配"},
		{"mybucket.s3.amazonaws.com", true, "裸写的深层主机照样覆盖它自己的子域"},
		{"amazonaws.com", true, "裸写的平台顶级域"},
		{"*.OSS-CN-SH.ALIYUNCS.COM", true, "大小写不敏感"},
		{"*.myqcloud.com.", true, "尾点要归一"},
		{"  *.github.io  ", true, "两边空白要去掉"},
		{"*.apple.com", false, "品牌自控域,子域拿不到"},
		{"*.qq.com", false, "同上"},
		{"bucket.apple.com", false, "品牌自控域上的深层主机"},
		{"192.0.2.1", false, "IP 字面量不在任何平台清单里"},
		{"", false, "空串不判危险,交给既有的模式校验去报错"},
		{"*.", false, "只有通配前缀,去掉之后是空串"},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			hazard, reason, suggestion := DirectRuleHazard(tc.pattern)
			if hazard != tc.hazard {
				t.Fatalf("hazard = %t, want %t(%s)", hazard, tc.hazard, tc.why)
			}
			// DirectRisk 是它的薄壳,两者永不许分头判。
			if got := DirectRisk(tc.pattern); got != tc.hazard {
				t.Fatalf("DirectRisk = %t, want %t —— 薄壳与判据漂开了", got, tc.hazard)
			}
			if !tc.hazard {
				if reason != "" || suggestion != "" {
					t.Fatalf("不危险时不该有话说:reason=%q suggestion=%q", reason, suggestion)
				}
				return
			}
			// 说明必须回答两个问题:为什么危险、该怎么办。只说「危险」而不给
			// 出路,用户唯一的去处就是 --force,那等于没有这道门。
			if reason == "" || suggestion == "" {
				t.Fatalf("危险时 reason 与 suggestion 都要有:reason=%q suggestion=%q", reason, suggestion)
			}
			if !containsFold(reason, "subdomain") {
				t.Errorf("reason 里没说清风险来自子域可被他人注册:%q", reason)
			}
			// **出路必须是真出路。** 收窄到确切主机并不能解除这道门(规则仍然
			// 覆盖它的子域),所以说明里必须提到那个真的能过去的东西:--force。
			if !containsFold(suggestion, "--force") {
				t.Errorf("suggestion 里没给出真正过得去的那条路(--force):%q", suggestion)
			}
			if !containsFold(suggestion, "exact") {
				t.Errorf("suggestion 里没说「写窄一点」这件事:%q", suggestion)
			}
		})
	}
}

// **这条钉住的是 2026-09-11 那个 Critical 本身。**
//
// 清单里每一条**裸写**的域名都必须被判危险,而且 bx 的匹配器对它真的会放行
// 一个陌生人注册的子域 —— 两半都断言,因为只钉通配形式等于在钉缺陷旁边的东西:
// 收窄那一版下 `amazonaws.com` 判 false,而 route.NewDomainSet 照样让
// evil.amazonaws.com 直连出去。
func TestEveryOpenPlatformIsHazardousWrittenBareAndReallyCoversStrangers(t *testing.T) {
	if len(riskyDirectDomains) == 0 {
		t.Fatal("平台清单是空的 —— 守卫读错了地方")
	}
	for _, entry := range riskyDirectDomains {
		t.Run(entry, func(t *testing.T) {
			if hazard, _, _ := DirectRuleHazard(entry); !hazard {
				t.Errorf("%q 裸写时没被判危险", entry)
			}
			// 匹配器那一半:这条规则真的会把一个陌生人的子域放成直连。
			stranger := "evil." + entry
			if !route.NewDomainSet([]string{entry}).Match(stranger) {
				t.Fatalf("route.NewDomainSet([%q]) 不匹配 %q —— 这条守卫的前提变了,先核匹配器",
					entry, stranger)
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
