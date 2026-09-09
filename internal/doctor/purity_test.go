package doctor

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本包只允许依赖两个内部包:rulereview(规则体检判据)与 config(配置类型)。
//
// **守卫的范围是「本包自己的文件不 import 这些」,传递依赖不在此守卫范围内** ——
// config 自己就会拖进 net/os(它要读文件、解析链接),所以不能把它说成「纯计算的
// 叶子」;本包用到的只是它的类型。真正要挡住的是本包自己去读文件、跑命令、联网,
// 那会让判据重新变成只能靠人读的东西。
// 依赖 guardian 会成环(guardian 要调本包),依赖 cli/supervisor/install 会把判据拖回
// 「只能靠人读」的位置。
var allowedInternalDeps = map[string]struct{}{
	"github.com/getbx/bx/internal/rulereview": {},
	"github.com/getbx/bx/internal/config":     {},
}

func TestDoctorPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]string{
		"os": "读文件/读环境是采集方的事", "os/exec": "跑命令属于组装层",
		"net": "判据不许联网", "net/http": "判据不许联网", "syscall": "判据不碰内核",
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		checked++
		file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(".", name), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, _ := strconv.Unquote(spec.Path.Value)
			if why, bad := banned[path]; bad {
				t.Errorf("%s import 了 %q —— %s", name, path, why)
			}
			if strings.HasPrefix(path, "github.com/getbx/bx/internal/") {
				if _, ok := allowedInternalDeps[path]; !ok {
					t.Errorf("%s import 了 %q:纯判据只允许依赖 %v", name, path, allowedInternalDeps)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("一个源文件都没检查到 —— 守卫读不懂目录结构")
	}
}
