package guardian

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// bareCoreRunnerExemptions 是**全仓允许裸调 NewExecCoreRunner 的全部位置**,
// 键是 `<相对仓库根的路径>:<所在函数名>`,值是允许的次数与理由。
//
// **带次数不是讲究**:只按函数名放行的话,将来有人往这几个函数里再加一个裸调用,
// 守卫一个字都不会说 —— 而白名单里那条目看起来仍然与生效中的一模一样。
// 次数对不上(多了或少了)都要红:少了说明这是一条陈旧条目,它什么也不守。
var bareCoreRunnerExemptions = map[string]struct {
	count  int
	reason string
}{
	"internal/guardian/testrunner_test.go:newTestCoreRunner": {
		count:  1,
		reason: "helper 本身 —— 它就是那条被认可的路,总得有人真的调一次构造器",
	},
	"internal/guardian/corestartfailure_test.go:TestTheStartFailureRecordAlwaysHasAPlaceToLive": {
		count:  1,
		reason: "断言的对象就是生产默认值;换成 t.TempDir() 的 runner 这一句恒真、什么也不守",
	},
	"internal/cli/corestartfailure_roundtrip_test.go:newRoundtripRunner": {
		count:  1,
		reason: "cli 侧的同款 helper,两个路径字段一起指进 t.TempDir()",
	},
	"internal/cli/corestartfailure_roundtrip_test.go:TestTheCoreWritesExactlyWhatTheGuardianReads": {
		count:  1,
		reason: "跨包地断言生产构造器交出来的 StartFailurePath 就是 corestartfailure.DefaultPath",
	},
}

// TestTestsNeverPointACoreRunnerAtTheProductionPaths 钉住:**测试里不许再出现
// 裸的 NewExecCoreRunner(**。
//
// 它交出来的 StatePath 与 StartFailurePath 都指着 /var/lib/bx —— 项目所有者
// 真机上正在用的那个目录。普通 `go test` 下这只是一行 EACCES 日志,而
// `sudo go test ./internal/guardian/` 会让 Start 之前那次
// discardStaleStartFailureRecord 真的去扫那个目录(Discard 连
// `.core-start-failure-*` 那些原子写碎片一起扫)。
//
// **这个失效是静默的**:错在测试里,产出的信号是「一切正常」——
// 那正是需要一条守卫而不是一条约定的理由。
//
// 判据取 AST 不取文本:注释里、字符串字面量里(本文件的白名单表里就写着这个
// 名字)、以及生产代码 daemon.go 里那一处,都不该算。
func TestTestsNeverPointACoreRunnerAtTheProductionPaths(t *testing.T) {
	root := repoRootForGuard(t)

	// 命中计数:键与白名单同形。
	bare := map[string]int{}
	// 出现过裸调用的位置,按文件:行 记下来给失败信息用。
	where := map[string][]string{}
	scannedFiles := 0
	sawHelperCall := false

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if path != root && (strings.HasPrefix(name, ".") || name == "dist") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		scannedFiles++
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			// 语法都读不动就不是「没找到」,是守卫瞎了。
			t.Fatalf("解析不了 %s,守卫失去意义:%v", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.Ident:
					if fun.Name == "newTestCoreRunner" {
						sawHelperCall = true
					}
					if fun.Name != "NewExecCoreRunner" {
						return true
					}
				case *ast.SelectorExpr:
					if fun.Sel.Name != "NewExecCoreRunner" {
						return true
					}
				default:
					return true
				}
				key := rel + ":" + fn.Name.Name
				bare[key]++
				where[key] = append(where[key], fset.Position(call.Pos()).String())
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败,守卫失去意义:%v", err)
	}

	// 两道下限。一条安静地扫了零个文件的守卫,与没有这条守卫在输出上完全一样,
	// 而它看起来更让人放心。
	if scannedFiles == 0 {
		t.Fatal("一个 *_test.go 都没扫到 —— 守卫走错了地方")
	}
	if !sawHelperCall {
		t.Fatal("全仓一次 newTestCoreRunner( 都没见着 —— 要么 helper 改名了、\n" +
			"要么这条守卫扫的根本不是本仓库;两种都让它此后恒绿")
	}

	for key, got := range bare {
		allowed, ok := bareCoreRunnerExemptions[key]
		if !ok {
			t.Errorf("%s 裸调了 %d 次 NewExecCoreRunner(%s)——\n"+
				"它开箱就指着 /var/lib/bx 下的 core-process.json 与 core-start-failure.json,\n"+
				"而 Start 在 spawn 之前会把后者连同临时碎片一起扫掉。\n"+
				"改用 newTestCoreRunner(t, …)(cli 侧是 newRoundtripRunner);\n"+
				"确实要断言生产默认值的话,把这个位置加进 bareCoreRunnerExemptions 并写明理由。",
				key, got, strings.Join(where[key], ", "))
			continue
		}
		if got != allowed.count {
			t.Errorf("%s 裸调了 %d 次,白名单里写的是 %d 次(%s)——\n"+
				"次数变多就是有人在一个被放行的函数里搭了顺风车;\n"+
				"位置:%s", key, got, allowed.count, allowed.reason, strings.Join(where[key], ", "))
		}
	}

	// 反向:白名单里不许有指向已经不存在的位置的陈旧条目。它们看起来与生效中的
	// 一模一样而什么也不守(本仓库为「一份四分之三是假的清单」栽过一次)。
	var stale []string
	for key := range bareCoreRunnerExemptions {
		if bare[key] == 0 {
			stale = append(stale, key)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("bareCoreRunnerExemptions 里这些条目已经没有对应的裸调用了:%v ——\n"+
			"删掉它们;留着的话下一次同名函数长出一个裸调用会被静默放行", stale)
	}
}

// repoRootForGuard 从本包所在目录往上找 go.mod。写死 `../..` 也行,但那会在
// 有人挪动包的时候安静地扫到别处去。
func repoRootForGuard(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("定位不到工作目录,守卫失去意义:%v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("往上一直没找到 go.mod —— 守卫不知道该扫哪儿")
		}
		dir = parent
	}
}
