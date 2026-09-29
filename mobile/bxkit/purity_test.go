package bxkit

import (
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// allowedInternalDeps:判据与措辞,没有管道。依赖 supervisor / embedded 会把起进程的代码与
// 23MB 的 sing-box 二进制一起拖进 iPhone App(GOOS=ios 满足 darwin 标签)。
var allowedInternalDeps = map[string]struct{}{
	"github.com/getbx/bx/internal/config":       {},
	"github.com/getbx/bx/internal/explainwords": {},
	"github.com/getbx/bx/internal/route":        {},
	"github.com/getbx/bx/internal/routerbuild":  {},
}

// 本包必须保持纯判据:不联网、不读文件、不跑命令。
//
// 读不懂目录时**必须响亮失败**:一个找不到源文件就自动通过的守卫,等于没有守卫。
func TestBxkitPackageStaysPure(t *testing.T) {
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

// 直接 import 之外,**传递**依赖也要看:config 为解 bx:// 链接经 blink 拉进了 tunnel(于是
// os/exec 被链进 App,但从不被调用)—— 那条已知,写在这里;不许再多出来的是会把起进程的
// 编排、内嵌的 sing-box/brook 二进制、下载逻辑带进 iPhone App 的那几个。
func TestBxkitPullsInNoPlumbingTransitively(t *testing.T) {
	cmd := exec.Command("go", "list", "-deps", ".")
	cmd.Env = append(os.Environ(), "GOOS=ios", "GOARCH=arm64", "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list failed, the guard cannot see anything: %v", err)
	}
	deps := strings.Split(string(out), "\n")
	if len(deps) < 10 {
		t.Fatal("go list listed almost nothing; its output format changed and this guard no longer reads it")
	}
	for _, d := range deps {
		for _, banned := range []string{"/internal/supervisor", "/internal/embedded", "/internal/provision", "/internal/guardian", "/internal/cli", "/internal/install"} {
			if strings.HasSuffix(strings.TrimSpace(d), banned) {
				t.Errorf("bxkit transitively depends on %s; that drags plumbing (or embedded binaries) into the iPhone app", d)
			}
		}
	}
}
