package leakcheck

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// **结论句的中文与英文原句一一对应,英文原句就是 key**(与菜单的 L 表同一条纪律)。
// 每一处 `say("…", …)` / `sayf` 与骨架里的 Title 都要有译文;译文里的 %s 数目必须与原句
// 相同(少一个就是一句丢了值的话);表里不许有陈旧条目。JSON(`bx leak-check`)保持英文:
// Localize 只在页面那条路上调。
func TestEveryLeakSentenceHasAChineseTranslation(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	keyRe := regexp.MustCompile(`(?s)\b(?:say|also|tail)\(\s*"((?:[^"\\]|\\.)*)"`)
	titleRe := regexp.MustCompile(`Title:\s*"((?:[^"\\]|\\.)*)"`)
	used := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || name == "i18n.go" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range keyRe.FindAllStringSubmatch(string(raw), -1) {
			used[unescape(m[1])] = true
		}
		for _, m := range titleRe.FindAllStringSubmatch(string(raw), -1) {
			used[unescape(m[1])] = true
		}
	}
	if len(used) < 40 {
		t.Fatalf("只找到 %d 句 —— 守卫读不懂 say(…) / Title: 的写法了", len(used))
	}
	for key := range used {
		zh, ok := zhHans[key]
		if !ok {
			t.Errorf("没有中文译文:%q", key)
			continue
		}
		if a, b := strings.Count(key, "%s"), strings.Count(zh, "%s"); a != b {
			t.Errorf("占位符数目不同(英 %d / 中 %d):%q", a, b, key)
		}
	}
	for key := range zhHans {
		if !used[key] {
			t.Errorf("词表里的 %q 已经没有 say/Title 在用 —— 删掉它,或者是英文原句改了而词表没跟上", key)
		}
	}
}

func unescape(s string) string {
	return strings.NewReplacer(`\"`, `"`, `\\`, `\`).Replace(s)
}

// Localize 按语言重渲染每条结论的标题与句子;英文原样,认不出的语言也原样(不猜)。
// 证据行是数据,不翻。
func TestLocalizeRendersChineseAndLeavesEnglishAlone(t *testing.T) {
	f := Finding{ID: FindingCarrier, Title: "Who carries your traffic"}
	f.say("Your public traffic is carried by bx (%s).", "utun7")
	if f.Summary != "Your public traffic is carried by bx (utun7)." {
		t.Fatalf("say must render English by default, got %q", f.Summary)
	}
	rep := Report{Findings: []Finding{f}}
	zh := Localize(rep, LangZHHans)
	if zh.Findings[0].Summary != "你的公网流量正经由 bx(utun7)。" || zh.Findings[0].Title != "谁在承载你的流量" {
		t.Fatalf("zh-Hans = %q / %q", zh.Findings[0].Title, zh.Findings[0].Summary)
	}
	if en := Localize(rep, LangEN); en.Findings[0].Summary != f.Summary || en.Findings[0].Title != f.Title {
		t.Fatalf("en must be untouched, got %+v", en.Findings[0])
	}
	if other := Localize(rep, Lang("fr")); other.Findings[0].Summary != f.Summary {
		t.Fatalf("an unknown language must fall back to English, got %+v", other.Findings[0])
	}
	if rep.Findings[0].Summary != f.Summary {
		t.Fatal("Localize must not mutate its input")
	}
}

// ParseLang 只认得出的两个;别的一律英文 —— 页面把 navigator.language 原样递过来,
// "zh-CN"/"zh-Hans-CN"/"zh" 都要落到简体中文。
func TestParseLangFoldsBrowserTagsToTheTwoWeHave(t *testing.T) {
	for in, want := range map[string]Lang{"": LangEN, "en-US": LangEN, "zh-CN": LangZHHans, "zh-Hans": LangZHHans, "zh-Hans-CN": LangZHHans, "zh": LangZHHans, "zh-TW": LangEN, "fr": LangEN} {
		if got := ParseLang(in); got != want {
			t.Errorf("ParseLang(%q) = %q, want %q", in, got, want)
		}
	}
}
