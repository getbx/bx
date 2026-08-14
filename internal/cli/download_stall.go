package cli

import (
	"errors"
	"io"
	"sync"
	"time"
)

// errTransferStalled 是「连接还在,但一个字节都不来了」。
//
// 措辞刻意说人话:上一版用户看到的是
// `context deadline exceeded (Client.Timeout or context cancellation while reading body)`,
// 它既没说是下载、也没说该怎么办。
var errTransferStalled = errors.New("download stalled: no data received for a while — check the network and try again")

// downloadStallTimeout 是「多久没收到字节就放弃」。
//
// **它替代的是一个总时限**(`http.Client{Timeout: 90s}`),而那个总时限覆盖整个
// 请求含读完 body:33MB 的包因此实际要求全程 375 KB/s 不掉,慢一点的网络必然失败
// (2026-08-14 真机)。停滞时限只问「还在不在动」——慢的网络会慢慢下完,
// 真的断了的连接照样很快失败。
// **它是 var 而不是 const**:让测试能把它调短,好造出一个真正卡死的下载。
// 上一版这条接线的守卫用「慢但一直在动」的服务端,而那种流**去掉停滞 reader
// 也照样成功** —— 守卫没有承重,变异验证当场发现。
var downloadStallTimeout = 60 * time.Second

// newStallTimeoutReader 包一个 reader,使它在**连续一段时间读不到任何字节**时失败。
//
// 与总时限的区别是全部要点:总时限惩罚慢,停滞时限惩罚停。
func newStallTimeoutReader(r io.Reader, stall time.Duration) io.Reader {
	return &stallTimeoutReader{inner: r, stall: stall}
}

type stallTimeoutReader struct {
	inner io.Reader
	stall time.Duration
}

type readOutcome struct {
	n   int
	err error
}

func (s *stallTimeoutReader) Read(b []byte) (int, error) {
	// 每次 Read 单独计时。底层 Read 阻塞在一个死掉的连接上时,这里到点就返回;
	// **那个 goroutine 会一直挂着** —— 这是有意接受的代价:它持有的是一个已经
	// 没救的连接,而调用方随后会关掉 response body,让它自己结束。
	done := make(chan readOutcome, 1)
	var once sync.Once
	go func() {
		n, err := s.inner.Read(b)
		once.Do(func() { done <- readOutcome{n: n, err: err} })
	}()
	timer := time.NewTimer(s.stall)
	defer timer.Stop()
	select {
	case out := <-done:
		return out.n, out.err
	case <-timer.C:
		return 0, errTransferStalled
	}
}
