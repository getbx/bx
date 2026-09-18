package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/getbx/bx/internal/stats"
)

// 下载进度**按字节报,不按秒报**。
//
// 起因是一个真机画面(2026-09-18):一个 39MB 的包经隧道下了十几分钟,而菜单上
// 唯一的数字是 `Downloading and installing… 199s`。**一个时钟在下载已经死掉之后
// 照样在涨** —— 它结构上答不了用户唯一想问的那个问题「是不是卡住了」,于是那个
// 问题只能由人来问一遍。一个朝着已知终点走的字节数自己就证明自己活着。
const (
	// progressStepBytes:每多下这么多就打一行。下得快时给出细粒度,而**总行数
	// 随包大小有界**(39MB ⇒ 约 39 行),不会把日志刷成几百行。
	progressStepBytes = 1 << 20
	// progressHeartbeat:哪怕几乎没进展,隔这么久也要打一行。
	// 少了它,一个 5 KB/s 的下载可以三分多钟一个字不吭 —— 而那恰恰是用户最会
	// 怀疑它卡住的时候。
	progressHeartbeat = 10 * time.Second
)

// formatDownloadProgress 是纯判据:给定已下与总量,返回那一行。
//
// **总量未知时绝不编一个百分比。** 「不知道一共多大」与「知道、正好是这么多」
// 是两件事;编出来的百分比会在下载过半时突然跳一下,而用户无从分辨那是进度
// 还是错的。单位换算与 `bx status` 共用 stats.HumanBytes —— 同一个数在两处
// 长得不一样,用户会以为那是两件事。
func formatDownloadProgress(done, total int64) string {
	if total <= 0 {
		return fmt.Sprintf("⏳ downloaded %s", stats.HumanBytes(done))
	}
	return fmt.Sprintf("⏳ downloaded %s of %s (%.0f%%)",
		stats.HumanBytes(done), stats.HumanBytes(total), float64(done)*100/float64(total))
}

// shouldEmitProgress 是纯判据:这一刻该不该再打一行。两条各管一头,见上面的常量。
func shouldEmitProgress(doneBytes, lastBytes int64, since time.Duration) bool {
	return doneBytes-lastBytes >= progressStepBytes || since >= progressHeartbeat
}

// newProgressReader 在一个 reader 上旁听字节数。**它不改变读到的任何东西** ——
// 只是数,然后按上面那两条判据把一行话交给 emit。
//
// 收尾那一行(EOF 时)**无条件打**:否则进度会停在 97% 然后画面一跳,而「停在
// 97%」与「卡在 97%」在屏幕上完全一样。
func newProgressReader(inner io.Reader, total int64, emit func(string)) io.Reader {
	return &progressReader{inner: inner, total: total, emit: emit, now: time.Now, last: time.Now()}
}

type progressReader struct {
	inner    io.Reader
	total    int64
	emit     func(string)
	now      func() time.Time
	done     int64
	lastSent int64
	last     time.Time
	finished bool
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.inner.Read(b)
	p.done += int64(n)
	if err == nil && shouldEmitProgress(p.done, p.lastSent, p.now().Sub(p.last)) {
		p.send()
	}
	if err != nil && !p.finished {
		// 无论是 EOF 还是真的出错,都把最后一次读到的量说出来 —— 一次半途失败的
		// 下载,「下到哪儿断的」正是唯一有用的那个数。
		p.finished = true
		p.send()
	}
	return n, err
}

func (p *progressReader) send() {
	p.lastSent = p.done
	p.last = p.now()
	if p.emit != nil {
		p.emit(formatDownloadProgress(p.done, p.total))
	}
}
