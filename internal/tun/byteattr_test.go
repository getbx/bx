package tun

import (
	"io"
	"sync"
	"testing"
	"time"
)

// byteCall 是字节归因收到的一次记账。**udp 必须一起记** —— TCP 与 UDP 的
// 端口空间相互独立,同一个端口号可能同时被两个协议占用,合并成一个键会让
// 后写入的那个协议静默覆盖先写入的归因。
type byteCall struct {
	srcPort uint16
	udp     bool
	n       int64
	up      bool
}

type fakeByteAttributor struct {
	mu    sync.Mutex
	calls []byteCall
}

func (f *fakeByteAttributor) AddUp(srcPort uint16, udp bool, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, byteCall{srcPort, udp, n, true})
}

func (f *fakeByteAttributor) AddDown(srcPort uint16, udp bool, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, byteCall{srcPort, udp, n, false})
}

// total 把某一方向的字节加总。分次写入会拆成多次回调,断言总数而不是次数。
func (f *fakeByteAttributor) total(up bool) (n int64, ports map[uint16]bool, udps map[bool]bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ports, udps = map[uint16]bool{}, map[bool]bool{}
	for _, c := range f.calls {
		if c.up != up {
			continue
		}
		n += c.n
		ports[c.srcPort] = true
		udps[c.udp] = true
	}
	return n, ports, udps
}

// 字节按**应用侧源端口**记账,上下行分开,协议维度一起带下去。
func TestRelayAttributesBytesToSourcePort(t *testing.T) {
	attr := &fakeByteAttributor{}
	appSide, localSide := tcpPair(t)
	upstreamSide, serverSide := tcpPair(t)
	engine := &Engine{idleTimeout: time.Second, bytes: attr}

	done := make(chan struct{})
	go func() {
		engine.relay(localSide, upstreamSide, []byte("hi"), 51234, false)
		close(done)
	}()

	// 服务端下发 5 字节,应用侧应当读到,并被记成 down。
	if _, err := serverSide.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(appSide, buf); err != nil {
		t.Fatal(err)
	}
	// 应用侧再上行 3 字节。
	if _, err := appSide.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 5) // "hi" 首包 + "abc"
	if _, err := io.ReadFull(serverSide, got); err != nil {
		t.Fatal(err)
	}

	appSide.Close()
	serverSide.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay 没有收尾")
	}

	up, upPorts, upUDP := attr.total(true)
	if up != 5 { // 首包 "hi" + "abc"
		t.Fatalf("上行字节 = %d, want 5", up)
	}
	if len(upPorts) != 1 || !upPorts[51234] {
		t.Fatalf("上行源端口 = %v, want 只有 51234", upPorts)
	}
	if len(upUDP) != 1 || upUDP[true] {
		t.Fatalf("上行协议维度 = %v, want 只有 udp=false", upUDP)
	}
	down, downPorts, downUDP := attr.total(false)
	if down != 5 {
		t.Fatalf("下行字节 = %d, want 5", down)
	}
	if len(downPorts) != 1 || !downPorts[51234] {
		t.Fatalf("下行源端口 = %v, want 只有 51234", downPorts)
	}
	if len(downUDP) != 1 || downUDP[true] {
		t.Fatalf("下行协议维度 = %v, want 只有 udp=false", downUDP)
	}
}

// **udp 不许写死。** 写死成 false 不会有编译错误,而后果是所有 UDP 流量的
// 字节被记到 TCP 的端口键上 —— 界面上看不出任何异常。
func TestRelayCarriesTheUDPFlagIntoByteAttribution(t *testing.T) {
	attr := &fakeByteAttributor{}
	appSide, localSide := tcpPair(t)
	upstreamSide, serverSide := tcpPair(t)
	engine := &Engine{idleTimeout: time.Second, bytes: attr}

	done := make(chan struct{})
	go func() {
		engine.relay(localSide, upstreamSide, nil, 7777, true)
		close(done)
	}()
	if _, err := serverSide.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1)
	if _, err := io.ReadFull(appSide, buf); err != nil {
		t.Fatal(err)
	}
	appSide.Close()
	serverSide.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay 没有收尾")
	}

	_, ports, udps := attr.total(false)
	if len(ports) != 1 || !ports[7777] {
		t.Fatalf("源端口 = %v, want 只有 7777", ports)
	}
	if len(udps) != 1 || !udps[true] {
		t.Fatalf("协议维度 = %v, want 只有 udp=true", udps)
	}
}

// **上行的记账不许跟着 stats 的有无走。** 上行那半原本只在 e.stats != nil 时
// 才有 onWrite 闭包,照抄那个形状会让「没接 stats 但有人订阅应用视图」时
// 上行字节恒为 0 —— 而下行是好的,于是看起来只是「这个应用只在下载」。
func TestRelayAttributesUpstreamBytesWithoutStats(t *testing.T) {
	attr := &fakeByteAttributor{}
	appSide, localSide := tcpPair(t)
	upstreamSide, serverSide := tcpPair(t)
	engine := &Engine{idleTimeout: time.Second, bytes: attr} // stats 为 nil

	done := make(chan struct{})
	go func() {
		engine.relay(localSide, upstreamSide, nil, 42, false)
		close(done)
	}()
	if _, err := appSide.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(serverSide, buf); err != nil {
		t.Fatal(err)
	}
	appSide.Close()
	serverSide.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay 没有收尾")
	}

	if up, _, _ := attr.total(true); up != 4 {
		t.Fatalf("上行字节 = %d, want 4(stats 为 nil 时也必须记)", up)
	}
}

// 没人订阅(bytes 为 nil)时不许 panic —— 这是隐私前提:没人看时数据面零记账。
func TestRelayWithoutByteAttributorDoesNotPanic(t *testing.T) {
	appSide, localSide := tcpPair(t)
	upstreamSide, serverSide := tcpPair(t)
	engine := &Engine{idleTimeout: time.Second}

	done := make(chan struct{})
	go func() {
		engine.relay(localSide, upstreamSide, []byte("x"), 1, false)
		close(done)
	}()
	buf := make([]byte, 1)
	if _, err := io.ReadFull(serverSide, buf); err != nil {
		t.Fatal(err)
	}
	appSide.Close()
	serverSide.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay 没有收尾")
	}
}

// WithByteAttribution 是接线用的 Option。组装根那一跳(dialer 与 engine
// 必须拿到同一个实例)住在 internal/supervisor,靠 ByteAttributorOf 观察。
func TestWithByteAttributionSetsTheAttributor(t *testing.T) {
	attr := &fakeByteAttributor{}
	var e Engine
	WithByteAttribution(attr)(&e)
	if ByteAttributorOf(&e) != ByteAttributor(attr) {
		t.Fatal("Option 没把归因器装上")
	}
}
