package tunnel

import (
	"bufio"
	"io"
	"log"
	"os/exec"
	"strings"
	"sync"
)

const (
	// recentStderrLines 是环形缓冲保留的行数。健康检查失败时要看的是最近一次
	// 尝试的原因,不是无限增长的历史。
	recentStderrLines = 20
	// maxStderrLineLength 防止子进程用一行超长输出刷爆日志。
	maxStderrLineLength = 512
	redactedPlaceholder = "<redacted>"
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

	mu     sync.Mutex
	recent []string
}

// newStderrSink 建一个汇聚器。secrets 里的每个串会在**写日志之前**被抹掉——
// 日志本身就是泄露面。空白串会被忽略:strings.ReplaceAll(line, "", x) 会在每个
// 字符之间插入 x,把整行变成垃圾。
func newStderrSink(label string, secrets ...string) *stderrSink {
	sink := &stderrSink{label: label}
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
	log.Printf("%s: %s", s.label, line)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.recent = append(s.recent, line)
	if len(s.recent) > recentStderrLines {
		s.recent = s.recent[len(s.recent)-recentStderrLines:]
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
