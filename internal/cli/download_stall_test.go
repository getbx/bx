package cli

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// **大文件不该用总时限,该用停滞时限。**
//
// 真机(2026-08-14):菜单里点 Update,转圈两分钟后失败,日志里只有
// `context deadline exceeded (Client.Timeout or context cancellation while reading body)`。
// 根因是下载用了 `http.Client{Timeout: 90 * time.Second}` —— 而 Timeout 覆盖**整个
// 请求含读完 body**,包是 33MB,于是它实际要求「全程 375 KB/s 不掉」。
// 那不是超时,那是对用户带宽下限的一个未言明的要求。
//
// 正确的判据是**有没有在动**:只要还在收字节就继续等,连续 N 秒一个字节都没有
// 才放弃。慢的网络会慢慢下完,真的断了的连接照样很快失败。
func TestStallTimeoutAllowsSlowButMovingTransfers(t *testing.T) {
	// 一个「很慢但一直在动」的流:每次读之间隔 40ms,总时长远超停滞时限。
	slow := &pacedReader{chunks: 20, gap: 40 * time.Millisecond, payload: "0123456789"}
	reader := newStallTimeoutReader(slow, 200*time.Millisecond)
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("慢但一直在动的传输被判超时:%v", err)
	}
	if len(data) != 200 {
		t.Fatalf("读到 %d 字节,want 200", len(data))
	}
}

// 反面:**真的卡住了要很快失败**,否则这个改动只是把超时去掉。
func TestStallTimeoutFailsWhenNothingArrives(t *testing.T) {
	stalled := &pacedReader{chunks: 1, gap: 0, payload: "x", thenBlock: true}
	reader := newStallTimeoutReader(stalled, 80*time.Millisecond)
	start := time.Now()
	_, err := io.ReadAll(reader)
	if err == nil {
		t.Fatal("卡死的传输没有失败")
	}
	if !errors.Is(err, errTransferStalled) {
		t.Fatalf("错误没说清是停滞:%v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("卡死之后等了 %v 才失败 —— 停滞判定必须及时", elapsed)
	}
}

// **错误要说人话。** 用户看到的上一版是
// `context deadline exceeded (Client.Timeout or context cancellation while reading body)`,
// 它既没说是下载、也没说该怎么办。
func TestStallErrorExplainsItself(t *testing.T) {
	text := errTransferStalled.Error()
	for _, want := range []string{"stalled", "network"} {
		if !strings.Contains(strings.ToLower(text), want) {
			t.Errorf("停滞错误里没有 %q:%q", want, text)
		}
	}
}

type pacedReader struct {
	chunks    int
	gap       time.Duration
	payload   string
	thenBlock bool
	sent      int
}

func (p *pacedReader) Read(b []byte) (int, error) {
	if p.sent >= p.chunks {
		if p.thenBlock {
			select {} // 永远不再产出字节 —— 模拟真正的卡死
		}
		return 0, io.EOF
	}
	if p.gap > 0 {
		time.Sleep(p.gap)
	}
	p.sent++
	return copy(b, p.payload), nil
}

// **接线守卫:下载路径必须真的用上停滞判定,而且不许再设总 Timeout。**
//
// 判据对而接线错是这个仓库全部事故的形状。这条测试起一个「响应头很快、
// body 很慢」的服务端 —— 那正是真机上失败的形状:33MB 的包在一个 400 KB/s
// 的链路上要 80 秒,而总时限是 90 秒,稍微一抖就炸。
func TestDownloadSurvivesASlowBody(t *testing.T) {
	const size = 300 * 1024
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		chunk := make([]byte, 1024)
		for sent := 0; sent < size; sent += len(chunk) {
			_, _ = w.Write(chunk)
			if flusher != nil {
				flusher.Flush()
			}
			// 每 KB 停 1ms:总时长 ~300ms,远超任何「一次读」的间隔,
			// 但从不停滞。旧的总时限模型对这种流是致命的。
			time.Sleep(time.Millisecond)
		}
	}))
	defer server.Close()

	client := &http.Client{Transport: stallSafeTransport()}
	if client.Timeout != 0 {
		t.Fatal("下载用的 client 又设了总时限 —— 那正是把「慢」判成「失败」的东西")
	}
	data, err := downloadBytesContext(context.Background(), client, server.URL)
	if err != nil {
		t.Fatalf("一个慢但一直在动的下载失败了:%v", err)
	}
	if len(data) != size {
		t.Fatalf("下载了 %d 字节,want %d", len(data), size)
	}
}

// **接线守卫,这次是承重的。**
//
// 服务端发完响应头与几个字节之后**永远不再发**。没有停滞判定时 io.ReadAll 会
// 一直挂着(测试超时);有它时几百毫秒内以 errTransferStalled 失败。
//
// 上一版的守卫用「慢但一直在动」的流,而那种流去掉停滞 reader 也照样成功 ——
// 它证明的是「慢不算失败」(有价值),但**证明不了接线还在**。
func TestDownloadFailsFastWhenTheBodyStopsForever(t *testing.T) {
	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1048576")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("start"))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-blocked // 再也不发任何字节
	}))
	// **顺序要紧**:defer 是后进先出,所以要先注册 server.Close(),再注册
	// close(blocked) —— 反过来收尾时 server.Close() 会等 handler,而 handler
	// 在等 blocked 关闭,两边互等(第一版就是这么死的)。
	defer server.Close()
	defer close(blocked)

	restore := downloadStallTimeout
	downloadStallTimeout = 150 * time.Millisecond
	defer func() { downloadStallTimeout = restore }()

	done := make(chan error, 1)
	go func() {
		_, err := downloadBytesContext(context.Background(), &http.Client{Transport: stallSafeTransport()}, server.URL)
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, errTransferStalled) {
			t.Fatalf("卡死的下载失败原因不对:%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("卡死的下载没有及时失败 —— 停滞判定没有接到下载路径上")
	}
}
