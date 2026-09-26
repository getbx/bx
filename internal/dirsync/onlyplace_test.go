package dirsync_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// **目录同步只许长在 internal/dirsync 里。**
//
// 它此前在三个包里各写了一遍(guardian 的状态存储、启动标记、升级事务,
// 以及 internal/update 的安装器)。四份拷贝的问题不是难看:Windows 上目录
// fsync 一律 `Access is denied`,而**漏改一份的失败方式是静默的** —— 写盘
// 照常成功,只有掉电那一次才看得出来;而在 windows 那条 CI 腿上,它表现为
// 515 条失败里的 345 条,直接让整条腿永远不可能绿。
//
// 判据是**形状**不是名字:**跟着绑定走** —— 从 `x, err := os.Open(...)` 取出那个
// 名字,再要求 `x.Sync()` 出现。钉名字挡不住下一个人换个名字重写一遍。
//
// **第一版写的是「函数体里同时出现 os.Open 与 .Sync()」,而它当场误报了一次**:
// 当时的 internal/toolkeys/audit.go(2026-09-25 随零调用方的 toolkeys 包删掉)先 `os.Open` 读一遍旧审计日志、再对
// `CreateTemp` 出来的**文件**句柄 Sync —— 两件事都在同一个函数体里,而它同步的
// 根本不是目录。那是本仓库编号的第五种失效写法反过来:断言被满足,但是因为
// 别的理由。而**一条会误报的闸门比没有闸门更糟**,它训练人去删掉守卫。
func TestOnlyOnePlaceSyncsADirectory(t *testing.T) {
	root := filepath.Join("..", "..")
	scanned := 0
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if strings.Contains(filepath.ToSlash(path), "/internal/dirsync/") ||
				strings.Contains(filepath.ToSlash(path), "/internal/embedded/") ||
				strings.Contains(filepath.ToSlash(path), "/internal/winfw/") {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return rerr
			}
			scanned++
			src := string(b)
			for _, body := range goFuncBodies(src) {
				for _, name := range readOnlyOpenBindings(body) {
					if strings.Contains(body, name+".Sync()") {
						t.Errorf("%s 里又手写了一遍目录同步(%s)—— 它必须走 dirsync.Sync/SyncRoot,"+
							"否则 windows 上那句 `Access is denied` 会再长出一份:\n%s", path, name, body)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatalf("走不进 %s:%v —— 读不出要扫的源码时必须响亮失败", dir, err)
		}
	}
	// 一条安静地扫了零个文件的守卫,与没有这条守卫在输出上完全一样,
	// 而它看起来更让人放心。
	if scanned < 50 {
		t.Fatalf("只扫到 %d 个 .go 文件 —— 守卫没走到该走的地方", scanned)
	}
}

// goFuncBodies 按花括号配平切出每个顶层函数的函数体。
func goFuncBodies(src string) []string {
	var out []string
	for i := 0; i+5 < len(src); i++ {
		if !strings.HasPrefix(src[i:], "\nfunc ") {
			continue
		}
		open := strings.Index(src[i:], "{")
		if open < 0 {
			continue
		}
		depth, start := 0, i+open
		for j := start; j < len(src); j++ {
			switch src[j] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					out = append(out, src[start:j+1])
					j = len(src)
				}
			}
		}
	}
	return out
}

// readOnlyOpenBindings 取出函数体里由**只读打开**绑定出来的那些名字。
// 写文件走的是 os.Create / os.CreateTemp / os.OpenFile,不在此列 ——
// 对它们的 Sync 是文件 fsync,在任何平台上都成立、也正是该做的事。
func readOnlyOpenBindings(body string) []string {
	var names []string
	for _, opener := range []string{"os.Open(", ".Open(\".\")", "root.Open("} {
		from := 0
		for {
			i := strings.Index(body[from:], opener)
			if i < 0 {
				break
			}
			i += from
			from = i + len(opener)
			// 往左找到这一行的行首,再取 `名字, err :=` 里那个名字。
			lineStart := strings.LastIndexByte(body[:i], '\n') + 1
			left := strings.TrimSpace(body[lineStart:i])
			cut := strings.Index(left, ",")
			if cut < 0 {
				continue
			}
			name := strings.TrimSpace(left[:cut])
			if name != "" && !strings.ContainsAny(name, " \t(){}=:") {
				names = append(names, name)
			}
		}
	}
	return names
}
