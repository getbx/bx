package leakcheck

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 本包必须保持**纯判据**:不起服务、不跑命令、不读文件。
//
// 这不是洁癖。判据是本功能唯一值钱的部分,它必须能被表驱动测试与变异验证完整
// 覆盖;一旦 net/http 或 os/exec 溜进来,「判得对不对」与「接线对不对」就重新
// 变成同一件事,而本仓库的全部事故都在后者。
//
// 读不懂目录时**必须响亮失败**:一个找不到源文件就自动通过的守卫,等于没有守卫。
func TestLeakcheckPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]string{
		"net/http": "起服务/发请求属于 internal/leakserve",
		"os/exec":  "跑命令属于 internal/leakserve 的事实采集",
		"net":      "拨号与地址解析都是 I/O;判据只吃已经采到的字符串",
		"os":       "读环境/读文件会让判据依赖运行环境",
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
			if strings.HasPrefix(path, "github.com/getbx/bx/internal/") &&
				path != "github.com/getbx/bx/internal/observe" {
				t.Errorf("%s import 了 %q:纯判据只允许依赖 internal/observe(三态类型),"+
					"依赖控制面/安装/监督任何一个包都会把它拖回不可测的位置", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("本包一个非测试 .go 文件都没找到:守卫读不懂现在的目录结构,请连同它一起重写")
	}
}
