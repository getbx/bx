package cli

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	cli "github.com/urfave/cli/v2"

	"github.com/getbx/bx/internal/appattr"
	"github.com/getbx/bx/internal/supervisor"
)

// supervisorAppTraffic 是控制面 /v0/apps 的应答。取本地别名是为了让采样那一半
// 免于依赖真 socket 而可测 —— 判定与接线分开,是这个仓库反复付学费换来的形状。
type supervisorAppTraffic = supervisor.AppTrafficResponse

// appsSampleWindow 是默认采样窗口。
//
// **为什么必须采样而不是拉一次**:`/v0/apps?subscribe=1` 的第一次调用只是
// **订阅** —— 采集从那一刻才开始(设计前提是「没人看时不问内核、不记字节、
// 不攒历史」)。一个拉完就返回的命令给出的必然是空报告,而空报告与「这台机器
// 上真的没有连接」在输出上完全一样。
//
// 取 6 秒:短于订阅的 30 秒 TTL(不必续期),又够长到让一台空闲机器上冒出几条
// 连接。它是**下限不是全貌** —— 窗口里没发生的事这份报告答不出来,这句话
// 跟着输出一起给。
const appsSampleWindow = 6 * time.Second

// sampleAppTraffic 订阅 → 等一个窗口 → 再读。
//
// sleep 做成参数,是为了让上面那条形状(先订阅、真的等过、再读)可以被断言,
// 而不必真睡 6 秒:一个偶发慢的测试与一个偶发红的闸门同样糟。
func sampleAppTraffic(fetch func() (supervisorAppTraffic, error), sleep func(time.Duration), window time.Duration) (appsReport, error) {
	first, err := fetch()
	if err != nil {
		return appsReport{}, err
	}
	// **「没订阅上」与「订阅了但确实没有连接」是两件事。** 压成同一份空报告
	// 正是 apptraffic 整套设计里最贵的那条纪律要防的(三态原样透传)。
	if !first.Subscribed {
		reason := first.Error
		if reason == "" {
			reason = "Core 没有接受订阅,原因未说明"
		}
		return appsReport{}, errors.New("应用流量采集没有开始:" + reason)
	}
	sleep(window)
	second, err := fetch()
	if err != nil {
		return appsReport{}, err
	}
	if !second.Subscribed {
		reason := second.Error
		if reason == "" {
			reason = "订阅在采样窗口内失效,原因未说明"
		}
		return appsReport{}, errors.New("应用流量采集中断:" + reason)
	}
	return appsProjection(second.Report), nil
}

// appsProjection 把 Core 的应用流量报告投影成**给人和 agent 的那一份**。
//
// **它刻意不带可执行路径。** `appattr.AppRow.ExecPath` 是一次记档在案的信息面
// 扩大(能暴露安装位置、用户名 `/Users/<name>/…`、以及从 App Store 之外装了
// 什么),而 internal/appattr/publication_test.go 那条守卫写着:今天的发布面
// **恰好只有一条**(Core 控制 socket → Guardian 的 owner 门 → 菜单窗口),
// 而且「**加进来是可以的,悄悄加不行**」。
//
// 菜单要那个路径是为了画图标;agent 要回答的是「哪个应用走哪条路」,不是
// 「它装在哪儿」。所以这里按需要投影掉它 —— 这条路上它一个字节都不会出现,
// 那条守卫因此不必被改动。
//
// **这个窄化与 bx_status 当年那个窄化是两回事,别混为一谈**:那一个是**意外**的
// (没人决定过、Guardian 那半长出十几个键而投影没跟上,漏掉的字段不会有任何
// 东西报错);这一个是**刻意**的,有理由、有测试盯着,而且盯的是「序列化之后
// 的字节里不许出现路径」而不是「结构体里没有那个字段」。
type appsReport struct {
	Groups []appsGroup `json:"groups"`
	// BytesCaveat 跟着数据一起走,不是装饰:一个显示 0 B 却明明有活连接的行,
	// 不说明白会被读成「这条流是闲的」(菜单窗口底部那行小字的同一条理由)。
	BytesCaveat string `json:"bytes_caveat"`
}

type appsGroup struct {
	Path string    `json:"path"` // tunnel | direct | blocked
	Rows []appsRow `json:"rows"`
}

type appsRow struct {
	// App 为空串 = 问不出应用名。**保留而不是滤掉**:「unknown 占比高」本身
	// 是可用的故障信号,滤掉它等于把一个信号变成沉默。
	App       string   `json:"app"`
	Conns     int      `json:"conns"`
	BytesUp   int64    `json:"bytes_up"`
	BytesDown int64    `json:"bytes_down"`
	Rules     []string `json:"rules,omitempty"`
}

// appsBytesCaveat 与菜单窗口底部那行小字是同一句话(AppTrafficModel.swift 的
// appTrafficApproximateNote)。两处各写一份会漂开,而这句话的准确性本身就是
// 它存在的理由 —— 故措辞逐字对齐,改一处要记得改另一处。
const appsBytesCaveat = "Byte counts are approximate: ports get reused, and an app listed in " +
	"two sections may show all its bytes on one side."

func appsProjection(report appattr.Report) appsReport {
	out := appsReport{BytesCaveat: appsBytesCaveat}
	for _, g := range report.Groups {
		group := appsGroup{Path: string(g.Path)}
		for _, r := range g.Rows {
			group.Rows = append(group.Rows, appsRow{
				App:       r.App,
				Conns:     r.Conns,
				BytesUp:   r.BytesUp,
				BytesDown: r.BytesDown,
				Rules:     append([]string(nil), r.Rules...),
			})
		}
		out.Groups = append(out.Groups, group)
	}
	return out
}

// appsFlags / appsAction 是 `bx apps` 那条命令。
//
// **人和 agent 同一条路**:与 bx observe / bx inspect 同一个模式(CLI 出 JSON,
// MCP 薄转发)。多开一条只给 agent 的路,就是让两份输出各自演化、迟早说不同的话。
func appsFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "print machine-readable results"},
		&cli.DurationFlag{
			Name:  "for",
			Value: appsSampleWindow,
			Usage: "sampling window (how long to wait after subscribing before reading; anything that did not happen inside the window is not in this report)",
		},
	}
}

func appsAction(c *cli.Context) error {
	window := c.Duration("for")
	if window <= 0 {
		window = appsSampleWindow
	}
	rep, err := sampleAppTraffic(func() (supervisorAppTraffic, error) {
		return supervisor.FetchAppTraffic(statusSocketPath())
	}, func(d time.Duration) {
		select {
		case <-time.After(d):
		case <-c.Context.Done():
		}
	}, window)
	if err != nil {
		return cli.Exit(err.Error(), 1)
	}
	if c.Bool("json") {
		return writeJSON(os.Stdout, rep)
	}
	fmt.Printf("bx apps(最近 %s)\n", window)
	for _, g := range rep.Groups {
		if len(g.Rows) == 0 {
			continue
		}
		fmt.Printf("  %s\n", strings.ToUpper(g.Path))
		for _, r := range g.Rows {
			name := r.App
			if name == "" {
				// **不写成「(none)」之类的好话**:问不出应用名是一个事实,
				// 而「unknown 占比高」本身是可用的故障信号。
				name = "unknown app"
			}
			line := fmt.Sprintf("    %-28s 连接 %-4d ↑ %-9s ↓ %s", name, r.Conns,
				observeBytes(r.BytesUp), observeBytes(r.BytesDown))
			if len(r.Rules) > 0 {
				line += "  规则 " + strings.Join(r.Rules, ",")
			}
			fmt.Println(line)
		}
	}
	fmt.Println("  " + rep.BytesCaveat)
	return nil
}
