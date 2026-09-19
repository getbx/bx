package cli

import (
	"sort"
	"strings"
	"testing"

	urfavecli "github.com/urfave/cli/v2"
)

// `bx --help` 顶上那一组(没有分类、不带标题)**只许放天天用的那几条**。
//
// 起因是把 help 树当用户读了一遍:28 个命令平铺成一列,而天天用的 `up`/`down`/
// `status` 排在第 15、16、26 位,前面挤着 `server`/`invite`/`user` 这些装服务端
// 才用的东西。现在按 Category 分组,顶上留日常四条。
//
// **这条守卫守的是「新命令不会静默落进顶上那一组」** —— urfave 把没有 Category 的
// 命令渲染在最前且不带标题,于是漏填的后果不是「没分组」,是**它看起来像一条天天
// 要用的命令**。方向刻意如此:宁可逼人回答「这算日常吗」,也不让它悄悄插队。
func TestEveryTopLevelCommandDeclaresWhereItBelongs(t *testing.T) {
	// 顶上那一组:装完之后天天会敲的。改这张表要想清楚 —— 它就是 `bx --help`
	// 第一屏的内容。
	everyday := map[string]bool{"up": true, "down": true, "status": true, "update": true}

	app := New()
	var uncategorized, seen []string
	for _, cmd := range app.Commands {
		if cmd.Hidden {
			continue
		}
		seen = append(seen, cmd.Name)
		if strings.TrimSpace(cmd.Category) == "" {
			uncategorized = append(uncategorized, cmd.Name)
		}
	}
	if len(seen) == 0 {
		t.Fatal("一个可见命令都没有 —— 守卫读不懂现在的 App 了")
	}

	sort.Strings(uncategorized)
	for _, name := range uncategorized {
		if !everyday[name] {
			t.Errorf("命令 %q 没有 Category,于是它被渲染在 `bx --help` 顶上那一组、"+
				"与 up/down/status 并排 —— 读起来像一条天天要用的命令。\n"+
				"  要么给它一个 Category(Diagnose / First run / For agents / "+
				"Routing rules / Server side / Windows only),要么把它加进这条测试的"+
				"everyday 表并说明它凭什么是日常命令。", name)
		}
	}
	// 反向:everyday 表里不许留已经不存在的命令 —— 陈旧条目什么也不守。
	present := map[string]bool{}
	for _, name := range seen {
		present[name] = true
	}
	for name := range everyday {
		if !present[name] {
			t.Errorf("everyday 表里的 %q 已经不是一条可见命令了", name)
		}
	}
}

// `bx --help` 与每条子命令的 `--help` 里不许有 markdown 的 `**`。
//
// 终端不渲染它,用户读到的是**字面上的星号**。本仓库为这条在 corestartadvice 与
// leakcheck 两处各立过一次守卫(TestNoRenderedAdviceCarriesMarkdown),而 help
// 文案此前一直在那两条的射程之外 —— 2026-09-18 给 leakcheck 写 Description 时
// 当场又写进去一对,是看终端输出才发现的。
func TestNoHelpTextCarriesMarkdown(t *testing.T) {
	app := New()
	checked := 0
	var walk func(prefix string, cmds []*urfavecli.Command)
	walk = func(prefix string, cmds []*urfavecli.Command) {
		for _, cmd := range cmds {
			for label, text := range map[string]string{
				"Usage":       cmd.Usage,
				"Description": cmd.Description,
				"ArgsUsage":   cmd.ArgsUsage,
			} {
				checked++
				if strings.Contains(text, "**") {
					t.Errorf("%s%s 的 %s 里有 markdown 的 `**` —— 终端不渲染它,"+
						"用户读到的是字面星号:%q", prefix, cmd.Name, label, text)
				}
			}
			walk(prefix+cmd.Name+" ", cmd.Subcommands)
		}
	}
	walk("bx ", app.Commands)
	if checked == 0 {
		t.Fatal("一条文案都没查到 —— 守卫读不懂现在的 App 了")
	}
}
