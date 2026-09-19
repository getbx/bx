package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

// **一个新用户读到的第一批字**,是 dmg 里那三份东西:README.txt、install.sh 与
// uninstall.sh 打印的话。2026-09-18 之前它们**整片都是中文**,而产品(App、CLI、
// 菜单)这一轮已经全改英文 —— 也就是说唯一还说中文的那个面,恰好是给还没用过
// bx 的人看的那个。
//
// 判据只查**用户可见的行**:以 `#` 开头的是给改这个脚本的人看的注释,与全仓
// 其余部分同一条纪律(注释中文、用户可见文案英文),不在射程内。
func TestTheMacOSPackageSpeaksToUsersInEnglish(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "scripts", "package-macos-release.sh"))
	if err != nil {
		t.Fatalf("读不到打包脚本 —— 这条守卫失去意义,必须响亮失败:%v", err)
	}
	script := string(raw)

	// 三份进包的文件各自由一个 heredoc 生成。找不到任何一个就说明脚本形态变了,
	// 那时必须响亮失败,而不是「没有中文」。
	blocks := map[string][2]string{
		"install.sh":   {`cat > "$RELEASE_DIR/install.sh" <<'SCRIPT'`, "\nSCRIPT\n"},
		"uninstall.sh": {`cat > "$RELEASE_DIR/uninstall.sh" <<'SCRIPT'`, "\nSCRIPT\n"},
		"README.txt":   {`cat > "$RELEASE_DIR/README.txt" <<TXT`, "\nTXT\n"},
	}
	checked := 0
	for name, marks := range blocks {
		start := strings.Index(script, marks[0])
		if start < 0 {
			t.Fatalf("打包脚本里找不到生成 %s 的那段 —— 守卫读不懂它了,先修守卫", name)
		}
		rest := script[start+len(marks[0]):]
		end := strings.Index(rest, marks[1])
		if end < 0 {
			t.Fatalf("%s 那段 heredoc 没有收尾 —— 守卫读不懂它了", name)
		}
		for i, line := range strings.Split(rest[:end], "\n") {
			checked++
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue // 给开发者看的注释
			}
			for _, r := range line {
				if unicode.Is(unicode.Han, r) {
					t.Errorf("%s 第 %d 行是用户会读到的字,而它是中文:%s",
						name, i+1, strings.TrimSpace(line))
					break
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("一行都没查到 —— 判据认不出这些 heredoc 的形状了")
	}
}
