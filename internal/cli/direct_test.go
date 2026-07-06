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
	out := string(editYAMLRuleList([]byte(in), "direct", []string{"taobao.com"}, nil))
	if !strings.Contains(out, "taobao.com") {
		t.Fatalf("add 后应含 taobao.com:\n%s", out)
	}
	if !strings.Contains(out, "server:") || !strings.Contains(out, "killswitch") {
		t.Fatalf("其它段应保留:\n%s", out)
	}
}

func TestEditYAMLRuleListAddIsIdempotent(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n"
	out := string(editYAMLRuleList([]byte(in), "direct", []string{"taobao.com"}, nil))
	if n := strings.Count(out, "taobao.com"); n != 1 {
		t.Fatalf("重复 add 应幂等,taobao.com 出现 %d 次:\n%s", n, out)
	}
}

func TestEditYAMLRuleListRemove(t *testing.T) {
	in := "rules:\n  - direct:\n      - taobao.com\n      - jd.com\n"
	out := string(editYAMLRuleList([]byte(in), "direct", nil, []string{"taobao.com"}))
	if strings.Contains(out, "taobao.com") {
		t.Fatalf("remove 后不应含 taobao.com:\n%s", out)
	}
	if !strings.Contains(out, "jd.com") {
		t.Fatalf("remove 只删指定项,jd.com 应保留:\n%s", out)
	}
}

func TestEditYAMLRuleListProxyField(t *testing.T) {
	out := string(editYAMLRuleList([]byte("server: x\n"), "proxy", []string{"ads.cn"}, nil))
	if !strings.Contains(out, "proxy") || !strings.Contains(out, "ads.cn") {
		t.Fatalf("proxy 字段 add 应生效:\n%s", out)
	}
}
