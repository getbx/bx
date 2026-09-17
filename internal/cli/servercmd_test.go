package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/elevate"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/supervisor"
	"github.com/urfave/cli/v2"
)

// 清单要一眼看出**哪台在用**,并且带上出口主机 —— 名字可以是任意的,
// 而用户真正关心的是「流量从哪出去」。
func TestRenderServerList(t *testing.T) {
	// **一条用 bx:// 换壳,一条裸链接。** 真实配置里存的是换壳的,而第一版
	// fixture 全用裸链接 —— 于是真机上整串 base64 被当成主机名打给了用户,
	// 而测试全绿(2026-08-14)。
	view := serverListView{
		Current: "us",
		Entries: []guardian.ServerEntry{
			{Name: "hk", Host: "1.1.1.1", UDPHost: "1.1.1.1:udp"},
			{Name: "us", Host: "2.2.2.2", Current: true},
		},
	}
	out := renderServerList(view)
	if !strings.Contains(out, "1.1.1.1") || !strings.Contains(out, "2.2.2.2") {
		t.Errorf("没列出出口主机:\n%s", out)
	}
	// **只在服务器那几行里数。** 第一版数的是整段里的 ● —— 而表头的图例
	// (「● = 当前在用」)也含它,末尾那句 `bx server use <name>` 还含 "us";
	// 于是两条断言都在测旁边的东西。判据是「恰好一行被标记,且是当前那一行」。
	marked, rows := 0, 0
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.Contains(line, ".") {
			continue // 不是服务器行(表头、空行、末尾提示)
		}
		rows++
		if strings.HasPrefix(strings.TrimSpace(line), "●") {
			marked++
			if !strings.Contains(line, "us") {
				t.Errorf("标记打在了错误的那一行:%q", line)
			}
		}
	}
	if rows != 2 {
		t.Fatalf("识别出 %d 行服务器,应为 2:\n%s", rows, out)
	}
	if marked != 1 {
		t.Errorf("被标记的行有 %d 行,应当恰好 1 行:\n%s", marked, out)
	}
	// UDP 有没有单独一条要看得出来 —— 少了它 UDP 会静默走主传输。
	if !strings.Contains(out, "UDP") {
		t.Errorf("没说清哪台带独立 UDP 传输:\n%s", out)
	}
	if out == "" || !strings.Contains(renderServerList(serverListView{}), "没有") {
		t.Errorf("空清单时应当说人话")
	}
}

// **切换之后说什么,取决于热切成没成。**
//
// 热切成功 → 立即生效;失败 → 必须明说要重启,否则用户以为已经切过去了
// (那正是 2026-08-06 那次事故的形状)。
func TestSwitchOutcomeMessage(t *testing.T) {
	hot := switchOutcomeMessage("us", "2.2.2.2", nil)
	if strings.Contains(hot, "bx up") {
		t.Errorf("热切成功却还让用户重启:%s", hot)
	}
	if !strings.Contains(hot, "2.2.2.2") {
		t.Errorf("没说清现在从哪出去:%s", hot)
	}

	cold := switchOutcomeMessage("us", "2.2.2.2", errors.New("dial unix: no such file"))
	for _, want := range []string{"bx down", "bx up", "已写入"} {
		if !strings.Contains(cold, want) {
			t.Errorf("热切失败时没说清下一步(缺 %q):%s", want, cold)
		}
	}
	// **不许把失败说成成功。**
	if strings.Contains(cold, "已生效") {
		t.Errorf("热切失败却说已生效:%s", cold)
	}
}

// **`/v0/server` 是 commit-confirmed:武装之后必须验证再确认,否则死手会还原。**
//
// 真机(2026-08-14):我的第一版只调了武装那一步,命令打出「立即生效」,而
// Core 日志里 `死手自动回滚:已还原到 last-known-good` 出现了两次 —— 用户看到
// 的「切过去了」只是回滚前的那个窗口。**那是这个仓库最忌讳的一类失败:
// 把没做成的事报成做成了。**
//
// 而这个设计本身是对的:新服务器要是连不上,死手会自动把你救回来。
// 缺的只是「验证 + 确认」这两步。
func TestSwitchServerArmsVerifiesThenCommits(t *testing.T) {
	var steps []string
	err := supervisor.SwitchServer(supervisor.SwitchDeps{
		Arm:      func(link, udp string) error { steps = append(steps, "arm"); return nil },
		Healthy:  func() bool { steps = append(steps, "verify"); return true },
		Commit:   func() error { steps = append(steps, "commit"); return nil },
		Rollback: func() error { steps = append(steps, "rollback"); return nil },
	}, "us", "vless://u@2.2.2.2:443", "")
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(steps, "→")
	if got != "arm→verify→commit" {
		t.Fatalf("步骤 = %s,want arm→verify→commit", got)
	}
}

// **新隧道不健康时立刻回滚,并且如实报错。**
//
// 等死手自己动手也能回到原状,但那要等上一段时间,而这段时间里用户的流量
// 走在一条不通的隧道上 —— 而且命令还会告诉他切成功了。
func TestSwitchServerRollsBackWhenTheNewTunnelIsUnhealthy(t *testing.T) {
	var steps []string
	err := supervisor.SwitchServer(supervisor.SwitchDeps{
		Arm:      func(string, string) error { steps = append(steps, "arm"); return nil },
		Healthy:  func() bool { steps = append(steps, "verify"); return false },
		Commit:   func() error { steps = append(steps, "commit"); return nil },
		Rollback: func() error { steps = append(steps, "rollback"); return nil },
	}, "us", "vless://u@2.2.2.2:443", "")
	if err == nil {
		t.Fatal("新隧道不健康却报告成功")
	}
	got := strings.Join(steps, "→")
	if strings.Contains(got, "commit") {
		t.Fatalf("不健康却确认了:%s", got)
	}
	if !strings.HasSuffix(got, "rollback") {
		t.Fatalf("没有立刻回滚:%s", got)
	}
	if !strings.Contains(err.Error(), "ROLLED BACK") {
		t.Errorf("错误里没说清已经回滚,用户不知道自己现在在哪台:%v", err)
	}
}

// 武装就失败时**不许**再去回滚(没有东西可回滚),也不许确认。
func TestSwitchServerStopsWhenArmFails(t *testing.T) {
	var steps []string
	err := supervisor.SwitchServer(supervisor.SwitchDeps{
		Arm:      func(string, string) error { steps = append(steps, "arm"); return errors.New("boom") },
		Healthy:  func() bool { steps = append(steps, "verify"); return true },
		Commit:   func() error { steps = append(steps, "commit"); return nil },
		Rollback: func() error { steps = append(steps, "rollback"); return nil },
	}, "us", "vless://u@2.2.2.2:443", "")
	if err == nil {
		t.Fatal("武装失败却报告成功")
	}
	if strings.Join(steps, "→") != "arm" {
		t.Fatalf("武装失败后还做了别的:%v", steps)
	}
}

// **CLI 与菜单必须显示同一批字段。** 两个界面对同一台服务器说不同的话,是这个
// 仓库反复栽的形状 —— 而它们现在也确实来自同一个端点、同一份数据。
func TestServerListShowsLatencyAndThroughput(t *testing.T) {
	out := renderServerList(serverListView{
		Current: "hk", Tested: true,
		Entries: []guardian.ServerEntry{
			{
				Name: "hk", Host: "1.1.1.1", Current: true,
				Probe: &guardian.ProbeReport{Measured: true, Reachable: true, RTTMS: 12}, PeakBPS: 3_100_000,
			},
			{
				Name: "us", Host: "2.2.2.2",
				Probe:   &guardian.ProbeReport{Measured: true, Reachable: true, RTTMS: 180},
				PeakBPS: 8_000_000, PeakAgeSeconds: 7200,
			},
		},
	})
	for _, want := range []string{"12 ms", "180 ms", "3.1 MB/s", "8.0 MB/s"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里没有 %q:\n%s", want, out)
		}
	}
	// **历史数字必须带年龄。** 不带年龄的历史读起来像现状。
	if !strings.Contains(out, "2 小时前") {
		t.Errorf("历史吞吐没标出年龄:\n%s", out)
	}
	// 刚观测到的那台不写「0 分钟前」—— 那只会让人怀疑数字坏了。
	if strings.Contains(out, "0 分钟前") {
		t.Errorf("刚观测到的却写了年龄:\n%s", out)
	}
}

// 探测失败要说**原因**,不是一个光秃秃的叉:用户要分得清是服务器关了,
// 还是自己这条网络的问题。
//
// **fixture 里 `Measured` 不许省。** 省掉它写出来的是一份**生产永远产不出**的
// 报告(没测成、却报着一个延迟 / 一个失败原因),而那正是本仓库「测试输入让
// 待守属性不可见」的形状:这几条此前全靠 `Error != ""` 那条反推才绿。
func TestServerListExplainsProbeFailures(t *testing.T) {
	out := renderServerList(serverListView{
		Tested: true,
		Entries: []guardian.ServerEntry{
			{Name: "dead", Host: "3.3.3.3", Probe: &guardian.ProbeReport{
				Measured: true, Error: "超时(没有应答)", ErrorCode: supervisor.ProbeErrTimeout,
			}},
		},
	})
	if !strings.Contains(out, "超时") {
		t.Errorf("没说清失败原因:\n%s", out)
	}
	if strings.Contains(out, "0 ms") {
		t.Errorf("没通却显示成 0 ms:\n%s", out)
	}
}

// **三态由 `Measured` 说了算,不许用 `Error != ""` 反推**(spec §6.1 明令禁止)。
//
// 这一段此前完全不读 `Measured`:`Reachable == false` 时看 `Error` 空不空,
// 空就写「不可达」。今天输出恰好是对的,只是因为**生产那三个产地**在
// `Measured=false` 时总带一句中文原因 —— 也就是说这条判据的正确性挂在另一个
// 包的实现细节上,而不是挂在契约上。菜单那半(`probePresentation`)读的是
// `Measured`,两个消费方就此漂开。
//
// 三种形状各喂一遍,而**决定性的是第三种**:没测成、原因也没说 —— 那时说
// 「不可达」就是把一台**根本没测过**的服务器判死。
func TestServerListReadsTheMeasuredFlagNotTheErrorString(t *testing.T) {
	render := func(p *guardian.ProbeReport) string {
		return renderServerList(serverListView{
			Tested:  true,
			Entries: []guardian.ServerEntry{{Name: "hk", Host: "1.1.1.1", Probe: p}},
		})
	}
	t.Run("测了、通了", func(t *testing.T) {
		out := render(&guardian.ProbeReport{Measured: true, Reachable: true, RTTMS: 12})
		if !strings.Contains(out, "12 ms") {
			t.Errorf("测通了却没写延迟:\n%s", out)
		}
	})
	t.Run("测了、没通", func(t *testing.T) {
		out := render(&guardian.ProbeReport{
			Measured: true, Error: "连接被拒(端口没在听)",
			ErrorCode: supervisor.ProbeErrRefused,
		})
		if !strings.Contains(out, "连接被拒") {
			t.Errorf("没说清失败原因:\n%s", out)
		}
	})
	t.Run("没测成、原因也没说", func(t *testing.T) {
		out := render(&guardian.ProbeReport{Measured: false})
		if strings.Contains(out, "不可达") {
			t.Errorf("没测成却被判成「不可达」—— 一台好服务器被说成坏的:\n%s", out)
		}
		if !strings.Contains(out, "没测成") {
			t.Errorf("没测成这件事一个字都没说:\n%s", out)
		}
	})
	t.Run("测了、没通、原因也没说", func(t *testing.T) {
		// 反面自检:少了它,一个「永远说没测成」的实现照样满足上面那条,
		// 而「这台服务器真的连不上」就再也说不出来了。
		out := render(&guardian.ProbeReport{Measured: true})
		if !strings.Contains(out, "不可达") {
			t.Errorf("测了确实没通,却没说不可达:\n%s", out)
		}
	})
}

// 没测过的那台**一个字都不说** —— 每台后面挂一行「未测试」是墙纸,
// 而墙纸会训练人忽略这一栏。
func TestServerListSaysNothingAboutUntestedServers(t *testing.T) {
	out := renderServerList(serverListView{
		Entries: []guardian.ServerEntry{{Name: "hk", Host: "1.1.1.1", Current: true}},
	})
	for _, unwanted := range []string{"未测试", "ms", "峰值"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("没测过却出现了 %q:\n%s", unwanted, out)
		}
	}
	// 但要告诉用户怎么测 —— 否则这个功能没人找得到。
	if !strings.Contains(out, "--test") {
		t.Errorf("没告诉用户怎么测:\n%s", out)
	}
}

// **退化必须说出来。** Guardian 不可达时安静地少几列,用户会以为那几台真的
// 没有数据,而实际上是没问到 —— 与 Tristate 同一条纪律。
func TestDegradedServerListSaysWhatItCouldNotAsk(t *testing.T) {
	out := renderServerList(serverListView{
		Degraded: true,
		Entries:  []guardian.ServerEntry{{Name: "hk", Host: "1.1.1.1", Current: true}},
	})
	if !strings.Contains(out, "1.1.1.1") {
		t.Errorf("退化时连配置里的内容都没显示:\n%s", out)
	}
	if !strings.Contains(out, "没问到") {
		t.Errorf("退化了却不说:\n%s", out)
	}
	// 退化时不该再劝用户 --test:那条路这会儿本来就走不通。
	if strings.Contains(out, "--test") {
		t.Errorf("Guardian 不可达却让用户去 --test:\n%s", out)
	}
}

// 年龄的门槛与菜单侧一致(两分钟)。两边不一致的话,同一条记录在 CLI 里
// 「刚刚」而在菜单里「1 分钟前」。
func TestHumanAgeMatchesTheMenuThreshold(t *testing.T) {
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{
		{0, ""},
		{119 * time.Second, ""},
		{2 * time.Minute, "2 分钟前"},
		{2 * time.Hour, "2 小时前"},
		{72 * time.Hour, "3 天前"},
	} {
		if got := humanAge(tc.age); got != tc.want {
			t.Errorf("humanAge(%v) = %q, want %q", tc.age, got, tc.want)
		}
	}
}

// **终端不许再断言配置里那台就是流量出口。**
//
// 热切换是先写配置再切,所以切换失败的那一刻配置已经是新那台了。只按 Current
// 打那个 ●,`bx server list` 就在同一秒里断言你的流量从一台它其实没走的机器
// 出去 —— 正是这一轮从窗口里拿掉的那句话,而两个界面对同一台服务器说不同的话
// 是这个仓库反复栽的形状。
func TestServerListMarksTheRunningOneNotJustTheConfiguredOne(t *testing.T) {
	out := renderServerList(serverListView{
		Current: "us", Running: "hk",
		Entries: []guardian.ServerEntry{
			{Name: "hk", Host: "1.1.1.1"},
			{Name: "us", Host: "2.2.2.2", Current: true},
		},
	})
	// 只在服务器那几行里数(表头图例本身也含这两个符号)。
	var marked, configured string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(line, ".") || len(strings.Fields(line)) < 2 {
			continue
		}
		if strings.HasPrefix(trimmed, "●") {
			marked = trimmed
		}
		if strings.HasPrefix(trimmed, "○") {
			configured = trimmed
		}
	}
	if !strings.Contains(marked, "hk") {
		t.Errorf("● 没打在实际在跑的那一行上:%q\n%s", marked, out)
	}
	if !strings.Contains(configured, "us") {
		t.Errorf("配置里选的那一行没有单独标出来:%q\n%s", configured, out)
	}
	// 两者不一致本身要说出来,并给出路 —— 一个用户看不懂的符号等于没标。
	for _, want := range []string{"配置里选的是 us", "流量此刻从 hk 出去", "" + elevate.Prefix + "bx up"} {
		if !strings.Contains(out, want) {
			t.Errorf("输出里没有 %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "● = 当前在用") {
		t.Errorf("两者不一致时还在说「当前在用」:\n%s", out)
	}
}

// **问不出来时别拿配置去冒充它。**
//
// 旧 Guardian 不发 running 这个键,Core 没在跑时也发不出;那时 ● 只能表示
// 「配置里选的」,图例必须如实说 —— 与 Tristate 同一条:「没问出来」不是
// 一个确定的答案。
func TestServerListDoesNotClaimTheConfiguredOneIsRunningWhenItCannotAsk(t *testing.T) {
	out := renderServerList(serverListView{
		Current: "us",
		Entries: []guardian.ServerEntry{{Name: "us", Host: "2.2.2.2", Current: true}},
	})
	if strings.Contains(out, "● = 当前在用") {
		t.Errorf("没问出来实际在跑的是哪一台,却断言了「当前在用」:\n%s", out)
	}
	if !strings.Contains(out, "没问到") {
		t.Errorf("没问出来却不说:\n%s", out)
	}
	// 配置里那台仍要标出来 —— 否则这条守卫可以靠「什么都不标」满足。
	if !strings.Contains(out, "●") {
		t.Errorf("配置里选的那台连标记都没有:\n%s", out)
	}
}

// **Guardian 报的 running 必须真的到得了渲染层。**
//
// 少接一个字段不会有编译错误,输出上也只是少了一句话 —— 而它恰恰是这一整条
// 改动唯一要说的那句。
func TestServerListViewCarriesTheRunningServerFromGuardian(t *testing.T) {
	view := serverListViewFrom(guardian.ServerListResponse{
		Servers: []guardian.ServerEntry{{Name: "hk"}, {Name: "us", Current: true}},
		Current: "us", Running: "hk",
	}, false)
	if view.Running != "hk" {
		t.Fatalf("Guardian 报的 running 在路上被丢掉了:%q", view.Running)
	}
	if view.Current != "us" {
		t.Fatalf("Current = %q, want us —— 两者并列,绝不合并", view.Current)
	}
}

// **这条「怎么加第二台」的提示必须是一条真跑得起来的命令。**
//
// 上一版写的是 `bx setup --name <名字> '<链接>'`,而 `bx setup` 根本没有
// `--name` 这个 flag —— urfave/cli 遇到未知 flag 直接报错,所以窗口和终端给出
// 的唯一一条出路**必定失败**。同一句死提示在服务器窗口里也有一份(那一份已经
// 整个删掉,窗口里有按钮)。
//
// 判据不是「文案里没有那个串」,那只钉住这一次的拼法;判据是**它点名的东西
// 在 CLI 里真的存在** —— `bx server deploy` 得是个命令,而它得真有 `--name`。
func TestServerListEmptyHintNamesACommandThatExists(t *testing.T) {
	hint := renderServerList(serverListView{})
	if strings.Contains(hint, "bx setup --name") {
		t.Error("提示又指向 `bx setup --name` —— bx setup 没有这个 flag,那条命令必定失败")
	}
	if !strings.Contains(hint, "bx server deploy") {
		t.Fatalf("提示没有点名任何一条真能往 servers: 里写一台的路:%s", hint)
	}
	server := findAppCommand(New(), "server")
	if server == nil {
		t.Fatal("CLI 里没有 server 命令 —— 守卫已经失效,先修守卫")
	}
	var deploy *cli.Command
	for _, sub := range server.Subcommands {
		if sub.Name == "deploy" {
			deploy = sub
		}
	}
	if deploy == nil {
		t.Fatal("提示点名了 `bx server deploy`,而 CLI 里没有这条子命令")
	}
	if !commandHasFlag(deploy, "name") {
		t.Error("提示写了 `--name`,而 `bx server deploy` 没有这个 flag —— " +
			"照着敲会被 urfave/cli 直接拒掉")
	}
}
