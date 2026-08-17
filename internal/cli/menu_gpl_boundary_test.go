package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 菜单栏 app 与 Go 核心之间那条**许可证边界**。
//
// ## 它守的是一个今天为真、但没人保证的性质
//
// bx 是 GPL-3.0。核心必须继续开源 —— 它的可信度就来自可审计。而 Swift 那个
// app 是**另一个产物**:版权同属项目所有者,所以将来它可以被重新授权
// (brook 就是这么做的:GPL 的 Go CLI 公开,而下载的那个 GUI 从来没进过仓库)。
//
// **这条路只在 app 不链接 GPL 代码时成立。** 今天它成立,而那是「菜单变瘦
// 客户端」那次重构的**副产品** —— 那次改动的目的是修控制面架构,把最后 9 处
// spawn 砍成 0、全走 socket,顺带把这条边界划干净了。
//
// 没有任何东西阻止下一次改动把它破坏掉,**而破坏是静默的**:加一个 cgo 桥、
// 一个静态库、一个 .h —— 编译照过、测试全绿,而「app 可以闭源」这个选项
// 当场消失,且没人会发现。这条守卫就是那个「有人会发现」。
//
// (通行读法:socket IPC 与 spawn 子进程不构成衍生作品。不是法律意见;
// 真要商业化前值得找人确认一次。)
func TestMenuAppDoesNotLinkTheGPLCore(t *testing.T) {
	root := filepath.Join("..", "..", "apps", "macos", "BxMenu")

	manifest, err := os.ReadFile(filepath.Join(root, "Package.swift"))
	if err != nil {
		t.Fatalf("读不到 Package.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	// **清单里不许出现任何把外部二进制/库拉进来的东西。**
	for _, banned := range []string{
		"linkedLibrary", "linkedFramework", "unsafeFlags",
		"systemLibrary", "binaryTarget", "dependencies: [.package",
		".c(", ".cxx(", "cSettings", "cxxSettings",
	} {
		if strings.Contains(string(manifest), banned) {
			t.Errorf("Package.swift 里出现了 %q —— 一旦链接进 GPL 代码,"+
				"这个 app 就再也不能被重新授权了", banned)
		}
	}

	entries, err := os.ReadDir(filepath.Join(root, "Sources", "BxMenu"))
	if err != nil {
		t.Fatalf("读不到 Sources:%v —— 守卫已经失效,先修守卫", err)
	}
	var swift int
	for _, e := range entries {
		name := e.Name()
		// **只允许 .swift。** 一个 .h / .m / .c 出现在这里,就意味着有原生
		// 代码被编进同一个二进制 —— 那正是边界被跨过去的样子。
		if !strings.HasSuffix(name, ".swift") {
			t.Errorf("Sources 里有非 Swift 文件 %q —— 原生代码会被编进同一个二进制", name)
			continue
		}
		swift++
		body, err := os.ReadFile(filepath.Join(root, "Sources", "BxMenu", name))
		if err != nil {
			t.Fatalf("读不到 %s:%v —— 守卫已经失效,先修守卫", name, err)
		}
		for _, banned := range []string{"@_silgen_name", "import CBx", "bridging"} {
			if strings.Contains(string(body), banned) {
				t.Errorf("%s 里出现了 %q —— 那是直接调进 Go 代码的形状", name, banned)
			}
		}
	}
	if swift == 0 {
		t.Fatal("一个 Swift 文件都没找到 —— 守卫已经失效,先修守卫")
	}
}

// **app 碰核心只有两条路:unix socket 与 spawn 子进程。**
//
// 这条与上面那条互补:上面禁的是「编进同一个二进制」,这条钉的是「实际用的
// 就是那两条 arm's-length 的路」—— 否则守卫可以靠「什么都不做」满足。
func TestMenuAppReachesTheCoreOnlyAtArmsLength(t *testing.T) {
	client, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu",
		"Sources", "BxMenu", "GuardianClient.swift"))
	if err != nil {
		t.Fatalf("读不到 GuardianClient.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	// unix socket:进程间,不共享地址空间。
	if !strings.Contains(string(client), "sockaddr_un") && !strings.Contains(string(client), "guardian.sock") {
		t.Fatal("菜单不再经 unix socket 与核心通信 —— 那条许可证边界没了")
	}
	main, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu",
		"Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatalf("读不到 main.swift:%v —— 守卫已经失效,先修守卫", err)
	}
	if !strings.Contains(string(main), "Process()") {
		t.Fatal("菜单不再 spawn bx 子进程 —— 若改成了别的方式,先确认它仍在边界之内")
	}
}
