package rulereview

import "testing"

// isNetworkLiteral 必须认出 CIDR 与裸 IP(v4/v6 两种形状),而普通域名——包括
// 带 `*.` 前缀、带端口感的写法——不能被误判成网络字面量。
func TestIsNetworkLiteral(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"10.0.0.0/8", true},
		{"192.168.1.1", true},
		{"2001:db8::/32", true},
		{"::1", true},
		{"myqcloud.com", false},
		{"*.myqcloud.com", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isNetworkLiteral(c.in); got != c.want {
			t.Errorf("isNetworkLiteral(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// normalizeRule 只用于比对,绝不用于呈现:小写、去空白、去 `*.` 前缀、去尾点。
func TestNormalizeRule(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"*.MyQcloud.com", "myqcloud.com"},
		{"  qq.com  ", "qq.com"},
		{"Torchfun.com.", "torchfun.com"},
		{"*.a.b.", "a.b"},
	}
	for _, c := range cases {
		if got := normalizeRule(c.in); got != c.want {
			t.Errorf("normalizeRule(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// domainRules 必须:跳过网络字面量、跳过空白项、按归一化形式去重并保留第一条原文。
func TestDomainRulesFiltersAndDedupes(t *testing.T) {
	got := domainRules([]string{"*.myqcloud.com", "MYQCLOUD.COM", "  ", "10.0.0.0/8", "*.qq.com"})
	want := []domainRule{
		{raw: "*.myqcloud.com", norm: "myqcloud.com"},
		{raw: "*.qq.com", norm: "qq.com"},
	}
	if len(got) != len(want) {
		t.Fatalf("domainRules = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("domainRules[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}
