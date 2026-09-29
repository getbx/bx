package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// test-windows 这条 CI 腿跑「带 *_windows_test.go 或 purity_test.go 的包」(清单从 git ls-files
// 现取,见 TestWindowsCILegDerivesItsPackageList)。这些包里没有构建标签的测试会在 Windows 上
// 真跑,而 Windows 的提权前缀是空的(elevate.Prefix)—— 期望里写死 "sudo bx" 的断言在本机
// 恒绿、只在 CI 那条腿上红。2026-09-28 起 doctor 那条就这样让 master 红了一天多。
// 判据只看**字符串字面量**(注释里写 sudo 是在讲历史,不是断言)。
func TestWindowsRunTestsDoNotHardCodeSudo(t *testing.T) {
	root := repoRootForMenuGuard(t)
	out, err := exec.Command("git", "-C", root, "ls-files", "*purity_test.go", "*_windows_test.go").Output()
	if err != nil {
		t.Fatalf("git ls-files: %v", err)
	}
	dirs := map[string]bool{}
	for _, f := range strings.Fields(string(out)) {
		dirs[filepath.Dir(f)] = true
	}
	if len(dirs) == 0 {
		t.Fatal("no package runs on the Windows leg according to git ls-files; this guard would scan nothing")
	}
	scanned := 0
	for dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(root, dir, "*_test.go"))
		for _, path := range files {
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			if hasBuildConstraint(f) {
				continue // tagged files (darwin/linux/…) do not run on Windows
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err == nil && strings.Contains(s, "sudo bx") {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s:%d hard-codes %q; this file runs on the Windows CI leg, where the prefix is empty — build the expectation from elevate.Prefix", rel, fset.Position(lit.Pos()).Line, s)
				}
				return true
			})
		}
	}
	if scanned == 0 {
		t.Fatal("scanned zero untagged test files in the Windows-leg packages")
	}
}

func hasBuildConstraint(f *ast.File) bool {
	for _, cg := range f.Comments {
		if cg.Pos() > f.Package {
			break
		}
		for _, c := range cg.List {
			if strings.HasPrefix(c.Text, "//go:build") {
				return true
			}
		}
	}
	return false
}
