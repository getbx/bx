package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// **Windows 托盘的盾牌与 macOS 菜单栏的是同一个盾**(所有者 2026-09-28:「托盘状态,这种
// 盾牌就行,其他里面的换成新版」)。轮廓与裂缝的坐标在两处各有一份 —— Swift 的
// shieldOutlinePoints / shieldCrackPoints 与 Python 的 SHIELD_OUTLINE / SHIELD_CRACK ——
// 这里钉住它们逐字相同,否则两个平台上的盾会悄悄长成两个样。
func TestWindowsTrayShieldMatchesTheMacOSOutline(t *testing.T) {
	swift, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "MenuIcon.swift"))
	if err != nil {
		t.Fatal(err)
	}
	py, err := os.ReadFile(filepath.Join("..", "..", "winres", "gen-icons.py"))
	if err != nil {
		t.Fatal(err)
	}
	pair := regexp.MustCompile(`\(\s*([0-9.]+)\s*,\s*([0-9.]+)\s*\)`)
	extract := func(src, name string) string {
		i := strings.Index(src, name)
		if i < 0 {
			t.Fatalf("找不到 %s —— 守卫的锚点漂了", name)
		}
		// Swift 的声明是 `let x: [(x: Double, y: Double)] = [ … ]`:值列表从 `= [` 之后开始,
		// 不是名字后面第一个 `]`(那是类型标注里的)。
		rest := src[i:]
		open := strings.Index(rest, "= [")
		if open < 0 {
			t.Fatalf("%s 没有 `= [`", name)
		}
		rest = rest[open+3:]
		end := strings.Index(rest, "]")
		if end < 0 {
			t.Fatalf("%s 没有收尾", name)
		}
		var pts []string
		for _, m := range pair.FindAllStringSubmatch(rest[:end], -1) {
			pts = append(pts, m[1]+","+m[2])
		}
		if len(pts) == 0 {
			t.Fatalf("%s 里一个点都没读到", name)
		}
		return strings.Join(pts, " ")
	}
	if a, b := extract(string(swift), "shieldOutlinePoints"), extract(string(py), "SHIELD_OUTLINE"); a != b {
		t.Fatalf("盾形轮廓两处不同:\n swift  %s\n python %s", a, b)
	}
	if a, b := extract(string(swift), "shieldCrackPoints"), extract(string(py), "SHIELD_CRACK"); a != b {
		t.Fatalf("裂缝两处不同:\n swift  %s\n python %s", a, b)
	}
	// 四个 ico 都要在,且不是空文件。
	for _, name := range []string{"protected", "warning", "failed", "off"} {
		st, err := os.Stat(filepath.Join("..", "..", "internal", "tray", "icons", name+".ico"))
		if err != nil || st.Size() < 500 {
			t.Fatalf("internal/tray/icons/%s.ico 缺或太小:%v", name, err)
		}
	}
}
