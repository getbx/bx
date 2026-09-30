package cli

import (
	"encoding/json"
	"image"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// iPhone 上「保护」页那面盾,与 macOS 菜单栏、Windows 托盘是同一面盾(形态即状态:实心/空心/虚线/
// 裂开)。坐标在 apps/ios/App/Shield.swift 各抄一份 —— 这里钉住与 Mac 的逐字相同,否则三个平台上的
// 盾会悄悄长成两个样,而任何一边的测试都不会红。
func TestIOSShieldMatchesTheMacOSOutline(t *testing.T) {
	read := func(parts ...string) string {
		b, err := os.ReadFile(filepath.Join(append([]string{"..", ".."}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	mac := read("apps", "macos", "BxMenu", "Sources", "BxMenu", "MenuIcon.swift")
	ios := read("apps", "ios", "App", "Shield.swift")
	pair := regexp.MustCompile(`\(\s*([0-9.]+)\s*,\s*([0-9.]+)\s*\)`)
	extract := func(src, name string) string {
		i := strings.Index(src, "let "+name)
		if i < 0 {
			t.Fatalf("找不到 %s —— 守卫的锚点漂了", name)
		}
		rest := src[i:]
		open := strings.Index(rest, "= [")
		if open < 0 {
			t.Fatalf("%s 没有 `= [`", name)
		}
		rest = rest[open+3:]
		end := strings.Index(rest, "]")
		var pts []string
		for _, m := range pair.FindAllStringSubmatch(rest[:end], -1) {
			pts = append(pts, m[1]+","+m[2])
		}
		if len(pts) == 0 {
			t.Fatalf("%s 里一个点都没读到", name)
		}
		return strings.Join(pts, " ")
	}
	for _, name := range []string{"shieldOutlinePoints", "shieldCrackPoints"} {
		if a, b := extract(mac, name), extract(ios, name); a != b {
			t.Fatalf("%s 两处不同:\n mac %s\n ios %s", name, a, b)
		}
	}
}

// iPhone 的 App 图标:浅色、深色、着色三种外观都在,每张 1024×1024,且**没有 alpha 通道**
// (App Store 拒收带透明通道的图标,而本机构建照过 —— 这类错只在上传那一刻才出现)。
// 工程必须真的指名这个图标集,否则资源编进去了、桌面上仍是白板。
func TestIOSAppIconHasEveryAppearanceAndNoAlpha(t *testing.T) {
	dir := filepath.Join("..", "..", "apps", "ios", "App", "Assets.xcassets", "AppIcon.appiconset")
	raw, err := os.ReadFile(filepath.Join(dir, "Contents.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contents struct {
		Images []struct {
			Filename    string `json:"filename"`
			Size        string `json:"size"`
			Appearances []struct {
				Value string `json:"value"`
			} `json:"appearances"`
		} `json:"images"`
	}
	if err := json.Unmarshal(raw, &contents); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, img := range contents.Images {
		look := "any"
		if len(img.Appearances) > 0 {
			look = img.Appearances[0].Value
		}
		seen[look] = true
		f, err := os.Open(filepath.Join(dir, img.Filename))
		if err != nil {
			t.Fatalf("%s 外观的图标缺文件:%v", look, err)
		}
		m, format, err := image.Decode(f)
		f.Close()
		if err != nil || format != "png" {
			t.Fatalf("%s 不是 png:%v", img.Filename, err)
		}
		if b := m.Bounds(); b.Dx() != 1024 || b.Dy() != 1024 || img.Size != "1024x1024" {
			t.Fatalf("%s 是 %dx%d(声明 %s),要 1024x1024", img.Filename, b.Dx(), b.Dy(), img.Size)
		}
		// 按 PNG 头里的 color type 判,不按解码出的 Go 类型判:image/png 把不带 alpha 的真彩色
		// 也解成 *image.RGBA,按类型判会把每张图都说成「带 alpha」(第一版就这么错过)。
		// 0 = 灰度、2 = 真彩色:没有 alpha;4 / 6 带 alpha;3(调色板)可能挂 tRNS,一并拒。
		png, err := os.ReadFile(filepath.Join(dir, img.Filename))
		if err != nil || len(png) < 26 {
			t.Fatalf("读不了 %s 的 PNG 头:%v", img.Filename, err)
		}
		if ct := png[25]; ct != 0 && ct != 2 {
			t.Fatalf("%s 的 PNG color type 是 %d(带或可能带 alpha):App Store 会拒收", img.Filename, ct)
		}
		if strings.Contains(string(png), "tRNS") {
			t.Fatalf("%s 带 tRNS 透明块:App Store 会拒收", img.Filename)
		}
	}
	for _, look := range []string{"any", "dark", "tinted"} {
		if !seen[look] {
			t.Fatalf("图标缺 %s 外观", look)
		}
	}
	yml, err := os.ReadFile(filepath.Join("..", "..", "apps", "ios", "project.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(yml), "ASSETCATALOG_COMPILER_APPICON_NAME: AppIcon") {
		t.Fatal("project.yml 没有指名 AppIcon:图标编进去了,桌面上仍是白板")
	}
}

// bx:// 链接能从 App 外面打开 App(Mac 部署窗口给的二维码,用相机一扫)。**任何网页都能打开一个
// bx:// 链接**,所以外面来的链接只许走「先确认、说出地址」那一条路:onOpenURL 只交给 receive(link:),
// 而 receive 自己不许 importLink —— 真正换服务器只在用户点了 Add 之后(acceptIncoming)。
func TestIOSOpenedLinksAreConfirmedBeforeUse(t *testing.T) {
	read := func(parts ...string) string {
		b, err := os.ReadFile(filepath.Join(append([]string{"..", "..", "apps", "ios"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	app := read("App", "BxApp.swift")
	i := strings.Index(app, ".onOpenURL")
	if i < 0 {
		t.Fatal("BxApp.swift 没有 .onOpenURL —— 守卫的锚点漂了")
	}
	handler := app[i:min(len(app), i+300)]
	if !strings.Contains(handler, "receive(link:") || strings.Contains(handler, "importLink") {
		t.Fatalf("外面打开的链接没有先确认:%s", handler)
	}
	ctl := read("App", "TunnelController.swift")
	j := strings.Index(ctl, "func receive(link raw: String)")
	k := strings.Index(ctl, "func acceptIncoming()")
	if j < 0 || k < 0 || k < j {
		t.Fatal("读不出 receive / acceptIncoming —— 守卫的锚点漂了")
	}
	if strings.Contains(ctl[j:k], "importLink") {
		t.Fatal("receive 直接 importLink 了 —— 一个网页就能悄悄换掉用户的服务器")
	}
	if !strings.Contains(read("project.yml"), "CFBundleURLSchemes: [bx]") {
		t.Fatal("没有注册 bx:// —— 相机扫了二维码打不开 App")
	}
}
