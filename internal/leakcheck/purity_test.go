package leakcheck

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
		"os/exec": "跑命令属于 internal/leakserve 的事实采集",
		"os":      "读环境/读文件会让判据依赖运行环境",
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
			if why, bad := bannedNetImport(path); bad {
				t.Errorf("%s import 了 %q:本包必须保持纯判据 —— %s", name, path, why)
			}
			if strings.HasPrefix(path, "github.com/getbx/bx/internal/") &&
				path != allowedInternalDep {
				t.Errorf("%s import 了 %q:纯判据只允许依赖 %s(三值枚举的叶子包),"+
					"依赖控制面/安装/监督任何一个包都会把它拖回不可测的位置", name, path, allowedInternalDep)
			}
		}
	}
	if checked == 0 {
		t.Fatal("本包一个非测试 .go 文件都没找到:守卫读不懂现在的目录结构,请连同它一起重写")
	}
}

// allowedInternalDep 是本包唯一允许的仓库内依赖:一个只放三值枚举、
// 自己不 import 本仓库任何东西的叶子包。
const allowedInternalDep = "github.com/getbx/bx/internal/tristate"

// bannedNetImport 挡住整棵 `net` 子树,**只留一个明写的例外 `net/netip`**。
//
// 原先的黑名单是逐字匹配 `"net"` 与 `"net/http"` 两条,于是 `net/url`、
// `net/http/httptest` 这些一样会拨号/起服务的包一个都拦不住;而判据真正需要的
// 那个恰恰是唯一无害的 `net/netip`。**前缀禁 + 明写例外**比逐字黑名单严格,
// 也让例外可被质疑,而不是靠「黑名单里恰好没写它」这种沉默放行。
//
// 为什么 `net/netip` 不违反本包的纯度:它是**纯值类型**(`Addr`/`Prefix` 的解析与
// 判定),不做名字解析、不拨号、不碰 syscall。实测 `go list -deps net/netip` 的
// 输出里没有 `net`,也没有 `syscall`/`os` —— 全部是 runtime/strconv/unique 这类。
// 判据需要它的理由是硬的:「回声返回的到底是不是一个 IPv6 地址」是规则二不误报的
// 前提(fake-IP 之下会返回 v4 字面量),手写地址解析在 v4-mapped、`::1` 这些形状上
// 出错的概率远高于它带来的依赖代价。
func bannedNetImport(path string) (string, bool) {
	if path != "net" && !strings.HasPrefix(path, "net/") {
		return "", false
	}
	if path == "net/netip" {
		return "", false
	}
	return "整棵 net 子树只允许 net/netip(纯值类型,不拨号不解析名字);" +
		"起服务/发请求属于 internal/leakserve", true
}

// **上面那条只查直接 import,不够 —— 而「不够」这件事是实测出来的。**
//
// 本包最初直接 import 的是 internal/observe(当时的白名单项),看起来只有一个
// 依赖;而 observe 为了做观测要 import install / supervisor,于是 `go list -deps`
// 实测本包传递依赖了 **23 个内部包**,含 install、supervisor、provision、tun、
// dialer —— 整个数据面和控制面,全部代价只为一个三值枚举。
//
// 也就是说:**守卫是绿的,而它写在注释里的那句话是假的。** 把枚举沉到
// internal/tristate 之后降到 2 个(本包 + 那个叶子包)。
//
// 所以这一条查的是**传递闭包**,不是 import 行。它比上面那条严格得多,也正是
// 上面那条自以为在守的东西。
func TestLeakcheckHasNoTransitiveControlPlaneDependency(t *testing.T) {
	out, err := exec.Command("go", "list", "-deps", ".").Output()
	if err != nil {
		t.Fatalf("跑不了 go list,守卫失去意义: %v", err)
	}
	var internal []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "github.com/getbx/bx/internal/") {
			internal = append(internal, line)
		}
	}
	if len(internal) == 0 {
		t.Fatal("一个内部依赖都没列出来 —— go list 的输出格式变了,守卫已经读不懂它")
	}
	for _, dep := range internal {
		if dep != allowedInternalDep && dep != "github.com/getbx/bx/internal/leakcheck" {
			t.Errorf("传递依赖了 %q。判据必须留在能被表驱动测试完整覆盖的位置,"+
				"而一条通往控制面的依赖链会把它拖回「判得对」与「接线对」重新纠缠在一起的地方 ——"+
				"本仓库的全部事故都在后者。完整依赖:%v", dep, internal)
		}
	}
}
