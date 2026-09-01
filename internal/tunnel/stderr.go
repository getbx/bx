package tunnel

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// recentStderrLines 是环形缓冲保留的行数。健康检查失败时要看的是最近一次
	// 尝试的原因,不是无限增长的历史。
	recentStderrLines = 20
	// maxStderrLineLength 防止子进程用一行超长输出刷爆日志。
	maxStderrLineLength = 512
	redactedPlaceholder = "<redacted>"
	// stderrRepeatWindow 是同一行的最小重复写入间隔。
	//
	// 真机 2026-09-01:sing-box 以约 2 次/秒无限重复 `network: missing default
	// interface`(在 bx 的配置下良性 —— 全仓没有一处 auto_detect_interface,
	// bx 从不让它去探接口),几周里写出 435,128 行、把 Guardian 自己那几千行
	// 真日志埋在 99% 的噪声底下。**单行截断挡不住这个形状**:它挡的是「一行
	// 很长」,而这里是「一行很短、重复很多次」。
	stderrRepeatWindow = time.Minute
	// stderrDistinctTracked 是抑制表的条目上限。
	//
	// 表满不是折叠失效的理由,而是**折叠对这种形状本来就无能为力**:每行都不同
	// 的输出(带连接 ID 那种)去重去不掉任何东西。此时宁可照常写日志,也不拿
	// 一个无界增长的 map 去换日志体积。
	stderrDistinctTracked = 64
)

// stderrSink 收集一个传输子进程的 stderr。
//
// 这些进程(brook / sing-box)此前的 stderr 是被**丢弃**的:6 处 exec.Command
// 都没设过 cmd.Stderr,而 Go 的语义是把它接到 os.DevNull。真机事故 2026-08-06:
// reality 连不通,运维能拿到的只有「隧道健康检查超时(20s)」,分不出是握手失败、
// 超时还是被 reset,用户在没有线索的情况下反复重装,最后卸载换回 brook。
type stderrSink struct {
	label   string
	secrets []string
	// now 是给测试的时钟缝。折叠的判据是「距上次写这一行过了多久」,
	// 而一条要靠 sleep 才测得到的判据等于没有测试。
	now func() time.Time

	mu      sync.Mutex
	recent  []string
	repeats map[string]*repeatState
}

// repeatState 记一行「上次写进日志是什么时候」与「此后折了多少次」。
type repeatState struct {
	lastLogged time.Time
	folded     int
}

// newStderrSink 建一个汇聚器。secrets 里的每个串会在**写日志之前**被抹掉——
// 日志本身就是泄露面。空白串会被忽略:strings.ReplaceAll(line, "", x) 会在每个
// 字符之间插入 x,把整行变成垃圾。
func newStderrSink(label string, secrets ...string) *stderrSink {
	sink := &stderrSink{label: label, now: time.Now, repeats: map[string]*repeatState{}}
	for _, secret := range secrets {
		if strings.TrimSpace(secret) == "" {
			continue
		}
		sink.secrets = append(sink.secrets, secret)
	}
	return sink
}

// consume 逐行读到 EOF。子进程退出时管道关闭,调用自然返回。
func (s *stderrSink) consume(r io.Reader) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 4096), maxStderrLineLength*4)
	for scanner.Scan() {
		s.writeLine(scanner.Text())
	}
	// 子进程退出时把还没报出去的折叠计数补一笔。少了它,一个刷了十万次然后
	// 退出的子进程在日志里只留下**一行** —— 而那正是最该被看见的形状。
	s.flush()
}

// flush 把抑制表里所有待报的折叠计数写出去并清空表。
func (s *stderrSink) flush() {
	s.mu.Lock()
	pending := s.drainFoldedLocked(func(*repeatState) bool { return true })
	s.mu.Unlock()
	s.emit(pending)
}

// drainFoldedLocked 摘掉 keep 选中的条目,返回它们里待报的折叠说明。
// **调用方必须持锁**;日志写在锁外(写日志是 I/O,不该压在这把锁里)。
func (s *stderrSink) drainFoldedLocked(match func(*repeatState) bool) []string {
	var pending []string
	for line, st := range s.repeats {
		if !match(st) {
			continue
		}
		if st.folded > 0 {
			pending = append(pending, foldNotice(line, st.folded))
		}
		delete(s.repeats, line)
	}
	return pending
}

func (s *stderrSink) emit(lines []string) {
	for _, line := range lines {
		log.Printf("%s: %s", s.label, line)
	}
}

func foldNotice(line string, folded int) string {
	return fmt.Sprintf("%s [同一行重复 %d 次已折叠]", line, folded)
}

// admit 判断这一行现在该不该写进日志。
//
// 返回的 text 为空表示折叠掉了。**折叠永远不丢信息**:重复次数会跟着下一次
// 写入(或 flush)一起报出来 —— 少了那个数,「一句话刷了 43 万次」在日志里
// 与「这句话出现过一次」完全一样,而后者不值得看,前者就是事故本身。
func (s *stderrSink) admit(line string) (text string, evicted []string) {
	now := s.now()
	st, tracked := s.repeats[line]
	if tracked {
		if now.Sub(st.lastLogged) < stderrRepeatWindow {
			st.folded++
			return "", nil
		}
		folded := st.folded
		st.lastLogged, st.folded = now, 0
		if folded > 0 {
			return foldNotice(line, folded), nil
		}
		return line, nil
	}

	if len(s.repeats) >= stderrDistinctTracked {
		// 先清掉已经过窗口的条目(它们对折叠已无作用),把它们欠的计数报出去。
		evicted = s.drainFoldedLocked(func(st *repeatState) bool {
			return now.Sub(st.lastLogged) >= stderrRepeatWindow
		})
	}
	if len(s.repeats) >= stderrDistinctTracked {
		// 表仍满 = 这个子进程正在吐每行都不同的输出,折叠对它无能为力。
		// 照常写日志,但不占表 —— 见 stderrDistinctTracked 的注释。
		return line, evicted
	}
	s.repeats[line] = &repeatState{lastLogged: now}
	return line, evicted
}

func (s *stderrSink) writeLine(line string) {
	line = strings.TrimRight(line, "\r")
	if line == "" {
		return
	}
	// 顺序要紧:先截断(限制单行体量),再抹密(截断可能切断 secret,故抹密必须
	// 在其后对最终文本执行),最后才写日志与缓冲。
	if len(line) > maxStderrLineLength {
		line = line[:maxStderrLineLength]
	}
	for _, secret := range s.secrets {
		line = strings.ReplaceAll(line, secret, redactedPlaceholder)
	}
	s.mu.Lock()
	// 环形缓冲**不受折叠影响**:它是诊断出口(健康检查失败时要看的最近几行),
	// 压缩的只是写进日志的那一份。
	s.recent = append(s.recent, line)
	if len(s.recent) > recentStderrLines {
		s.recent = s.recent[len(s.recent)-recentStderrLines:]
	}
	text, evicted := s.admit(line)
	s.mu.Unlock()

	s.emit(evicted)
	if text != "" {
		log.Printf("%s: %s", s.label, text)
	}
}

// RecentStderr 返回最近若干行的副本。
func (s *stderrSink) RecentStderr() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.recent...)
}

// stderrTailer 是 Runner 的可选能力:取出子进程最近的 stderr。
//
// 刻意不加进 Runner 接口——已有假实现在用它,加方法会全线破坏,而这个能力对
// 隧道的运行逻辑不是必需的,只对诊断有用。
type stderrTailer interface {
	RecentStderr() []string
}

// startWithStderr 接上 stderr 管道后启动子进程,并在后台把它逐行转发进 bx 的日志。
//
// secrets 会在写日志前从每一行里抹掉。传输链接必须登记:brook 的链接就在 argv 上,
// 它有可能把链接回显进自己的日志,而链接自带凭据。
func startWithStderr(cmd *exec.Cmd, label string, secrets ...string) (*execRunner, error) {
	pipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	sink := newStderrSink(label, secrets...)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	go sink.consume(pipe)
	return &execRunner{cmd: cmd, stderr: sink}, nil
}
