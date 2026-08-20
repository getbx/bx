package appattr

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// execPathPublicationAllowlist 是**允许提到可执行路径的全部位置**。
//
// `AppRow.ExecPath` 是一次刻意的信息面扩大(见 report.go 里 `Owner` 头上那段):
// 在它之前离开 Core 的只有显示名(「Google Chrome」),现在是完整可执行路径 ——
// 能暴露安装位置、用户名(`/Users/<name>/…`)、以及从 App Store 之外装了什么。
// 今天的发布面**恰好只有一条**:Core 控制 socket → Guardian 的 owner 门 → 菜单
// 那个窗口;它不进 `bx status --json`、不进日志、不进诊断包。
//
// **这条守卫不是冲着「有人故意转发」去的。** 危险形状是将来某个人把
// `appattr.Report` 整个 `%+v` 进一行诊断日志、或者顺手把它塞进一个新的只读端点
// —— 每一步单看都合理,而「发布面扩大靠 review」在这个仓库是已知的弱环(本功能
// 自己的守卫在一支上被绕过八次)。多一处就红,红了就逼作者回来读那段字段注释,
// 想清楚再把自己加进这张表 —— **加进来是可以的,悄悄加不行。**
//
// 形状照 `TestPublicIPProbeDomainsAreNotChinaDirect`:拿真实的树逐个比对,
// 读不动就响亮失败。
var execPathPublicationAllowlist = map[string]string{
	"internal/appattr/owner.go":                               "DisplayName 的形参:把路径**换成**显示名,它不发布路径",
	"internal/appattr/owner_test.go":                          "上面那条的测试",
	"internal/appattr/report.go":                              "字段定义 + 聚合(代表值的选法)",
	"internal/appattr/report_test.go":                         "上面那条的测试",
	"internal/appattr/publication_test.go":                    "这条守卫自己",
	"internal/supervisor/appsource_darwin.go":                 "唯一的生产者:与显示名同源同一次读",
	"internal/supervisor/apptraffic_test.go":                  "端到端穿过 Snapshot 的测试",
	"internal/cli/macos_menu_apptraffic_test.go":              "菜单侧图标接线的文本守卫",
	"apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift":  "唯一的消费者(解码 + 取 .app 包)",
	"apps/macos/BxMenu/Sources/BxMenu/AppTrafficWindow.swift": "画那个图标的地方",
	"apps/macos/BxMenu/Tests/AppTrafficModelTests.swift":      "上面那条的测试",
}

// 可执行路径不许长出第二条发布路径。
func TestExecutablePathHasExactlyOnePublicationPath(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("定位不到仓库根,守卫失去意义:%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("仓库根上没有 go.mod(%s):%v —— 守卫扫错了地方", root, err)
	}
	found := map[string]bool{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() {
			// 隐藏目录(.git/.build/.superpowers)与构建产物里会有源码副本,
			// 它们不是发布面。
			if path != root && (strings.HasPrefix(name, ".") || name == "dist") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, ".swift") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(body)
		if !strings.Contains(text, "ExecPath") && !strings.Contains(text, "exec_path") &&
			!strings.Contains(text, "execPath") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		found[filepath.ToSlash(rel)] = true
		return nil
	})
	if err != nil {
		t.Fatalf("遍历仓库失败,守卫失去意义:%v", err)
	}
	// 一个都没扫到 = 守卫扫空了,不是「没有泄漏」。
	if len(found) == 0 {
		t.Fatal("一处可执行路径都没扫到 —— 守卫读不到源码了,先修守卫")
	}
	// 生产者与消费者必须在场:少了它们说明扫描范围塌了,而不是发布面变干净了。
	for _, required := range []string{
		"internal/supervisor/appsource_darwin.go",
		"apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift",
	} {
		if !found[required] {
			t.Fatalf("连 %s 都没扫到 —— 守卫的扫描范围塌了,先修守卫", required)
		}
	}
	for rel := range found {
		if _, ok := execPathPublicationAllowlist[rel]; !ok {
			t.Errorf("%s 提到了可执行路径,而它不在发布面白名单里 —— "+
				"这是一次信息面扩大(完整可执行路径能暴露安装位置、用户名、装了什么)。"+
				"先读 internal/appattr/report.go 里 Owner 头上那段,想清楚再把自己"+
				"加进 execPathPublicationAllowlist:加进来可以,悄悄加不行。", rel)
		}
	}
}
