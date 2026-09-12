package cli

import (
	"os"
	"path/filepath"
	"regexp"
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
	// 那一档条件不同,归 otherServersEmptyNote —— 两句都必须真的被摆进视图树。
	for _, judge := range []string{
		"serverListEmptyReason(list: list)",
		"otherServersEmptyNote(list: list, core: core)",
	} {
		if !strings.Contains(body, judge) {
			t.Errorf("render 没有用 %s —— 对一份单服务器配置说「还没有服务器」"+
				"是一句当场就能被证伪的假话,而只有一台时那一句压根不会出现", judge)
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
	if !strings.Contains(funnel, "core: core,") {
		t.Error("那份实时数据没有被传进窗口")
	}
	// 在飞状态也必须到得了窗口,否则确认之后二十几秒屏幕上什么都不发生。
	if !strings.Contains(funnel, "switchingTo: switchingTo,") {
		t.Error("切换在飞状态没有传进窗口 —— 那一行不会说 Switching…、Use 也不会灰")
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
	// 那一块上的每一样都得真的进格子:少一样不会报错,只会静默不显示。
	for _, field := range []string{
		"panel.endpoint", "panel.statusLine", "panel.udpLine",
		"panel.throughput", "panel.coreSilentNote", "panel.runningNote",
		"panel.runningConfirmed",
	} {
		if !strings.Contains(panel, field) {
			t.Errorf("当前那一块没用上 %s —— 那一项永远不显示,而界面看起来完全正常", field)
		}
	}
	// **只有实测失败才画红。** `statusLineIsBad` 是那个三态判据的落点:
	// 窗口自己写 `healthy == false` 就会把「没说」画成「不健康」。
	if !strings.Contains(panel, "panel.statusLineIsBad") {
		t.Error("隧道健不健康的红色不是由 statusLineIsBad 决定的")
	}
}

// **三个动词按 `servers_edit` 门控,不按 `servers`。**
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
	if !strings.Contains(funnel, "canEdit: canEdit") {
		t.Error("那道门没有传进窗口")
	}
	// 窗口那一半:`⋯` 必须真的挂在这道门后面,而不是无条件画出来。
	window := stripSwiftComments(menuServersWindowSource(t))
	body, ok := swiftFunctionBody(window, "private func serverView(_ row: ServerRow) -> NSView")
	if !ok {
		t.Fatal("读不出 serverView 的函数体 —— 守卫已经失效,先修守卫")
	}
	more := strings.Index(body, "moreButton(")
	if more < 0 {
		t.Fatal("候选行上没有那个 ⋯ —— 三个动词一个都点不到")
	}
	gate := strings.LastIndex(body[:more], "if canEdit {")
	other := strings.LastIndex(body[:more], "if ")
	if gate < 0 || gate != other {
		t.Errorf("⋯ 不是由 canEdit 直接门控的(最近的条件在 %d,门在 %d)", other, gate)
	}
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
		"private func confirmAndRemoveServer(name: String, host: String)")
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
