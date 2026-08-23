package appattr

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// scanRepoFor 从仓库根遍历全部 .go/.swift 源码文件,对每个文件的内容跑一次
// match,返回命中文件(相对仓库根)的集合。
//
// **两条覆盖发布面的守卫**(execPathPublicationAllowlist/destPublicationAllowlist)
// 除了「匹配那一句」和各自的白名单表之外,WalkDir、目录跳过(.git/.build/
// .superpowers/dist)、后缀过滤(.go/.swift)逐字重复过 —— 抽成这一个函数,免得
// 将来有人给一条守卫加 `.m` 后缀或加一条 vendor 排除,另一条静默留在旧行为上。
// 本仓库记档过这个根因:「同一个根因修一处漏两处」。
//
// **两张白名单表本身不合并**——brief 禁止的是合并表,不是合并遍历;两次信息面
// 扩大的理由、边界、成员都不同,合并表会让任一方的放宽静默带上另一方。
//
// 遇错直接 `t.Fatalf`:两个调用点都是「守卫读不到源码就立刻失败」,没有谁需要
// 把这个错误继续往上传。
func scanRepoFor(t *testing.T, match func(body []byte) bool) map[string]bool {
	t.Helper()
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
		if !match(body) {
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
	return found
}

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
	found := scanRepoFor(t, func(body []byte) bool {
		text := string(body)
		return strings.Contains(text, "ExecPath") || strings.Contains(text, "exec_path") ||
			strings.Contains(text, "execPath")
	})
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

// destPublicationAllowlist 是**允许提到目的地发布面的全部位置** ——
// 这是这个包的**第二次**刻意信息面扩大(第一次是上面的 execPathPublicationAllowlist)。
//
// 目的地比可执行路径更敏感:一份目的地列表接近「这台机器在访问什么」。发布面与
// ExecPath 完全一致:Core 控制 socket → Guardian 的 owner 门 → 菜单那个窗口;
// 不进 `bx status --json`、不进日志、不进诊断包。见 report.go 里 ConnRecord 头上
// 关于「第二次信息面扩大」的说明。
//
// **不与 execPathPublicationAllowlist 合并成一张表**:两次扩大的理由、边界、
// 白名单成员都不同,合并之后任何一方的放宽都会静默地把另一方也放宽。
//
// **这条守卫扫的是「发布面」,不是「这个词出现过」。** 真正离开 Core 的是
// `AppRow.Dests`、json 标签 `dests`/`dests_more`,以及将来 Swift 侧的
// `destsMore` —— 这几个复数/连字形态在本仓库是独一无二的,扫它们不会误伤任何
// 无关代码。
//
// **单数 `Dest`/`dest` 刻意不在扫描范围内,这是一个明确的取舍,不是遗漏**:
// 那是本仓库里一个通用词,已经被路由目的地前缀(`internal/supervisor/
// windows_routes.go` 的 `winRoute.Dest`)、路由表查询(`internal/leakserve` 的
// `LookupRoute(ctx, dest, ipv6)`)、安装目标路径(`internal/install/
// unified_darwin.go` 的 `AppDestination`)这类既有代码合法占用。第一版守卫扫了
// 单数形式,结果 19 个命中里 12 个是这些无关文件、只能标「不相关」塞进白名单 ——
// 那正是这条守卫要防的失效本身:一张 12/19 都是「不相关」的白名单,读者学到的
// 是「往里加一行就行」,而不是回来读这段注释。收窄之后,`internal/dialer/
// dialer.go` 里 `recordApp` 选值用的那个 `dest` 形参、`internal/supervisor/
// apptraffic.go` 里 live 表的 `dest` 字段,都**不会**被这条守卫扫到 —— 它们是
// 生产者内部的局部变量/字段名,不构成发布;它们真要泄漏,泄的形式是
// `rec.Dest`/结构体字面量把值带出去,那时候值早已经过了 `AppRow.Dests` 这道
// 发布面,已经被这条守卫盯着。
//
// 这不削弱这条守卫要拦的危险形状:将来某个人把 `appattr.Report` 整个 `%+v`
// 进一行诊断日志、或者顺手把它塞进一个新的只读端点 —— 两种情形都会带着
// `Dests`/`dests` 一起出现,照样会被扫到。
var destPublicationAllowlist = map[string]string{
	"internal/appattr/report.go":             "字段定义 + 聚合(去重、封顶、DestsMore)",
	"internal/appattr/report_test.go":        "上面那条的测试",
	"internal/appattr/publication_test.go":   "这条守卫自己",
	"internal/supervisor/apptraffic_test.go": "端到端穿过 Snapshot 的测试(断言 row.Dests)",

	// —— 菜单侧的消费者。**2026-08-22 这条守卫真的拦了一次**:目的地渲染上菜单
	// 时它当场转红,作者因此回来读了上面那段、再把自己加进来 —— 那正是这张表
	// 想要的效果(加进来可以,悄悄加不行)。发布面没有变宽:仍然是 Core 控制
	// socket → Guardian 的 owner 门 → 这一个窗口,不进 `bx status --json`、
	// 不进日志、不进诊断包。
	"apps/macos/BxMenu/Sources/BxMenu/AppTrafficModel.swift": "唯一的消费者:解码 dests/dests_more,算摘要与 toolTip",
}

// destWordPattern 只认发布面会实际出现的复数/连字形态 —— 不是单词边界版的
// `Dest`/`dest`。见上面 destPublicationAllowlist 头上的说明:扫单数会拖进十几个
// 与本功能无关的既有代码(路由目的地前缀、路由表查询、安装目标路径),把白名单
// 变成墙纸。`Dests`/`DestsMore`/`destsMore` 走单词边界,`dests_more` 走单词边界
// (下划线是 \w 的一部分,不会被 `Dests` 那半提前截断)。
//
// **`dests` 走裸单词边界,不再要求带引号。** 修复轮 2 复审实测:只认带引号的
// `"dests"` 会漏掉 Swift 消费者按本仓库既有风格写的
// `enum CodingKeys { case app, conns, dests }`(键名与属性同名时不写字符串,
// `AppTrafficModel.swift:37` 就是这个写法)——这恰是 Task 4 最可能踩的近路,
// 而当时的模式对它视而不见。放宽成裸单词边界之后,已实测**全仓扫描结果不变**
// (仍然只有下面这 4 个文件,一个新的无关命中都没有),故直接放宽模式而不是只在
// 注释里写边界。
var destWordPattern = regexp.MustCompile(`\bDests\b|\bDestsMore\b|\bdestsMore\b|\bdests\b|\bdests_more\b`)

// 目的地不许长出第二条发布路径。
func TestDestinationHasExactlyOnePublicationPath(t *testing.T) {
	found := scanRepoFor(t, destWordPattern.Match)
	if len(found) == 0 {
		t.Fatal("一处目的地都没扫到 —— 守卫读不到源码了,先修守卫")
	}
	// 定义与端到端消费必须在场:少了它们说明扫描范围塌了,而不是发布面变干净了。
	for _, required := range []string{
		"internal/appattr/report.go",
		"internal/supervisor/apptraffic_test.go",
	} {
		if !found[required] {
			t.Fatalf("连 %s 都没扫到 —— 守卫的扫描范围塌了,先修守卫", required)
		}
	}
	for rel := range found {
		if _, ok := destPublicationAllowlist[rel]; !ok {
			t.Errorf("%s 提到了目的地发布面(Dests/DestsMore/dests_more),而它不在"+
				"白名单里 —— 这是一次信息面扩大(一份目的地列表接近「这台机器在访问"+
				"什么」)。先读 internal/appattr/publication_test.go 里"+
				"destPublicationAllowlist 头上那段,想清楚再把自己加进去:"+
				"加进来可以,悄悄加不行。", rel)
		}
	}
}
