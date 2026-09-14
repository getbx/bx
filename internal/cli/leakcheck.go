package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/getbx/bx/internal/leakcheck"
	"github.com/getbx/bx/internal/leakserve"
	"github.com/urfave/cli/v2"
)

func leakcheckFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读结果"},
	}
}

// guardLeakCheckPrivileges 拒绝 root。
//
// **不是「照跑但更强大」。** 以 root 跑会让那个 loopback HTTP 服务变成 root
// 进程的端口 —— 一个隐私工具不该为了做体检而让 root 进程对浏览器开 HTTP。
// 而且答案不会更准:需要的本机事实(route -n get / scutil / networksetup /
// Guardian 的 0666 socket)一个都不需要 root。
//
// 吃 euid 而不是自己去读,是为了让它在任何身份下都可测。接线由
// TestLeakCheckActionCallsThePrivilegeGuardFirst 单独证明 —— 判定存在不等于
// 判定被调用了。
func guardLeakCheckPrivileges(euid int) error {
	if euid != 0 {
		return nil
	}
	return errors.New(
		"bx leakcheck 不能用 sudo 运行。它需要的本机事实一个都不需要 root," +
			"而以 root 打开一个对浏览器开放的本机端口会把这个检测本身变成风险。\n" +
			"请去掉 sudo,直接运行:bx leakcheck  (run it again without sudo)",
	)
}

func leakcheckAction(c *cli.Context) error {
	if err := guardLeakCheckPrivileges(os.Geteuid()); err != nil {
		return cli.Exit(err.Error(), 1)
	}

	ctx := c.Context
	// **联网之前先说要联系谁。** 这条纪律此前整个由页面兑现(它在用户点
	// 「Run the check」之前原样列出四个第三方),而可达性探测是 **bx 自己**从这台
	// 机器发出去的、页面一个字节都不经手 —— 于是这句话只能由 CLI 说,而且必须排在
	// 第一个请求**之前**,不是事后在报告里补一句。
	// --json 走 stderr:stdout 那一份是机器读的,不许掺东西。
	announceReachTargets(c.Bool("json"))

	// 本机事实那一半在起服务之前就采好,而且**从不下发给页面**:页面拿到的只有
	// 判完的结论(见 internal/leakserve/page.go 的 pageData)。
	facts := collectLeakCheckFacts(ctx, leakserve.LiveFactDeps(), leakserve.LiveReachDeps())

	srv, err := leakserve.Listen(leakserve.Options{
		Judge: func(browser leakcheck.BrowserReport) leakcheck.Report {
			return leakcheck.Judge(time.Now(), browser, facts)
		},
	})
	if err != nil {
		return cli.Exit("起不了本机检测服务:"+err.Error(), 1)
	}
	defer func() { _ = srv.Close() }()
	go srv.Serve()

	if !c.Bool("json") {
		fmt.Println("正在打开浏览器页面。页面会先列出要联系的第三方,点「Run the check」才开始。")
		fmt.Println("入口(浏览器没自动打开就手动粘贴):", srv.URL())
		fmt.Println("最多等待 2 分钟,之后本机服务会自己关闭。")
	}
	if err := openBrowserURL(ctx, srv.URL()); err != nil {
		// 打不开浏览器不是失败:URL 已经打印出来了,用户可以自己粘。
		if !c.Bool("json") {
			fmt.Println("没能自动打开浏览器(", err, "),请手动打开上面那个地址。")
		}
	}

	report := srv.Wait(ctx)

	if c.Bool("json") {
		// **这份 JSON 是只写的**:verdict 序列化成 ok/bad/not checked 三个词,
		// leakcheck.Verdict 刻意没有 UnmarshalJSON。谁将来要在 Go 里读回它,
		// 必须连同 internal/leakcheck 那条守卫一起论证(见 verdict_test.go)。
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	for _, line := range renderLeakCheckReport(report) {
		fmt.Println(line)
	}
	return nil
}

// renderLeakCheckReport 把成品结论渲染成终端里的行。
//
// **不做任何总结性的好话。** 一份全 not checked 的报告渲染出「没有发现泄漏」
// 就是设计风险四那种最坏的失败:把「没跑成」说成「一切正常」。异常数为 0 完全
// 可能是三条一条都没检查成,所以那个数字永远与 not checked 的条数并排出现。
func renderLeakCheckReport(rep leakcheck.Report) []string {
	lines := []string{
		"",
		// **说清这一行说的是谁。** 裸着一个「Contacted:」会被读成「bx 这一轮联系过的
		// 全部第三方」,而第四段接上之后那已经不真了:可达性探测是 bx 自己从本机
		// 发的四个 GET(披露在 announceReachTargets,而且排在第一个请求之前;这份
		// 报告里由第四段的标题与每一条结论各自点名)。少报一个第三方不是排版问题
		// —— 而一行**范围说小了**的实话,好过一行读起来涵盖一切的假话。
		"Contacted by the browser half: " + strings.Join(rep.Endpoints.All(), " , "),
		"",
	}
	shown := map[leakcheck.Section]bool{}
	for _, f := range rep.Findings {
		// 分段标题在这里也要出 —— 两段的责任人不同,而那个区别正是重点:
		// 漏了是 bx 该修的,指纹大多不是。
		if !shown[f.Section] {
			shown[f.Section] = true
			switch f.Section {
			case leakcheck.SectionIdentity:
				lines = append(lines,
					"CAN YOU BE SINGLED OUT",
					"  Mostly not bx's to fix — browser and system traits that can identify",
					"  you even when nothing leaks.",
					"")
			case leakcheck.SectionSurface:
				lines = append(lines,
					"WHAT SITES CAN READ",
					"  Neither good nor bad, and nothing here is a verdict. Listed so you can",
					"  see what every site gets without asking.",
					"")
			case leakcheck.SectionReach:
				// **第四段必须有自己的标题。** 它此前落进下面那个 default,于是四条
				// 可达性结论被画在「流量去哪儿」这个**安全**标题底下 —— 用户会把
				// 「连不上」读成「流量泄漏」,正是 spec §6.1 要避免的那件事,只是
				// 发生在渲染层。这一段的坏消息是「你用不了」,不是「你泄漏了」。
				lines = append(lines,
					"CAN THIS PATH REACH THE AI SERVICES",
					"  Not a security question. bx contacted each of these from this machine to",
					"  see whether this path gets through — \"could not reach\" is not a leak.",
					"")
			default:
				lines = append(lines,
					"WHERE YOUR TRAFFIC GOES",
					"  What bx, or whichever tunnel is carrying this machine, is answerable for.",
					"")
			}
		}
		// verdict 三态**逐字**打印。只在 bad 时打印它,会让 not checked 从输出里
		// 消失 —— 那正好读成「一切正常」。
		lines = append(lines, fmt.Sprintf("[%s] %s", f.Verdict, f.Title))
		lines = append(lines, "    "+f.Summary)
		for _, e := range f.Evidence {
			lines = append(lines, "      · "+e)
		}
		lines = append(lines, "")
	}
	if len(rep.Evidence) > 0 {
		lines = append(lines, "Observed:")
		for _, e := range rep.Evidence {
			lines = append(lines, "  · "+e)
		}
		lines = append(lines, "")
	}
	notChecked := 0
	for _, f := range rep.Findings {
		// **第四段不进这一格。** 可达性的「没问出来」与「像是人机挑战」两态都映射
		// 成 Verdict NotChecked,不拦住它们,这个数就从「有几条泄漏结论没检查成」
		// 悄悄变成「泄漏结论 + 可达性结论一共有几条没检查成」—— 两段各自计数、
		// 绝不合成,是这份报告的整条纪律(它们自己的计数在下面那行)。
		if f.Section == leakcheck.SectionReach {
			continue
		}
		if f.Verdict == leakcheck.NotChecked {
			notChecked++
		}
	}
	// **两个数分开报。** 合成一个总数时它永远不为零(一台普通 Chrome 就是不防
	// 指纹),于是会被训练成噪声,连带把真正的泄漏一起淹掉。
	lines = append(lines, fmt.Sprintf(
		"%d leak(s) in the traffic path, %d identifying trait(s), %d not checked.",
		rep.AnomalyCount, rep.IdentityCount, notChecked,
	))
	// **第四段单独一行,五态并排,为零也打印。**
	//
	// 合成之后「一条都没查出来」与「查了、全可达」在屏幕上长得一模一样,而这一段
	// 存在的唯一理由就是把这两件事分开。用的是 ReachState 自己那套词
	// (reachable/refused/unreachable/challenged/undetermined),**刻意不复用上面
	// 那句里的 "not checked"** —— 两个数用同一个词,读的人分不清哪一格在说什么,
	// 而这正是上面那个循环刚刚堵掉的那种合并。
	lines = append(lines, fmt.Sprintf(
		"AI services: %d reachable, %d refused, %d unreachable, %d challenged, %d undetermined.",
		rep.Reach.Reachable, rep.Reach.Refused, rep.Reach.Unreachable,
		rep.Reach.Challenged, rep.Reach.Undetermined,
	))
	lines = append(lines, "Nothing was stored: bx keeps no history of this check.")
	return lines
}

// collectLeakCheckFacts 采一轮本机事实,再**单独一跳**跑可达性探测。
//
// **绝不把探测塞进 CollectFacts。** 那一轮的预算是 leakserve.DefaultFactsBudget
// = 5 秒,量的是毫秒级的本机只读观测;而可达性探测是四次跨洋 TLS 握手,最坏
// 4×8 秒。塞进去的后果不是慢一点,是探测跑到一半被掐断、剩下的目标全部变成
// 「没问出来」—— 那个答案在屏幕上与「这条路真的不通」一模一样,而这一段存在的
// 全部意义就是把这两件事分开。预算住在 leakserve.CollectReach 里。
func collectLeakCheckFacts(ctx context.Context, deps leakserve.FactDeps, reach leakserve.ReachDeps) leakcheck.LocalFacts {
	facts := leakserve.CollectFacts(ctx, deps)
	facts.ReachProbes = leakserve.CollectReach(ctx, reach)
	return facts
}

// announceReachTargets 在发出第一个探测**之前**把要联系的地址原样列出来。
//
// 清单从 leakcheck.ReachTargets() 现取,**不手抄第二份** —— 页面那条
// 「Contacted:」曾因手写拼接而少报一个第三方(见 EndpointDisclosure.All 的注释):
// 少报一个第三方不是排版问题,是把「联网之前把要联系的人说全」这个用户可见契约
// 打了折。
func announceReachTargets(jsonOut bool) {
	w := io.Writer(os.Stdout)
	if jsonOut {
		// stdout 那一份是机器读的。
		w = os.Stderr
	}
	fmt.Fprintln(w, "bx leakcheck")
	fmt.Fprintln(w, "bx 现在会从**这台机器**探测下面这些地址(走你当前的网络路径,不绕过隧道):")
	for _, tgt := range leakcheck.ReachTargets() {
		fmt.Fprintln(w, "  ·", tgt.URL)
	}
}
