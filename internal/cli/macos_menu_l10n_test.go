package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// —— 菜单的界面语言(2026-09-26)——
//
// 源码里每一句用户看得见的话写英文原句、包在 `L("…")` 里,原句就是查表的 key;
// 简体中文词表是 `Localization_zhHans.swift` 里的一个字典字面量。
//
// 这一对之间的漂移**完全静默**:漏翻的那句退回英文,界面上中英混杂而没有任何东西
// 报错;改了英文原句而没改词表,那句话的中文就悄悄没了;词表里多一条没人用的,
// 它只是占地方,但它会让下一个人以为那句话还在界面上。
//
// 所以三件事双向钉住:每个 `L("…")` 都有译文、每条译文都还有人用、两边的
// `{0}` 占位符一一对应(译文少一个占位符,那个值就从中文界面上消失了)。

const (
	menuSourcesDir  = "../../apps/macos/BxMenu/Sources/BxMenu"
	zhHansTableFile = "Localization_zhHans.swift"
)

var (
	swiftStringEntry = regexp.MustCompile(`^\s*"((?:[^"\\]|\\.)*)"\s*:\s*"((?:[^"\\]|\\.)*)",?\s*$`)
	lCallLiteral     = regexp.MustCompile(`\bL\("((?:[^"\\]|\\.)*)"`)
	lCallAny         = regexp.MustCompile(`\bL\(`)
	placeholder      = regexp.MustCompile(`\{\d+\}`)
)

// readZhHansTable 读词表。**格式钉死成一行一条**:认不出的非注释行一律报错,
// 否则一条写成两行的译文会被这里安静地跳过,然后被判成「没有译文」或「没人用」
// 的反面 —— 守卫读不懂的东西必须说出来。
func readZhHansTable(t *testing.T) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(menuSourcesDir, zhHansTableFile))
	if err != nil {
		t.Fatalf("读不到词表: %v", err)
	}
	table := map[string]string{}
	inLiteral := false
	for i, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "let zhHansTranslations"):
			inLiteral = true
			continue
		case !inLiteral || trimmed == "" || strings.HasPrefix(trimmed, "//"):
			continue
		case trimmed == "]":
			inLiteral = false
			continue
		}
		m := swiftStringEntry.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("%s:%d 认不出这一行(词表必须一行一条 \"英文\": \"中文\",):%s",
				zhHansTableFile, i+1, trimmed)
		}
		if _, dup := table[m[1]]; dup {
			// Swift 的字典字面量遇到重复 key 会在**运行时**崩,不是编译错误。
			t.Errorf("%s:%d 重复的 key %q —— 菜单一启动就会崩", zhHansTableFile, i+1, m[1])
		}
		table[m[1]] = m[2]
	}
	if len(table) == 0 {
		t.Fatal("词表一条都没读到 —— 守卫读不懂它了")
	}
	return table
}

// menuLKeys 扫 Sources 里全部 `L("…")`,返回 key → 出现位置。
func menuLKeys(t *testing.T) map[string][]string {
	t.Helper()
	entries, err := os.ReadDir(menuSourcesDir)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string][]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".swift") || name == zhHansTableFile {
			continue
		}
		source, err := os.ReadFile(filepath.Join(menuSourcesDir, name))
		if err != nil {
			t.Fatal(err)
		}
		for i, code := range swiftCodeLines(string(source)) {
			where := name + ":" + strconv.Itoa(i+1)
			literal := lCallLiteral.FindAllStringSubmatchIndex(code, -1)
			for _, m := range literal {
				key := code[m[2]:m[3]]
				if strings.Contains(key, `\(`) {
					t.Errorf("%s 的 L(…) 里有字符串插值:每次插出来的句子都不一样,查不了表。"+
						"改用 {0} 占位符并把值作为后续实参:%s", where, strings.TrimSpace(code))
					continue
				}
				keys[key] = append(keys[key], where)
			}
			// 第一个实参不是字面量的 L( —— key 在运行时才知道,守卫核对不了它有没有译文。
			if len(lCallAny.FindAllStringIndex(code, -1)) > len(literal) &&
				!strings.Contains(code, "func L(") {
				t.Errorf("%s 的 L( 第一个实参必须是字符串字面量(否则无从核对有没有译文):%s",
					where, strings.TrimSpace(code))
			}
		}
	}
	return keys
}

func sortedPlaceholders(s string) string {
	found := placeholder.FindAllString(s, -1)
	sort.Strings(found)
	return strings.Join(found, ",")
}

func TestMenuEveryLocalizedStringHasAChineseTranslation(t *testing.T) {
	table := readZhHansTable(t)
	keys := menuLKeys(t)
	if len(keys) < len(table)/2 {
		// 一个扫不到调用点的守卫会把整张词表都报成「没人用」,而把这条读成
		// 「全翻译了」的人什么也得不到。数量级不对时先说守卫自己坏了。
		t.Fatalf("只扫到 %d 个 L(…) 调用,而词表有 %d 条 —— 扫描读不懂源码了", len(keys), len(table))
	}
	var missing []string
	for key, where := range keys {
		zh, ok := table[key]
		if !ok {
			missing = append(missing, where[0]+"  "+key)
			continue
		}
		if strings.TrimSpace(zh) == "" {
			t.Errorf("%q 的译文是空的", key)
		}
		if sortedPlaceholders(key) != sortedPlaceholders(zh) {
			t.Errorf("占位符对不上:%q 有 [%s],译文 %q 有 [%s] —— 少一个,那个值就从中文界面上消失了",
				key, sortedPlaceholders(key), zh, sortedPlaceholders(zh))
		}
	}
	sort.Strings(missing)
	for _, m := range missing {
		t.Errorf("没有中文译文(中文界面上这句会是英文):%s", m)
	}
	var stale []string
	for key := range table {
		if _, used := keys[key]; !used {
			stale = append(stale, key)
		}
	}
	sort.Strings(stale)
	for _, key := range stale {
		t.Errorf("词表里的 %q 已经没有 L(…) 在用 —— 删掉它,或者是英文原句改了而词表没跟上", key)
	}
}

var (
	swiftBareLiteral   = regexp.MustCompile(`"((?:[^"\\]|\\.)*)"`)
	proseCapitalized   = regexp.MustCompile(`^[A-Z][a-z]+(?: |$)`)
	proseLowercase     = regexp.MustCompile(`^[a-z]+(?: [a-z']+){2,}`)
	identityTermMapped = regexp.MustCompile(`case "((?:[^"\\]|\\.)*)": return L\("((?:[^"\\]|\\.)*)"\)`)
)

// 不经 `L(…)`、而刻意保持英文的裸字面量。**加一条就要写一句为什么** ——
// 这张表的存在就是那句要被逼着回答的话:「这是用户看不见的,还是我忘了翻?」
var bareEnglishAllowed = map[string]string{
	"Library": "路径的一段(~/Library/Logs),不是显示给人看的字",
	"Logs":    "路径的一段(~/Library/Logs),不是显示给人看的字",
}

// **忘了包 `L(…)` 的那句英文,在中文界面上就是一句英文 —— 而没有任何东西会报错。**
//
// 上面那条守卫只核对「包了的有没有译文」,够不着「根本没包」。这一条扫每一个
// 不在 `L(` 里的字面量,长得像一句人话的(大写开头的词、或三个以上小写词)
// 只许是两种之一:
//   - **既当判据又要显示的原词**(「Repair Required」「Via」):状态与行标签里存
//     英文原词、显示时经 `menuStateMessageText` / `menuRowLabelText` 翻 —— 判据
//     是「这个词在某张 `case "X": return L("X")` 映射表里」,而不是「看起来像标识符」;
//   - `bareEnglishAllowed` 里写明了理由的。
//
// 判据是启发式的(小写开头的两词短语、`bx ` 开头的句子它都放过),所以它是一张网,
// 不是证明;真机截图仍然是最后一道。它当场抓到过两句漏网的弹窗文案。
func TestMenuUserVisibleEnglishGoesThroughL(t *testing.T) {
	entries, err := os.ReadDir(menuSourcesDir)
	if err != nil {
		t.Fatal(err)
	}
	mapped := map[string]bool{}
	sources := map[string]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".swift") || strings.HasPrefix(name, "Localization") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(menuSourcesDir, name))
		if err != nil {
			t.Fatal(err)
		}
		sources[name] = string(raw)
		for _, m := range identityTermMapped.FindAllStringSubmatch(string(raw), -1) {
			if m[1] == m[2] {
				mapped[m[1]] = true
			}
		}
	}
	if len(mapped) < 5 {
		t.Fatalf("只找到 %d 个「原词 → L(原词)」映射 —— 守卫读不懂 menuStateMessageText / menuRowLabelText 了", len(mapped))
	}
	checked := 0
	for name, source := range sources {
		inMultiline := false
		for i, code := range swiftCodeLines(source) {
			if strings.Count(code, `"""`)%2 == 1 {
				inMultiline = !inMultiline
				continue
			}
			if inMultiline {
				continue
			}
			for _, loc := range swiftBareLiteral.FindAllStringSubmatchIndex(code, -1) {
				text := code[loc[2]:loc[3]]
				if strings.HasSuffix(code[:loc[0]], "L(") {
					continue
				}
				checked++
				if !proseCapitalized.MatchString(text) && !proseLowercase.MatchString(text) {
					continue
				}
				if strings.HasPrefix(text, "sudo ") || strings.HasPrefix(text, "do shell script") {
					continue // 交给 shell 的命令,不是给人读的话
				}
				if mapped[text] {
					continue
				}
				if _, ok := bareEnglishAllowed[text]; ok {
					continue
				}
				t.Errorf("%s:%d 这句英文没经 L(…),中文界面上它会原样露出来:%q\n"+
					"  是给人看的就包进 L(\"…\") 并在 %s 里加译文;是判据用的原词就在显示处经映射表翻;"+
					"真的不给人看就加进 bareEnglishAllowed 并写明理由。",
					name, i+1, text, zhHansTableFile)
			}
		}
	}
	if checked < 100 {
		t.Fatalf("只检查了 %d 个字面量 —— 扫描读不懂源码了", checked)
	}
}
