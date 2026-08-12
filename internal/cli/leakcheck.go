package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	// 本机事实那一半在起服务之前就采好,而且**从不下发给页面**:页面拿到的只有
	// 判完的结论(见 internal/leakserve/page.go 的 pageData)。
	facts := leakserve.CollectFacts(ctx, leakserve.LiveFactDeps())

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
		fmt.Println("bx leakcheck")
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
		"Contacted: " + rep.Endpoints.EchoV4 + " , " + rep.Endpoints.EchoV6 + " , " + rep.Endpoints.STUN,
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
	lines = append(lines, "Nothing was stored: bx keeps no history of this check.")
	return lines
}
