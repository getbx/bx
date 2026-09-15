package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// **用户可见的串里不许再出现裸的 `sudo bx <本机命令>`。**
//
// 2026-09-15 在 `030-SJWJ-GSR-B`(Win10)上逐条抓到:`bx explain` 说
// 「要看 bx 的判定先 sudo bx up」、`bx status` 说「启动:sudo bx up」、
// `bx doctor` 三处 HINT、`bx leak-check` 的「下一步」—— 而 **Windows 上没有
// sudo**,照着敲的结果是 `'sudo' 不是内部或外部命令`。每一处的唯一目的都是
// 让人照着敲,本仓库为 `bx force-teardown` 写过同一句话:读到它的人正处在
// 最需要它管用的时刻。
//
// **`bx server` / `bx user` / `bx invite` 刻意豁免** —— 那些命令跑在**那台
// Linux VPS** 上,不是用户手边这台机器。给它们去掉 sudo 才是新造一句假话,
// 而这正是这条守卫差点自己犯的错(第一版的机械替换把它们一起改了)。
func TestNoUserFacingCopyTellsWindowsUsersToRunSudo(t *testing.T) {
	root := filepath.Join("..", "..")
	out, err := exec.Command("git", "-C", root, "ls-files", "*.go").Output()
	if err != nil {
		t.Fatalf("列不出源文件:%v —— 这条守卫读不懂现在的仓库了,先修它", err)
	}
	files := strings.Fields(string(out))
	if len(files) == 0 {
		t.Fatal("一个 .go 文件都没扫到 —— 安静地扫了零个的守卫,与没有这条守卫完全一样")
	}

	// 只认**字符串字面量**:注释与本守卫自己的散文里出现 sudo 是正常的。
	lit := regexp.MustCompile(`"([^"\\]*(?:\\.[^"\\]*)*)"`)
	remote := regexp.MustCompile(`^sudo bx (server|user|invite)\b`)
	scanned := 0
	for _, rel := range files {
		if strings.HasSuffix(rel, "_test.go") {
			continue
		}
		if !strings.HasPrefix(rel, "internal/") && !strings.HasPrefix(rel, "cmd/") {
			continue
		}
		// darwin/linux 专属文件按构造到不了 Windows。
		if strings.HasSuffix(rel, "_darwin.go") || strings.HasSuffix(rel, "_linux.go") {
			continue
		}
		b, rerr := os.ReadFile(filepath.Join(root, rel))
		if rerr != nil {
			t.Fatalf("读不出 %s:%v", rel, rerr)
		}
		scanned++
		for _, m := range lit.FindAllStringSubmatch(string(b), -1) {
			for _, hit := range regexp.MustCompile(`sudo bx [a-z\-]+`).FindAllString(m[1], -1) {
				if remote.MatchString(hit) {
					continue // 跑在 VPS 上的命令,那儿真有 sudo
				}
				t.Errorf("%s 的用户可见串里有 %q —— Windows 上那条命令不存在;"+
					"本机命令要走 elevate.Prefix / elevate.Cmd:%s", rel, hit, m[1])
			}
		}
	}
	if scanned < 100 {
		t.Fatalf("只扫到 %d 个文件 —— 守卫没走到该走的地方", scanned)
	}
}
