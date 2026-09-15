package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/getbx/bx/internal/supervisor"
)

// 服务器清单在菜单里的**接线**守卫。
//
// 判断本身住在 ServersModel.swift 的纯函数里,由 Swift 套件钉住;**调不调它们
// 在 main.swift,而 main.swift 编不进 scripts/test-macos-menu.sh**(它要 AppKit)。
// 漏接线不会有任何编译错误,也不会有任何 Swift 测试转红 —— 这几条是唯一在 CI 里
// 真正跑着的证明。
//
// 教训在前:这类文本守卫在本仓库被攻破过八次,形状都一样 —— 钉拼法而不是语义。
// 所以下面每一条钉的都是「某个判据出现在某个函数体里」,而不是某个字符串存在;
// 并且**读不懂现在的代码时一律 t.Fatal 响亮失败**,绝不静默放行。

func menuMainSwiftSource(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "..", "apps", "macos", "BxMenu", "Sources", "BxMenu", "main.swift"))
	if err != nil {
		t.Fatalf("读不到 main.swift:%v", err)
	}
	return string(source)
}

// 服务器入口只在 Guardian 声明了 servers 能力时出现。
//
// **键缺席 = 旧版 Guardian**,那时画出来的按钮每次点都失败,而用户看不出为什么。
// 判据必须是 serverSwitchingAvailable,不是 rulesEditingAvailable —— 后者在
// 「装了带规则、不带服务器的那一版」上会把入口画出来。
func TestMacMenuServersEntryIsGatedByItsOwnCapability(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t), "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu() 的函数体 —— 守卫已经失效,先修守卫")
	}
	const action = "#selector(openServersWindow)"
	idx := strings.Index(body, action)
	if idx < 0 {
		t.Fatal("rebuildMenu() 里没有服务器入口 —— 用户在菜单里看不到服务器清单")
	}
	// 往回找**最近**的一个 if,它必须是这个能力判据。固定字节窗口在本仓库被
	// 邻近函数满足过,所以这里找的是「包着它的那个条件」。
	before := body[:idx]
	gate := strings.LastIndex(before, "if serverSwitchingAvailable(")
	other := strings.LastIndex(before, "if ")
	if gate < 0 || gate != other {
		t.Fatalf("服务器入口不是由 serverSwitchingAvailable 直接门控的 —— "+
			"最近的条件在 %d,能力判据在 %d", other, gate)
	}
}

// **换服务器之前必须先确认。**
//
// 换出口是有后果的事(正在登录的会话、风控、正在下载的东西),这正是项目所有者
// 拒绝自动容灾的理由:自动切会在用户不知情时换掉出口 IP。既然选择交给人,那就
// 必须让人先看见后果 —— 确认框在**发出请求之前**,不是之后。
func TestMacMenuConfirmsBeforeSwitchingServer(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func confirmAndSwitchServer(name: String, host: String)")
	if !ok {
		t.Fatal("读不出 confirmAndSwitchServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	confirm := strings.Index(body, "alert.runModal() == .alertFirstButtonReturn")
	if confirm < 0 {
		t.Fatal("没有等用户确认就换服务器")
	}
	if !strings.Contains(body[:confirm], "serverSwitchConfirmMessage(") {
		t.Error("确认框的文案不是 serverSwitchConfirmMessage —— 那句话负责点明会换出口 IP")
	}
	send := strings.Index(body, "switchServer(name:")
	if send < 0 {
		t.Fatal("函数体里根本没有发出切换请求")
	}
	if send < confirm {
		t.Fatal("请求发在确认之前 —— 用户点「取消」时出口已经换掉了")
	}
	// **`guard … else { return }`**:确认框回的不是第一个按钮时必须原地返回。
	// 少了它,「取消」与「切换」的效果一模一样。
	if !strings.Contains(body[confirm:], "else { return }") {
		t.Error("用户点取消之后没有原地返回")
	}
}

// **热切没成功时不许说「已切换」。**
//
// 服务端把「配置写好了」与「正在跑的实例也换过去了」分成两件事报(applied),
// 而这条守卫钉的是**菜单真的读了它**:文案由 serverSwitchOutcomeMessage 生成、
// 标题由 outcome.applied 分支。写死一句 "Switched" 不会有编译错误,也不会让
// 任何 Swift 测试转红 —— 那正是 `bx server use` 第一版那句谎的形状。
func TestMacMenuNeverClaimsASwitchThatDidNotApply(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func confirmAndSwitchServer(name: String, host: String)")
	if !ok {
		t.Fatal("读不出 confirmAndSwitchServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	// 切换那一半已经搬进无确认的 switchServer(Add Server 那条路要复用它),
	// 结局仍由 confirmAndSwitchServer 自己的 completion 渲染 —— 锚点跟着搬。
	success := strings.Index(body, "switchServer(name: name) { outcome in")
	if success < 0 {
		t.Fatal("读不出结局回调 —— 守卫已经失效,先修守卫")
	}
	tail := body[success:]
	if !strings.Contains(tail, "outcome.applied ?") {
		t.Error("标题没有按 outcome.applied 分支 —— 只写了配置也会显示成「已切换」")
	}
	if !strings.Contains(tail, "switchOutcomeMessage(outcome)") {
		t.Error("文案不是 switchOutcomeMessage 生成的 —— " +
			"那个纯函数才是「四种结局四句话」的所在(其中两句此前是错的:" +
			"已生效但确认失败被说成没切过去,回滚失败被说成关了再开就行)")
	}
}

// 换过去之后旧的探测结果必须作废。留着它,用户读到的是**上一台**的出口 IP,
// 而他刚做的恰恰是换出口 —— 这是这个界面最容易骗到人的一处。
func TestMacMenuDropsTheStaleExitIPAfterSwitching(t *testing.T) {
	// 作废那一步住在无确认的 switchServer 里 —— 两条路(确认框那条与
	// Add Server 那条)都经它,所以守在这儿才守得住两条。
	body, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func switchServer(name: String, completion:")
	if !ok {
		t.Fatal("读不出 switchServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	reset := strings.Index(body, "exitIPProbe = .unknown")
	send := strings.Index(body, "GuardianClient().switchServer(name:")
	if reset < 0 {
		t.Fatal("换服务器之后没有作废旧的出口 IP 探测结果")
	}
	if send >= 0 && reset > send {
		t.Error("作废发生在请求之后 —— 中间那段时间界面显示的是上一台的出口")
	}
}

// **出口探测由菜单自己发,不经 Guardian。**
//
// 菜单以普通用户身份跑,它的流量和浏览器走同一条路 —— 那才是「网站看到的是
// 什么」的忠实答案。而让一个 root 守护进程再多长一条对外请求的能力,换不来更准的
// 结果:/v1/update-check 是本地 socket 上**唯一**一个能让 root 守护进程出网的端点,
// 这条守卫在这里的作用是让它保持唯一。
//
// 端点必须是那个过了「不在 china 直连列表」守卫的域名。用一个在列表里的域名
// (ipify 就栽过,而且是文档自己推荐错的)会让探测走直连、报出用户真实的 ISP 出口,
// 方向正好相反。
func TestMacMenuExitIPProbeUsesTheVettedEndpointAndValidatesTheAnswer(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t), "private func checkExitIP()")
	if !ok {
		t.Fatal("读不出 checkExitIP() 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(body, "https://ipv4.icanhazip.com") {
		t.Error("探测端点不是 icanhazip —— 换端点之前先过「不在 china 直连列表」那道守卫")
	}
	for _, banned := range []string{"ipify.org", "ifconfig.me", "ifconfig.co", "ipapi.co"} {
		if strings.Contains(body, banned) {
			t.Errorf("用了 %s —— 它在 china 直连列表里,探测会走直连报出真实 ISP 出口", banned)
		}
	}
	if !strings.Contains(body, "parseExitIPResponse(") {
		t.Error("应答没有过校验 —— 一段 HTML 错误页会被原样当成「你的出口 IP」显示出来")
	}
	if !strings.Contains(body, "?? .failed") {
		t.Error("解不出来时没有落到 .failed —— 「没问出来」不许被说成某个具体答案")
	}
}

// **部署表单不许经手 SSH 凭据。**
//
// 这是 `bx server deploy` 从第一天起的设计(密码/密钥/agent/known_hosts 全归系统
// ssh),GUI 不该把它推翻。菜单是 LSUIElement 应用、没有 TTY —— 真在 app 里收
// 密码,就等于既推翻了那条设计,又要自己保管一个我们没有能力保管的东西。
//
// 守卫钉的是**语义**:交给 Terminal 的那段脚本必须由 deployScriptText 生成,
// 而这个函数体里不许出现任何收密码的迹象。
func TestMacMenuDeployNeverHandlesSSHCredentials(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func handOffDeployToTerminal(_ target: DeployTarget)")
	if !ok {
		t.Fatal("读不出 handOffDeployToTerminal 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(body, "deployScriptText(target)") {
		t.Error("交给 Terminal 的不是 deployScriptText 生成的脚本 —— " +
			"手拼一条命令会绕过那个纯函数里的引号转义")
	}
	for _, smell := range []string{
		"password", "Password", "passphrase", "sshpass",
		"SSH_ASKPASS", "secureTextField", "NSSecureTextField",
	} {
		if strings.Contains(body, smell) {
			t.Errorf("部署路径里出现了 %q —— bx 不经手 SSH 凭据", smell)
		}
	}
	// 整个菜单 app 里都不许有密码输入框:这条比上面那条宽,挡的是「换个函数
	// 再收一次」。
	if strings.Contains(menuMainSwiftSource(t), "NSSecureTextField") {
		t.Error("菜单里出现了密码输入框")
	}
}

// 临时脚本里有目标主机与登录名,权限必须是 0700。
//
// 它落在 /tmp,那是**所有用户都能读**的目录 —— 默认权限会把「这台 Mac 的主人
// 在管理哪几台机器、用什么登录名」交给本机任何一个进程。
func TestMacMenuDeployScriptIsNotWorldReadable(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func handOffDeployToTerminal(_ target: DeployTarget)")
	if !ok {
		t.Fatal("读不出 handOffDeployToTerminal 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(body, "0o700") {
		t.Error("临时脚本没有收紧到 0700 —— 它落在 /tmp,里面有目标主机与登录名")
	}
	write := strings.Index(body, "write(toFile:")
	open := strings.Index(body, "NSWorkspace.shared.open(")
	perm := strings.Index(body, "posixPermissions")
	if write < 0 || open < 0 || perm < 0 {
		t.Fatal("读不出写盘 / 收权限 / 打开这三步 —— 守卫已经失效,先修守卫")
	}
	if !(write < perm && perm < open) {
		t.Errorf("顺序不对(写=%d 收权限=%d 打开=%d)—— 必须先收紧再交出去", write, perm, open)
	}
}

// **测不成不许把服务器画成红的。**
//
// 探测失败最常见的原因是 bx 没在跑(直连拨号器在 Core 手里)。把那种情况画成
// 「不可达」,等于把一整排好服务器说成坏的,而用户据此去换服务器 —— 与 ipify
// 那次同一类、方向相反的错。失败分支必须保持上一轮的清单原样。
func TestMacMenuProbeFailureDoesNotPaintServersRed(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t), "private func probeServers()")
	if !ok {
		t.Fatal("读不出 probeServers() 的函数体 —— 守卫已经失效,先修守卫")
	}
	failure := strings.Index(body, "case .failure(let error):")
	if failure < 0 {
		t.Fatal("读不出失败分支 —— 守卫已经失效,先修守卫")
	}
	tail := body[failure:]
	// 失败分支重画时用的必须是**缓存**(lastServers),而不是任何新数据。
	if !strings.Contains(tail, "self.presentServers(self.lastServers ?? ServerList()") {
		t.Error("失败分支没有保持上一轮的清单原样 —— 一次测不成会把界面清空或标红")
	}
	if strings.Contains(tail, "self.lastServers =") {
		t.Error("失败分支改写了缓存 —— 「没问出来」被写成了一个具体答案")
	}
}

// 探测**只在用户点的时候发**:它走在隧道外面,会让网络上看得见这台机器联系过
// 那几个地址。绝不挂在任何定时器上(与 2026-08-13 否掉菜单栏常驻红字同一条理由:
// 一个隐私工具不该定期发不受保护的流量去确认不受保护的路还通)。
func TestMacMenuNeverProbesServersOnATimer(t *testing.T) {
	source := menuMainSwiftSource(t)
	body, ok := swiftFunctionBody(source, "private func loadState(reconnectInFlight: Bool, snapshot: RecoverySnapshot?)")
	if !ok {
		t.Fatal("读不出 loadState 的函数体 —— 守卫已经失效,先修守卫")
	}
	if strings.Contains(body, "probeServers()") {
		t.Fatal("轮询路径里发起了探测 —— 那是定期向隧道外面发包")
	}
	// 唯一的调用点必须挂在窗口那个按钮的回调上。
	callers := strings.Count(source, "self?.probeServers()") + strings.Count(source, "self.probeServers()")
	if callers != 1 {
		t.Errorf("probeServers 有 %d 个调用点,应当只有「用户点了 Test All」那一个", callers)
	}
	if !strings.Contains(source, "controller.onProbe = { [weak self] in") {
		t.Error("那唯一的调用点不是窗口按钮的回调")
	}
}

// **构建产物不许被 Spotlight 索引。**
//
// 真机上实测的后果:搜 "bx" 出来五个 Bx.app,四个是 dist 下的构建产物 ——
// 而点开其中一个,跑的是一份陈旧的菜单二进制,对着当前的 Guardian。那不是噪声,
// 是一条会真的坏事的路。
//
// `.metadata_never_index` 只在**卷根目录**生效;同一台机器上那个文件在、目录
// 照样被索引。macOS 真正认的是**目录名以 .noindex 结尾**。这条守卫钉住两件事:
// 产物落在 .noindex 下,且那个失效的机制没有被重新引入。
func TestBuildArtifactsStayOutOfSpotlight(t *testing.T) {
	for _, name := range []string{
		"package-macos-release.sh", "package-macos-menu.sh",
		"package-macos-dmg.sh", "verify-macos-release.sh",
	} {
		path := filepath.Join("..", "..", "scripts", name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读不到 %s:%v —— 守卫已经失效,先修守卫", name, err)
		}
		text := string(data)
		if strings.Contains(text, `touch "$`) && strings.Contains(text, ".metadata_never_index\"") {
			t.Errorf("%s 又用回了 .metadata_never_index —— 它对非卷根目录不生效", name)
		}
		// 凡是提到 dist 的地方都必须是 dist.noindex。
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue // 注释里可以谈论旧路径
			}
			if strings.Contains(line, "dist/") {
				t.Errorf("%s 仍然把产物放进会被索引的 dist/:%s", name, strings.TrimSpace(line))
			}
		}
	}
}

// **README 承诺的安装方式必须真的被生成出来。**
//
// 在此之前 README.txt 第一行写着「安装(推荐用 .dmg)」,而 package-macos-dmg.sh
// **一次都没有被调用过** —— 脚本写好了、放在那儿、没人跑。于是每一个拿到这个包的
// 人都被指向一个不存在的文件。
func TestReleasePackagingActuallyBuildsTheDMGItRecommends(t *testing.T) {
	path := filepath.Join("..", "..", "scripts", "package-macos-release.sh")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读不到打包脚本:%v —— 守卫已经失效,先修守卫", err)
	}
	text := string(data)
	if !strings.Contains(text, "推荐用 .dmg") {
		t.Fatal("README 文案变了 —— 守卫已经失效,先修守卫")
	}
	// **必须剥掉注释再判。** 第一版直接在全文里找 "package-macos-dmg.sh",
	// 而调用点上方那段解释性注释里正好提到了它 —— 于是把调用整行删掉,守卫
	// 照样绿。「断言被代码自己的注释兜绿」是本仓库记录过的形状,这里当场又栽了一次。
	var code []string
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); !strings.HasPrefix(trimmed, "#") {
			code = append(code, line)
		}
	}
	if !strings.Contains(strings.Join(code, "\n"), "package-macos-dmg.sh") {
		t.Fatal("README 推荐 .dmg,而打包脚本从不生成它 —— 用户被指向一个不存在的文件")
	}
}

// **菜单必须有一个卸载入口。**
//
// 在此之前一个都没有:于是一个想删掉 bx 的普通用户,唯一显而易见的动作是把
// Bx.app 拖进废纸篓 —— 而那**只删掉界面**。Guardian 仍以 root 在跑、保护仍然
// 开着、DNS 与路由仍然被接管,而他刚好删掉了唯一能关掉它的那个东西;此后只剩
// 终端一条路,没有终端的人就卡在那里。这是「关不掉」那一类里最容易发生的一种。
func TestMacMenuOffersAWayToUninstall(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t), "private func rebuildMenu()")
	if !ok {
		t.Fatal("读不出 rebuildMenu() 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(body, "#selector(uninstallBx)") {
		t.Fatal("菜单里没有卸载入口 —— 用户只剩「拖进废纸篓」那条会把自己锁住的路")
	}
	// **按花括号深度钉住它在 switch 之外。**
	//
	// 只查「存在」抓不到「挪回某个 case 里」—— 挪进去之后出现次数还是 1,而
	// 卸载入口会在 .off / .missing / .setupNeeded 那几个状态下消失。**想删掉 bx
	// 的人最可能正处在那几个状态**,那正是这个入口存在的理由。
	// (退出项被同样的形状咬过一次:TestMacMenuQuitActionPresentInEveryState。)
	//
	// 允许深度 1:它包在 `if cliIsInstalled()` 里,那是刻意的(没装就没什么可卸)。
	depth, outside := 0, 0
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if depth <= 1 && !strings.HasPrefix(trimmed, "//") && strings.Contains(trimmed, "#selector(uninstallBx)") {
			outside++
		}
		depth += strings.Count(line, "{") - strings.Count(line, "}")
	}
	if outside != 1 {
		t.Fatalf("卸载入口必须在 switch 之外(最多包一层 cliIsInstalled 判断),"+
			"实际在那个深度出现 %d 次 —— 挪进某个 case 会让它在最需要它的状态里消失", outside)
	}
}

// **卸载走系统里那个 bx,而且不许带 `bx uninstall` 没有的 flag。**
//
// ① 内嵌那份属于这个 bundle,而 bundle 马上就要被删掉;要停 launchd 服务、
//
//	拆路由的是系统上正在跑的那一套。
//
// ② `bx uninstall` 没有 --yes(它只要求 root,不问确认),而 urfave/cli 遇到
//
//	未知 flag 直接失败 —— 失败在 osascript 里,用户看到的只是「没成功」,
//	完全不知道为什么。**第一版就写错成 --yes,是查了 CLI 定义才发现的。**
func TestMacMenuUninstallInvokesTheSystemCLIWithNoBogusFlags(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t), "private func uninstallBx()")
	if !ok {
		t.Fatal("读不出 uninstallBx() 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(body, "shellSingleQuoted(bxPath)") {
		t.Error("没有用系统里那个 bx —— 内嵌那份随 bundle 一起会被删掉")
	}
	if strings.Contains(body, "uninstall --") {
		t.Error("给 `bx uninstall` 带了 flag —— 它一个都不接受,会在 osascript 里静默失败")
	}
	// 确认必须在提权之前。
	confirm := strings.Index(body, "alert.runModal() == .alertFirstButtonReturn")
	run := strings.Index(body, "runPrivileged(")
	if confirm < 0 || run < 0 {
		t.Fatal("读不出确认或提权那两步 —— 守卫已经失效,先修守卫")
	}
	if run < confirm {
		t.Fatal("没等确认就提权卸载了")
	}
}

// **一次只许有一次切换在飞。**
//
// 一次切换是「写配置 → 武装 → 等隧道健康 → 确认」四步、最长二十几秒,而窗口
// 在那期间一直开着 —— 再点一下是很自然的动作。服务端也会拒(409),菜单这一道
// 是为了不让用户撞上一个他看不懂的失败。
//
// **必须在弹确认框之前就挡住**:挡在后面的话,用户会看到一个确认框、点了确认、
// 然后什么都没发生。
func TestMacMenuRefusesASecondSwitchWhileOneIsInFlight(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func confirmAndSwitchServer(name: String, host: String)")
	if !ok {
		t.Fatal("读不出 confirmAndSwitchServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	guardAt := strings.Index(body, "guard switchingTo == nil else { return }")
	confirm := strings.Index(body, "alert.runModal() == .alertFirstButtonReturn")
	if guardAt < 0 {
		t.Fatal("没有在飞守卫 —— 用户能在二十几秒的窗口里再点一次")
	}
	if confirm >= 0 && guardAt > confirm {
		t.Error("守卫在确认框之后 —— 用户会看到确认框、点了确认、然后什么都没发生")
	}
	// 真正发请求的那一半在无确认的 switchServer 里:它自己也要挡一道
	// (Add Server 那条路不经确认框),而且标志位必须被放开,否则第一次之后
	// 永远切不了。
	send, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func switchServer(name: String, completion:")
	if !ok {
		t.Fatal("读不出 switchServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(send, "guard switchingTo == nil else {") {
		t.Error("switchServer 自己没有在飞守卫 —— Add Server 那条路不经确认框,挡不住第二次")
	}
	if !strings.Contains(send, "self.switchingTo = nil") {
		t.Error("没有放开在飞标志 —— 第一次切换之后就再也切不了了")
	}
	// **在飞状态必须到得了窗口,而且是在请求发出**之前**。**
	//
	// 它此前是 main.swift 的私有量,于是用户点完确认之后二十几秒屏幕上什么都
	// 不发生、再点一次连对话框都不弹(被上面那道守卫挡掉,而那道守卫是对的)。
	// 判据打在**顺序**上:设了标志却不重画,与压根没设在屏幕上完全一样;
	// 重画排在 `async` 之后就是二十几秒之后才画,等于没有。
	set := strings.Index(send, "switchingTo = name")
	paint := strings.Index(send, "presentServers(")
	async := strings.Index(send, "DispatchQueue.global(")
	if set < 0 {
		t.Fatal("switchServer 没有记下正在切哪一台 —— 窗口无从在那一行上说 Switching…")
	}
	if paint < 0 || paint < set {
		t.Error("记下在飞状态之后没有立刻重画 —— 用户点完确认之后屏幕上什么都不发生")
	}
	if async >= 0 && paint > async {
		t.Errorf("那次重画落在后台队列之后(paint=%d async=%d)—— 等它画出来切换早就结束了", paint, async)
	}
}

// **探测失败的原因码是一条跨语言契约,而它此前根本不存在 —— 服务端那句中文
// 直接被端进了全英文菜单。**
//
// `supervisor.probeServer` 对一台关着的服务器把 Error 填成「连接被拒(端口没在听)」,
// 那是最常见的失败路径;它经 Guardian 的 ProbeReport 一路流到服务器窗口那一行,
// 还被画成红的。`TestMacMenuUserFacingStringsAreEnglish` 只扫菜单自己的源码,
// **看不见从服务端来的字符串**,所以没有任何东西会红。
//
// 修法是服务端发码、菜单出话。这条守卫钉住那条契约的两头:
//   - Go 能发出的每一个码,菜单里都得有一句英文(少一句就静默退回笼统的兜底);
//   - 菜单里的每一条,都得是一个真的码(陈旧条目与生效中的长得一模一样,什么也不守)。
//
// 还有第三件,而它才是让这类 bug 在构造上不可能的那一件:菜单**根本不解**
// 服务端那个 `error` 键。解出来就迟早有人显示它。
func TestProbeErrorCodesAllHaveAnEnglishSentenceInTheMenu(t *testing.T) {
	model := stripSwiftComments(readMenuSwiftSource(t, "ServersModel.swift"))
	body, ok := swiftFunctionBody(model, "func probeFailureText(code: String, fallback: String) -> String")
	if !ok {
		t.Fatal("读不出 probeFailureText 的函数体 —— 守卫已经失效,先修守卫")
	}

	inMenu := map[string]bool{}
	for _, m := range regexp.MustCompile(`case "([a-z_]+)":`).FindAllStringSubmatch(body, -1) {
		inMenu[m[1]] = true
	}
	if len(inMenu) == 0 {
		t.Fatal("probeFailureText 里一条 case 都没有 —— 守卫已经失效,先修守卫")
	}

	known := map[string]bool{}
	for _, code := range supervisor.ProbeErrorCodes {
		known[code] = true
		if !inMenu[code] {
			t.Errorf("码 %q 在菜单里没有对应的英文 —— 那一行会静默退回笼统的兜底文案,"+
				"而没有任何东西会报错", code)
		}
	}
	for code := range inMenu {
		if !known[code] {
			t.Errorf("菜单里有一条 %q,而 supervisor.ProbeErrorCodes 里没有这个码 —— "+
				"陈旧条目与生效中的长得一模一样,什么也不守", code)
		}
	}

	// **菜单不许解服务端那句人话。** 它是中文的(它服务 `bx server list`),
	// 而这个界面通篇英文;不解这个键,是让「显示它」在构造上不可能。
	// **判据打在 JSON 键的原始值上,不打在 Swift 标识符上。**
	//
	// 上一版查的是 `\.error\b|case\s+error\b` —— 那钉的是拼法。
	// `case serverSaid = "error"` 拉进的是同一个中文键,而它换了个标识符,
	// 守卫一声不吭。要守的性质是「不解 `error` 这个 **JSON 键**」,所以:
	//   ① 文件里不许出现字符串字面量 `"error"`(显式 rawValue 那种写法);
	//   ② CodingKeys 的隐式 rawValue 就是标识符本身,所以任何一个 `case` 的
	//      标识符列表里也不许出现光秃秃的 `error`。
	// (`"error_code"` / `errorCode` 是另一个键,两条都不误伤。)
	if strings.Contains(model, `"error"`) {
		t.Error(`ServersModel 里出现了 JSON 键 "error" —— 不管它映射到哪个 Swift ` +
			"标识符,拉进来的都是服务端那句中文,而这个界面通篇英文")
	}
	for _, line := range strings.Split(model, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "case ") {
			continue
		}
		for _, token := range strings.Split(strings.TrimPrefix(trimmed, "case "), ",") {
			name, _, _ := strings.Cut(token, "=")
			if strings.TrimSpace(name) == "error" {
				t.Errorf("CodingKeys 里有一个隐式映射到 %q 的 case:%s", "error", trimmed)
			}
		}
	}
	// 读一个叫 error 的字段同样不许(字段还在的话,上面两条迟早会被绕开)。
	if regexp.MustCompile(`\.error\b`).MatchString(model) {
		t.Error("ServersModel 又开始读服务端那句人话了 —— 它是中文的")
	}
}

// **空清单不是死路 —— 而它此前就是一条死路。**
//
// `render` 的 `rows.isEmpty` 分支摆一句「No servers yet」加一行
// `bx setup --name <name> '<link>'` 然后 `return`,而按钮带是在那个 return
// **之后**才画的。于是零行时:没有 Add Server、没有 New Server、没有 Test、
// 没有 Exit IP,只剩一条 `urfave/cli` 会直接拒掉的命令(`bx setup` 没有
// `--name` 这个 flag)。**空清单恰恰是最需要 Add Server… 的那一刻。**
//
// 判据打在**视图树**上,不是「文件里出现过 Add Server」:那个字符串在
// return 之后的死代码里照样在。而「摆进了视图树」还不够 —— 上一版只查了
// 「不在空清单那一支里面」,于是把整条按钮带搬**进**任何一个别的分支照样全绿。
// 这一版查的是**花括号深度**:按钮带那一句必须直接落在 `render` 的顶层,
// 也就是任何一个 `if` 都管不着它。
func TestMacMenuServersWindowKeepsTheButtonsWhenTheListIsEmpty(t *testing.T) {
	window := stripSwiftComments(menuServersWindowSource(t))
	body, ok := swiftFunctionBody(window, "private func render(preservingScroll: Bool)")
	if !ok {
		t.Fatal("读不出 render 的函数体 —— 守卫已经失效,先修守卫")
	}
	if strings.Contains(window, "--name") {
		// 那条死命令整个删掉:`bx setup` 没有 --name 这个 flag。
		t.Error("窗口里还留着 `bx setup --name …` —— urfave/cli 遇到未知 flag 直接报错")
	}

	// 措辞由**配置里有没有 servers 清单**决定,不由行数决定;而「只有一台」
	// 那一档条件不同,归 otherServersEmptyNote。
	//
	// **两句都必须真的被摆进视图树,而这条断言此前只查了「提到过」。**
	// 变异实测:两句 `stack.addArrangedSubview(wrapped(reason))` 都改成
	// `_ = wrapped(reason)`,编得过、整套全绿 —— 而单服务器配置的用户打开
	// Servers 看到的是一个配置路径、四个按钮,和**没有任何解释**。
	for _, judge := range []string{
		"serverListEmptyReason(list: list)",
		"otherServersEmptyNote(list: list, core: core)",
	} {
		if !strings.Contains(body, judge) {
			t.Errorf("render 没有用 %s —— 对一份单服务器配置说「还没有服务器」"+
				"是一句当场就能被证伪的假话,而只有一台时那一句压根不会出现", judge)
			continue
		}
		if !swiftValueReachesViewTree(body, judge) {
			t.Errorf("%s 算出来的那句话没有被摆进视图树 —— 它被算出来然后扔掉了,"+
				"而用户看到的是一片没有任何解释的空白", judge)
		}
	}
	// **那两句空状态文案不许留在这个文件里。** 它们此前正是以
	// `emptyReason ?? "No servers to switch to."` 的形式住在 AppKit 这一半 ——
	// 一行 Swift 测试都盖不到,而它恰恰是最常见的那一句。
	if strings.Contains(window, "No servers") {
		t.Error("空状态文案又长回了窗口里 —— 判据(与措辞)归 ServersModel,这一半测不到")
	}

	// 按钮带必须在**顶层**摆进视图树:深度 0 = 任何 if / for 都管不着它。
	bar := "stack.addArrangedSubview(buttonBar())"
	at := strings.Index(body, bar)
	if at < 0 {
		t.Fatal("按钮带压根没进视图树 —— 守卫已经失效,先修守卫")
	}
	if depth := swiftBraceDepthAt(body, at); depth != 0 {
		t.Errorf("按钮带落在 render 的第 %d 层花括号里 —— 它是有条件画的,"+
			"而空清单恰恰是最需要 Add Server… 的那一刻", depth)
	}
	// 而按钮带里那四个按钮必须真的都在。「摆了一条空的按钮带」与没有按钮带
	// 在屏幕上是同一件事。
	barBody, ok := swiftFunctionBody(window, "private func buttonBar() -> NSView")
	if !ok {
		t.Fatal("读不出 buttonBar 的函数体 —— 守卫已经失效,先修守卫")
	}
	for _, marker := range []string{
		"buttons.addArrangedSubview(add)",
		"buttons.addArrangedSubview(deploy)",
		"buttons.addArrangedSubview(test)",
		"buttons.addArrangedSubview(check)",
	} {
		if !strings.Contains(barBody, marker) {
			t.Errorf("%s 不在按钮带里 —— 用户拿到的是一条缺了按钮的带子", marker)
		}
	}
}

// swiftBraceDepthAt 数 body[:at] 里没配平的 `{` 有几个 —— 也就是 at 这一处
// 落在第几层花括号里。0 = 函数体顶层,任何 if / for 都管不着它。
//
// **数括号在抹白副本上做**(字符串里的 `}` 不是结构,本仓库为此栽过一次假红),
// 而 `blankSwiftStringLiterals` 逐字节保持偏移,所以 at 可以直接用。
func swiftBraceDepthAt(body string, at int) int {
	blank := blankSwiftStringLiterals(body)
	if at > len(blank) {
		at = len(blank)
	}
	depth := 0
	for i := 0; i < at; i++ {
		switch blank[i] {
		case '{':
			depth++
		case '}':
			depth--
		}
	}
	return depth
}

// ---------------------------------------------------------------------------
// 这一段是**摆放**守卫的判据本身,值得先读。
//
// 上一版这一堆守卫栽在同一个物种上,而 carry-in 文件恰好点过它的名:断言钉的是
// 「某个标识符**被提到过**」,而要守的性质是「那个值**真的被摆进了视图树**」。
// 五条变异因此全绿,其中两条让这扇窗要消灭的两句假话原样回来 ——
// `stack.addArrangedSubview(wrapped(reason))` 改成 `_ = wrapped(reason)` 编得过、
// 全绿,而单服务器配置的用户看到的是一片没有任何解释的空白。
//
// 判据因此改成一个**很小的数据流跟随器**:从种子表达式(`panel.runningNote`
// 这种)出发,在**包着它的那个最内层花括号块**里顺着 `let x = …种子…` 把变量名
// 一路收下去,最后要求其中某个名字出现在一次 `addArrangedSubview(` 的实参里。
//
// **限定在最内层块里是承重的**:`let label = hint(note)` 在这个文件里出现三次
// (三个不同的 if let 分支各一次),不限定的话「A 分支摆了」会替「B 分支没摆」
// 作证 —— 那正好又是一次「守卫钉住的是缺陷旁边的东西」。

// swiftEnclosingBlockOpen 往回找**包着 at 的那个**没配平的 `{` 的下标;
// at 落在 body 顶层时返回 -1。
//
// 「没配平」这三个字是这一整段的要害:它区分「包着它的那个块」与「在它前面
// 某处出现过的那个块」,而后者正是 swiftNearestEnclosingIf 上一版的判据 ——
// 见 swiftEnclosingGate 头上那段。
func swiftEnclosingBlockOpen(blank string, at int) int {
	if at > len(blank) {
		at = len(blank)
	}
	depth := 0
	for i := at - 1; i >= 0; i-- {
		switch blank[i] {
		case '}':
			depth++
		case '{':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

// swiftBlockAt 取包着 at 这一处的**最内层**花括号块的内容(不含花括号本身)。
// 找不到(at 落在函数体顶层)就返回整个 body。
func swiftBlockAt(body string, at int) string {
	blank := blankSwiftStringLiterals(body)
	open := swiftEnclosingBlockOpen(blank, at)
	if open < 0 {
		return body
	}
	depth := 0
	depth = 0
	for i := open; i < len(blank); i++ {
		switch blank[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return body[open+1 : i]
			}
		}
	}
	return body[open+1:]
}

// swiftCallArgs 取 body 里每一次 `callee(` 的实参原文(按括号配平取,所以嵌套
// 调用不会被截断)。
func swiftCallArgs(body, callee string) []string {
	blank := blankSwiftStringLiterals(body)
	var out []string
	from := 0
	for {
		idx := strings.Index(blank[from:], callee+"(")
		if idx < 0 {
			return out
		}
		start := from + idx + len(callee) + 1
		depth := 1
		i := start
		for ; i < len(blank) && depth > 0; i++ {
			switch blank[i] {
			case '(':
				depth++
			case ')':
				depth--
			}
		}
		if depth != 0 {
			return out
		}
		out = append(out, body[start:i-1])
		from = i
	}
}

// swiftArgumentIsPlainly 判断**这一次**调用的实参表里有没有 `label: value` 这一
// 项,而且值是那次光秃秃的取值(`core: core`,不是 `core: nil`、也不是一个可以在
// 两行之外被改掉的中间变量)。
//
// **它存在的理由是「一个调用点替另一个满足了断言」。** 漏斗
// `presentServers` 里有 `show(` 与 `refreshIfVisible(` 两处调用,而此前的判据是
// `strings.Contains(整个函数体, "canEdit: canEdit")` —— 整枝 review 实测:只把
// `show(` 那一处写死成 `canEdit: true`(另一处一字未动),整套 `TestMacMenuServer*`
// **全绿**。而 `show(` 恰恰是用户点「Servers…」走的那条路:对着一台只声明
// `servers` 的旧 Guardian,`⋯` 照画、Remove… 落进兼容分支 —— 出口 IP 被换到他
// 想删掉的那一台,菜单还报成功。
//
// 这是这一支上「守卫钉住的是缺陷旁边的东西」的**第十二次**,形状与同一轮修复在
// `swiftValueReachesViewTree` 里学到的那条一模一样(作用域限定在最内层块,否则
// 一个分支替另一个背书),只是没被带回八百行外的漏斗守卫。判据因此下沉到**每一
// 个实参表**:调用点各查各的,谁也替不了谁。
// **值后面只许跟逗号或实参表结尾,不许跟「随便什么」。** 收尾那个字符类原先写的
// 是 `[^A-Za-z0-9_.(]`,而**空格满足它** —— 定向复审实测:把漏斗里那次
// `canEdit: canEdit)` 改成 `canEdit: canEdit || legacyEditFallback)`,变异落上而
// 守卫**保持绿**。那正是把门重新打开的方向,而「给旧 Guardian 加一条回落」在这个
// 代码库里是隔三差五就会长出来的形状。要让这个缺陷回来,必须在值后面接上点什么
// —— 判据因此打在「值之后到下一个逗号(或结尾)之间什么都没有」上。
func swiftArgumentIsPlainly(args, label, value string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(label) +
		`\s*:\s*` + regexp.QuoteMeta(value) + `\s*(,|$)`).MatchString(args)
}

// swiftEachCallArgs 取 body 里 callee 的全部实参表,并要求它恰好被调用 want 次
// —— 少了或多了都说明守卫的锚点漂了,而一条锚点漂了还绿着的守卫等于没有守卫。
func swiftEachCallArgs(t *testing.T, body, callee string, want int) []string {
	t.Helper()
	args := swiftCallArgs(body, callee)
	if len(args) != want {
		t.Fatalf("%s 被调用了 %d 次,应当 %d 次 —— 守卫已经失效,先修守卫", callee, len(args), want)
	}
	return args
}

// swiftMentionsIdentifier 判断 text 里有没有把 name 当成一个**完整标识符**用到
// (而不是某个更长名字的一截:`label` 不该被 `labelWithString` 满足)。
func swiftMentionsIdentifier(text, name string) bool {
	return regexp.MustCompile(`(^|[^A-Za-z0-9_.])` + regexp.QuoteMeta(name) + `($|[^A-Za-z0-9_])`).
		MatchString(text)
}

// swiftBindingRegexp 认 `let x = …` / `if let x = …` / `guard let x = …` 三种
// 绑定 —— 少了后两种,`if let line = panel.statusLine {` 这种最常见的形状就跟不
// 下去,而这个文件里那七项里有五项是它。
//
// **2026-09-14 补上 `for x in y`**:规则窗口把一个组的域名画出来的写法是
// `for domain in row.group.domains { … addArrangedSubview(line) }` —— 那是和
// `let` 同样真实的一条到达路径,不认它就是**假阴性**(值确实摆进了视图树,
// 守卫却说没有)。扩它会让判据变松一点,所以那条守卫用变异验证过:
// 把最后那句 `addArrangedSubview` 去掉之后它仍然转红。
var swiftBindingRegexp = regexp.MustCompile(
	`(?m)(?:^|[^A-Za-z0-9_])(?:let\s+([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.+)$` +
		`|for\s+([A-Za-z_][A-Za-z0-9_]*)\s+in\s+(.+?)\s*\{)`,
)

// swiftValueReachesViewTree 回答:从 seed 派生出来的东西,有没有被摆进视图树。
//
// 见本段开头那大段注释 —— 这是「摆进去了」与「提到过」的分水岭。
func swiftValueReachesViewTree(body, seed string) bool {
	blank := blankSwiftStringLiterals(body)
	at := strings.Index(blank, seed)
	if at < 0 {
		return false
	}
	lineStart := strings.LastIndexByte(blank[:at], '\n') + 1
	lineEnd := lineStart + strings.IndexByte(blank[lineStart:], '\n')
	if lineEnd < lineStart {
		lineEnd = len(blank)
	}
	var scope string
	if strings.HasSuffix(strings.TrimSpace(blank[lineStart:lineEnd]), "{") {
		// seed 在一个分支的头上(`if let x = seed {`)。作用域是它开的那个块,
		// 而绑定名写在头那一行 —— 两段拼起来才跟得下去。
		scope = body[lineStart:lineEnd] + "\n" + swiftBlockAt(body, lineEnd+1)
	} else {
		scope = swiftBlockAt(body, at)
	}
	names := []string{seed}
	// 三轮足够:seed → 绑定名 → 包一层 → 摆进去。多了只会把不相干的名字收进来。
	for round := 0; round < 3; round++ {
		for _, m := range swiftBindingRegexp.FindAllStringSubmatch(scope, -1) {
			bound, source := m[1], m[2]
			if bound == "" {
				bound, source = m[3], m[4] // `for x in y` 那一支
			}
			for _, name := range names {
				if swiftMentionsIdentifier(source, name) {
					names = append(names, bound)
					break
				}
			}
		}
	}
	for _, arg := range swiftCallArgs(scope, "addArrangedSubview") {
		for _, name := range names {
			if swiftMentionsIdentifier(arg, name) {
				return true
			}
		}
	}
	return false
}

// swiftEnclosingGate 取**真的包着 at 的那个块**的头一行(到它的 `{` 为止)。
//
// **上一版叫 swiftNearestEnclosingIf,而它没有验证「包着」** —— 它算的是
// 「at 之前最近的那一行 `if `」。名字与两条失败文案都写着「**直接**门控」,
// 而判据只是「前面有一个匹配的 if」。reviewer 实测的后果:留一个诱饵
// `if canEdit { _ = row.name }`,把 `box.addArrangedSubview(moreButton(…))`
// **挪到它外面** —— 编得过、整套全绿,而 `⋯` 从此无条件画出来。那正是这道门
// 存在的理由所要挡的 Critical:对着只声明 `servers` 的 Guardian 提供
// remove / replace,而 replace 在那儿会落进兼容分支、拿旧链接热切一次,
// 菜单还报成功。
//
// 这是这一支上同一个物种的**第十一次**,而且这次长在守卫自己的 helper 里 ——
// 每一条建在它上面的断言都继承了这个弱点。现在判据由构造保证:先按括号配平
// 往回找**包着** at 的那个 `{`(`swiftEnclosingBlockOpen`),再取它那一行。
//
// at 落在 body 顶层(没有任何块包着它)时返回 ("", false) —— 那正是诱饵变异
// 的形状,调用方必须把它当成失败。
func swiftEnclosingGate(body string, at int) (string, bool) {
	blank := blankSwiftStringLiterals(body)
	open := swiftEnclosingBlockOpen(blank, at)
	if open < 0 {
		return "", false
	}
	lineStart := strings.LastIndexByte(blank[:open], '\n') + 1
	return strings.TrimSpace(body[lineStart : open+1]), true
}

// swiftRedGates 取 body 里**每一处** needle 各自被哪个块包着(顺序无关,调用方
// 自己排序比对)。顶层那一处 —— 也就是「谁都没包着它」—— 记成空串。
//
// 判据从「恰好一处 .systemRed」升级成这个,是因为这一块上现在有**两处**该红的
// 地方(隧道不健康、探测实测失败),而两处各有各的三态判据。只数个数就只能在
// 「一处」和「随便几处」之间选,前者拦住正当的第二处,后者放过任何一处不受判据
// 管的红 —— 而这一整块存在的理由正是「不许把没测成画成坏了」。
func swiftRedGates(body, needle string) []string {
	blank := blankSwiftStringLiterals(body)
	var gates []string
	from := 0
	for {
		i := strings.Index(blank[from:], needle)
		if i < 0 {
			return gates
		}
		at := from + i
		gate, ok := swiftEnclosingGate(body, at)
		if !ok {
			gate = ""
		}
		gates = append(gates, gate)
		from = at + len(needle)
	}
}

// **每一个渲染点都必须带上 `/v1/status` 的那份实时数据,而漏掉它不会有任何
// 编译错误** —— `core` 是可空的,漏传的后果只是当前那一块永远说「Core not
// answering」,而界面看起来完全正常。规则窗口那次就是漏了一个渲染点。
//
// 判据不是「每个调用点都写了 core:」(那要求守卫自己去枚举调用点,而漏看一个
// 与代码漏掉一个在守卫眼里一模一样),而是**只有一个渲染点**:
// `serversWindow.show(` / `.refreshIfVisible(` 只许出现在 `presentServers` 里,
// 而那一处传的必须是光秃秃的 `maintenanceReport?.core`。
func TestMacMenuServersWindowIsRenderedThroughASingleFunnel(t *testing.T) {
	source := menuMainSwiftCode(t)
	funnel, ok := swiftFunctionBody(source, "private func presentServers(_ list: ServerList, forceShow: Bool)")
	if !ok {
		t.Fatal("读不出 presentServers 的函数体 —— 守卫已经失效,先修守卫")
	}
	for _, call := range []string{"serversWindow.show(", "serversWindow.refreshIfVisible("} {
		all := strings.Count(source, call)
		inside := strings.Count(funnel, call)
		if inside != 1 {
			t.Fatalf("presentServers 里有 %d 处 %s,应当恰好一处 —— 守卫已经失效,先修守卫", inside, call)
		}
		if all != 1 {
			t.Errorf("%s 在 main.swift 里出现 %d 次 —— 漏斗之外的那些渲染点"+
				"迟早会漏掉 core:,而那半数据不显示时界面看起来完全正常", call, all)
		}
	}
	// **实参必须是那次光秃秃的取值。** `core: nil` 编得过,而后果正是这条守卫
	// 要挡的那件事;一个中间变量也不行 —— 它可以在两行之外被改成别的。
	if !strings.Contains(funnel, "let core = maintenanceReport?.core") {
		t.Error("漏斗里的 core 不是直接取自 maintenanceReport —— " +
			"当前那一块会永远显示「Core not answering」而界面看起来完全正常")
	}
	// **两个调用点各查各的。** 判据打在整个函数体上时,`show(` 与
	// `refreshIfVisible(` 会**互相背书** —— 整枝 review 实测:只把 `show(` 那一处
	// 的三个实参写死(`core: nil` / `switchingTo: false` / `canEdit: true`),
	// 整套全绿,而那正是用户点「Servers…」走的那条路。见 swiftArgumentIsPlainly。
	for _, call := range []string{"serversWindow.show", "serversWindow.refreshIfVisible"} {
		args := swiftEachCallArgs(t, funnel, call, 1)[0]
		if !swiftArgumentIsPlainly(args, "core", "core") {
			t.Errorf("%s 那一处没把那份实时数据传进窗口 —— "+
				"当前那一块会永远显示「Core not answering」而界面看起来完全正常", call)
		}
		// 在飞状态也必须到得了窗口,否则确认之后二十几秒屏幕上什么都不发生。
		if !swiftArgumentIsPlainly(args, "switchingTo", "switchingTo") {
			t.Errorf("%s 那一处没把切换在飞状态传进窗口 —— "+
				"那一行不会说 Switching…、Use 也不会灰", call)
		}
	}
}

// **窗口自己一次 `reachable` 都不许判。**
//
// 那份判据只有一份(`answeringCore`,MenuRows.swift),而窗口这一半在本仓库
// 一行 Swift 测试都盖不到:在这儿写 `core?.reachable == true` 不会有任何测试
// 转红,而它与纯模型漂开的那一刻,就是界面对同一台机器说两句相反的话。
// 规则窗口刚因为「有一份判据没被用上」出过同一个 bug。
func TestMacMenuServersWindowNeverJudgesReachabilityItself(t *testing.T) {
	window := stripSwiftComments(menuServersWindowSource(t))
	for _, banned := range []string{"reachable", "tunnelHealthy", "latencyMS", "answeringCore("} {
		if strings.Contains(window, banned) {
			t.Errorf("窗口里出现了 %q —— Core 那几项的判据全在 ServersModel 的纯函数里"+
				"(currentServerPanel / otherServerRows),这一半只摆放", banned)
		}
	}
	// 反面自检:窗口确实**在用**那两个纯函数。少了这一条,一个把当前那一块
	// 整个删掉的实现照样满足上面那几条禁令。
	body, ok := swiftFunctionBody(window, "private func render(preservingScroll: Bool)")
	if !ok {
		t.Fatal("读不出 render 的函数体 —— 守卫已经失效,先修守卫")
	}
	for _, judge := range []string{"currentServerPanel(list: list, core: core)", "otherServerRows(list: list, core: core)"} {
		if !strings.Contains(body, judge) {
			t.Errorf("render 没有调用 %s —— 那正是「判据只有一份」的那一份", judge)
		}
	}
}

// **当前那台必须真的被摆出来。**
//
// `currentServerPanel` 落地时是零生产调用方 —— 于是一个只有一台服务器的用户
// (也就是绝大多数人)打开这扇窗,看到的是一片空白:候选行按定义排除了当前
// 那台,而那一块没有人摆。这是这次重做最主要的那句假话的后半段。
func TestMacMenuServersWindowPlacesTheCurrentServerPanel(t *testing.T) {
	window := stripSwiftComments(menuServersWindowSource(t))
	body, ok := swiftFunctionBody(window, "private func render(preservingScroll: Bool)")
	if !ok {
		t.Fatal("读不出 render 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(body, "stack.addArrangedSubview(currentPanelView(panel))") {
		t.Error("当前那一块没被摆进视图树 —— 只有一台服务器的用户看不到自己在哪台上")
	}
	panel, ok := swiftFunctionBody(window, "private func currentPanelView(_ panel: CurrentServerPanel) -> NSView")
	if !ok {
		t.Fatal("读不出 currentPanelView 的函数体 —— 守卫已经失效,先修守卫")
	}
	// 那一块上的每一样都得真的**进格子**,不是「被提到过」。
	//
	// 上一版查的是 `strings.Contains(panel, field)`,于是留着
	// `if let note = panel.runningNote {` 而把里面那句 `box.addArrangedSubview(label)`
	// 删掉,整套全绿 —— 而那一行正是橙色的分歧提示,也就是这扇窗第三句假话的
	// 另一半:配置指着这一台而 Core 在跑别的,用户永远看不到这句话。
	for _, field := range []string{
		"panel.endpoint", "panel.statusLine", "panel.udpLine",
		"panel.throughput", "panel.probeLine", "panel.coreSilentNote", "panel.runningNote",
		"panel.runningConfirmed",
	} {
		if !strings.Contains(panel, field) {
			t.Errorf("当前那一块没用上 %s —— 那一项永远不显示,而界面看起来完全正常", field)
			continue
		}
		if !swiftValueReachesViewTree(panel, field) {
			t.Errorf("%s 被读出来之后没有被摆进视图树 —— 那一项永远不显示,"+
				"而界面看起来完全正常", field)
		}
	}
	// **每一处红色都必须被一个三态判据包着,而这一块上恰好有两处该红的地方:**
	// 隧道明确说了不健康(`statusLineIsBad`)、探测**实测**失败
	// (`probe.isFailure`)。窗口自己写 `healthy == false` 会把「没说」画成
	// 「不健康」;自己看 `reachable` 会把「没测成」(bx 没在跑)画成「这台坏了」
	// —— 两者都是把一台好机器说成坏的。
	//
	// 判据是**包着每一处红色的那个条件**,不是「提到过这个属性」,也不是个数:
	// 只数个数时,「恰好一处」会拦住正当的第二处,「随便几处」会放过任何一处
	// 不受判据管的红。
	wantGates := []string{"if panel.probe.isFailure {", "if panel.statusLineIsBad {"}
	gotGates := swiftRedGates(panel, ".systemRed")
	sort.Strings(gotGates)
	if !reflect.DeepEqual(gotGates, wantGates) {
		t.Errorf("当前那一块里那几处红色被包在 %q 里,应当恰好是 %q —— "+
			"多出来的那些不受三态判据管,少了的那一项则永远不会变红", gotGates, wantGates)
	}
}

// **`⋯` 里那两个动词按 `servers_edit` 门控,不按 `servers`。**
//
// **Add 表单里那个 UDP 框刻意不在这道门后面。** `action:add` 认 `udp` 这个键
// 早于这一支(Guardian 一直在收,只是 Swift 客户端从不发),对着只声明
// `servers` 的旧 Guardian 发它得到的是正确的行为 —— 一并门控只会在那种机器上
// 拿掉一个本来能用的功能。这道门存在的理由恰恰相反:remove / replace 在那种
// 机器上会做出一件危险的事。
//
// 后者的含义早于 remove / replace:一台只声明 `servers` 的旧 Guardian 收到
// `{"action":"remove"}` 走的是它那一版唯一的行为 —— **换到那一台**。于是在
// 「文件换了、进程没换」那个记录在案的升级窗口里,用户点一下 Delete,出口 IP
// 与国家换到了他想删掉的那一台。
func TestMacMenuServerVerbsAreGatedByTheEditCapability(t *testing.T) {
	funnel, ok := swiftFunctionBody(menuMainSwiftCode(t),
		"private func presentServers(_ list: ServerList, forceShow: Bool)")
	if !ok {
		t.Fatal("读不出 presentServers 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(funnel, "serverEditingAvailable(capabilities: maintenanceReport?.capabilities)") {
		t.Error("那几个动词不是由 serverEditingAvailable 门控的 —— " +
			"对着只声明 servers 的那一版发 remove,它会把出口换到用户想删的那台")
	}
	if strings.Contains(funnel, "canEdit = serverSwitchingAvailable(") {
		t.Error("用了 serverSwitchingAvailable 当门 —— 那个能力早于 remove / replace")
	}
	// **两个调用点各查各的**(见 swiftArgumentIsPlainly):写在整个函数体上时,
	// 只把 `show(` 那一处改成 `canEdit: true` 整套照样全绿 —— 而 `show(` 正是
	// 用户点「Servers…」那条路,`⋯` 于是对着一台只声明 `servers` 的 Guardian
	// 无条件画出来。
	for _, call := range []string{"serversWindow.show", "serversWindow.refreshIfVisible"} {
		args := swiftEachCallArgs(t, funnel, call, 1)[0]
		if !swiftArgumentIsPlainly(args, "canEdit", "canEdit") {
			t.Errorf("%s 那一处没把那道门传进窗口 —— "+
				"对着只声明 servers 的那一版发 remove,它会把出口换到用户想删的那台", call)
		}
	}
	// 窗口那一半:`⋯` 必须真的挂在这道门后面,而不是无条件画出来。
	window := stripSwiftComments(menuServersWindowSource(t))
	// **候选行与当前那一块都要查。**
	//
	// 上一版只读了 serverView —— 而当前那一块上也有一个 ⋯,删掉包着它的
	// `if canEdit {` 整套全绿。那条路的后果被完整追过:对着一台只声明 `servers`
	// 的 Guardian 发 replace,请求落进「空 action = 换到 Name 那一台」的兼容
	// 分支 —— `SetCurrentServer` 加一次**用旧链接**的热切换;应答又恰好能被
	// 解成一份空的 ServerList,于是窗口翻成「No servers yet」,隧道在用户脚下
	// 换了地方,而菜单说的是「Saved the new link for tokyo.」
	for _, fn := range []string{
		"private func serverView(_ row: ServerRow) -> NSView",
		"private func currentPanelView(_ panel: CurrentServerPanel) -> NSView",
	} {
		// **这个循环里一律 t.Errorf,不许 t.Fatalf。** 两个主体各查一遍,而
		// `Fatalf` 会在第一个主体上停住 —— 于是「两处都坏了」只报出一处,
		// 下一个人修完那一处会以为修完了。本仓库为「Fatal 让后面的断言够不着」
		// 栽过两次,这一支里就有。
		fnBody, ok := swiftFunctionBody(window, fn)
		if !ok {
			t.Errorf("读不出 %s 的函数体 —— 守卫已经失效,先修守卫", fn)
			continue
		}
		// **恰好一处。** 判据只看第一处时,同一个函数体里晚一点再画一个
		// **不受这道门管**的 `moreButton(` 照样绿 —— 八行之外那两条
		// `.systemRed` 守卫早就为同一个理由改成了 Count == 1。
		if n := strings.Count(fnBody, "moreButton("); n != 1 {
			t.Errorf("%s 里有 %d 处 moreButton(,应当恰好一处 —— "+
				"多出来的那些不受 canEdit 这道门管", fn, n)
			continue
		}
		more := strings.Index(fnBody, "moreButton(")
		if more < 0 {
			t.Errorf("%s 上没有那个 ⋯ —— 那几个动词点不到", fn)
			continue
		}
		gate, ok := swiftEnclosingGate(fnBody, more)
		if !ok || gate != "if canEdit {" {
			t.Errorf("%s 里的 ⋯ 不是**包在** canEdit 里的(包着它的那个块的头是 %q)—— "+
				"一个留在旁边的 `if canEdit { … }` 诱饵不算数", fn, gate)
		}
	}
}

// **那道能力门在动作那一侧也要有一道,而这不是重复。**
//
// 渲染那道门(`presentServers` 的 `canEdit`)只决定「画不画 `⋯`」。而
// `NSMenu.popUp` 跑的是一个**嵌套事件循环** —— 从画出那个 `⋯` 到用户点下去
// 之间,窗口完全可能被环境刷新重画一遍(watch 时代刷新是事件驱动的),而那
// 一拍手里的能力清单可以是另一份(Guardian 刚在升级窗口里被换掉,正是
// 「文件换了、进程没换」那个记录在案的形状)。
//
// 拨出去的代价不是一次失败的请求:只声明 `servers` 的那一版收到
// `{"action":"remove"}` 走的是它唯一的行为 —— **换到那一台**,用户的出口 IP
// 与国家换到了他想删掉的机器上,而菜单报成功。本仓库明写「绝不试着拨一下
// 看看」,这两处正是它适用的地方;GuardianClient.swift 上那两句「只有
// serverEditingAvailable 判定支持时才该调用它」此前**没有任何东西**在执行。
//
// 两层是刻意的:漏斗那道门刚因为「两个调用点互相背书」被绕过一次,
// **一道防线不该只有一层。**
func TestMacMenuServerEditVerbsRecheckTheCapabilityBeforeSending(t *testing.T) {
	source := menuMainSwiftCode(t)
	for _, fn := range []string{
		"private func confirmAndRemoveServer(name: String, host: String,",
		"private func replaceServerLinkFromWindow(name: String)",
	} {
		// **一律 t.Errorf。** 两个动词各查一遍,Fatalf 会在第一个上停住,于是
		// 「两处都坏了」只报出一处 —— 本仓库为这个形状栽过两次。
		body, ok := swiftFunctionBody(source, fn)
		if !ok {
			t.Errorf("读不出 %s 的函数体 —— 守卫已经失效,先修守卫", fn)
			continue
		}
		args := swiftCallArgs(body, "serverEditingAvailable")
		if len(args) != 1 {
			t.Errorf("%s 里有 %d 处 serverEditingAvailable,应当恰好一处 —— "+
				"这个动词会在旧 Guardian 上把用户的出口换到他想删的那台", fn, len(args))
			continue
		}
		// **实参必须是此刻那份状态里那次光秃秃的取值。** 一个中间变量(或者
		// 窗口传过来的那个陈旧标志)可以在两行之外被改成别的,而这道门要挡的
		// 恰恰是「画出 ⋯ 之后能力变了」。
		if !swiftArgumentIsPlainly(args[0], "capabilities", "maintenanceReport?.capabilities") {
			t.Errorf("%s 那道门问的不是此刻那份能力清单:%q", fn, args[0])
		}
		gate := strings.Index(blankSwiftStringLiterals(body), "serverEditingAvailable(")
		dial := strings.Index(blankSwiftStringLiterals(body), "GuardianClient()")
		if dial < 0 {
			t.Errorf("%s 里没有那次拨号 —— 守卫已经失效,先修守卫", fn)
			continue
		}
		if gate > dial {
			t.Errorf("%s 先拨号后查门 —— 那次请求已经发出去了", fn)
		}
		// **上面三条对「一道什么也不做的门」和「一道装反了的门」全部成立。**
		// 定向复审两条变异各自落上、各自全绿:把 `guard … else { …; return }`
		// 改成不带 return 的 `if !… { … }`,以及给判据加一个 `!`。后者对只声明
		// `servers` 的旧 Guardian 直接放行 replace —— 出口国被换到用户正在编辑
		// 的那台机器上,而菜单报成功,正是这道门存在的全部理由逐字复活。
		// 故判据打在**极性与控制流**上,不打在「有没有这么个东西」上
		// (先例:leakcheck 那条 TestPageJSNeverAssertsThatAProbeLanded)。
		if why := swiftGuardStopsHere(body, "serverEditingAvailable("); why != "" {
			t.Errorf("%s 那道门拦不住这次请求:%s", fn, why)
		}
	}
}

// swiftGuardStopsHere 回答一件事:`callee` 那次调用是不是一条**真的会挡住后面
// 代码**的门 —— `guard <callee>(…) else { … return … }`,判据不带 `!`。
//
// 返回空串表示合格,否则返回一句说明哪一环不成立。判据分三段,**每一段都对应
// 一条实测落上过的变异**:
//   - 前面必须紧挨着 `guard`(而不是 `if`,也不是 `!`):`if !x { refuse }` 少了
//     `return`,拒绝的话说了、请求照发;`guard !x` 则是把门装反,只有该放行的
//     时候才拦。
//   - `else` 必须真的在那儿:`guard x` 后面接别的就不是这个形状,守卫读不懂就
//     响亮说读不懂,不安静放过。
//   - `else` 那个块里必须有 `return`:一个 `else {}` 在语法上不合法,但一个
//     只弹框不返回的 `else`(经 fallthrough 到别处)会让门形同虚设。
func swiftGuardStopsHere(body, callee string) string {
	blank := blankSwiftStringLiterals(stripSwiftComments(body))
	at := strings.Index(blank, callee)
	if at < 0 {
		return "读不出那次调用 —— 守卫已经失效,先修守卫"
	}
	// 往前跳过空白,落点必须正好是 `guard` 这个完整关键字。`!` / `if` / 任何
	// 别的东西都在这里被挡下,不需要再单独写一条「不许带叹号」。
	i := at
	for i > 0 && (blank[i-1] == ' ' || blank[i-1] == '\t' || blank[i-1] == '\n' || blank[i-1] == '\r') {
		i--
	}
	const kw = "guard"
	if i < len(kw) || blank[i-len(kw):i] != kw {
		before := strings.TrimSpace(blank[maxInt(0, i-24):at])
		return fmt.Sprintf("它前面不是一句光秃秃的 `guard`,而是 %q —— "+
			"一道不带 return 的 if(或者装反了的 `!`)什么也拦不住", before)
	}
	// 括号配平跳过实参表。
	open := at + len(callee) - 1
	depth := 0
	j := open
	for ; j < len(blank); j++ {
		if blank[j] == '(' {
			depth++
		} else if blank[j] == ')' {
			depth--
			if depth == 0 {
				break
			}
		}
	}
	if depth != 0 {
		return "实参表括号配不平 —— 守卫已经失效,先修守卫"
	}
	rest := blank[j+1:]
	trimmed := strings.TrimLeft(rest, " \t\n\r")
	if !strings.HasPrefix(trimmed, "else") {
		return fmt.Sprintf("`guard` 后面没有 `else`,而是 %q", strings.TrimSpace(trimmed[:minInt(24, len(trimmed))]))
	}
	elseBody, ok := swiftFunctionBody(rest, "else")
	if !ok {
		return "读不出 `else` 那个块 —— 守卫已经失效,先修守卫"
	}
	if !swiftMentionsIdentifier(elseBody, "return") {
		return "`else` 那个块里没有 return —— 拒绝的话说了,请求照发"
	}
	return ""
}

// **判据自己也要被证明不是恒真。** `swiftGuardStopsHere` 合格时返回空串,而
// 「恒返回空串」与「一条都不查」在输出上完全一样 —— 这个仓库为这个形状栽过
// 很多次。三条不合格的形状各喂一遍,外加一条合格的反向自检。
//
// 第三条(`else` 里没有 return)在 Swift 里编不过,所以真机变异造不出它;
// 但判据必须挡着,否则哪天换成 `else { refuse() }` 接一个别处的 fallthrough,
// 这一段就静默失效了。
func TestSwiftGuardStopsHereRejectsAGateThatDoesNotStop(t *testing.T) {
	const callee = "gate("
	cases := []struct {
		name string
		body string
		ok   bool
	}{
		{"合格", "\n    guard gate(x: y) else {\n        refuse()\n        return\n    }\n    dial()\n", true},
		{"不带 return 的 if", "\n    if !gate(x: y) {\n        refuse()\n    }\n    dial()\n", false},
		{"装反了的 guard", "\n    guard !gate(x: y) else {\n        refuse()\n        return\n    }\n    dial()\n", false},
		{"else 里没有 return", "\n    guard gate(x: y) else {\n        refuse()\n    }\n    dial()\n", false},
		{"根本没有 else", "\n    guard gate(x: y)\n    dial()\n", false},
	}
	for _, tc := range cases {
		why := swiftGuardStopsHere(tc.body, callee)
		if tc.ok && why != "" {
			t.Errorf("%s:合格的形状被判不合格:%s", tc.name, why)
		}
		if !tc.ok && why == "" {
			t.Errorf("%s:拦不住的门被判合格 —— 判据形同虚设", tc.name)
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// **候选行的红色必须由 `ProbePresentation.isFailure` 决定,而这一处此前无人守。**
//
// 变异实测:`if row.probe.isFailure` 改成 `if row.probe != .notChecked`,整个
// internal/cli 套件与 24 个 Swift 套件全绿 —— 而那是这扇窗要消灭的第二句假话
// **逐字重现**:bx 没在跑时点一下 Test All,每一行都变红,各配一句
// `not measured — could not measure (is bx running?)`。
//
// 判据是「**包着那个 `.systemRed` 的条件**是哪一个」,不是「文件里提到过
// isFailure」;另外钉住这个函数里**只有一处** `.systemRed`,否则再加一处
// 不受这个判据管的红色照样绿。
func TestMacMenuServerRowRedComesOnlyFromAMeasuredFailure(t *testing.T) {
	window := stripSwiftComments(menuServersWindowSource(t))
	body, ok := swiftFunctionBody(window, "private func serverView(_ row: ServerRow) -> NSView")
	if !ok {
		t.Fatal("读不出 serverView 的函数体 —— 守卫已经失效,先修守卫")
	}
	if n := strings.Count(body, ".systemRed"); n != 1 {
		t.Fatalf("serverView 里有 %d 处 .systemRed,应当恰好一处 —— "+
			"多出来的那些不受「只有实测失败才红」这个判据管", n)
	}
	red := strings.Index(body, ".systemRed")
	gate, ok := swiftEnclosingGate(body, red)
	if !ok || gate != "if row.probe.isFailure {" {
		t.Errorf("候选行的红色不是由 row.probe.isFailure 决定的(包着它的条件是 %q)—— "+
			"「没测过」与「没测成」被画成红的,等于把一整排好服务器说成坏的", gate)
	}
	// 那句话本身也得真的被摆进视图树:少了它,红不红都无所谓了。
	if !swiftValueReachesViewTree(body, "row.note") {
		t.Error("候选行那句副标题没有被摆进视图树 —— 探测结论算出来之后被扔掉了")
	}
	// **「这一台正在被用」那句橙字同款。** 它此前只有兄弟那句 `row.note` 被钉着:
	// 整枝 review 实测把它的 `box.addArrangedSubview(label)` 换成 `_ = label`,
	// 整套全绿。而这一句正是热切失败之后**用户真正的出口在哪**的唯一提示 ——
	// 少了它,他会盯着上面那块加粗的当前那台找原因。
	if !swiftValueReachesViewTree(body, "row.runningNote") {
		t.Error("候选行那句「正在被用」没有被摆进视图树 —— " +
			"热切失败之后用户真正的出口在哪,界面上一个字都不说")
	}
}

// **窗口必须真的**用**那份在飞状态,不只是收下它。**
//
// 漏斗那条守卫证明的是「传进去了」。变异实测:
// `let switching = false` / `let mine = false`,整套全绿 —— 而屏幕上恢复的正是
// Step 4 点名的那个症状:确认之后四十几秒什么都不发生,再点一次静默没反应。
//
// 判据是**数据流**:`Use` 的标题与它的可用性,都得由 `switchingTo` 派生出来的
// 东西决定。
func TestMacMenuServerRowShowsTheSwitchInFlight(t *testing.T) {
	window := stripSwiftComments(menuServersWindowSource(t))
	body, ok := swiftFunctionBody(window, "private func serverView(_ row: ServerRow) -> NSView")
	if !ok {
		t.Fatal("读不出 serverView 的函数体 —— 守卫已经失效,先修守卫")
	}
	derived := swiftDerivedNames(body, "switchingTo")
	if len(derived) < 2 {
		t.Fatalf("serverView 里没有从 switchingTo 派生出任何东西(拿到 %v)—— "+
			"那份在飞状态被收下之后没有被用", derived)
	}
	enabled := regexp.MustCompile(`use\.isEnabled\s*=\s*(.+)`).FindStringSubmatch(body)
	if enabled == nil {
		t.Fatal("读不出 Use 那个按钮的可用性 —— 守卫已经失效,先修守卫")
	}
	if !swiftAnyIdentifier(enabled[1], derived) {
		t.Errorf("Use 的可用性不是由 switchingTo 决定的(它是 %q)—— "+
			"切换在飞时按钮不会变灰,用户会再点一次而什么都不发生", strings.TrimSpace(enabled[1]))
	}
	titles := swiftCallArgs(body, "NSButton")
	if len(titles) == 0 {
		t.Fatal("serverView 里没有那个 Use 按钮 —— 守卫已经失效,先修守卫")
	}
	found := false
	for _, arg := range titles {
		if swiftAnyIdentifier(arg, derived) {
			found = true
		}
	}
	if !found {
		t.Error("Use 的标题不是由 switchingTo 决定的 —— 那一行永远不会说 Switching…")
	}
}

// swiftDerivedNames 把 seed 以及从它 `let` 出来的名字都收上来(同
// swiftValueReachesViewTree 的第一半,只是不去看视图树)。
func swiftDerivedNames(body, seed string) []string {
	names := []string{seed}
	for round := 0; round < 3; round++ {
		for _, m := range swiftBindingRegexp.FindAllStringSubmatch(body, -1) {
			for _, name := range names {
				if swiftMentionsIdentifier(m[2], name) {
					names = append(names, m[1])
					break
				}
			}
		}
	}
	return names
}

func swiftAnyIdentifier(text string, names []string) bool {
	for _, name := range names {
		if swiftMentionsIdentifier(text, name) {
			return true
		}
	}
	return false
}

// **删除必须先弹确认,而且那句话要说清链接跟着没。**
//
// 这是与规则窗口刻意相反的一处:那边删除不弹确认、只留 Undo,而这里菜单在
// **构造上**做不到 Undo —— 链接是凭据,`/v1/servers` 从不发它(
// `TestServerListNeverShipsTheLinkItself` 钉着),所以删掉之后菜单无法把它加
// 回去。一个撤不回的 Undo 比没有 Undo 更糟。
//
// 另一半:**当前那台的 Remove 要置灰**,而且回调里再拦一道 —— 只靠 isEnabled
// 的保护会在下一次有人从别处触发这个 action 时失效。
func TestMacMenuConfirmsBeforeRemovingAServer(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftCode(t),
		"private func confirmAndRemoveServer(name: String, host: String,")
	if !ok {
		t.Fatal("读不出 confirmAndRemoveServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	confirm := strings.Index(body, "alert.runModal() == .alertFirstButtonReturn")
	send := strings.Index(body, "GuardianClient().removeServer(name:")
	if confirm < 0 {
		t.Fatal("删除没有确认框 —— 一次误点就静默毁掉一条凭据,而菜单加不回去")
	}
	if send < 0 {
		t.Fatal("函数体里根本没有发出删除请求")
	}
	if send < confirm {
		t.Fatal("请求发在确认之前 —— 用户点「取消」时那台已经没了")
	}
	if !strings.Contains(body[:confirm], "serverRemoveConfirmMessage(") {
		t.Error("确认框文案不是 serverRemoveConfirmMessage —— 那句话负责说清链接会跟着没")
	}
	if !strings.Contains(body[confirm:], "else { return }") {
		t.Error("用户点取消之后没有原地返回")
	}

	window := stripSwiftComments(menuServersWindowSource(t))
	menu, ok := swiftFunctionBody(window, "@objc private func showRowMenu(_ sender: NSButton)")
	if !ok {
		t.Fatal("读不出 showRowMenu 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(menu, "remove.isEnabled = !row.isCurrent") {
		t.Error("当前那台的 Remove 没有置灰 —— 用户点完才读到一句拒绝")
	}
	action, ok := swiftFunctionBody(window, "@objc private func removeServer(_ sender: NSMenuItem)")
	if !ok {
		t.Fatal("读不出 removeServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	// **「这一台此刻在不在承载流量」必须从窗口带过去,而且带的是那个三态。**
	// 一台在跑、但配置里已经不是 current 的服务器,Remove… 是亮着的(合规),
	// 而确认框若不说这件事,用户读到的是「删掉一条不用的记录」,删的却是他此刻
	// 的出口。**不许在 main.swift 那边自己再判一遍**:那份判据只有一份
	// (`serverTrafficState`,门是 `answeringCore`),而 main.swift 手里清单上的
	// running 在 Core 静默时是可能陈旧的。
	//
	// **判据打在「不是 Bool」上,这是本条修改的要点。** 这一路一度是
	// `isRunningNow: Bool`:Core 静默、或者 Guardian 自己说不出是哪一台时,
	// 它交出去的是 false —— 与「Core 答了话、确认这台闲着」完全无法区分,而
	// 确认框对 false 一个字都不说。在这个窗口的词汇表里沉默读作那句让人放心的
	// 答案,于是**最该出声的那一档反而静默**。
	if !strings.Contains(action, "onRemove?(row.name, row.host, row.traffic)") {
		t.Error("删除回调没把那个三态带过去 —— " +
			"确认框会把用户此刻的出口说成一条不用的记录")
	}
	if strings.Contains(action, "row.isRunningNow") {
		t.Error("删除回调交出去的是 Bool —— 「没问出来」会被压成 false," +
			"而确认框对 false 一个字都不说")
	}
	if !strings.Contains(body, "traffic: traffic") {
		t.Error("确认文案没吃那个三态 —— 它算出来了,却没进那句话")
	}
	// 窗口两处 `⋯` 交给按钮的也必须是算好的三态,不是就地拼一个 Bool 或字面量。
	for _, want := range []string{"traffic: panel.traffic", "traffic: row.traffic"} {
		if !strings.Contains(stripSwiftComments(menuServersWindowSource(t)), want) {
			t.Errorf("`⋯` 那个按钮没拿到算好的三态(缺 %q)—— "+
				"就地拼一个标志位就是第二份判据,而它恰好会答反", want)
		}
	}
	if !strings.Contains(action, "guard !row.isCurrent else { return }") {
		t.Error("回调里没有再拦一道 —— 只靠 isEnabled 的保护在别处触发这个 action 时失效")
	}
}

// **换的是当前那台时:如实说「重连后生效」,给一条重连的路,但绝不替他按。**
//
// 换链接不动 current、也不热切任何东西,所以配置改了而跑着的隧道还连着旧地址。
// 判据在 `replaceLinkFollowUp`(纯函数),这条守卫钉的是**菜单真的读了它**,
// 以及那次重连是**用户按的**。
func TestMacMenuReplaceLinkNeverReconnectsOnItsOwn(t *testing.T) {
	code := menuMainSwiftCode(t)
	body, ok := swiftFunctionBody(code, "private func replaceServerLinkFromWindow(name: String)")
	if !ok {
		t.Fatal("读不出 replaceServerLinkFromWindow 的函数体 —— 守卫已经失效,先修守卫")
	}
	if !strings.Contains(body, "udpHint: udpFieldHint(replacing: true)") {
		t.Error("替换表单没有按自己那条路取 UDP 提示 —— " +
			"服务端对空 UDP 是「保持原样」,说成「留空 = 删掉」是一句后果静默的假话")
	}
	if !strings.Contains(body, "replaceLinkFollowUp(name: name, isCurrent: isCurrent)") {
		t.Error("收尾那句话不是 replaceLinkFollowUp 给的 —— 写死一句会对着没在跑的那台叫用户重连")
	}
	// **isCurrent 取自服务端刚返回的那份清单**,不是窗口传来的一个陈旧标志。
	if !strings.Contains(body, "let isCurrent = list.servers.contains { $0.current && $0.name == name }") {
		t.Error("isCurrent 不是从应答里派生的 —— 一个陈旧的标志会让这句话说反")
	}
	if strings.Contains(body, "reconnectBx()") {
		t.Error("换完链接直接重连了 —— 重连会断掉正在跑的连接,那必须是用户自己的一下")
	}
	follow, ok := swiftFunctionBody(code, "private func followUpAfterLinkReplaced(_ follow: ReplaceLinkFollowUp)")
	if !ok {
		t.Fatal("读不出 followUpAfterLinkReplaced 的函数体 —— 守卫已经失效,先修守卫")
	}
	gate := strings.Index(follow, "guard follow.offersReconnect else")
	ask := strings.Index(follow, "alert.runModal() == .alertFirstButtonReturn")
	call := strings.Index(follow, "reconnectBx()")
	if gate < 0 || ask < 0 || call < 0 {
		t.Fatal("读不出「按应答决定给不给重连按钮」那三步 —— 守卫已经失效,先修守卫")
	}
	if !(gate < ask && ask < call) {
		t.Errorf("顺序不对(门=%d 问=%d 重连=%d)—— 重连必须在用户按下那个按钮之后", gate, ask, call)
	}
}
