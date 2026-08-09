package supervisor

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mergeHostOverrides 的冲突判定必须调用 config.NormalizeHostName,而不是在
// hostsmerge.go 里就地重新拼一套等价逻辑——这正是本包踩过两次的坑:第一次漏了
// 大小写归一,补上后又漏了去尾点,两次都是因为归一化规则活在两份独立实现里,
// 靠人工保持一致。行为测试(本文件其它用例)只能证明"在测过的输入上,两份实现
// 算出来的结果一样",证明不了"这里用的就是同一份代码"——复审把 hostsmerge.go
// 换成一份逐字节行为相同的手写 normalize 函数,所有行为测试原样通过,没有一条
// 报警。能拦住这个的只有直接读源码。
//
// **这条守卫的边界(说清楚,不夸大)**:它只匹配几个具体的标识符
// (strings.ToLower / strings.TrimRight / strings.TrimSuffix)和
// config.NormalizeHostName 的调用次数。绕过它并不难——换个包名导入
// (import . "strings")、手写一个 rune 循环、用 unicode.ToLower、正则替换,
// 都不会被这条测试发现。它抓的是"最省事的那种局部重写"(照抄旧代码里已经出现过
// 两次的那个模式),不是全部可能的重写方式。是一条弱守卫,但它断言的是它真正
// 检查到的东西,不假装比这更强。
func TestHostsMergeUsesSharedNormalizerNotALocalReimplementation(t *testing.T) {
	root, err := repoRootFromWorkingDirSupervisor()
	if err != nil {
		t.Fatalf("定位仓库根目录失败(守卫无法读取源码): %v", err)
	}
	path := filepath.Join(root, "internal", "supervisor", "hostsmerge.go")
	data, err := os.ReadFile(path)
	if err != nil {
		// 读不到就报错,绝不 skip:静默跳过的守卫等于没有守卫。
		t.Fatalf("读取 %s 失败(文件被改名或移动了?守卫必须跟着改): %v", path, err)
	}
	src := string(data)

	if n := strings.Count(src, "config.NormalizeHostName("); n < 2 {
		t.Fatalf("hostsmerge.go 必须调用 config.NormalizeHostName 判定冲突(serverStatic 与 user 两侧各一次),"+
			"实际出现 %d 次。归一化规则只能有一份实现,不得在本文件里重新拼一遍", n)
	}

	for _, banned := range []string{"strings.ToLower(", "strings.TrimRight(", "strings.TrimSuffix("} {
		if strings.Contains(src, banned) {
			t.Fatalf("hostsmerge.go 里出现了 %q——这正是本文件此前两次踩过的坑"+
				"(先漏大小写、后漏尾点)的重写模式,域名归一化必须经 config.NormalizeHostName 完成,不得就地重写", banned)
		}
	}
}

// repoRootFromWorkingDirSupervisor 从测试的工作目录(包目录)逐级上溯找 go.mod。
// internal/install 的同名测试已有一份不导出的实现,这里的重复是刻意的——
// 这两个包互相不应该为了一个测试辅助函数产生依赖。
func repoRootFromWorkingDirSupervisor() (string, error) {
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
