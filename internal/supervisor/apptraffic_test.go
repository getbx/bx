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
	mu sync.Mutex
	// owners 只有显示名,execPaths 是可选的第二半 —— 绝大多数用例不关心路径
	// (它只喂图标),保持原来一眼看得懂的形状。
	owners    map[appattr.PortKey]string
	execPaths map[appattr.PortKey]string
	err       error
	calls     int

	// whileAnswering 在「正在回答内核那一问」的当口被调一次。**唯一的用途是
	// 从里面去抢 t.mu**:调用方若持着那把锁来问内核,这里就会死锁,超时即红。
	// 与 TestAppTrafficByteAccountingTakesNoLockWhileUnsubscribed 同一手法。
	whileAnswering func()
}

func (f *fakeAppSource) OwnersByPort() (map[appattr.PortKey]appattr.Owner, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.whileAnswering != nil {
		f.whileAnswering()
	}
	if f.err != nil {
		// **报错时返回 nil map,不是空 map** —— 与非 darwin 桩同一条纪律:
		// 空 map 会被读成「查过了,一个应用都没有」。
		return nil, f.err
	}
	if f.owners == nil {
		return nil, nil
	}
	out := make(map[appattr.PortKey]appattr.Owner, len(f.owners))
	for k, name := range f.owners {
		out[k] = appattr.Owner{Name: name, ExecPath: f.execPaths[k]}
	}
	return out, nil
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
	tr := newAppTrafficNoResolver(src, time.Now)

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

// **这条测试原来断言的是「未订阅时 Record 连 t.mu 都不碰」,那个断言在
// 2026-08-20 之后不再成立,而且不成立是刻意的。** Record 现在无条件维护活连接
// 表(未订阅时也写),所以它必然要抢锁 —— 原来那条断言只能靠削弱修复来满足,
// 于是它被换成「未订阅时到底不该做哪些事」这个仍然为真、也仍然值得守的性质。
//
// 换句话说:未订阅时的代价从「一次 atomic 读」变成了「每条连接**两次全局锁获取
// + 四次 map 操作**」(建连时 Record 读改写一次、关闭时 ConnClosed 读删一次;
// 那把锁与字节记账是同一把),**不是每个包一次** —— 字节记账那条
// 路径(AddUp/AddDown,每次转发写都要走)仍然被一次 atomic 读挡在锁外,由下面
// 的白盒断言钉住。
//
// 三件未订阅时不许发生的事,逐条断言:① 不问内核(不调 appSource);
// ② 不记字节(bytesUp/bytesDn 保持 nil);③ 不攒历史(records 保持 nil)。
// 这三条正是隐私前提的实质内容,「零开销」只是它当初的实现手段。
func TestAppTrafficDoesNotAttributeOrAccrueWhileUnsubscribed(t *testing.T) {
	src := &fakeAppSource{}
	tr := newAppTrafficNoResolver(src, time.Now)

	for i := 0; i < 100; i++ {
		tr.Record(uint16(i), false, appattr.PathTunnel, "default", "")
		tr.AddUp(uint16(i), false, 10)
		tr.AddDown(uint16(i), false, 20)
	}
	if src.callCount() != 0 {
		t.Errorf("未订阅时问了 %d 次内核 —— 一次都不该问", src.callCount())
	}
	tr.mu.Lock()
	if tr.records != nil {
		t.Errorf("未订阅时攒下了历史: %d 条", len(tr.records))
	}
	if tr.bytesUp != nil || tr.bytesDn != nil {
		t.Errorf("未订阅时开了字节表: up=%v dn=%v", tr.bytesUp, tr.bytesDn)
	}
	tr.mu.Unlock()
}

// **字节记账**那半仍然连锁都不碰,这一条不能跟着 Record 一起放弃:它挂在
// copyOneWay 的 onWrite 上,整机每一次转发写都会走,而 Record 只是每条连接一次。
// 白盒手法:测试自己占住 t.mu,未订阅时的 AddUp/AddDown 必须立刻返回;一旦它们
// 去抢锁就会卡住,超时即红。
//
// 失败时**刻意不解锁**:解了锁那个 goroutine 会带着 nil 缓冲往下走并 panic,
// 把一条清晰的断言失败变成一堆栈噪声。goroutine 停在锁上,进程退出即回收。
func TestAppTrafficByteAccountingTakesNoLockWhileUnsubscribed(t *testing.T) {
	src := &fakeAppSource{}
	tr := newAppTrafficNoResolver(src, time.Now)

	tr.mu.Lock()
	done := make(chan struct{})
	go func() {
		tr.AddUp(7, false, 10)
		tr.AddDown(7, false, 20)
		close(done)
	}()
	select {
	case <-done:
		tr.mu.Unlock()
	case <-time.After(2 * time.Second):
		t.Error("未订阅时字节记账仍去抢 t.mu —— 少了那道 atomic 短路")
	}
}

func TestAppTrafficRecordsAndAggregatesWhileSubscribed(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{
		tcpKey(7): "Slack",
		tcpKey(8): "Google Chrome",
	}}
	tr := newAppTrafficNoResolver(src, time.Now)
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
	tr := newAppTrafficNoResolver(src, time.Now)
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

// 订阅带 TTL:菜单被强杀时没人来退订,而「没人看时不问内核、不记字节、不攒
// 历史」是这个设计的隐私前提 —— 不能靠对方守规矩来保证。
func TestAppTrafficSubscriptionExpiresAndClearsBuffers(t *testing.T) {
	now := time.Unix(1000, 0)
	clock := func() time.Time { return now }
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := newAppTrafficNoResolver(src, clock)
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
	tr := newAppTrafficNoResolver(src, clock)
	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.AddUp(7, false, 4242)
	// **这一行是 2026-08-20 补的,而且是承重的**:续订时活连接表会给新缓冲播种,
	// 所以「上一轮的残留」必须是一条**真的已经结束**的连接,否则这条测试断言的
	// 就不是残留、而是「还开着的连接不许出现」——那正好与本次修复相反。
	tr.ConnClosed(7, false)

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
	tr := newAppTrafficNoResolver(src, time.Now)
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
	tr := newAppTrafficNoResolver(src, time.Now)
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
	tr := newAppTrafficNoResolver(src, time.Now)
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
	tr := newAppTrafficNoResolver(src, time.Now)
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

// **环形缓冲摊平出来的必须是真正的时间序。**
//
// Record 在端口复用时只清**字节账**,不删旧的 ConnRecord —— 所以同一个键在一个
// 30 秒窗口里被复用多次时,缓冲里会**同时存在好几条**该键的记录。而
// appattr.Aggregate 的字节归属规则是「同一个键最近的那条记录赢」(倒序遍历 +
// counted 去重),于是那笔字节记到哪个应用/哪一组,**完全取决于
// liveRecordsLocked 返回的是不是真正的时间序**。
//
// 上一版的 TestAppTrafficRingBufferDropsOldestWhenFull 守不住这件事:它用空的
// owners map,所有记录都塌进同一行 unknown,只比 Conns 总数 —— 把两个 append
// 对调之后它照样绿。这是这个仓库反复栽的同一个形状:守卫钉住的是缺陷旁边的东西。
//
// 这里让**同一个端口**先后走过两条不同的路(direct → tunnel),字节只在最后那条
// 之后加,于是「哪条记录幸存/哪条最新」变成可判定的:字节必须落在 tunnel 组。
func TestAppTrafficKeepsTimeOrderAcrossRingBoundaries(t *testing.T) {
	const n = appTrafficMaxRecords
	cases := []struct {
		name  string
		total int
	}{
		{"没绕圈(差一条写满)", n - 1},
		{"刚好写满", n},
		{"刚绕过一条", n + 1},
		{"绕过两圈半", n*2 + n/2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
			tr := newAppTrafficNoResolver(src, time.Now)
			tr.Subscribe()

			// directAt 挑在「还能活到最后」的位置上:它之后还会写 n-9 条,
			// 少于缓冲容量 n,所以这条旧记录必然幸存 —— 否则测的就只是
			// 「被覆盖掉了」而不是顺序。
			directAt := tc.total - n + 8
			if directAt < 0 {
				directAt = 0
			}
			for i := 0; i < tc.total; i++ {
				switch {
				case i == tc.total-1:
					tr.Record(7, false, appattr.PathTunnel, "default", "")
					tr.AddUp(7, false, 1000) // 只在**最新**那条之后加账
				case i == directAt:
					tr.Record(7, false, appattr.PathDirect, "user_direct", "*.qq.com")
				default:
					// 填充走 blocked 组,不污染要断言的那两组;端口从 8 起,
					// 永远躲开 7。
					tr.Record(uint16(i%60000)+8, false, appattr.PathBlocked, "builtin", "")
				}
			}

			report, _, err := tr.Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			tunnel, direct := report.Groups[0], report.Groups[1]
			if len(tunnel.Rows) != 1 || tunnel.Rows[0].App != "Slack" {
				t.Fatalf("tunnel 组应恰有 Slack 一行: %#v", tunnel.Rows)
			}
			if len(direct.Rows) != 1 || direct.Rows[0].App != "Slack" {
				t.Fatalf("direct 组应恰有 Slack 一行(旧记录必须幸存): %#v", direct.Rows)
			}
			if tunnel.Rows[0].BytesUp != 1000 {
				t.Fatalf("字节应记给**最新**那条(tunnel),得到 tunnel=%d direct=%d —— 摊平出来的不是时间序",
					tunnel.Rows[0].BytesUp, direct.Rows[0].BytesUp)
			}
			if direct.Rows[0].BytesUp != 0 {
				t.Fatalf("旧记录不该拿到字节,得到 %d", direct.Rows[0].BytesUp)
			}
			// 顺带钉住容量:写满之后总条数稳定在 n,一条不多一条不少。
			var conns int
			for _, g := range report.Groups {
				for _, row := range g.Rows {
					conns += row.Conns
				}
			}
			if want := min(tc.total, n); conns != want {
				t.Fatalf("总条数应为 %d,得到 %d", want, conns)
			}
		})
	}
}

// 竞态:热路径(数据面多 goroutine)与 Snapshot(控制面)同时跑。
// 这条测试的价值全在 -race 下 —— 没有它,-race 只是在几条串行测试上空转。
func TestAppTrafficConcurrentRecordAndSnapshot(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := newAppTrafficNoResolver(src, time.Now)

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
				// ConnClosed 与 Record 走的是同一张活连接表、同一把锁,而它
				// **只在这条测试里**会与 Subscribe 的种子遍历真正并发 ——
				// 种子在锁内遍历 live,ConnClosed 在锁内改它。少了这一行,
				// -race 从来没有覆盖过这条新路径。
				tr.ConnClosed(port, udp)
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

// **UDP 一个 socket 服务多个对端,那些流属于同一个应用,字节必须累加。**
//
// gVisor 的 UDP forwarder 按 5 元组建流(已核对上游 forwarder.go:CreateEndpoint
// 注册的是完整 TransportEndpointID),而应用侧源端口是固定的:一个腾讯会议
// socket 打 STUN + TURN + 多个 peer 就产生 N 条流 ⇒ N 次 Record ⇒ 同一个
// PortKey。若照 TCP 那样「每次 Record 都清账」,**每来一条新流就把这个端口
// 已攒的字节抹掉** —— 表现是字节数系统性偏低而连接数完全正常,没有任何一处
// 报错,而它恰好命中这个功能最初的用例。
func TestAppTrafficAccumulatesBytesAcrossUDPFlowsOnTheSamePort(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{udpKey(9): "Tencent Meeting"}}
	tr := newAppTrafficNoResolver(src, time.Now)
	tr.Subscribe()

	// 一个 socket、三条流(STUN / TURN / peer),字节交替到账。
	tr.Record(9, true, appattr.PathTunnel, "udp_proxy", "")
	tr.AddUp(9, true, 100)
	tr.AddDown(9, true, 200)
	tr.Record(9, true, appattr.PathTunnel, "udp_proxy", "")
	tr.AddUp(9, true, 30)
	tr.Record(9, true, appattr.PathTunnel, "udp_proxy", "")
	tr.AddDown(9, true, 7)

	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	var up, down int64
	for _, g := range report.Groups {
		for _, row := range g.Rows {
			up += row.BytesUp
			down += row.BytesDown
		}
	}
	if up != 130 || down != 207 {
		t.Fatalf("UDP 多流累加 up=%d down=%d, want 130/207 —— 新流把已攒的字节抹掉了", up, down)
	}
}

// 反过来也要钉:UDP 那侧新建一条流,不许把**同号 TCP 端口**的账清掉。
// 与 TestAppTrafficPortReuseDoesNotClearTheOtherProtocol 互为镜像 ——
// 那条只证明了 TCP 不碰 UDP。
func TestAppTrafficUDPFlowDoesNotClearTheTCPAccount(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{
		tcpKey(11): "Slack",
		udpKey(11): "Tencent Meeting",
	}}
	tr := newAppTrafficNoResolver(src, time.Now)
	tr.Subscribe()

	tr.Record(11, false, appattr.PathTunnel, "default", "")
	tr.AddUp(11, false, 400)
	tr.Record(11, true, appattr.PathTunnel, "udp_proxy", "") // UDP 侧的新流
	tr.AddUp(11, true, 6)

	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int64{}
	for _, row := range report.Groups[0].Rows {
		got[row.App] = row.BytesUp
	}
	if got["Slack"] != 400 {
		t.Fatalf("UDP 新流把同号 TCP 端口的账清了: %#v", got)
	}
	if got["Tencent Meeting"] != 6 {
		t.Fatalf("UDP 侧的账不对: %#v", got)
	}
}

// ==== 2026-08-20:真机暴露的缺陷 —— 订阅之前就已经建好的连接看不见 ====
//
// 现象:项目所有者打开窗口只看到 2 个应用,而 `bx status` 同时报 66 条活跃连接。
// 根因是 Record 只在 dialer 建连的那一刻被调用,于是**订阅之前建好的连接从来
// 没有被 Record 过**,窗口只看得见「打开它之后新拨的连接」。后果最重的正是长
// 连接(会议媒体流、WebSocket、SSH、Colima 隧道):建连一次跑几小时,全在盲区。
//
// **十二轮审查没抓到它,因为所有测试都是「先订阅、再造连接」** —— 测试输入让
// 缺陷不可见。下面这几条测试的形状(先建连、后订阅)就是那个缺失的形状。

// 订阅**之前**建立、订阅时仍然活着的连接,必须出现在报告里。
func TestAppTrafficSeedsSubscriptionWithConnectionsOpenedBeforeIt(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{
		tcpKey(7):  "腾讯会议",
		udpKey(19): "腾讯会议",
	}}
	tr := newAppTrafficNoResolver(src, time.Now)

	// 会议已经开到一半:两条连接早就建好了,此刻才有人打开窗口。
	tr.Record(7, false, appattr.PathDirect, "user_direct", "*.qq.com")
	tr.Record(19, true, appattr.PathTunnel, "default", "")

	tr.Subscribe()

	report, subscribed, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if !subscribed {
		t.Fatal("订阅了却报 subscribed=false")
	}
	tunnel, direct := report.Groups[0], report.Groups[1]
	if len(tunnel.Rows) != 1 || tunnel.Rows[0].App != "腾讯会议" || tunnel.Rows[0].Conns != 1 {
		t.Fatalf("tunnel 组 = %#v,want 订阅前那条 UDP 长连接被种子带进来", tunnel.Rows)
	}
	if len(direct.Rows) != 1 || direct.Rows[0].App != "腾讯会议" || direct.Rows[0].Conns != 1 {
		t.Fatalf("direct 组 = %#v,want 订阅前那条 TCP 长连接被种子带进来", direct.Rows)
	}
	// 判定原文也要跟着种子进来 —— 只带端口的话界面上会显示一条没有归因的行,
	// 而「为什么走这条路」正是这个功能要回答的问题。
	if len(direct.Rows[0].Rules) != 1 || direct.Rows[0].Rules[0] != "*.qq.com" {
		t.Fatalf("种子丢了规则原文: %#v", direct.Rows[0].Rules)
	}
}

// 订阅**之前**建立、且**在订阅之前就关掉**的连接,不得出现 —— 种子发布的是
// 「此刻还活着的」,不是「历史上出现过的」。少了这一条,活连接表就会变成一份
// 跨订阅留存的历史记录,那既不准也违反「不留存」。
func TestAppTrafficDoesNotSeedConnectionsClosedBeforeSubscribe(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Safari"}}
	tr := newAppTrafficNoResolver(src, time.Now)

	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.ConnClosed(7, false)

	tr.Subscribe()
	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range report.Groups {
		if len(g.Rows) != 0 {
			t.Fatalf("订阅前就关掉的连接进了报告: %s %#v", g.Path, g.Rows)
		}
	}
}

// **活连接表不许泄漏。** 它是这次修复付出的新代价:未订阅时也要维护,所以
// 它的唯一边界就是「连接关闭时删掉」。少了那一步,**任何一条既有测试都不会红**
// —— 报告仍然正确,泄漏在测试里不可见。
//
// **后果不是 OOM。** 键是 PortKey{uint16, bool},硬上限 131072 条、约 10–15MB,
// 泄漏满了就到此为止。真正的后果发作得更早也更糟:陈旧条目攒到几千条之后,
// 一次 Subscribe 的种子就能把 4096 格的环形缓冲填满并绕圈,**新记录被自己的
// 陈旧种子挤掉** —— 报告从「正确但残缺」退化成「错的」。
func TestAppTrafficLiveTableDropsClosedConnections(t *testing.T) {
	tr := newAppTrafficNoResolver(&fakeAppSource{}, time.Now)

	const n = 5000
	for i := 0; i < n; i++ {
		port := uint16(1024 + i%40000)
		tr.Record(port, false, appattr.PathTunnel, "default", "")
		tr.Record(port, true, appattr.PathDirect, "china_domain", "")
		tr.ConnClosed(port, false)
		tr.ConnClosed(port, true)
	}
	if got := tr.liveSize(); got != 0 {
		t.Fatalf("开关 %d 轮之后活连接表还剩 %d 条 —— 关闭时没有删", n, got)
	}

	// UDP 一个源端口会有多条并存的流(STUN/TURN/多 peer),refs 记数必须配平:
	// 三开三关之后归零,三开两关之后仍在(还有一条流活着)。
	tr.Record(19, true, appattr.PathTunnel, "default", "")
	tr.Record(19, true, appattr.PathTunnel, "default", "")
	tr.Record(19, true, appattr.PathTunnel, "default", "")
	tr.ConnClosed(19, true)
	tr.ConnClosed(19, true)
	if got := tr.liveSize(); got != 1 {
		t.Fatalf("同端口三条 UDP 流关掉两条后表大小 = %d, want 1(还有一条活着)", got)
	}
	tr.ConnClosed(19, true)
	if got := tr.liveSize(); got != 0 {
		t.Fatalf("最后一条 UDP 流关掉后表大小 = %d, want 0", got)
	}
	// 多关一次不许把表算成负数、也不许 panic(防御性:引擎那一侧只要有一条路径
	// 重复 defer,这里就会被多调一次)。
	tr.ConnClosed(19, true)
	if got := tr.liveSize(); got != 0 {
		t.Fatalf("多余的 ConnClosed 之后表大小 = %d, want 0", got)
	}
}

// 种子只在**一次订阅开始时**播一次。菜单每 5 秒拉一次、每次都调 Subscribe 续期,
// 续期时再播一遍种子就会让同一条连接每 5 秒多算一次 —— 界面上表现为连接数随时间
// 线性膨胀,而没有任何一处报错。
func TestAppTrafficSeedIsNotReplayedOnRenewal(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := newAppTrafficNoResolver(src, time.Now)

	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.Subscribe()
	tr.Subscribe()
	tr.Subscribe()

	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := report.Groups[0]
	if len(tunnel.Rows) != 1 || tunnel.Rows[0].Conns != 1 {
		t.Fatalf("三次续期后 tunnel 组 = %#v,want 那条连接只算一次", tunnel.Rows)
	}
}

// 种子进来的记录与订阅之后的新记录不许重复计数:同一条连接不能既在种子里、
// 又因为后来的某次调用再算一遍。**端口复用是另一回事** —— 订阅后同一个端口
// 上真的又建了一条新连接,那本来就是两条,必须都算。
func TestAppTrafficSeedAndFreshRecordsDoNotDoubleCount(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{
		tcpKey(7): "Slack",
		tcpKey(8): "Google Chrome",
	}}
	tr := newAppTrafficNoResolver(src, time.Now)

	tr.Record(7, false, appattr.PathTunnel, "default", "") // 订阅前建立,始终活着
	tr.Subscribe()
	tr.Record(8, false, appattr.PathTunnel, "default", "") // 订阅后新建

	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	tunnel := report.Groups[0]
	if len(tunnel.Rows) != 2 {
		t.Fatalf("tunnel 组 = %#v,want 两行(种子一条 + 新建一条)", tunnel.Rows)
	}
	for _, row := range tunnel.Rows {
		if row.Conns != 1 {
			t.Fatalf("%s 连接数 = %d, want 1 —— 同一条连接被算了两次", row.App, row.Conns)
		}
	}
}

// **已知缺口,这条测试断言的是「这是已知行为」,不是「这样是对的」。**
//
// 活连接表按 PortKey 记,同键最后写入者胜;种子每键只发一条记录。于是一个会议
// socket 同时打 STUN(直连)+ TURN(走隧道)时:
// 订阅**前**建立 ⇒ 只出现在**一个**组里(最后写入的那条判定),连接数 1;
// 订阅**后**建立 ⇒ 正确地出现在**两个**组里,连接数各 1。
// 同一个事实,按窗口打开时机给出不同答案 —— 而「腾讯会议为什么绕一圈」正是这个
// 功能要回答的问题。今天不修的理由写在 AppTraffic 的类型注释里(相对修复前的
// 「完全看不见」这仍是巨大改善;真修要把键重新设计成能分辨同一 socket 上的不同流,
// 因为 ConnClosed(port, udp) 无从知道该减哪一档)。
//
// 钉住它是因为**它今天一个字都没被记录**,那样的话下一个人会把它当成新 bug 重查
// 一遍;而一旦有人真的去修,这条测试会立刻转红,提醒他连同注释与 spec 一起改。
func TestAppTrafficSeedCollapsesConcurrentFlowsOnOneSocket(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{udpKey(9): "Tencent Meeting"}}

	// (一) 订阅前建立的两条流 —— 种子压成一条。
	before := newAppTrafficNoResolver(src, time.Now)
	before.Record(9, true, appattr.PathDirect, "china_domain", "") // STUN 直连
	before.Record(9, true, appattr.PathTunnel, "udp_proxy", "")    // TURN 走隧道
	before.Subscribe()
	seeded, _, err := before.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	tunnel, direct := seeded.Groups[0], seeded.Groups[1]
	if len(tunnel.Rows) != 1 || tunnel.Rows[0].Conns != 1 {
		t.Fatalf("种子的 tunnel 组 = %#v, want 一行一条(最后写入的那条判定)", tunnel.Rows)
	}
	if len(direct.Rows) != 0 {
		t.Fatalf("种子的 direct 组 = %#v, want 空 —— 当前实现每键只发一条记录", direct.Rows)
	}

	// (二) 同样两条流、订阅**之后**建立 —— 两个组都在。两段的输入完全一样,
	// 差别只有 Subscribe 的位置,这正是那个「按时机给出不同答案」的形状。
	after := newAppTrafficNoResolver(src, time.Now)
	after.Subscribe()
	after.Record(9, true, appattr.PathDirect, "china_domain", "")
	after.Record(9, true, appattr.PathTunnel, "udp_proxy", "")
	fresh, _, err := after.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh.Groups[0].Rows) != 1 || len(fresh.Groups[1].Rows) != 1 {
		t.Fatalf("订阅后建立的两条流 tunnel=%#v direct=%#v, want 各一行", fresh.Groups[0].Rows, fresh.Groups[1].Rows)
	}
}

// 可执行路径必须一路穿到报告里 —— 菜单侧的图标只认路径。
//
// **这一条钉的是「穿过去了」,不是「谁当代表」**:代表值的选法由
// appattr.Aggregate 自己的测试守着,在这里再断言一次就是第二份判据。
func TestAppTrafficReportsCarryExecutablePaths(t *testing.T) {
	src := &fakeAppSource{
		owners:    map[appattr.PortKey]string{tcpKey(7): "Slack"},
		execPaths: map[appattr.PortKey]string{tcpKey(7): "/Applications/Slack.app/Contents/MacOS/Slack"},
	}
	tr := newAppTrafficNoResolver(src, time.Now)
	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")

	report, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows := report.Groups[0].Rows
	if len(rows) != 1 {
		t.Fatalf("tunnel 组 %d 行, want 1", len(rows))
	}
	if rows[0].ExecPath != "/Applications/Slack.app/Contents/MacOS/Slack" {
		t.Fatalf("ExecPath = %q —— 路径在 Core 侧就被丢掉了,菜单画不出图标", rows[0].ExecPath)
	}
}

// ---- 滚动窗口 + 后台归因(2026-08-20)----

// newAppTrafficNoResolver 构造一个**不带后台 resolver** 的 AppTraffic。
//
// 后台 resolver 每 250ms 问一次内核,会让「调了几次 appSource」「快照里有几行」
// 这类断言变成掷骰子 —— **一个偶发红的闸门比没有闸门更糟**(它训练人去重跑)。
// 所以默认关掉,要验后台那半的测试自己开(见下面三条)。
func newAppTrafficNoResolver(src appSource, now func() time.Time) *AppTraffic {
	tr := NewAppTraffic(src, now)
	tr.resolveInterval = 0
	return tr
}

// renewUntil 模拟菜单的行为:每 5 秒调一次 Subscribe 续期,把假时钟推到 until。
// **续期而不是重新订阅** —— 重新订阅会重建缓冲,那样什么都测不到。
func renewUntil(tr *AppTraffic, at *time.Time, until time.Time) {
	for at.Before(until) {
		*at = at.Add(5 * time.Second)
		tr.Subscribe()
	}
}

// **报告只覆盖最近 60 秒。** 窗口外的记录不进报告,窗口内的进。
//
// 这是「unknown 只涨不落」的直接修法:一条早已结束、永远查不回主人的连接,
// 过了窗口就不该再占着界面。
func TestAppTrafficDropsRecordsOlderThanTheReportWindow(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	src := &fakeAppSource{owners: map[appattr.PortKey]string{}}
	tr := newAppTrafficNoResolver(src, func() time.Time { return at })

	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.ConnClosed(7, false) // 短连接:建完就关,此后永远查不回主人

	renewUntil(tr, &at, at.Add(70*time.Second))

	// 窗口内又来一条,证明缓冲本身没被整个清掉。
	tr.Record(8, false, appattr.PathDirect, "china", "")
	tr.ConnClosed(8, false)

	rep, subscribed, err := tr.Snapshot()
	if err != nil || !subscribed {
		t.Fatalf("续期之后不该掉订阅:subscribed=%v err=%v", subscribed, err)
	}
	if n := len(rep.Groups[0].Rows); n != 0 {
		t.Fatalf("70 秒前那条连接还在 tunnel 组里(%d 行)—— 报告没有按时间裁剪,"+
			"unknown 会一直只涨不落:%#v", n, rep.Groups[0].Rows)
	}
	if n := len(rep.Groups[1].Rows); n != 1 || rep.Groups[1].Rows[0].Conns != 1 {
		t.Fatalf("窗口内那条连接被误裁了:%#v", rep.Groups[1].Rows)
	}
}

// **记录被裁掉之后,它那个端口的字节账也必须一起裁掉。**
//
// 只裁一半的后果不是「总量偏大」这么轻:UDP 刻意不在端口复用时清账(一个会议
// socket 服务多个对端),于是一笔一分钟前就该消失的账会整个算到下一个拿到这个
// 端口的应用头上。用 UDP 正是因为它是唯一能把这个缺陷显形的协议。
func TestAppTrafficDropsByteAccountWhenItsRecordsLeaveTheWindow(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	src := &fakeAppSource{owners: map[appattr.PortKey]string{udpKey(7): "Zoom"}}
	tr := newAppTrafficNoResolver(src, func() time.Time { return at })

	tr.Subscribe()
	tr.Record(7, true, appattr.PathTunnel, "default", "")
	tr.AddUp(7, true, 1000)
	tr.AddDown(7, true, 2000)
	tr.ConnClosed(7, true)

	renewUntil(tr, &at, at.Add(70*time.Second))
	tr.Snapshot() // 触发一次裁剪

	tr.mu.Lock()
	up, dn := len(tr.bytesUp), len(tr.bytesDn)
	tr.mu.Unlock()
	if up != 0 || dn != 0 {
		t.Errorf("记录裁掉了而字节表还留着 %d/%d 条 —— 滚动窗口只滚了一半", up, dn)
	}

	// 行为上的后果:同一个 UDP 端口被下一个应用拿到时,不该继承那笔旧账。
	tr.Record(7, true, appattr.PathTunnel, "default", "")
	rep, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows := rep.Groups[0].Rows
	if len(rows) != 1 {
		t.Fatalf("tunnel 组该有 1 行,得 %#v", rows)
	}
	if rows[0].BytesUp != 0 || rows[0].BytesDown != 0 {
		t.Fatalf("新连接继承了一分钟前那笔账:up=%d down=%d",
			rows[0].BytesUp, rows[0].BytesDown)
	}
}

// **还开着的连接不许被时间裁掉。**
//
// 窗口问的是「此刻谁在连谁」,而一条开了两小时还在灌流的会议媒体流恰恰是最该
// 被看见的那种。只按时间裁会让长连接在开窗 60 秒后集体消失 —— 那正是
// 2026-08-20 那个真机 bug(长连接全在盲区)换一种方式复发。
func TestAppTrafficKeepsRecordsOfConnectionsThatAreStillOpen(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "ssh"}}
	tr := newAppTrafficNoResolver(src, func() time.Time { return at })

	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "") // 不 ConnClosed:长连接
	tr.AddUp(7, false, 500)

	renewUntil(tr, &at, at.Add(5*time.Minute))

	rep, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows := rep.Groups[0].Rows
	if len(rows) != 1 || rows[0].App != "ssh" {
		t.Fatalf("开了五分钟还开着的连接从报告里消失了:%#v", rows)
	}
	if rows[0].BytesUp != 500 {
		t.Errorf("还开着的连接,字节账被裁掉了:up=%d", rows[0].BytesUp)
	}
}

// **本次修复的核心证据:归因在连接关闭之前就完成,并且存进记录里。**
//
// 老形状是「读取时现查」(OwnersByPort 全仓只在 Snapshot 里被调、5 秒一次),
// 于是任何活不过一次刷新的连接在被归因之前 socket 就没了,结构性地落进 unknown。
// 这里用确定的一拍 resolveOnce 代替后台 goroutine,把那一拍与「连接随后关闭」
// 的先后关系钉死。
func TestAppTrafficResolverAttributesBeforeTheSocketDisappears(t *testing.T) {
	src := &fakeAppSource{
		owners:    map[appattr.PortKey]string{tcpKey(7): "Slack"},
		execPaths: map[appattr.PortKey]string{tcpKey(7): "/Applications/Slack.app/Contents/MacOS/Slack"},
	}
	tr := newAppTrafficNoResolver(src, time.Now)

	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")
	tr.AddUp(7, false, 42)

	tr.resolveOnce() // 后台 resolver 的一拍:连接还开着,归因拿得到

	// 连接关掉,内核里再也查不到这个端口 —— 现查必然是 unknown。
	tr.ConnClosed(7, false)
	src.mu.Lock()
	src.owners, src.execPaths = map[appattr.PortKey]string{}, nil
	src.mu.Unlock()

	rep, _, err := tr.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	rows := rep.Groups[0].Rows
	if len(rows) != 1 {
		t.Fatalf("tunnel 组该有 1 行,得 %#v", rows)
	}
	if rows[0].App != "Slack" {
		t.Fatalf("连接一关归因就丢了(App=%q)—— 归因没有存进记录里,"+
			"这正是短连接全部塌进 unknown 的原因", rows[0].App)
	}
	if rows[0].ExecPath == "" {
		t.Error("可执行路径没跟着存进记录 —— 那一行会无声地失去图标")
	}
}

// **resolver 绝不许持着 t.mu 去问内核。**
//
// OwnersByPort 是两次 sysctl(真机实测 451µs~1.5ms),而 t.mu 是整机每条连接、
// 每次转发写都要过的那把全局锁 —— 持锁去问就是每 250 毫秒把整机的记账阻塞一次。
// 白盒手法与 TestAppTrafficByteAccountingTakesNoLockWhileUnsubscribed 同款:
// 让被注入的 appSource 在被调用时自己去抢 t.mu,resolver 若持着锁就死锁,超时即红。
func TestAppTrafficResolverTakesNoLockWhileAskingTheKernel(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := newAppTrafficNoResolver(src, time.Now)
	src.whileAnswering = func() {
		tr.mu.Lock()
		tr.mu.Unlock() //nolint:staticcheck // 只为证明这把锁此刻是拿得到的
	}

	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")

	done := make(chan struct{})
	go func() {
		tr.resolveOnce()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		// 刻意不解锁也不 Fatal:那个 goroutine 停在锁上,进程退出即回收。
		t.Error("resolver 持着 t.mu 去问内核 —— 每 250ms 会把整机的记账阻塞一次")
	}
}

// **未订阅时后台 resolver 一次都不许跑。**
//
// 「没人看时不问内核、不记字节、不攒历史」是这个设计的隐私前提;一个自己滴答的
// goroutine 会把它悄悄破掉,而界面上完全看不出来。
func TestAppTrafficResolverDoesNotRunWhileUnsubscribed(t *testing.T) {
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := NewAppTraffic(src, time.Now) // 刻意用生产构造器:后台 resolver 是开着的
	tr.Record(7, false, appattr.PathTunnel, "default", "")

	time.Sleep(4 * appResolveInterval) // 够跑好几拍
	if n := src.callCount(); n != 0 {
		t.Fatalf("没人订阅,后台 resolver 却问了 %d 次内核", n)
	}
}

// 后台 resolver 在订阅期间**真的在跑**(不是只有 resolveOnce 那条手动路径),
// 且 TTL 过期之后**停下来**。
//
// 判据是「记录里真的被填上了 Owner」而不是「goroutine 起来了」——
// 这个仓库反复栽在「守卫钉住的是缺陷旁边的东西」上(证明 channel 存在 ≠
// goroutine 跑过)。轮询 + 宽超时,不用固定 sleep 去猜时序。
func TestAppTrafficBackgroundResolverRunsWhileSubscribedAndStopsAfterTTL(t *testing.T) {
	at := time.Now()
	src := &fakeAppSource{owners: map[appattr.PortKey]string{tcpKey(7): "Slack"}}
	tr := NewAppTraffic(src, func() time.Time { return at })

	tr.Subscribe()
	tr.Record(7, false, appattr.PathTunnel, "default", "")

	deadline := time.Now().Add(5 * time.Second)
	for !tr.recordedOwnerName(tcpKey(7)) {
		if time.Now().After(deadline) {
			t.Fatal("订阅期间后台 resolver 没有把归因填进记录 —— 它根本没在跑")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// TTL 过期 ⇒ 循环必须停。用 appSource 的调用数当证据:停下来之后它不再涨。
	at = at.Add(appTrafficTTL + time.Second)
	tr.Record(9, false, appattr.PathTunnel, "default", "") // 触发一次惰性结算
	time.Sleep(4 * appResolveInterval)
	before := src.callCount()
	time.Sleep(6 * appResolveInterval)
	if after := src.callCount(); after != before {
		t.Fatalf("订阅过期之后 resolver 还在问内核(%d → %d)", before, after)
	}
}

// recordedOwnerName 报告环形缓冲里某个键**已经存下**归因了没有。
//
// **白盒窗口是刻意的**:后台 resolver 有没有真的跑过,从 Snapshot 的输出上看不
// 出来 —— Snapshot 自己也会做最后一次现查,两条路径给出同一份报告。要证明的是
// 「归因存进了记录」这件事本身,那只能直接看记录。
func (t *AppTraffic) recordedOwnerName(key appattr.PortKey) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, br := range t.orderedBufferedLocked() {
		if br.rec.SrcPort == key.Port && br.rec.UDP == key.UDP && br.rec.Owner.Name != "" {
			return true
		}
	}
	return false
}
