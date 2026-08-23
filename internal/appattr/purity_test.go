package appattr

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedInternalDeps 为空:本包不依赖任何 internal 包。它只解释内核字节、
// 选归属进程、把可执行路径变成显示名 —— 一份纯计算,与 bx 的其余部分零耦合。
var allowedInternalDeps = map[string]struct{}{}

// 本包必须保持纯判据:不读文件、不联网、不跑命令、不碰系统调用。
//
// 读不懂目录时**必须响亮失败**:一个找不到源文件就自动通过的守卫,等于没有守卫。
func TestAppattrPackageStaysPure(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读不到本包目录,守卫失去意义: %v", err)
	}
	banned := map[string]string{
		"os":               "读文件会让判据依赖运行环境;取数据是调用方的事",
		"os/exec":          "跑命令属于组装层,且 fork lsof 正是本设计要避免的",
		"net":              "判据不许联网",
		"net/http":         "判据不许联网",
		"syscall":          "系统调用属于 appsource_darwin.go,不属于判据",
		"golang.org/x/sys": "同上 —— 前缀匹配,x/sys/unix 也在内",
		// **「不进日志」是这个包两次信息面扩大的边界条件之一**,而它此前不在
		// 禁令里。本包持有 ExecPath(完整可执行路径:安装位置、用户名、装了什么)
		// 与 Dest(目的地域名:这台机器在访问什么);两处字段注释都把「不进日志」
		// 写成了发布面的一部分。写日志不落在「不读文件、不联网、不跑命令」这句
		// 话里,但它是这个包最现实的泄漏出口。
		"log": "本包持有 ExecPath 与 Dest,「不进日志」是它们发布面的边界条件",
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
			for prefix, why := range banned {
				if path == prefix || strings.HasPrefix(path, prefix+"/") {
					t.Errorf("%s import 了 %q:本包必须保持纯判据 —— %s", name, path, why)
				}
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
