package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/verify.sh 是「提交前验过了」这句话的唯一实现。**它漏掉一步,就等于
// 那一步从此不再被验**,而漏掉的表现是安静的:verify 照样打印 ✓。
//
// 这里钉的是**清单**(哪些步骤必须在场),不是语义 —— 步骤本身对不对由它们各自
// 的工具负责,而「有没有这一步」正是文本匹配恰当的场合(同
// menu_plist_generators_darwin_test.go 那条守卫的定位)。
//
// 读不到脚本必须响亮失败:一个因为拿不到文件而自动通过的守卫,与没有守卫是同一回事。
func TestVerifyScriptCoversEveryGate(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "verify.sh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到 %s:%v —— 这条守卫失去意义,必须响亮失败而不是放过", path, err)
	}
	script := string(raw)

	for _, gate := range []struct{ needle, why string }{
		{"go build ./...", "编译"},
		{"go vet ./...", "vet"},
		{"go test ./... -count=1", "全量单测(必须带 -count=1,否则可能全是缓存)"},
		{"gofumpt", "格式漂移"},
		{"go test -race", "竞态"},
		{"GOOS=", "交叉编译"},
		{"swift build --package-path apps/macos/BxMenu", "Swift 侧编译(Tests/ 不属于任何 target,只有它编 Sources/)"},
		{"scripts/test-macos-menu.sh", "Swift 测试套件(swift build 编不到 Tests/,两步不可互相替代)"},
		{"macOS menu tests passed", "收尾横幅 —— 脚本提前 exit 0 时退出码是 0,只有它抓得住"},
	} {
		if !strings.Contains(script, gate.needle) {
			t.Errorf("verify.sh 缺少 %q(%s)—— 少一步就等于那一步从此不再被验,而它照样打印 ✓",
				gate.needle, gate.why)
		}
	}

	// **最后一行必须是判据。** 脚本刻意不用 `set -e`(要一次跑完知道全部坏了什么),
	// 代价是每一步都自己收退出码 —— 而漏掉最后那个 exit 的话,它会在有步骤失败时
	// 依然退 0,正好变成它要消灭的那种东西。
	if !strings.Contains(script, `if [ "$failed" -ne 0 ]`) {
		t.Error("verify.sh 没有在结尾按累计失败数退出 —— 它自己会变成一个不会失败的检查")
	}
	// 跳过必须与通过长得不一样,否则「在 Linux 上跑过 verify」会被读成
	// 「macOS 那半也验过了」。
	if !strings.Contains(script, "SKIPPED") {
		t.Error("verify.sh 必须把跳过的步骤显式标出来,不能与通过混为一谈")
	}
}
