package cli

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/supervisor"
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
	if !strings.Contains(err.Error(), "已回滚") {
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
				Probe: &guardian.ProbeReport{Reachable: true, RTTMS: 12}, PeakBPS: 3_100_000,
			},
			{
				Name: "us", Host: "2.2.2.2",
				Probe:   &guardian.ProbeReport{Reachable: true, RTTMS: 180},
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
func TestServerListExplainsProbeFailures(t *testing.T) {
	out := renderServerList(serverListView{
		Tested: true,
		Entries: []guardian.ServerEntry{
			{Name: "dead", Host: "3.3.3.3", Probe: &guardian.ProbeReport{Error: "超时(没有应答)"}},
		},
	})
	if !strings.Contains(out, "超时") {
		t.Errorf("没说清失败原因:\n%s", out)
	}
	if strings.Contains(out, "0 ms") {
		t.Errorf("没通却显示成 0 ms:\n%s", out)
	}
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
