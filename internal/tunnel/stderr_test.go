package tunnel

import (
	"bytes"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"
)

// 传输子进程的 stderr 此前被直接丢进 /dev/null(6 处 exec.Command 都没设过
// cmd.Stderr,Go 的语义就是接 os.DevNull)。真机事故 2026-08-06:reality 连不通,
// 日志里只有「隧道健康检查超时(20s)」,分不出是握手失败、超时还是被 reset,
// 用户在没有线索的情况下反复重装,最后卸载换回 brook。
func TestStderrSinkForwardsAndRemembersLines(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	sink.consume(strings.NewReader("first line\nsecond line\n"))

	logged := restore()
	for _, want := range []string{"singbox: first line", "singbox: second line"} {
		if !strings.Contains(logged, want) {
			t.Errorf("日志里应有 %q,实际 =\n%s", want, logged)
		}
	}
	recent := sink.RecentStderr()
	if len(recent) != 2 || recent[0] != "first line" || recent[1] != "second line" {
		t.Errorf("RecentStderr() = %q, want 两行原文", recent)
	}
}

// 抹密必须发生在**写日志之前**——日志本身就是泄露面。brook connect -l <link>
// 的链接就在 argv 上,brook 有可能把它回显进自己的日志,而链接自带凭据。
func TestStderrSinkRedactsSecretsBeforeLogging(t *testing.T) {
	const link = "vless://11111111-2222-3333-4444-555555555555@203.0.113.123:443?security=reality"
	restore := captureLog(t)
	sink := newStderrSink("brook", link, "hunter2")
	sink.consume(strings.NewReader("dial failed for " + link + " with password hunter2\n"))

	logged := restore()
	if strings.Contains(logged, link) {
		t.Errorf("链接原文写进了日志:\n%s", logged)
	}
	if strings.Contains(logged, "hunter2") {
		t.Errorf("密码原文写进了日志:\n%s", logged)
	}
	if !strings.Contains(logged, "<redacted>") {
		t.Errorf("抹掉的位置应留下 <redacted>,实际 =\n%s", logged)
	}
	for _, line := range sink.RecentStderr() {
		if strings.Contains(line, link) || strings.Contains(line, "hunter2") {
			t.Errorf("环形缓冲里也不得留原文:%q", line)
		}
	}
}

// 空 secret 不能登记 —— 否则 strings.ReplaceAll(line, "", …) 会把每个字符之间
// 都塞进 <redacted>,整行变成垃圾。传输链接可选(如 brook 无 httpAddr 时)。
func TestStderrSinkIgnoresEmptySecrets(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox", "", "   ")
	sink.consume(strings.NewReader("plain message\n"))

	logged := restore()
	if !strings.Contains(logged, "singbox: plain message") {
		t.Errorf("空 secret 不该影响正常行,实际 =\n%s", logged)
	}
	if strings.Contains(logged, "<redacted>") {
		t.Errorf("空 secret 不得触发替换,实际 =\n%s", logged)
	}
}

// 一行刷爆日志是真实风险(子进程可能吐超长行)。截断到上限。
func TestStderrSinkTruncatesOverlongLine(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	sink.consume(strings.NewReader(strings.Repeat("x", maxStderrLineLength*3) + "\n"))
	restore()

	recent := sink.RecentStderr()
	if len(recent) != 1 {
		t.Fatalf("want 1 line, got %d", len(recent))
	}
	if len(recent[0]) > maxStderrLineLength {
		t.Errorf("行长 %d 超过上限 %d", len(recent[0]), maxStderrLineLength)
	}
}

// 只保留最后若干行:健康检查失败时要看的是最近一次尝试,不是无限增长的缓冲。
func TestStderrSinkKeepsOnlyRecentLines(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	var b strings.Builder
	for i := 0; i < recentStderrLines*3; i++ {
		b.WriteString("line\n")
	}
	sink.consume(strings.NewReader(b.String()))
	restore()

	if got := len(sink.RecentStderr()); got != recentStderrLines {
		t.Errorf("缓冲行数 = %d, want %d", got, recentStderrLines)
	}
}

// 返回副本:调用方改动不得污染缓冲。
func TestStderrSinkRecentReturnsCopy(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	sink.consume(strings.NewReader("original\n"))
	restore()

	got := sink.RecentStderr()
	got[0] = "mutated"
	if again := sink.RecentStderr(); again[0] != "original" {
		t.Errorf("修改返回值污染了缓冲:%q", again[0])
	}
}

// captureLog 把标准 logger 接到缓冲区,返回一个取回内容并恢复原状的函数。
func captureLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	flags := log.Flags()
	writer := log.Writer()
	log.SetOutput(&buf)
	log.SetFlags(0)
	restored := false
	restore := func() string {
		if !restored {
			log.SetOutput(writer)
			log.SetFlags(flags)
			restored = true
		}
		return buf.String()
	}
	t.Cleanup(func() { restore() })
	return restore
}

// 隧道对外必须能交出子进程最近的 stderr —— 用户实际看到的那句
// 「bx 隧道健康检查超时(20s)」在 supervisor 里拼装,不经隧道返回的 error,
// 所以光包装 error 没用,得有个取尾巴的出口。
func TestTunnelExposesRunnerStderr(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	sink.consume(strings.NewReader("outbound/vless[proxy]: dial tcp 203.0.113.9:443: i/o timeout\n"))
	restore()

	tun := New("127.0.0.1:0",
		func(string) (Runner, error) { return &execRunner{stderr: sink}, nil },
		func(string) (int64, error) { return 0, nil })
	tun.mu.Lock()
	tun.runner = &execRunner{stderr: sink}
	tun.mu.Unlock()

	got := tun.RecentStderr()
	if len(got) != 1 || !strings.Contains(got[0], "i/o timeout") {
		t.Fatalf("RecentStderr() = %q, want 子进程那一行", got)
	}
}

// 没有运行中的子进程时不得炸,返回空即可 —— 诊断路径不该因为没东西可报而失败。
func TestTunnelStderrEmptyWithoutRunner(t *testing.T) {
	tun := New("127.0.0.1:0",
		func(string) (Runner, error) { return nil, nil },
		func(string) (int64, error) { return 0, nil })
	if got := tun.RecentStderr(); len(got) != 0 {
		t.Errorf("无子进程时 RecentStderr() = %q, want 空", got)
	}
}

// —— 重复折叠 ——
//
// 真机 2026-09-01:`/var/log/bx-guard.err.log` 长到 **100MB 且从无轮转**,其中
// **435,128 行是同一句话**:sing-box 的 `network: missing default interface`,
// 约 2 次/秒、不停。那句话在 bx 的配置下**是良性的** —— 全仓没有一处
// auto_detect_interface/bind_interface,bx 从不让 sing-box 去探接口,出站靠系统
// 路由表。于是缺陷整个在这一侧:**一条恒定不变的良性消息被无条件转发了几周**,
// 把 Guardian 自己那几千行真日志(guardian_core_scan、needs_attention……)埋在
// 99% 的噪声底下。

// 同一行反复出现时,窗口内只写一次。
// 环形缓冲**不受影响** —— 它是诊断出口(健康检查失败时要看的那几行),压缩的
// 只是写进日志的那一份。
func TestStderrSinkFoldsAFloodOfIdenticalLines(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	frozen := time.Now()
	sink.now = func() time.Time { return frozen }

	for i := 0; i < 1000; i++ {
		sink.writeLine("network: missing default interface")
	}
	logged := restore()

	if n := strings.Count(logged, "missing default interface"); n != 1 {
		t.Errorf("同一行在窗口内写了 %d 次日志,应当只写 1 次", n)
	}
	if got := len(sink.RecentStderr()); got != recentStderrLines {
		t.Errorf("环形缓冲被折叠连累了:%d 行,应当仍是 %d", got, recentStderrLines)
	}
}

// 折叠必须**报出折了多少次**。
// 少了这个数,「一句话刷了 43 万次」在日志里与「这句话出现过一次」完全一样 ——
// 而那两者是完全不同的两件事,后者不值得看,前者是本次事故本身。
func TestStderrSinkReportsHowManyItFolded(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	at := time.Now()
	sink.now = func() time.Time { return at }

	for i := 0; i < 500; i++ {
		sink.writeLine("network: missing default interface")
	}
	at = at.Add(stderrRepeatWindow + time.Second)
	sink.writeLine("network: missing default interface")
	logged := restore()

	if !strings.Contains(logged, "499") {
		t.Errorf("没有报出折叠次数(应含 499):\n%s", logged)
	}
}

// **不同的行一条都不许丢。** 折叠是压缩,不是采样 —— 它把重复次数变成一个数字,
// 但绝不能让一条从没出现过的消息静默消失。
func TestStderrSinkNeverFoldsDistinctLines(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	frozen := time.Now()
	sink.now = func() time.Time { return frozen }

	for i := 0; i < 40; i++ {
		sink.writeLine(fmt.Sprintf("distinct failure %d", i))
	}
	logged := restore()

	for i := 0; i < 40; i++ {
		if !strings.Contains(logged, fmt.Sprintf("distinct failure %d", i)) {
			t.Fatalf("第 %d 行被折叠吞掉了", i)
		}
	}
}

// 子进程退出(管道 EOF)时,还没报出去的折叠计数必须补一笔。
// 否则一个刷了十万次然后退出的子进程,在日志里只留下**一行**,而那正是最该
// 被看见的形状。
func TestStderrSinkFlushesPendingFoldCountAtEOF(t *testing.T) {
	restore := captureLog(t)
	sink := newStderrSink("singbox")
	frozen := time.Now()
	sink.now = func() time.Time { return frozen }

	sink.consume(strings.NewReader(strings.Repeat("same line\n", 300)))
	logged := restore()

	if !strings.Contains(logged, "299") {
		t.Errorf("EOF 时没有补报折叠计数(应含 299):\n%s", logged)
	}
}

// 抑制表必须有上限。子进程若吐出**每行都不同**的输出(例如带连接 ID),
// 表会无界增长 —— 那是拿内存泄漏换日志体积,不划算。
func TestStderrSinkRepeatTableStaysBounded(t *testing.T) {
	restore := captureLog(t)
	defer restore()
	sink := newStderrSink("singbox")
	frozen := time.Now()
	sink.now = func() time.Time { return frozen }

	for i := 0; i < stderrDistinctTracked*20; i++ {
		sink.writeLine(fmt.Sprintf("conn %d failed", i))
	}
	if got := len(sink.repeats); got > stderrDistinctTracked {
		t.Errorf("抑制表涨到 %d 条,上限是 %d", got, stderrDistinctTracked)
	}
}
