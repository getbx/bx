package cli

import "testing"

// `bx leakcheck --lang zh-Hans` 把语言挂到打开的 URL 上(`&lang=`),页面与 /report 按它翻;
// 没传就不挂,页面按浏览器语言自己定。菜单切成中文时正是经这条旗标把语言传过来的。
func TestLeakcheckPageURLCarriesTheLanguageOnlyWhenGiven(t *testing.T) {
	base := "http://127.0.0.1:5555/?t=abc"
	if got := leakcheckPageURL(base, ""); got != base {
		t.Fatalf("no --lang must leave the URL alone, got %q", got)
	}
	if got := leakcheckPageURL(base, "zh-Hans"); got != base+"&lang=zh-Hans" {
		t.Fatalf("--lang must be appended, got %q", got)
	}
	if got := leakcheckPageURL(base, "zh CN"); got != base+"&lang=zh+CN" {
		t.Fatalf("the value must be URL-escaped, got %q", got)
	}
}
