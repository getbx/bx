//go:build darwin

package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// —— 菜单窗口的布局第一次有了闸门(2026-09-17)——
//
// 这半边(AppKit)在 CI 里一行测试都盖不到,于是「按钮跑到窗口外面去了」这类
// 缺陷只能靠人盯着屏幕发现 —— 而本仓库的「真机未验」清单里,菜单那几扇窗口
// 一直是最长的一段。它不必如此:窗口可以**离屏**渲染(不上屏、不抢焦点),
// 渲染出来的视图树是纯文本,而文本可断言。
//
// **闸门钉的是视图树,不是像素。** PNG 给人看(布局、截断、对齐),但像素比对
// 换个系统版本字体一变就全红,而一个会偶发红的闸门比没有闸门更糟。
//
// 它抓到的第一个真缺陷(就是它存在的理由):规则窗口的 Show/Hide 按钮
// `maxX = 420`,正好压在窗口右边缘,而竖直 stack 的右内边距是 18 ——
// 行溢出容器 18pt,按钮被滚动条盖掉半个、点不到。根因是四扇窗口各抄了一份
// 布局组装,而那份拷贝里的行从不被钉到容器宽度(见 MenuLayout.swift)。
//
// **判据不写魔法数字**:内边距由 dump 自己报出来。判「行活在内边距里面」而不是
// 「别超过窗口宽度」—— 后者会放过上面那个缺陷,因为它确实没有「超过」。
func TestMacMenuWindowsKeepEveryControlInsideTheContentWidth(t *testing.T) {
	out := t.TempDir()
	cmd := exec.Command("bash", filepath.Join("..", "..", "scripts", "snapshot-macos-menu.sh"), out)
	combined, err := cmd.CombinedOutput()
	text := string(combined)
	// **「跑不了」与「跑了没过」必须分开报。** 一条安静地扫了零个窗口的守卫,
	// 与没有这条守卫在输出上完全一样,而它看起来更让人放心。
	if strings.Contains(text, "SKIPPED:") {
		t.Skip(strings.TrimSpace(text))
	}
	if err != nil {
		t.Fatalf("快照脚本失败: %v\n%s", err, text)
	}
	if !strings.Contains(text, "macOS menu snapshots passed") {
		t.Fatalf("脚本没跑到收尾横幅 —— 可能中途 exit 0 而一个窗口都没渲染:\n%s", text)
	}

	trees, err := filepath.Glob(filepath.Join(out, "*.tree"))
	if err != nil {
		t.Fatal(err)
	}
	if len(trees) == 0 {
		t.Fatal("一个视图树都没产出 —— 这时候必须响亮失败,而不是「没有越界」")
	}
	for _, tree := range trees {
		t.Run(filepath.Base(tree), func(t *testing.T) {
			checkTree(t, tree)
		})
	}
}

var (
	snapshotHeader = regexp.MustCompile(`^(contentWidth|rightInset) (\d+)$`)
	snapshotMaxX   = regexp.MustCompile(`maxX=(-?\d+)`)
)

func checkTree(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不出 %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	header := map[string]int{}
	for _, line := range lines[:min(2, len(lines))] {
		if m := snapshotHeader.FindStringSubmatch(line); m != nil {
			n, _ := strconv.Atoi(m[2])
			header[m[1]] = n
		}
	}
	width, okW := header["contentWidth"]
	inset, okI := header["rightInset"]
	if !okW || !okI || width <= 0 {
		t.Fatalf("%s 的表头读不出 contentWidth / rightInset —— 守卫读不懂这份 dump 了,先修它", path)
	}
	limit := width - inset

	checked, withText := 0, 0
	for _, line := range lines[2:] {
		// **只查控件。** 滚动视图、clip、contentView 本来就该占满整宽,
		// 把它们算进来会让这条守卫恒红,然后被下一个人删掉。
		if !strings.Contains(line, "NSButton ") && !strings.Contains(line, "NSTextField ") {
			continue
		}
		m := snapshotMaxX.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		maxX, _ := strconv.Atoi(m[1])
		checked++
		if strings.Contains(line, `text="`) && !strings.Contains(line, `text=""`) {
			withText++
		}
		if maxX > limit {
			t.Errorf("控件越过内容右边界(%d > %d = %d-%d):\n  %s\n"+
				"行没有被钉到容器宽度时 AppKit 会老老实实把它画到窗口外面,**而且不报错**;"+
				"用户看到的是一个点不到的按钮。修法在 MenuLayout.swift 的 addFullWidthRow。",
				maxX, limit, width, inset, strings.TrimSpace(line))
		}
	}
	if checked == 0 {
		t.Fatalf("%s 里一个控件都没查到 —— 判据认不出这份 dump 的格式了", path)
	}
	// **一扇空窗口会毫不费力地通过上面每一条断言。**
	//
	// fixture 解成了空结构(wire 形状漂了、字段名写错)时,窗口照样建得出来、
	// 照样渲染,只是什么都没有 —— 而"没有控件越界"在那种图上恒真。
	// 这条守卫因此要求**真的画出了内容**:至少一个带非空文本的控件。
	// 它挡不住"内容是错的",但挡得住"内容是空的",而后者是这套工具最容易
	// 悄悄退化成的样子(与 verify.sh 那几处「收尾横幅」同一条理由)。
	if withText == 0 {
		t.Errorf("%s 里没有任何带文本的控件 —— 这扇窗口是空的。"+
			"多半是 fixture 解成了空结构(wire 形状漂了),而空窗口会通过上面每一条断言", path)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// **窗口里不许再裸调 `stack.addArrangedSubview`。**
//
// 那是缺陷的原形:竖直表的行不被钉到容器宽度时,AppKit 会老老实实按固有宽度
// 把它画到窗口外面 —— **而且不报错**,用户看到的是一个点不到的按钮。
// 四扇窗口各抄了一份布局组装,于是同一个缺陷存在了四份。
//
// 上面那条快照守卫抓的是**结果**(控件越界),这一条抓的是**成因**:
// 结果那条只覆盖今天有 fixture 的那扇窗口,而这一条对四扇都成立。
// 两条都要 —— 少了成因这条,新加的窗口会静默地带着同一个缺陷出生。
func TestMacMenuWindowsUseTheSharedFullWidthRowPrimitive(t *testing.T) {
	dir := filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu")
	entries, err := filepath.Glob(filepath.Join(dir, "*Window.swift"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("一个 *Window.swift 都没扫到 —— 守卫读不懂现在的仓库了,先修它")
	}
	for _, path := range entries {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不出 %s: %v", path, err)
		}
		body := stripSwiftComments(string(raw))
		if strings.Contains(body, "stack.addArrangedSubview(") {
			t.Errorf("%s 里还有裸的 stack.addArrangedSubview —— 用 addFullWidthRow"+
				"(MenuLayout.swift):不钉宽度的行会溢出到窗口外面,行尾按钮点不到",
				filepath.Base(path))
		}
	}
}
