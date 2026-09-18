package supervisor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 每个平台的 RehijackRoutes 都有一段**前置检查**(探网关、判模式、拿设备句柄),
// 它跑完之前一条路由都没被碰过。那一段里的失败必须包上 ErrRehijackNoChange ——
// 否则 liveMutator.Rehijack 的 apply 会把路由就绪位清成 false 并**永远**留在那儿
// (全仓只有「Hijack 成功」与「Rehijack 成功」两处会置真),而路径恢复的 verify
// 与 Guardian 的 health 门都读这一位。2026-09-17 真机上的代价:恢复连败 20 次、
// `bx update` 在 Core 重启前永久失败,而机器其实全程受保护。
//
// **判据不是「函数体里提到过这个哨兵」** —— 那在两处前置检查只标了一处时照样绿,
// 而 linux 与 windows 恰好各有两处。判据是:从函数开头到**第一处真正动路由的
// 调用**之间,每一个 return 都必须带上它。锚点写在下面这张表里,找不到就响亮失败
// —— 一条读不懂现在的代码却安静放行的守卫,在最需要它的时候恰好不可达。
func TestEveryRehijackPreflightFailureIsTaggedAsNoChange(t *testing.T) {
	// 文件 → 该实现里第一处真正修改路由的调用。
	mutationAnchors := map[string]string{
		"platform_darwin.go":  `runCmdQuiet("ifconfig"`,
		"platform_linux.go":   "nc.routeDown()",
		"platform_windows.go": "addPlannedRoutes(",
	}

	files, err := filepath.Glob("platform_*.go")
	if err != nil {
		t.Fatalf("列 platform_*.go: %v", err)
	}
	impls := map[string]string{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("读 %s: %v", f, err)
		}
		src := string(b)
		if strings.Contains(src, ") RehijackRoutes(") {
			impls[filepath.Base(f)] = src
		}
	}
	if len(impls) == 0 {
		t.Fatal("一个 RehijackRoutes 实现都没找到 —— 守卫读不懂现在的代码了")
	}
	for name := range impls {
		if _, ok := mutationAnchors[name]; !ok {
			t.Fatalf("%s 里有一个新的 RehijackRoutes 实现,而这条守卫不知道它的前置检查在哪结束;"+
				"把它的第一处改路由调用加进 mutationAnchors,别直接删掉这条断言", name)
		}
	}
	for name := range mutationAnchors {
		if _, ok := impls[name]; !ok {
			t.Fatalf("mutationAnchors 里的 %s 已经没有 RehijackRoutes 实现了 —— 陈旧条目什么也不守", name)
		}
	}

	for name, src := range impls {
		body, ok := rehijackRoutesBody(src)
		if !ok {
			t.Fatalf("%s:找不到 RehijackRoutes 的函数体 —— 守卫读不懂现在的代码了", name)
		}
		// **先剥注释再找锚点,这一步是判据的一部分。** 第一版没剥,而上面那几段
		// 解释性注释里就写着锚点本身(linux 的注释里有 "nc.routeDown()"),于是
		// 锚点在注释里就命中、前置区段被截成两行,漏标的那个 return 落在区段外面
		// —— 三个实现里有两个是假绿。变异实测才显形:去掉 linux 第二处的标记,
		// 守卫照样通过。
		lines := strings.Split(body, "\n")
		code := make([]string, len(lines))
		for i, line := range lines {
			c := line
			if j := strings.Index(c, "//"); j >= 0 {
				c = c[:j]
			}
			code[i] = c
		}
		anchor := mutationAnchors[name]
		anchorLine := -1
		for i, c := range code {
			if strings.Contains(c, anchor) {
				anchorLine = i
				break
			}
		}
		if anchorLine < 0 {
			t.Fatalf("%s:函数体里找不到锚点 %q(注释不算)—— 第一处改路由的调用换了写法,"+
				"回来把 mutationAnchors 改对", name, anchor)
		}
		for i := 0; i < anchorLine; i++ {
			if !strings.Contains(code[i], "return ") {
				continue
			}
			if !strings.Contains(code[i], "ErrRehijackNoChange") {
				t.Errorf("%s 前置检查第 %d 行的失败没有标成「什么都没改」:%s\n"+
					"  不标的后果是路由就绪位被永久清成 false,而没有任何东西会把它设回来",
					name, i+1, strings.TrimSpace(lines[i]))
			}
		}
	}
}

// rehijackRoutesBody 取出 RehijackRoutes 的函数体(到列首的 "}" 为止)。
func rehijackRoutesBody(src string) (string, bool) {
	i := strings.Index(src, ") RehijackRoutes(")
	if i < 0 {
		return "", false
	}
	rest := src[i:]
	j := strings.Index(rest, "\n}\n")
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}
