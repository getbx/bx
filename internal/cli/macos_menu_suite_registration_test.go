package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// apps/macos/BxMenu/Tests 下的每个 .swift 文件都必须在 scripts/test-macos-menu.sh
// 里登记过,否则它一次都不会跑,而 CI 照样绿。
//
// **这不是理论风险,是实测的。** 把 maintenance-presentation 那段 run_test 删掉:
// 套件数从 17 掉到 16,输出里再没有那个套件的名字,脚本仍旧打印
// 「macOS menu tests passed」并**退出码 0**。原因是 Tests/ 下的文件不属于任何
// SwiftPM target —— `swift build` 编不到它们,只有这个脚本用 swiftc 直接编直接跑,
// 而脚本是靠一条条 run_test 显式点名的。漏登记 = 静默不跑。
//
// CI 里已经有一条守卫断言收尾横幅确实打印过(防「run_test 块被整个删光」),
// 但那条挡不住「新加了一个文件却忘了登记」—— 横幅照打,只是少跑一个套件。
//
// **本测试是全仓库唯一在 CI 里跑得到的、能发现这件事的东西**,所以它读不懂脚本时
// 必须响亮地失败,而不是安静放过:一个找不到脚本就自动通过的守卫,与没有守卫
// 是同一回事,而这正是本项目的文本守卫被绕过八次的那个形状。
func TestEveryMacOSMenuTestSuiteIsRegistered(t *testing.T) {
	root := repoRootForMenuGuard(t)
	scriptPath := filepath.Join(root, "scripts", "test-macos-menu.sh")
	script, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("读不到 %s —— 这条守卫失去意义,必须响亮失败而不是放过: %v", scriptPath, err)
	}
	if !strings.Contains(string(script), "run_test ") {
		t.Fatalf("%s 里一条 run_test 都没有:脚本形态变了,本守卫已经读不懂它,"+
			"请更新守卫而不是删掉它", scriptPath)
	}

	testsDir := filepath.Join(root, "apps", "macos", "BxMenu", "Tests")
	entries, err := os.ReadDir(testsDir)
	if err != nil {
		t.Fatalf("读不到 %s: %v", testsDir, err)
	}

	found := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".swift") {
			continue
		}
		found++
		// 按文件名匹配即可:脚本里写的是 "$MENU/Tests/<名字>.swift"。
		if !strings.Contains(string(script), "/Tests/"+entry.Name()) {
			t.Errorf("%s 没有在 scripts/test-macos-menu.sh 里登记 —— 它一次都不会跑,"+
				"而 CI 会保持绿色。加一段 run_test 把它连同它依赖的 Sources 文件一起点名。",
				entry.Name())
		}
	}
	if found == 0 {
		t.Fatalf("%s 下一个 .swift 都没找到:目录结构变了,本守卫已经读不懂它", testsDir)
	}
}

// repoRootForMenuGuard 从本包所在目录往上找到仓库根(带 go.mod 的那一层)。
// 找不到就 Fatal —— 见上:安静放过等于没有守卫。
func repoRootForMenuGuard(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("取不到工作目录: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("从工作目录往上找不到 go.mod,定位不了仓库根")
		}
		dir = parent
	}
}
