package leakserve

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// **页面外壳的每一句英文都有中文**(2026-09-28,菜单切中文后这一页也要中文)。
// JS 里经 tr("…") 的、HTML 里带 data-i18n="…" 的,都必须在 ZH 那份 JSON 字典里;
// 字典里不许有陈旧条目。结论句不在这里 —— 它们由 Go 按 lang 翻(leakcheck.Localize)。
func TestPageShellStringsAllHaveChinese(t *testing.T) {
	raw, err := os.ReadFile("page.html")
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	start := strings.Index(page, "var ZH = {")
	end := strings.Index(page[start:], "\n};")
	if start < 0 || end < 0 {
		t.Fatal("找不到 ZH 字典 —— 守卫读不懂页面了")
	}
	var zh map[string]string
	if err := json.Unmarshal([]byte(page[start+len("var ZH = "):start+end+2]), &zh); err != nil {
		t.Fatalf("ZH 不是合法 JSON:%v", err)
	}
	used := map[string]bool{}
	for _, m := range regexp.MustCompile(`\btr\("((?:[^"\\]|\\.)*)"\)`).FindAllStringSubmatch(page, -1) {
		used[jsUnescape(m[1])] = true
	}
	for _, m := range regexp.MustCompile(`data-i18n="([^"]*)"`).FindAllStringSubmatch(page, -1) {
		used[m[1]] = true
	}
	// tr(n === 1 ? one : many) 与 tr(f.verdict) 这类经变量的:把可能的值列在这里。
	for _, k := range []string{
		"leak", "leaks", "identifying trait", "identifying traits", "ok", "bad", "info", "not checked",
		"Where your traffic goes", "What bx, or whichever tunnel is carrying this machine, is answerable for.",
		"Can you be singled out", "Mostly not bx's to fix — browser and system traits that can identify you even when nothing leaks.",
		"What sites can read", "Neither good nor bad, and nothing here is a verdict. Listed so you can see what every site gets without asking.",
		"Can this path reach the AI services", "bx contacted each of these from this machine to see whether this path gets through. “Could not reach” is not a leak.",
		"bx leak check",
	} {
		used[k] = true
	}
	if len(used) < 20 {
		t.Fatalf("只找到 %d 句,守卫读不懂 tr(…) / data-i18n 的写法了", len(used))
	}
	for k := range used {
		if _, ok := zh[k]; !ok {
			t.Errorf("没有中文:%q", k)
		}
	}
	for k := range zh {
		if !used[k] {
			t.Errorf("ZH 里的 %q 已经没人用 —— 删掉它,或者是英文原句改了而字典没跟上", k)
		}
	}
	// 章节标题那八句也在 titles 表里,拼写要与字典一致(它们经 tr(t[0]) 走,守卫看不见字面量)。
	for _, k := range []string{"Where your traffic goes", "Can you be singled out", "Can this path reach the AI services"} {
		if !strings.Contains(page, `"`+k+`"`) {
			t.Errorf("titles 表里找不到 %q —— 字典与章节标题漂了", k)
		}
	}
}

func jsUnescape(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`, `\n`, "\n").Replace(s)
}
