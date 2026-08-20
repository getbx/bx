package supervisor

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

// fakeAppSource 是 appSource 的注入替身。calls 计数让「有没有去问系统」
// 成为可断言的事实 —— 未订阅时它必须一次都不涨。
//
// 计数带锁:并发那条测试会从多个 goroutine 走到这里,不加锁 -race 会红,
// 而那种红是测试自己的缺陷,会把真正的竞态淹掉。
type fakeAppSource struct {
	mu     sync.Mutex
	owners map[appattr.PortKey]string
	err    error
	calls  int
}

func (f *fakeAppSource) OwnersByPort() (map[appattr.PortKey]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.owners, f.err
}

func (f *fakeAppSource) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// tcp/udp 两个小构造器,让测试里的键一眼看得出协议维度 —— 直接写
// appattr.PortKey{Port: 7} 会静默落在 TCP 那一半,正是这一期要防的错。
func tcpKey(p uint16) appattr.PortKey { return appattr.PortKey{Port: p, UDP: false} }
func udpKey(p uint16) appattr.PortKey { return appattr.PortKey{Port: p, UDP: true} }

// **不变量 2**:未订阅时不做任何归因工作。一条「悄悄开始采集」的回归在性能
// 数字上未必看得出来,所以钉的是「source 一次都没被调用」这个可判定的事实,
// 不是基准 —— 基准不会让 CI 转红。
func TestAppTrafficDoesNothingWhileUnsubscribed(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := NewAppTraffic(src, time.Now)

	for i := 0; i < 1000; i++ {
		tr.Record(uint16(i), false, appattr.PathTunnel, "default", "")
		tr.AddUp(uint16(i), false, 10)
		tr.AddDown(uint16(i), false, 20)
	}
	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if subscribed {
		t.Fatal("没订阅却报 subscribed=true")
	}
	if src.callCount() != 0 {
		t.Fatalf("未订阅时调了 %d 次 appSource —— 应该一次都不调", src.callCount())
	}
	for _, g := range report.Groups {
		if len(g.Rows) != 0 {
			t.Fatalf("未订阅却攒下了数据: %#v", g)
		}
	}
}

// 上一条测试钉的是「没攒下数据」,而**热路径连锁都不该碰**这件事它证明不了:
// 把 Record 开头那句 atomic 短路删掉,锁内那道 expiredLocked 仍会拦住写入,
// 于是那条测试照样绿。这里用白盒手法把这件事变成可判定的:测试自己占住
// t.mu,未订阅时的 Record/AddUp/AddDown 必须立刻返回;一旦它们去抢锁就会
// 卡住,超时即红。
//
// 失败时**刻意不解锁**:解了锁那个 goroutine 会带着 nil 缓冲往下走并 panic,
// 把一条清晰的断言失败变成一堆栈噪声。goroutine 停在锁上,进程退出即回收。
func TestAppTrafficHotPathTakesNoLockWhileUnsubscribed(t *testing.T) {
	src := &fakeAppSource{}
	tr := NewAppTraffic(src, time.Now)

	tr.mu.Lock()
	done := make(chan struct{})
	go func() {
		tr.Record(7, false, appattr.PathTunnel, "default", "")
		tr.AddUp(7, false, 10)
		tr.AddDown(7, false, 20)
		close(done)
	}()
	select {
	case <-done:
		tr.mu.Unlock()
	case <-time.After(2 * time.Second):
		t.Error("未订阅时热路径仍去抢 t.mu —— 少了那道 atomic 短路")
	}
}

func TestAppTrafficRecordsAndAggregatesWhileSubscribed(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{
		tcpKey(7): "Slack",
		tcpKey(8): "Google Chrome",
	}}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()

	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.AddUp(7, false, 100)
	tr.AddDown(7, false, 900)
	tr.Record(8, false, appattr.PathDirect, "user_direct", "*.qq.com")

	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !subscribed {
		t.Fatal("订阅了却报 subscribed=false")
	}
	tunnel := report.Groups[0]
	if len(tunnel.Rows) != 1 || tunnel.Rows[0].App != "Slack" || tunnel.Rows[0].BytesDown != 900 {
		t.Fatalf("tunnel 组不对: %#v", tunnel.Rows)
	}
	direct := report.Groups[1]
	if len(direct.Rows) != 1 || direct.Rows[0].App != "Google Chrome" {
		t.Fatalf("direct 组不对: %#v", direct.Rows)
	}
}

// **协议维度不许丢。** TCP 与 UDP 是两个独立的端口空间,同一个数字可能同时被
// 两边占用。Record 漏填 UDP 的后果是所有连接都以 TCP 键归因 —— Go 的零值语义
// 让这件事**不会有编译错误**,报告里也只会看到一个应用名,不会看到冲突。
// 这里让同一个端口号 7 在两个协议上属于**不同**应用,归因串了就必然红。
func TestAppTrafficKeepsTCPAndUDPPortsApart(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{
		tcpKey(7): "Slack",
		udpKey(7): "Tencent Meeting",
	}}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()

	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.AddUp(7, false, 11)
	tr.Record(7, true, appattr.PathTunnel, "default", "")
	tr.AddUp(7, true, 22)

	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, row := range report.Groups[0].Rows {
		got[row.App] = row.BytesUp
	}
	if len(got) != 2 {
		t.Fatalf("TCP 与 UDP 的 7 号端口被归成了 %d 个应用: %#v", len(got), report.Groups[0].Rows)
	}
	if got["Slack"] != 11 {
		t.Fatalf("TCP 7 应归 Slack/11,得到 %#v", got)
	}
	if got["Tencent Meeting"] != 22 {
		t.Fatalf("UDP 7 应归 Tencent Meeting/22,得到 %#v", got)
	}
}

// 订阅带 TTL:菜单被强杀时没人来退订,而「没人看的时候开销精确为零」是这个
// 设计的隐私前提 —— 不能靠对方守规矩来保证。
func TestAppTrafficSubscriptionExpiresAndClearsBuffers(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := NewAppTraffic(src, clock)
	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")

	now = now.Add(appTrafficTTL + time.Second)
	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if subscribed {
		t.Fatal("TTL 过期后仍报 subscribed=true")
	}
	for _, g := range report.Groups {
		if len(g.Rows) != 0 {
			t.Fatalf("TTL 过期后缓冲没清干净: %#v", g)
		}
	}

	// 过期之后再记的东西也不许被攒下来。
	tr.Record(7, false, appattr.PathTunnel, "default", "")
	if _, _, _ = tr.Snapshot(); src.callCount() != 0 {
		t.Fatalf("过期后仍调了 %d 次 appSource", src.callCount())
	}
}

// 过期之后**没人调过 Snapshot** 就来了新订阅:续期不许把上一轮的残留带进来。
// 这条形状只有在 Subscribe 自己先结算一次过期时才成立 —— 若 Subscribe 只看
// active 标志就续期,旧缓冲会原样活下去,而那正是「关掉窗口再打开,看到的
// 是上次的数字」。
func TestAppTrafficResubscribeAfterExpiryStartsClean(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := NewAppTraffic(src, clock)
	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.AddUp(7, false, 4242)

	now = now.Add(appTrafficTTL + time.Second)
	tr.Subscribe() // 中间没有任何一次 Snapshot

	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !subscribed {
		t.Fatal("刚续订就报 subscribed=false")
	}
	for _, g := range report.Groups {
		if len(g.Rows) != 0 {
			t.Fatalf("续订带进了上一轮的残留: %#v", g)
		}
	}
}

// 端口复用:同一个端口上来了新连接,旧连接的残留字节必须清掉,否则会算到
// 新应用头上。这是 spec 里承认的近似,但至少要做这一层缓解。
func TestAppTrafficResetsByteCountersOnPortReuse(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()

	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.AddUp(7, false, 1000)
	tr.Record(7, false, appattr.PathDirect, "user_direct", "*.qq.com") // 同端口,新连接
	tr.AddUp(7, false, 5)

	report, _, _ := tr.Snapshot()
	var total int64
	for _, g := range report.Groups {
		for _, row := range g.Rows {
			total += row.BytesUp
		}
	}
	if total > 100 {
		t.Fatalf("端口复用后残留字节 = %d —— 新连接继承了旧连接的账", total)
	}
}

// 端口复用清账只清**这一个键**,不许把同号的另一个协议一起抹掉。
func TestAppTrafficPortReuseDoesNotClearTheOtherProtocol(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{
		tcpKey(7): "Slack",
		udpKey(7): "Tencent Meeting",
	}}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()

	tr.Record(7, true, appattr.PathTunnel, "default", "")
	tr.AddUp(7, true, 500)
	tr.Record(7, false, appattr.PathTunnel, "default", "") // TCP 侧的新连接
	tr.AddUp(7, false, 3)

	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, row := range report.Groups[0].Rows {
		got[row.App] = row.BytesUp
	}
	if got["Tencent Meeting"] != 500 {
		t.Fatalf("TCP 端口复用把 UDP 同号端口的账也清了: %#v", got)
	}
	if got["Slack"] != 3 {
		t.Fatalf("TCP 侧新连接的账不对: %#v", got)
	}
}

// appSource 失败要如实上报,不许退化成「一个应用都没有」。
func TestAppTrafficReportsSourceFailureRatherThanEmptyReport(t *testing.T) {
	src := &fakeAppSource{err: errAppSourceUnsupported}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")

	report, subscribed, err := tr.Snapshot()
	if err == nil {
		t.Fatal("appSource 报错时 Snapshot 必须报错,不许返回空报告冒充「没有应用」")
	}
	if !errors.Is(err, errAppSourceUnsupported) {
		t.Fatalf("原因被吞了: %v", err)
	}
	// 「在看但问不出来」与「没人在看」是两种状态,不许合并。
	if !subscribed {
		t.Fatal("问不出来不等于没人订阅")
	}
	if len(report.Groups) != 0 {
		t.Fatalf("出错时不许同时给一份看起来正常的报告: %#v", report)
	}
}

// 环形缓冲满了丢最旧的,而不是无界增长:界面显示的是「此刻的分流构成」,
// 几万条之前的连接对它没有意义,而无界缓冲会在订阅期间一直长。
func TestAppTrafficRingBufferDropsOldestWhenFull(t *testing.T) {
	src := &fakeAppSource{}
	tr := NewAppTraffic(src, time.Now)
	tr.Subscribe()

	const extra = 10
	for i := 0; i < appTrafficMaxRecords+extra; i++ {
		tr.Record(uint16(i%60000+1), false, appattr.PathTunnel, "default", "")
	}
	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var conns int
	for _, g := range report.Groups {
		for _, row := range g.Rows {
			conns += row.Conns
		}
	}
	if conns != appTrafficMaxRecords {
		t.Fatalf("环形缓冲应稳定在 %d 条,得到 %d", appTrafficMaxRecords, conns)
	}
}

// 竞态:热路径(数据面多 goroutine)与 Snapshot(控制面)同时跑。
// 这条测试的价值全在 -race 下 —— 没有它,-race 只是在几条串行测试上空转。
func TestAppTrafficConcurrentRecordAndSnapshot(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := NewAppTraffic(src, time.Now)

	var wg sync.WaitGroup
	stop := make(chan struct{})
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				port := uint16((w*2000+i)%60000 + 1)
				udp := i%2 == 0
				tr.Record(port, udp, appattr.PathTunnel, "default", "")
				tr.AddUp(port, udp, 3)
				tr.AddDown(port, udp, 7)
			}
		}(w)
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				tr.Subscribe()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if _, _, err := tr.Snapshot(); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()

	// 前 8 个 worker 结束后再停两个常驻 goroutine。
	go func() {
		time.Sleep(200 * time.Millisecond)
		close(stop)
	}()
	wg.Wait()
}
