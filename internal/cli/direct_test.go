package cli

import (
	"strings"
	"testing"
)

func TestDirectRuleRiskFlagsOpenCloud(t *testing.T) {
	risky := []string{
		"aliyuncs.com",                 // 阿里 OSS/ECS,任何人开桶
		"oss-cn-hangzhou.aliyuncs.com", // 子域也算
		"myfile.oss-cn-beijing.aliyuncs.com",
		"myqcloud.com", // 腾讯 COS
		"bcebos.com",   // 百度 BOS
		"qiniucdn.com", "qbox.me", "clouddn.com",
		"upaiyun.com",
		"myhuaweicloud.com",
		"s3.amazonaws.com", "amazonaws.com",
		"d123.cloudfront.net",
		"foo.blob.core.windows.net",
		"storage.googleapis.com",
		"bucket.r2.dev", "app.workers.dev", "site.pages.dev",
		"user.github.io", "app.vercel.app", "site.netlify.app",
		"cdn.b-cdn.net",
	}
	for _, d := range risky {
		if directRuleRisk(d) == "" {
			t.Errorf("directRuleRisk(%q) 应给出风险提示(公有云/开放子域),却为空", d)
		}
	}
}

func TestDirectRuleRiskSilentOnBrandDomains(t *testing.T) {
	safe := []string{
		"taobao.com", "baidu.com", "bilibili.com", "qq.com",
		"icbc.com.cn", "gov.cn", "BAIDU.COM", "www.baidu.com",
		"alipay.com", "hdslb.com",
	}
	for _, d := range safe {
		if w := directRuleRisk(d); w != "" {
			t.Errorf("directRuleRisk(%q) 品牌自控域不应提示,却得 %q", d, w)
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
