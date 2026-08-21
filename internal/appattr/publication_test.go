package appattr

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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

// destPublicationAllowlist 是**允许提到目的地(域名/裸 IP)的全部位置** ——
// 这是这个包的**第二次**刻意信息面扩大(第一次是上面的 execPathPublicationAllowlist)。
//
// 目的地比可执行路径更敏感:一份目的地列表接近「这台机器在访问什么」。发布面与
// ExecPath 完全一致:Core 控制 socket → Guardian 的 owner 门 → 菜单那个窗口;
// 不进 `bx status --json`、不进日志、不进诊断包。见 report.go 里 ConnRecord 头上
// 关于「第二次信息面扩大」的说明。
//
// **不与 execPathPublicationAllowlist 合并成一张表**:两次扩大的理由、边界、
// 白名单成员都不同(`internal/dialer/dialer.go` 是目的地的生产者,但它跟 ExecPath
// 毫无关系;反过来 `internal/supervisor/appsource_darwin.go` 是 ExecPath 的生产者,
// 但它不产生目的地)。合并之后任何一方的放宽都会静默地把另一方也放宽。
//
// `internal/route` 里的文件不进来 —— `route.Meta.Domain` 早就在那儿,不是这次
// 新发布的东西;这张表只扫 `Dest`/`Dests`/`dest`/`dests`/`dests_more` 这几个
// **本次新增**的标识符,别扫 `Domain`(那会把整个路由包和 DNS 包拖进来,守卫
// 立刻变成墙纸)。
//
// **扫描按「单词」而不是子串匹配**(见下面 destWordPattern):`Dest`/`dest` 若按
// 子串扫会命中仓库里大量与本功能无关的既有代码(`Destination`、`DestPort`、
// `AppDestination` 这类网络路由/安装目标路径,和「应用连了谁」毫无关系)。按单词
// 边界扫仍然会命中一批**真正无关**的既有代码(见下面标了「不相关」的条目)——
// 那些用的是同一个词「目的地」但说的是路由/安装,不是这个功能;它们进白名单只是
// 因为文本扫描分不出语境,不代表它们值得被当成信息面扩大来审。
var destPublicationAllowlist = map[string]string{
	"internal/appattr/report.go":             "字段定义 + 聚合(去重、封顶、DestsMore)",
	"internal/appattr/report_test.go":        "上面那条的测试",
	"internal/appattr/publication_test.go":   "这条守卫自己",
	"internal/dialer/dialer.go":              "唯一的生产者:recordApp 从 route.Meta 选值",
	"internal/dialer/apprecorder_test.go":    "上面那条的测试",
	"internal/supervisor/apptraffic.go":      "live 表持有目的地 + 播种",
	"internal/supervisor/apptraffic_test.go": "端到端穿过 Snapshot 的测试(含播种用例)",

	// —— 以下与本功能无关,只是文本扫描分不出「应用连了谁」和「路由/安装的
	// 目的地」是两件事:两者都用「dest」这个词。列在这里是为了让扫描范围保持
	// 全仓(不给 internal/route 之外的包开路径级豁免),而不是因为它们构成
	// 信息面扩大。
	"internal/install/unified_darwin.go":         "不相关:App bundle 安装目标路径(AppDestination)",
	"internal/leakcheck/judge_routes.go":         "不相关:路由目的地 CIDR 解析(ParseRouteDestination)",
	"internal/leakcheck/routes_v6_test.go":       "不相关:上面那条的测试",
	"internal/leakserve/facts.go":                "不相关:LookupRoute(ctx, dest, ipv6) 路由查询",
	"internal/leakserve/facts_darwin.go":         "不相关:同上,darwin 实现",
	"internal/leakserve/facts_linux.go":          "不相关:同上,linux 实现",
	"internal/leakserve/facts_test.go":           "不相关:同上的测试",
	"internal/leakserve/facts_windows.go":        "不相关:同上,windows 实现",
	"internal/leakserve/routes_linux.go":         "不相关:路由表解析(Destination 字段)",
	"internal/supervisor/platform_windows.go":    "不相关:Windows 路由的目的地前缀(netip.Prefix)",
	"internal/supervisor/windows_routes.go":      "不相关:Windows 路由计划里的 Dest 字段(路由 CIDR)",
	"internal/supervisor/windows_routes_test.go": "不相关:上面那条的测试",
}

// destWordPattern 按**单词边界**匹配,不做子串扫描 —— `Dest`/`dest` 若按子串扫
// 会命中 `Destination`/`DestPort`/`AppDestination` 这类与本功能完全无关的既有
// 标识符,把守卫变成对着整个仓库网络代码报警的墙纸。单词边界仍然会命中一批
// **真正无关**的既有代码(见 destPublicationAllowlist 里标了「不相关」的条目)——
// 那是文本扫描分不出语境的代价,已经过白名单显式承认,不是漏网。
var destWordPattern = regexp.MustCompile(`\b(Dest|Dests|dest|dests|dests_more)\b`)

// 目的地不许长出第二条发布路径。
func TestDestinationHasExactlyOnePublicationPath(t *testing.T) {
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
		if !destWordPattern.Match(body) {
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
	if len(found) == 0 {
		t.Fatal("一处目的地都没扫到 —— 守卫读不到源码了,先修守卫")
	}
	// 生产者必须在场:少了它们说明扫描范围塌了,而不是发布面变干净了。
	for _, required := range []string{
		"internal/dialer/dialer.go",
		"internal/supervisor/apptraffic.go",
	} {
		if !found[required] {
			t.Fatalf("连 %s 都没扫到 —— 守卫的扫描范围塌了,先修守卫", required)
		}
	}
	for rel := range found {
		if _, ok := destPublicationAllowlist[rel]; !ok {
			t.Errorf("%s 提到了目的地,而它不在发布面白名单里 —— "+
				"这是一次信息面扩大(一份目的地列表接近「这台机器在访问什么」)。"+
				"先读 internal/appattr/report.go 里 ConnRecord 头上那段,想清楚再把自己"+
				"加进 destPublicationAllowlist:加进来可以,悄悄加不行。", rel)
		}
	}
}
