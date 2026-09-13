package tunnel

import (
	"slices"
	"testing"
)

// TestKind 锁定「scheme → 引擎」的唯一真相源。每加一种传输都要在此登记,
// 防 supervisor/setup/blink 各处派发发散(曾有 ss 加了但 setup 探测仍当 brook 的回归)。
func TestKind(t *testing.T) {
	cases := map[string]string{
		"vless://uid@1.2.3.4:443?security=reality": "reality",
		"hysteria2://pw@1.2.3.4:8443?sni=bing.com": "hysteria2",
		"hy2://pw@h:443":                           "hysteria2",
		"trojan://pw@1.2.3.4:443?sni=bing.com":     "trojan",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388#hk": "shadowsocks",
		"vmess://eyJhZGQiOiIxLjIuMy40In0":          "vmess",
		"brook://server?server=1.2.3.4%3A9999":     "brook",
		"anything-else":                            "brook",
	}
	for link, want := range cases {
		if got := Kind(link); got != want {
			t.Errorf("Kind(%q)=%q want %q", link, got, want)
		}
	}
}

// Kinds() 必须真的覆盖 Kind() 的值域 —— 两者都从 schemeKinds 派生,这条钉的是
// **那个派生没被拆开**(比如有人给 Kind 加回一个 switch 分支)。下游那些「每一种
// 传输都要登记一句什么」的表全靠 Kinds() 穷举,漏一种就是一个静默的错答案。
func TestKindsCoversEveryValueKindCanReturn(t *testing.T) {
	kinds := Kinds()
	if len(kinds) == 0 {
		t.Fatal("Kinds() 是空的 —— 拿它穷举的那些守卫此刻一条都不守")
	}
	for _, entry := range schemeKinds {
		got := Kind(entry.prefix + "whatever")
		if !slices.Contains(kinds, got) {
			t.Fatalf("Kind(%q…) = %q,而 Kinds() = %v 里没有它", entry.prefix, got, kinds)
		}
	}
	if got := Kind("anything-else"); !slices.Contains(kinds, got) {
		t.Fatalf("兜底的 Kind(乱串) = %q 不在 Kinds() = %v 里", got, kinds)
	}
	// 每次调用返回新切片:调用方排序/追加不许污染下一个人。
	first, second := Kinds(), Kinds()
	slices.Sort(first)
	if !slices.Equal(second, Kinds()) {
		t.Fatal("排过一次序之后 Kinds() 变了 —— 它交出去的是同一条底层切片")
	}
}

// TestIsClientLink 锁定「裸客户端链接」识别口径(六种 scheme),供 cli/blink 各处单一化。
// bx:// / blink:// 是换壳链接(由 blink 解壳),非裸链接;乱串也不是。
func TestIsClientLink(t *testing.T) {
	yes := []string{
		"vless://uid@1.2.3.4:443?security=reality",
		"hysteria2://pw@h:443", "hy2://pw@h:443",
		"trojan://pw@1.2.3.4:443",
		"ss://YWVzLTI1Ni1nY206cHc@1.2.3.4:8388",
		"vmess://eyJhZGQiOiIxLjIuMy40In0",
		"brook://server?server=1.2.3.4%3A9999",
	}
	no := []string{
		"bx://abc", "blink://abc", "anything-else", "", "http://x",
	}
	for _, l := range yes {
		if !IsClientLink(l) {
			t.Errorf("IsClientLink(%q)=false want true", l)
		}
	}
	for _, l := range no {
		if IsClientLink(l) {
			t.Errorf("IsClientLink(%q)=true want false", l)
		}
	}
}
