package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// —— CLAUDE.md 的大小预算(2026-09-23)——
//
// 根目录那份每次会话都全文加载。2026-09-13 按「判据留下、过程进 lessons」拆过一次,
// 从 20 万字符压到约 14.6 万;**五天后长回 17.4 万**,每天 5~6k。分家规则一直在,
// 只是没有守卫 —— 而这个仓库反复证明过:没有守卫的约定一定会漂。
//
// **出路不是删判据,是把只跟某一块代码有关的判据下沉到那块代码目录里的 CLAUDE.md**:
// Claude Code 只在读到那个子树里的文件时才加载它,判据仍然紧挨着代码、仍然自动加载,
// 但不再每次都全量加载。根目录那份只留跨领域的东西(架构、平台接缝、防环与
// kill-switch 不变量、命令模型、约定)。
//
// rootClaudeMDBudget **只许往下调**。撞上它时该做的不是把数字改大,而是想清楚
// 新写的那一节属于哪个子目录 —— 往上调的那一次 diff 本身就是要被 review 问住的东西。
const rootClaudeMDBudget = 150000

// subtreeClaudeMDBudget 是每一份子目录 CLAUDE.md 的上限。它取 Claude Code 自己对
// 单份记忆文件的告警线:子目录那份一旦也长到这个量级,说明它该再往下一层拆了。
const subtreeClaudeMDBudget = 40000

// projectMemoryFiles 返回仓库里全部 CLAUDE.md(相对仓库根),根目录那份排第一。
//
// **走目录树,不走 git ls-files**:新建的子目录 CLAUDE.md 在 git add 之前 git 看不见,
// 而那正是最需要被这几条守卫看见的时刻(本仓库栽过一次:守卫依赖 git grep,新包没
// add,守卫当场失明)。
func projectMemoryFiles(t *testing.T, root string) []string {
	t.Helper()
	var nested []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// 走不进去的目录(root 属主的测试日志之类)跳过 —— CLAUDE.md 不会在那儿。
			if info != nil && info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if info.IsDir() {
			if skippedScanDir(info.Name()) || info.Name() == ".build" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() != "CLAUDE.md" {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if rel != "CLAUDE.md" {
			nested = append(nested, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("走仓库找 CLAUDE.md: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "CLAUDE.md")); err != nil {
		t.Fatalf("根目录 CLAUDE.md 读不到: %v —— 读不到就响亮失败,不静默放行", err)
	}
	return append([]string{"CLAUDE.md"}, nested...)
}

func runeCountOf(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读 %s: %v", path, err)
	}
	return utf8.RuneCount(b)
}

func TestRootClaudeMDStaysWithinBudget(t *testing.T) {
	root := repoRootForTestNameRefs(t)
	n := runeCountOf(t, filepath.Join(root, "CLAUDE.md"))
	if n > rootClaudeMDBudget {
		t.Fatalf("根目录 CLAUDE.md 有 %d 字符,超出预算 %d。**别把预算改大** —— "+
			"它每次会话都全文加载。新写的那一节如果只跟某一块代码有关,把它放进那个目录的 "+
			"CLAUDE.md(并在根目录的「子目录里的 CLAUDE.md」一节登记);过程叙述进 docs/lessons/。",
			n, rootClaudeMDBudget)
	}
	t.Logf("根目录 CLAUDE.md:%d / %d 字符", n, rootClaudeMDBudget)
}

func TestEverySubtreeClaudeMDStaysWithinBudget(t *testing.T) {
	root := repoRootForTestNameRefs(t)
	for _, rel := range projectMemoryFiles(t, root)[1:] {
		if n := runeCountOf(t, filepath.Join(root, rel)); n > subtreeClaudeMDBudget {
			t.Errorf("%s 有 %d 字符,超出子目录预算 %d —— 该再往下一层拆了", rel, n, subtreeClaudeMDBudget)
		}
	}
}

// 子目录那份只在读到那个子树时才加载;一个在根目录工作的会话不知道它存在,就不会
// 去读它。所以根目录必须登记每一份 —— 而「登记了、文件却不在」由
// TestDocumentedFilePathsExist 那一侧抓(它的路径正则认 .md)。
func TestRootClaudeMDIndexesEverySubtreeMemory(t *testing.T) {
	root := repoRootForTestNameRefs(t)
	files := projectMemoryFiles(t, root)
	b, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range files[1:] {
		if !strings.Contains(string(b), rel) {
			t.Errorf("%s 存在,而根目录 CLAUDE.md 没有登记它 —— 只在根目录工作的会话"+
				"永远不会知道那里还有一份判据", rel)
		}
	}
}
