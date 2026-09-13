package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// 每个被 scripts/test-macos-menu.sh 登记的 Swift 套件文件里,**定义了的每一个
// `static func test*` 都必须在同一个文件的 `main()` 里被调用一次**。
//
// **这不是理论风险,是这条分支上真的发生过的事。** 2026-09-12 那一轮新加的三条
// 测试(`testCurrentPanelShowsItsOwnProbeResult`、
// `testCurrentPanelProbeKeepsTheThreeStatesApart`、
// `testRemoveConfirmationNamesTheServerCarryingTrafficRightNow`)全部只有定义、
// 没有调用 —— 而它们恰好是那一轮两条修复的**整个纯模型那一半**。变异实测:把
// 「这台正在承载你的流量」那段整个删掉,套件照样打印通过横幅并退出码 0。
//
// 隔壁那条 `TestEveryMacOSMenuTestSuiteIsRegistered` 守的是**文件**有没有被脚本
// 点名,它在结构上看不见「函数定义了但没人调」—— 文件跑了,横幅打了,只是里面
// 少执行了三个函数。两条守卫合起来才盖住整条链:文件进脚本 → 函数进 main()。
//
// **写这条守卫本身有两个陷阱,都实测踩过**:
//  1. `GuardianClientTests.swift` 用的是 `static func main() throws {`。按
//     `static func main() {` 逐字匹配的扫描器会**静默跳过**它 —— 一次假清白。
//     故签名只匹配到 `static func main(` 为止,函数体由括号配平取,不靠缩进。
//  2. 读不到脚本 / 读不到目录 / 某个登记过的文件里找不到 `main()` / 最后一个
//     文件都没扫到 —— 一律 `t.Fatal` 响亮失败。**一条安静地扫了零个文件的守卫,
//     与没有守卫在输出上完全一样,而它看起来更让人放心。**
func TestEveryMacOSMenuTestFunctionIsCalledFromItsMain(t *testing.T) {
	root := repoRootForMenuGuard(t)
	scriptPath := filepath.Join(root, "scripts", "test-macos-menu.sh")
	scriptBytes, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("读不到 %s —— 这条守卫失去意义,必须响亮失败而不是放过: %v", scriptPath, err)
	}
	script := string(scriptBytes)
	if !strings.Contains(script, "run_test ") {
		t.Fatalf("%s 里一条 run_test 都没有:脚本形态变了,本守卫已经读不懂它,"+
			"请更新守卫而不是删掉它", scriptPath)
	}

	testsDir := filepath.Join(root, "apps", "macos", "BxMenu", "Tests")
	entries, err := os.ReadDir(testsDir)
	if err != nil {
		t.Fatalf("读不到 %s —— 这条守卫失去意义: %v", testsDir, err)
	}

	defRE := regexp.MustCompile(`(?m)^[ \t]*static func (test[A-Za-z0-9_]*)[ \t]*\(`)

	scannedFiles := 0
	scannedFuncs := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".swift") {
			continue
		}
		// 没被脚本登记的文件由 TestEveryMacOSMenuTestSuiteIsRegistered 负责报,
		// 这里不重复报同一件事。
		if !strings.Contains(script, "/Tests/"+entry.Name()) {
			continue
		}
		path := filepath.Join(testsDir, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s: %v", path, err)
		}
		// 先剥注释(注释掉的调用不算数),再抹白字符串字面量(字面量里出现的
		// 函数名同样不算数);两步都保住字节偏移,括号配平才不会被推歪。
		clean := blankSwiftStringLiterals(stripSwiftComments(string(raw)))

		body, ok := swiftFunctionBody(clean, "static func main(")
		if !ok {
			t.Fatalf("%s 里找不到 `static func main(` 的函数体 —— 本守卫已经读不懂这个文件,"+
				"请更新守卫而不是让它安静放过", entry.Name())
		}

		scannedFiles++

		// 少数套件(FirstRunTests 一类)把断言直接写在 main() 里,没有具名的
		// `static func test*`。那种写法按构造不可能漏调,跳过即可 —— 但整体
		// 一个函数都没扫到仍然是 Fatal(见下)。
		defs := defRE.FindAllStringSubmatch(clean, -1)
		for _, m := range defs {
			name := m[1]
			scannedFuncs++
			if !strings.Contains(body, name+"(") {
				t.Errorf("%s 定义了 %s() 但 main() 里没有调用它 —— 它一次都不会跑,"+
					"而套件照样打印通过横幅并退出码 0。把它加进 main()。",
					entry.Name(), name)
			}
		}
	}
	if scannedFiles == 0 {
		t.Fatalf("%s 下一个登记过的 .swift 都没扫到:目录结构或脚本形态变了,"+
			"本守卫已经读不懂它们", testsDir)
	}
	if scannedFuncs == 0 {
		t.Fatal("一个 `static func test*` 都没扫到 —— 见上:安静地扫了零个东西等于没有守卫")
	}
}
