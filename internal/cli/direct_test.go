package cli

import (
	"strings"
	"testing"
)

// 通配 + 开放平台:仍然拦。
func TestDirectRuleRiskFlagsOpenCloud(t *testing.T) {
	for _, d := range []string{"*.oss-cn-hangzhou.aliyuncs.com", "*.s3.amazonaws.com", "*.github.io"} {
		msg := directRuleRisk(d)
		if msg == "" {
			t.Fatalf("%s 应当被拦下", d)
		}
		// **提示必须给出路**:只说危险不说怎么改,用户唯一的出路是 --force,
		// 那等于没有这道门。
		if !strings.Contains(strings.ToLower(msg), "exact") {
			t.Errorf("%s 的提示没给「改成确切主机」这条路:%s", d, msg)
		}
	}
}

// 品牌自控域、以及**开放平台上的确切主机**:不拦。
//
// 后者是 2026-09-11 的收窄:攻击者注册不到 mybucket.s3.amazonaws.com,
// 所以拦它只是在把人逼去用 --force。
func TestDirectRuleRiskSilentOnBrandDomains(t *testing.T) {
	for _, d := range []string{"*.apple.com", "*.qq.com", "taobao.com", "mybucket.s3.amazonaws.com", "amazonaws.com"} {
		if msg := directRuleRisk(d); msg != "" {
			t.Fatalf("%s 不该被拦:%s", d, msg)
		}
	}
}

func TestEditYAMLRuleListAddCreatesBlock(t *testing.T) {
	in := "server: vless://x@h:443?security=reality\nkillswitch: true\n"
	out, changed := editYAMLRuleList([]byte(in), "direct", []string{"taobao.com"}, nil)
	if !changed {
		t.Fatal("新增域名应 changed=true")
	}
	if !strings.Contains(string(out), "taobao.com") {
		t.Fatalf("add 后应含 taobao.com:\n%s", out)
	}
	if !strings.Contains(string(out), "server:") || !strings.Contains(string(out), "killswitch") {
		t.Fatalf("其它段应保留:\n%s", out)
	}
}

func TestEditYAMLRuleListAddIsIdempotent(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n"
	out, changed := editYAMLRuleList([]byte(in), "direct", []string{"taobao.com"}, nil)
	if changed {
		t.Fatal("已存在域名再 add 应 changed=false(无改动)")
	}
	if n := strings.Count(string(out), "taobao.com"); n != 1 {
		t.Fatalf("重复 add 应幂等,taobao.com 出现 %d 次:\n%s", n, out)
	}
}

func TestEditYAMLRuleListRemove(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n      - jd.com\n"
	out, changed := editYAMLRuleList([]byte(in), "direct", nil, []string{"taobao.com"})
	if !changed {
		t.Fatal("删除存在的域名应 changed=true")
	}
	if strings.Contains(string(out), "taobao.com") {
		t.Fatalf("remove 后不应含 taobao.com:\n%s", out)
	}
	if !strings.Contains(string(out), "jd.com") {
		t.Fatalf("remove 只删指定项,jd.com 应保留:\n%s", out)
	}
}

// 回归:域名在 rules[1](多 rule 块布局)时,rm 必须跨所有元素删,否则报删了其实还在直连(泄漏)。
func TestEditYAMLRuleListRemoveAcrossAllRules(t *testing.T) {
	in := "rules:\n  - proxy:\n      - ads.cn\n  - direct:\n      - leak.aliyuncs.com\n"
	out, changed := editYAMLRuleList([]byte(in), "direct", nil, []string{"leak.aliyuncs.com"})
	if !changed {
		t.Fatal("rules[1] 里的域名也应被删到(changed=true)")
	}
	if strings.Contains(string(out), "leak.aliyuncs.com") {
		t.Fatalf("rules[1].direct 的域名应被删除:\n%s", out)
	}
}

func TestEditYAMLRuleListRemoveAbsentIsNoChange(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n"
	_, changed := editYAMLRuleList([]byte(in), "direct", nil, []string{"notthere.com"})
	if changed {
		t.Fatal("删不存在的域名应 changed=false(不误报成功)")
	}
}

func TestEditYAMLRuleListProxyField(t *testing.T) {
	out, changed := editYAMLRuleList([]byte("server: x\n"), "proxy", []string{"ads.cn"}, nil)
	if !changed {
		t.Fatal("proxy 字段 add 应 changed=true")
	}
	if !strings.Contains(string(out), "proxy") || !strings.Contains(string(out), "ads.cn") {
		t.Fatalf("proxy 字段 add 应生效:\n%s", out)
	}
}

func TestEditYAMLRuleListProxyAddRemovesConflictingDirect(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n      - jd.com\n"
	out, changed := editYAMLRuleList([]byte(in), "proxy", []string{"taobao.com"}, nil)
	if !changed {
		t.Fatal("proxy add 应 changed=true")
	}
	if strings.Contains(string(out), "direct:\n        - taobao.com") ||
		strings.Contains(string(out), "direct:\n      - taobao.com") {
		t.Fatalf("proxy add 应从 direct 移除冲突域名:\n%s", out)
	}
	if !strings.Contains(string(out), "jd.com") || !strings.Contains(string(out), "proxy:") || !strings.Contains(string(out), "taobao.com") {
		t.Fatalf("proxy add 应保留其它 direct 并加入 proxy:\n%s", out)
	}
}

func TestEditYAMLRuleListDirectAddRemovesConflictingProxyAcrossRules(t *testing.T) {
	in := "rules:\n  - proxy:\n      - taobao.com\n      - ads.cn\n  - direct:\n      - bilibili.com\n"
	out, changed := editYAMLRuleList([]byte(in), "direct", []string{"taobao.com"}, nil)
	if !changed {
		t.Fatal("direct add 应 changed=true")
	}
	if strings.Contains(string(out), "proxy:\n        - taobao.com") ||
		strings.Contains(string(out), "proxy:\n      - taobao.com") {
		t.Fatalf("direct add 应从 proxy 移除冲突域名:\n%s", out)
	}
	if !strings.Contains(string(out), "ads.cn") || !strings.Contains(string(out), "direct:") || !strings.Contains(string(out), "taobao.com") {
		t.Fatalf("direct add 应保留其它 proxy 并加入 direct:\n%s", out)
	}
	if strings.Contains(string(out), "proxy: []") {
		t.Fatalf("冲突项移除后不应留下空 proxy 字段:\n%s", out)
	}
}

func TestEditYAMLRuleListConflictRemovalDropsEmptyOppositeField(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n  - proxy:\n      - ads.cn\n"
	out, changed := editYAMLRuleList([]byte(in), "direct", []string{"ads.cn"}, nil)
	if !changed {
		t.Fatal("direct add 移除唯一 proxy 冲突项应 changed=true")
	}
	if strings.Contains(string(out), "proxy: []") || strings.Contains(string(out), "proxy:\n") {
		t.Fatalf("冲突项移除后不应留下空 proxy 字段:\n%s", out)
	}
	if strings.Contains(string(out), "- {}") {
		t.Fatalf("冲突项移除后不应留下空 rule 块:\n%s", out)
	}
}

func TestEditYAMLRuleListAddRepairsExistingConflictWhenTargetAlreadyExists(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n  - proxy:\n      - taobao.com\n"
	out, changed := editYAMLRuleList([]byte(in), "proxy", []string{"taobao.com"}, nil)
	if !changed {
		t.Fatal("目标字段已存在但 opposite 有冲突时,add 应修复冲突并 changed=true")
	}
	if strings.Contains(string(out), "direct:") {
		t.Fatalf("proxy add 应清理已存在的 direct 冲突:\n%s", out)
	}
	if !strings.Contains(string(out), "proxy:") || !strings.Contains(string(out), "taobao.com") {
		t.Fatalf("proxy 目标规则应保留:\n%s", out)
	}
}
