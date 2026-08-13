package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// **README 里出现的每一条 `bx <子命令>` 都必须真的存在。**
//
// 起因是一条照着敲会得到 `command not found` 的命令进了文档。当时的提交信息声称
// 「各配一条守卫」,而事实是**一条都没有** —— 那句话本身是假的,这条测试是补齐它。
//
// 判据刻意不是「文档里不许出现某个字符串」:那种守卫只挡住已经犯过的那一次。
// 这里拿**真实注册的命令树**去对,所以下一次改名、删命令、写错子命令名都会被抓到。
//
// README 是普通用户唯一会照着敲的东西 —— 一条不存在的命令在这里的代价,
// 比在任何设计文档里都高。
func TestREADMEOnlyMentionsRealCommands(t *testing.T) {
	readme, err := os.ReadFile(filepath.Join("..", "..", "README.md"))
	if err != nil {
		t.Fatalf("读不到 README,守卫失去意义: %v", err)
	}

	known := map[string]bool{}
	for _, c := range New().Commands {
		known[c.Name] = true
		for _, sub := range c.Subcommands {
			known[c.Name+" "+sub.Name] = true
		}
	}
	if len(known) == 0 {
		t.Fatal("一条命令都没枚举到,守卫读不懂现在的命令树")
	}

	// **只在代码格式里找。** 散文里的 `bx will reconnect automatically`(一句 UI 文案)
	// 与 `bx fake-IP`(一个概念名)都不是命令,而用户照着敲的恰恰是带代码格式的那些。
	// 第一版没有这道限制,两条散文当场变成假阳性 —— 判据要挡的是「照着敲会失败」,
	// 那就该只看用户会照着敲的地方。
	re := regexp.MustCompile(`bx ([a-z][a-z0-9-]*)`)
	var bad []string
	seen := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(codeSpansAndFences(string(readme)), -1) {
		name := m[1]
		if known[name] || seen[name] {
			continue
		}
		seen[name] = true
		bad = append(bad, name)
	}
	// 这些是命令的**参数**或说明文字里的词,不是子命令。名单显式列出,
	// 加一个要有人当场判断它到底是不是命令。
	allowed := map[string]bool{
		"is": true, "and": true, "the": true, "or": true, "to": true,
		"in": true, "on": true, "with": true, "as": true, "for": true,
	}
	var real []string
	for _, name := range bad {
		if !allowed[name] {
			real = append(real, name)
		}
	}
	sort.Strings(real)
	if len(real) > 0 {
		t.Errorf("README 里这些 `bx <名字>` 不是已注册的命令,照着敲会 command not found:%v\n"+
			"已注册:%v", real, sortedKeys(known))
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// testkit 的 Tailscale DNS 探针 —— 上一轮加的,同样属于「声称配了守卫而没有」的那批。
// 它是排查「bx 与 Tailscale 共存时 *.ts.net 解析不了」唯一的现场证据。
func TestDarwinTestkitProbesTailscaleDNS(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "scripts", "darwin-testkit.sh"))
	if err != nil {
		t.Fatalf("读不到 testkit,守卫失去意义: %v", err)
	}
	text := string(source)
	for _, want := range []string{"100.100.100.100", "dig tailscale dns:"} {
		if !strings.Contains(text, want) {
			t.Errorf("testkit 丢了 Tailscale DNS 探针(%q)—— 共存问题会失去唯一的现场证据", want)
		}
	}
}

// codeSpansAndFences 抽出 markdown 里所有行内代码与围栏代码块的内容。
//
// 判据只该看这些地方:它们是用户会整段复制粘贴的东西。
func codeSpansAndFences(md string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			out.WriteString(line)
			out.WriteByte('\n')
			continue
		}
		// 行内代码:取每一对反引号之间的内容。奇数个反引号时最后一段不算。
		parts := strings.Split(line, "`")
		for i := 1; i < len(parts); i += 2 {
			out.WriteString(parts[i])
			out.WriteByte('\n')
		}
	}
	return out.String()
}
