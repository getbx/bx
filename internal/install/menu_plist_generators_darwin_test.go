//go:build darwin

package install

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 菜单栏 LaunchAgent 的 plist 由**四处**生成器各写一份,而且它们是竞争关系:
// 装机脚本写一份、打包脚本写一份、Go 的 MenuAgentPlistText 写一份,而
// InstanceGate.swift 那份每次菜单启动都会拿去和盘上的文件比对、不一致就覆写。
// 只要有一处漏掉 KeepAlive={SuccessfulExit:false},装对的 plist 就会被它悄悄
// 改回没有 KeepAlive——菜单栏崩溃后再也不自动回来,要等下次 bx up 或下次登录。
// 事实证明只靠 review 守不住(第四处生成器就是这么漏掉的),故落成自动化守卫:
// 直接把生成器源码当 testdata 读进来,逐字比对这段文本。
//
// Go 生成器不在此列——它由 unified_darwin_test.go 对**输出**断言,比读源码更强。
const canonicalKeepAliveBlock = `  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key>
    <false/>
  </dict>`

func TestEveryMenuPlistGeneratorEmitsKeepAliveElseMenuNeverComesBackAfterCrash(t *testing.T) {
	root, err := repoRootFromWorkingDir()
	if err != nil {
		t.Fatalf("定位仓库根目录失败(守卫无法读取生成器源码): %v", err)
	}

	generators := []string{
		"scripts/install-macos-menu.sh",
		"scripts/package-macos-menu.sh",
		"apps/macos/BxMenu/Sources/BxMenu/InstanceGate.swift",
	}

	for _, rel := range generators {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			// 文件读不到就报错,绝不 skip:静默跳过的守卫等于没有守卫。
			t.Errorf("%s: 读取失败(生成器被改名或移动了?守卫必须跟着改): %v", rel, err)
			continue
		}
		if !containsBlockAtAnyIndent(string(data), canonicalKeepAliveBlock) {
			t.Errorf("%s 缺少 KeepAlive={SuccessfulExit:false};四处 plist 生成器必须逐字一致,"+
				"否则装对的 plist 会被漏写的那处覆写掉。需要的文本(允许整块统一缩进):\n%s",
				rel, canonicalKeepAliveBlock)
		}
	}
}

// containsBlockAtAnyIndent 报告 text 是否含有 block,允许整块被统一多缩进若干空白
// ——Swift 的多行字符串字面量在源码里比渲染结果多缩进一层(相对结束定界符会被剥
// 掉),故按源码逐字找会假阴性;这里按「块内相对缩进一致 + 整块前缀相同」比对。
func containsBlockAtAnyIndent(text, block string) bool {
	lines := strings.Split(block, "\n")
	first := strings.TrimLeft(lines[0], " \t")
	baseIndent := lines[0][:len(lines[0])-len(first)]

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed != first {
			continue
		}
		found := line[:len(line)-len(trimmed)]
		if !strings.HasSuffix(found, baseIndent) {
			continue
		}
		extra := found[:len(found)-len(baseIndent)]
		var want strings.Builder
		for i, l := range lines {
			if i > 0 {
				want.WriteString("\n")
			}
			want.WriteString(extra)
			want.WriteString(l)
		}
		if strings.Contains(text, want.String()) {
			return true
		}
	}
	return false
}

// repoRootFromWorkingDir 从测试的工作目录(包目录)逐级上溯找 go.mod,避免把
// 仓库路径写死。
func repoRootFromWorkingDir() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("从工作目录上溯未找到 go.mod")
		}
		dir = parent
	}
}
