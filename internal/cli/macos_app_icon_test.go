package cli

import (
	"fmt"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// **Bx.app 的图标是产品标识,来自设计包的 iconset;菜单栏那个盾牌是保护状态,不动。**
//
// 2026-09-28 所有者:「我记得 logo 改过了,这个弹窗里 bx 也不对」—— 泄漏检测页与
// server ui 早换成了 b+x 的新标,而 Bx.app(Dock、更新弹窗、Finder)仍是 Windows 时代的
// 绿盾 + b,因为打包脚本一直从 winres/icon1024.png 缩出 iconset。现在 iconset 直接
// 来自设计包(~/Downloads/bx_B_integrated_production_v3/macos/Bx.iconset,vendored 进
// apps/macos/BxMenu/Resources),十档尺寸各自单独出图(README 明说别自己缩 1024)。
func TestMacOSAppIconComesFromTheDesignPackIconset(t *testing.T) {
	dir := filepath.Join("..", "..", "apps", "macos", "BxMenu", "Resources", "Bx.iconset")
	want := map[string]int{
		"icon_16x16.png": 16, "icon_16x16@2x.png": 32,
		"icon_32x32.png": 32, "icon_32x32@2x.png": 64,
		"icon_128x128.png": 128, "icon_128x128@2x.png": 256,
		"icon_256x256.png": 256, "icon_256x256@2x.png": 512,
		"icon_512x512.png": 512, "icon_512x512@2x.png": 1024,
	}
	for name, px := range want {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("iconset 缺 %s:%v", name, err)
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil {
			t.Fatalf("%s 不是能读的 PNG:%v", name, err)
		}
		if cfg.Width != px || cfg.Height != px {
			t.Fatalf("%s 是 %dx%d,要 %dx%d(每档单独出图,不是从 1024 缩的)", name, cfg.Width, cfg.Height, px, px)
		}
	}
	packager, err := os.ReadFile(filepath.Join("..", "..", "scripts", "package-macos-menu.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(packager)
	if !strings.Contains(s, "Resources/Bx.iconset") || !strings.Contains(s, "iconutil -c icns") {
		t.Fatal("package-macos-menu.sh 没有从 Resources/Bx.iconset 用 iconutil 出 AppIcon.icns")
	}
	// 钉的是动作不是提及:注释里可以谈论旧源图,但不许再 sips 缩它。
	if strings.Contains(s, `ICON_SRC="$ROOT/winres/icon1024.png"`) || strings.Contains(s, "sips -z") {
		t.Fatal("package-macos-menu.sh 仍从 winres/icon1024.png 缩图标 —— 那是旧的绿盾 + b")
	}
}

// **Windows 的 exe 图标与 macOS 同一个标**(所有者 2026-09-28:「win 上的 ico 也要改吧,统一下」)。
// 设计包 app_icon/light 的 16/32/48/256 四档 PNG vendored 成 winres/bx-{px}.png,winres.json 的
// icon group 直接引用它们(go-winres 只认 PNG,不吃设计包那份 .ico);托盘四态盾牌
// (internal/tray/icons)是保护状态,不动。
func TestWindowsExeIconIsTheDesignPackMark(t *testing.T) {
	for _, px := range []int{16, 32, 48, 256} {
		name := fmt.Sprintf("bx-%d.png", px)
		f, err := os.Open(filepath.Join("..", "..", "winres", name))
		if err != nil {
			t.Fatalf("winres/%s 缺:%v", name, err)
		}
		cfg, _, err := image.DecodeConfig(f)
		f.Close()
		if err != nil || cfg.Width != px || cfg.Height != px {
			t.Fatalf("winres/%s 应是 %dx%d 的 PNG,实际 %dx%d err=%v", name, px, px, cfg.Width, cfg.Height, err)
		}
	}
	cfg, err := os.ReadFile(filepath.Join("..", "..", "winres", "winres.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"bx-256.png"`, `"bx-16.png"`} {
		if !strings.Contains(string(cfg), want) {
			t.Fatalf("winres.json 的 icon group 缺 %s", want)
		}
	}
	if strings.Contains(string(cfg), `"icon.png"`) {
		t.Fatal("winres.json 仍引用 icon.png(旧的绿盾 + b)")
	}
	gen, err := os.ReadFile(filepath.Join("..", "..", "winres", "gen-icons.py"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(gen), `"icon.png"`) || strings.Contains(string(gen), `"icon1024.png"`) {
		t.Fatal("gen-icons.py 仍在生成 exe 图标(icon.png / icon1024.png)—— 它只该生成托盘四态")
	}
}
