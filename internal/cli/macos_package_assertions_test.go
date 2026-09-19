package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// `scripts/verify-macos-release.sh` 逐句钉住发布包里那三份文件的内容,而它**只在
// 发版流水线里跑** —— `scripts/verify.sh` 够不着它。于是「改了生成脚本的文案」与
// 「那些断言」之间的漂移**今天是无人看管的**:本机全绿,打 tag 才红,而那时你正
// 在发版。
//
// 2026-09-18 我自己制造了一次:把包内 README/install.sh 翻成英文,而那七条 grep
// 找的还是中文原句。**是读那个脚本才发现的,不是测试。**
//
// 这条守卫把两边对上,不需要真的打包:正向断言的每一句必须在对应的 heredoc 里
// 找得到,反向断言(`! grep`)的每一句必须找不到。
func TestReleaseVerifierAndPackagerAgreeOnEveryLiteral(t *testing.T) {
	root := filepath.Join("..", "..", "scripts")
	verifier, err := os.ReadFile(filepath.Join(root, "verify-macos-release.sh"))
	if err != nil {
		t.Fatalf("读不到 verify-macos-release.sh —— 守卫失去意义,必须响亮失败:%v", err)
	}
	packager, err := os.ReadFile(filepath.Join(root, "package-macos-release.sh"))
	if err != nil {
		t.Fatalf("读不到 package-macos-release.sh:%v", err)
	}

	bodies := map[string]string{
		"README.txt":   heredocBody(t, string(packager), `cat > "$RELEASE_DIR/README.txt" <<TXT`, "\nTXT\n"),
		"install.sh":   heredocBody(t, string(packager), `cat > "$RELEASE_DIR/install.sh" <<'SCRIPT'`, "\nSCRIPT\n"),
		"uninstall.sh": heredocBody(t, string(packager), `cat > "$RELEASE_DIR/uninstall.sh" <<'SCRIPT'`, "\nSCRIPT\n"),
	}

	// grep -qF [--] <"…"|'…'> "$RELEASE_DIR/<file>"
	line := regexp.MustCompile(`(!\s*)?grep -qF (?:-- )?(?:"((?:[^"\\]|\\.)*)"|'([^']*)') "\$RELEASE_DIR/([A-Za-z.]+)"`)
	checked := 0
	for _, m := range line.FindAllStringSubmatch(string(verifier), -1) {
		negated := strings.TrimSpace(m[1]) == "!"
		literal := m[2]
		if literal == "" {
			literal = m[3]
		}
		// shell 双引号里的 \" 到了 grep 手里是 "。
		literal = strings.ReplaceAll(literal, `\"`, `"`)
		body, ok := bodies[m[4]]
		if !ok {
			continue // 断言的是别的文件(Bx.app 里的东西),不在这条守卫范围内
		}
		checked++
		present := strings.Contains(body, literal)
		if negated && present {
			t.Errorf("verify-macos-release.sh 要求 %s **不含** %q,而生成脚本写进去了",
				m[4], literal)
		}
		if !negated && !present {
			t.Errorf("verify-macos-release.sh 要求 %s 含有 %q,而生成脚本里找不到它 ——\n"+
				"  这条只在发版流水线里跑,本机全绿、打 tag 才红,而那时你正在发版。",
				m[4], literal)
		}
	}
	if checked == 0 {
		t.Fatal("一条断言都没解析出来 —— 判据认不出 verify-macos-release.sh 的写法了,先修守卫")
	}
}

func heredocBody(t *testing.T, script, open, close string) string {
	t.Helper()
	i := strings.Index(script, open)
	if i < 0 {
		t.Fatalf("打包脚本里找不到 %q —— 守卫读不懂它了", open)
	}
	rest := script[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		t.Fatalf("%q 那段 heredoc 没有收尾", open)
	}
	return rest[:j]
}
