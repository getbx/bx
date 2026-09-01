package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

// `bx apps`:把「哪个应用走哪条路」发给**人和 agent**。
//
// 此前这份数据只有一条发布面:Core 控制 socket → Guardian 的 owner 门 →
// 菜单那个窗口。而 agent 拿不到它 —— 于是「腾讯会议为什么绕一圈」这个促成整个
// 功能的问题,agent 一个字都答不上来。
//
// **可执行路径刻意不出现在这条路上。**
// `appattr.AppRow.ExecPath` 是一次记档在案的信息面扩大(能暴露安装位置、用户名、
// 装了什么),而 internal/appattr/publication_test.go 那条守卫写着:今天的发布面
// 「恰好只有一条」,**加进来是可以的,悄悄加不行**。agent 要回答的是「哪个应用
// 走哪条路」,不是「它装在哪儿」—— 所以这条路按需要投影掉它,而不是顺手带上。
// 这个**刻意**的窄化与 bx_status 当年那个**意外**的窄化是两回事:那个是没人
// 决定过、字段就悄悄没了;这个有理由、有守卫。

func TestAppsProjectionNeverCarriesExecutablePaths(t *testing.T) {
	report := appattr.Report{Groups: []appattr.Group{{
		Path: appattr.PathTunnel,
		Rows: []appattr.AppRow{{
			App: "Google Chrome", Conns: 3, BytesUp: 100, BytesDown: 200,
			Rules:    []string{"*.example.com"},
			ExecPath: "/Users/somebody/Applications/Chrome.app/Contents/MacOS/Chrome",
		}},
	}}}

	out := appsProjection(report)
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	// 判据打在**序列化之后**:字段被投影掉了、还是只是 omitempty 恰好没输出,
	// 对读到这份 JSON 的 agent 是同一件事,而前者才是我们要的。
	for _, forbidden := range []string{"exec_path", "/Users/somebody", "Contents/MacOS"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("可执行路径漏进了 agent 那条路(命中 %q):%s", forbidden, encoded)
		}
	}
	// 而它该带的东西一样都不能少 —— 否则这条路就没有存在的意义。
	for _, want := range []string{"Google Chrome", "*.example.com", "tunnel"} {
		if !strings.Contains(string(encoded), want) {
			t.Fatalf("投影把有用的东西也丢了(缺 %q):%s", want, encoded)
		}
	}
}

// unknown 行(问不出应用名)**必须原样保留**,不许悄悄滤掉:
// 「unknown 占比高」本身是可用的故障信号(记档:2026-08-20 那次真机证据把
// 因果翻了过来),滤掉它等于把一个信号变成沉默。
func TestAppsProjectionKeepsUnknownRows(t *testing.T) {
	report := appattr.Report{Groups: []appattr.Group{{
		Path: appattr.PathDirect,
		Rows: []appattr.AppRow{{App: "", Conns: 7}},
	}}}
	out := appsProjection(report)
	if len(out.Groups) != 1 || len(out.Groups[0].Rows) != 1 {
		t.Fatalf("unknown 行被滤掉了: %+v", out)
	}
	if out.Groups[0].Rows[0].Conns != 7 {
		t.Fatalf("unknown 行的计数丢了: %+v", out.Groups[0].Rows[0])
	}
}

// 三组的分组必须原样过来:同一个应用可以同时出现在多组(一半域名直连、一半
// 走隧道是常态),压成一行等于把最有用的那一半扔掉。
func TestAppsProjectionKeepsEveryGroup(t *testing.T) {
	report := appattr.Report{Groups: []appattr.Group{
		{Path: appattr.PathTunnel, Rows: []appattr.AppRow{{App: "Zoom", Conns: 1}}},
		{Path: appattr.PathDirect, Rows: []appattr.AppRow{{App: "Zoom", Conns: 2}}},
		{Path: appattr.PathBlocked, Rows: []appattr.AppRow{{App: "Zoom", Conns: 3}}},
	}}
	out := appsProjection(report)
	if len(out.Groups) != 3 {
		t.Fatalf("分组被合并了: %+v", out.Groups)
	}
	seen := map[string]int{}
	for _, g := range out.Groups {
		seen[g.Path] = len(g.Rows)
	}
	for _, path := range []string{"tunnel", "direct", "blocked"} {
		if seen[path] != 1 {
			t.Fatalf("%s 组不见了或数目不对: %+v", path, seen)
		}
	}
}

// 字节数是近似值,这句话必须跟着数据走 —— 菜单窗口底部那行小字的同一条理由:
// 一个显示 0 B 却明明有活连接的行,不说明白会被读成「这条流是闲的」。
func TestAppsProjectionCarriesTheApproximationCaveat(t *testing.T) {
	out := appsProjection(appattr.Report{})
	if !strings.Contains(strings.ToLower(out.BytesCaveat), "approximate") {
		t.Fatalf("没有把「字节是近似值」这句话带给 agent: %q", out.BytesCaveat)
	}
}

// **一次性拉是拿不到数据的,而那正是最容易写出来的形状。**
//
// `/v0/apps?subscribe=1` 的第一次调用只是**订阅**:采集从那一刻才开始
// (设计前提是「没人看时不问内核、不记字节、不攒历史」)。菜单靠每 5 秒一拍
// 续期并逐步拿到数据;一个只拉一次就返回的命令,给出的必然是一份空报告 ——
// 而空报告与「这台机器上真的没有连接」在输出上完全一样。
//
// 故这条命令必须**采样一个窗口**:订阅 → 等 → 再拉。这条测试钉住那个形状。
func TestAppsSamplingSubscribesThenWaitsThenReads(t *testing.T) {
	var calls int
	fetch := func() (supervisorAppTraffic, error) {
		calls++
		if calls == 1 {
			// 第一次:订阅生效,但还没有任何数据 —— 真实形状。
			return supervisorAppTraffic{Subscribed: true}, nil
		}
		return supervisorAppTraffic{Subscribed: true, Report: appattr.Report{
			Groups: []appattr.Group{{
				Path: appattr.PathTunnel,
				Rows: []appattr.AppRow{{App: "Zoom", Conns: 2}},
			}},
		}}, nil
	}
	var slept []time.Duration
	out, err := sampleAppTraffic(fetch, func(d time.Duration) { slept = append(slept, d) }, 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if calls < 2 {
		t.Fatalf("只拉了 %d 次 —— 第一次只是订阅,拿到的必然是空报告", calls)
	}
	if len(slept) == 0 {
		t.Fatal("没有等待窗口 —— 订阅之后立刻读,采集还没来得及记任何东西")
	}
	if len(out.Groups) != 1 || len(out.Groups[0].Rows) != 1 {
		t.Fatalf("窗口结束后没有拿到数据: %+v", out)
	}
}

// 「没订阅上」与「订阅了但确实没有连接」是两件事,不许压成同一份空报告 ——
// 这是 apptraffic 那一整套设计里最贵的那条纪律(三态原样透传)。
func TestAppsSamplingDistinguishesNotSubscribedFromEmpty(t *testing.T) {
	_, err := sampleAppTraffic(func() (supervisorAppTraffic, error) {
		return supervisorAppTraffic{Subscribed: false, Error: "app attribution unavailable"}, nil
	}, func(time.Duration) {}, time.Second)
	if err == nil {
		t.Fatal("没订阅上却当成了「没有连接」—— 两者在输出上必须分得开")
	}
	if !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("没把 Core 给的理由带出来: %v", err)
	}
}

// **这条守卫盯的是设计约束,不是某一行代码。**
//
// 投影今天不带 ExecPath,而它最容易被「顺手补全」——「既然 AppRow 有这个字段,
// 一起带上不是更完整吗」。internal/appattr/publication_test.go 那条白名单守卫
// 拦得住**源码里提到字段名**,但拦不住有人在这里写
// `App: r.App + " (" + r.ExecPath + ")"` 之类的拼接。
//
// 所以这里从**行为**兜:任何看起来像可执行路径的东西都不许出现在序列化结果里。
func TestAppsProjectionRejectsPathLookingContentAnywhere(t *testing.T) {
	report := appattr.Report{Groups: []appattr.Group{{
		Path: appattr.PathTunnel,
		Rows: []appattr.AppRow{{
			App:      "/Applications/Sneaky.app/Contents/MacOS/Sneaky",
			Rules:    []string{"/Users/someone/secret"},
			ExecPath: "/Users/someone/Applications/X.app/Contents/MacOS/X",
			Conns:    1,
		}},
	}}}

	encoded, err := json.Marshal(appsProjection(report))
	if err != nil {
		t.Fatal(err)
	}
	// App 与 Rules 是 Core 给什么就是什么(应用名本来就可能带路径样子的内容),
	// 故这里只钉**ExecPath 那一个值**绝不出现 —— 它是唯一由本投影决定去留的。
	if strings.Contains(string(encoded), "/Users/someone/Applications") {
		t.Fatalf("ExecPath 漏进了 agent 那条路:%s", encoded)
	}
}
