package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// —— CLAUDE.md / README.md 点名的文件必须真的在(2026-08-24)——
//
// 与 TestEveryTestNameMentionedInProseExists 同一条纪律,换一个维度:那条管
// 「注释里点名的测试」,这条管「文档里点名的文件」。
//
// **范围刻意只有这两份,不含 docs/superpowers/{specs,plans}。** 首次全仓扫描的
// 结果决定了这个范围:文档里共点名 541 个路径,55 个不存在,而**这 55 个无一在
// CLAUDE.md**——全部在 plans 里。计划书是**有日期的意图记录**,它点名的是「将要
// 建的文件」;实施走偏、或功能后来被删,它的路径失效是预期的,不是谎。把 plans
// 拉进来只会制造 55 条假红,而假红的守卫会被下一个人删掉。
//
// CLAUDE.md 是**每次会话都加载**的那一份,它说的每一句都会被当作现状;README 是
// 用户看的。这两份里一个失效路径就是一次「按它去找、找不到」。
//
// 判据是文件存在性,几乎没有假阳性的空间 —— 与「注释里点名的标识符是否存在」
// 形成对照:后者试过,噪声太大(stdlib、Win32/AppKit API、plist 键、域名、以及
// 文档明确记述「已删」的东西),做不成闸门,所以没做。
func TestDocumentedFilePathsExist(t *testing.T) {
	root := repoRootForTestNameRefs(t)
	// 路径形状:以仓库里真实的顶层目录开头,以已知扩展名结尾。**不认裸目录**
	// (`internal/dialer` 这种没有扩展名的写法太容易和散文混在一起)。
	re := regexp.MustCompile(`\b((?:internal|cmd|scripts|apps|docs|packaging|winres)/[A-Za-z0-9_./-]+\.(?:go|sh|swift|html|yml|yaml|md|json|iss))\b`)

	docs := []string{"CLAUDE.md", "README.md"}
	total := 0
	for _, doc := range docs {
		b, err := os.ReadFile(filepath.Join(root, doc))
		if err != nil {
			t.Fatalf("读 %s: %v —— 读不到就响亮失败,不静默放行", doc, err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			for _, m := range re.FindAllStringSubmatch(line, -1) {
				total++
				if _, err := os.Stat(filepath.Join(root, m[1])); err != nil {
					t.Errorf("%s:%d 点名了 %s,而它不存在 —— "+
						"CLAUDE.md 是每次会话都加载的那一份,它说的每一句都会被当作现状;"+
						"一个失效路径就是一次「按它去找、找不到」", doc, i+1, m[1])
				}
			}
		}
	}
	if total < 30 {
		t.Fatalf("只扫出 %d 处路径引用,少得反常 —— 提取正则可能读不懂现在的写法了,"+
			"而一条什么都没扫到的守卫恒绿", total)
	}
}
