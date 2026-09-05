package pathview

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// 本包必须保持**纯判据**:不跑命令、不读文件、不拨号。I/O 全在 cli 那一侧采集;
// 判据能被表驱动测试完整覆盖,是它唯一值钱的地方。读不懂目录时响亮失败。
func TestPathviewPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]bool{"os": true, "os/exec": true, "net": true, "net/http": true, "syscall": true}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s: %v", name, err)
		}
		checked++
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if banned[path] {
				t.Errorf("%s 引入了 %q —— 判据包不许做 I/O", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个源文件都没检查到,守卫失去意义")
	}
}
