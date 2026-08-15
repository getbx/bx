package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	success := strings.Index(body, "case .success(let outcome):")
	if success < 0 {
		t.Fatal("读不出成功分支 —— 守卫已经失效,先修守卫")
	}
	tail := body[success:]
	if !strings.Contains(tail, "outcome.applied ?") {
		t.Error("标题没有按 outcome.applied 分支 —— 只写了配置也会显示成「已切换」")
	}
	if !strings.Contains(tail, "serverSwitchOutcomeMessage(result: outcome)") {
		t.Error("文案不是 serverSwitchOutcomeMessage(result:) 生成的 —— " +
			"那个纯函数才是「没生效就说没生效」那句话的所在")
	}
}

// 换过去之后旧的探测结果必须作废。留着它,用户读到的是**上一台**的出口 IP,
// 而他刚做的恰恰是换出口 —— 这是这个界面最容易骗到人的一处。
func TestMacMenuDropsTheStaleExitIPAfterSwitching(t *testing.T) {
	body, ok := swiftFunctionBody(menuMainSwiftSource(t),
		"private func confirmAndSwitchServer(name: String, host: String)")
	if !ok {
		t.Fatal("读不出 confirmAndSwitchServer 的函数体 —— 守卫已经失效,先修守卫")
	}
	reset := strings.Index(body, "exitIPProbe = .unknown")
	send := strings.Index(body, "switchServer(name:")
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
