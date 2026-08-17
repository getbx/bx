package rulereview

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedInternalDeps 是本包允许依赖的 internal 包 —— 两个都是**纯计算**的叶子:
// route 只做后缀/CIDR 匹配,policy.DirectRisk 只查一张写死的静态表。
//
// 依赖控制面(supervisor/guardian/cli/install/setup)任何一个,都会把判据拖回
// 「只能靠人读」的位置,而这个功能唯一值钱的部分就是判据。
var allowedInternalDeps = map[string]struct{}{
	"github.com/getbx/bx/internal/route":  {},
	"github.com/getbx/bx/internal/policy": {},
}

// 本包必须保持纯判据:不联网、不读文件、不跑命令。
//
// 读不懂目录时**必须响亮失败**:一个找不到源文件就自动通过的守卫,等于没有守卫。
func TestRulereviewPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]string{
		"os":       "读环境/读文件会让判据依赖运行环境;找列表文件是调用方的事",
		"os/exec":  "跑命令属于组装层",
		"net":      "判据不许联网(spec 非目标:不做主动探测)",
		"net/http": "判据不许联网",
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
			t.Fatalf("解析 %s 失败,守卫读不懂现在的代码: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s 的 import 解析失败: %v", name, err)
			}
			if why, bad := banned[path]; bad {
				t.Errorf("%s import 了 %q:本包必须保持纯判据 —— %s", name, path, why)
			}
			if strings.HasPrefix(path, "github.com/getbx/bx/internal/") {
				if _, ok := allowedInternalDeps[path]; !ok {
					t.Errorf("%s import 了 %q:纯判据只允许依赖 %v —— "+
						"依赖控制面任何一个包都会让「判得对不对」与「接线对不对」重新变成同一件事",
						name, path, allowedInternalDeps)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("本包一个非测试 .go 文件都没找到:守卫读不懂现在的目录结构,请连同它一起重写")
	}
}
