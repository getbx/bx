package supervisor

import (
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

// appSource 现问系统「哪个端口属于哪个应用」。**注入点** —— 测试用假的。
type appSource interface {
	// OwnersByPort 现问内核一次,返回 (端口,协议) → 应用身份(显示名 + 可执行路径)。
	// 键必须是 appattr.PortKey 而不是裸 uint16 —— TCP 与 UDP 端口空间相互独立,
	// 同一个数字可能同时被两个协议占用,合并成一个键会让后写入的那个协议静默
	// 覆盖先写入的归因。
	// 查不出应用的端口**不出现在 map 里**(调用方据此判 unknown)。
	//
	// 值是 appattr.Owner 而不是两张平行的 map[PortKey]string:两张同型的 map
	// 相邻排在参数表里,位置调换会静默编译通过 —— 这个仓库不缺这种形状的事故。
	OwnersByPort() (map[appattr.PortKey]appattr.Owner, error)
}

var errAppSourceUnsupported = errors.New("app attribution is only available on macOS")

// appTrafficTTL 是订阅的存活期。菜单被强杀、窗口进程崩溃时不会有人来退订,
// 而「没人看时不问内核、不记字节、不攒历史」是这个设计的隐私前提 —— 不能靠
// 对方守规矩来保证。
//
// **TTL 是惰性结算的,没有定时器。** 过期只在 Subscribe/Record/AddUp/AddDown/
// Snapshot 中任一个被调用时才由 expiredLocked 就地判定并清空缓冲。所以严格说,
// 订阅者消失之后若真的再没有任何一次调用,那批记录会在内存里留过 30 秒。
//
// 实际上 Record 是全网每条连接都要走的路径,真正的静默几乎不可能发生;
// 不加定时器是刻意的(它会变成「没人看时也有个 goroutine 在滴答」,
// 与 internal/toolkeys 那个唯一的持久化过期先例同一手法)。
// 但边界写在这里:代码里声称的性质,要么做到,要么如实写明边界。
const appTrafficTTL = 30 * time.Second

// appTrafficMaxRecords 是环形缓冲容量。满了就丢最旧的:界面显示的是「此刻的
// 分流构成」,几万条之前的连接对它没有意义,而无界缓冲会在订阅期间无限长。
const appTrafficMaxRecords = 4096

// liveConn 是一条**此刻还开着**的连接在活连接表里的样子:只有判定,没有应用
// 身份 —— 身份是 Snapshot 时才去问内核的,这是「未订阅时也维护这张表」在隐私上
// 仍然成立的原因(表里是端口和判定,不是「你开过什么应用」)。
//
// refs 是同键并存的流数,**UDP 需要它**:一个应用 socket 打 STUN + TURN + 多个
// peer,gVisor 按 5 元组建流 ⇒ 同一个源端口上有 N 条并存的流、N 次 Record。
// 不记数就会「第一条流关掉时把整条 socket 从活连接表里抹掉」,于是种子看不见
// 一个还在灌媒体流的会议 —— 正是这次修复要消灭的那种盲区。
// TCP 侧 refs 通常恒为 1,但它也顺手兜住了「旧连接的 ConnClosed 晚于新连接的
// Record 到达」这个真实竞态(裸 delete 会把刚建好的那条抹掉)。
type liveConn struct {
	path   appattr.Path
	source string
	rule   string
	refs   int
}

// AppTraffic 按 (源端口,协议) 记账。**归因、字节账与历史只在有人订阅时才攒**,
// 而**活连接表任何时候都维护**。
//
// **未订阅时的代价不是零**:每条连接**两次全局锁获取 + 四次 map 操作**
// (建连时 Record 读改写一次,关闭时 ConnClosed 读删一次)—— 是每条连接一次,
// **不是每个包一次**,而那把锁与 addBytes 是同一把全局锁。这个代价是 2026-08-20
// 那个真机 bug 换来的:Record 只在建连那一刻被调用,于是订阅之前就已经建好的
// 连接永远不会出现在窗口里,而长连接(会议媒体流、WebSocket、SSH)恰恰全是
// 这种。窗口打开时用这张表播种,才看得见「已经在跑的东西」。
//
// **热路径仍然不做归因**:问内核、解进程名全部发生在 Snapshot 里;字节记账
// (AddUp/AddDown,每次转发写都要走)仍由一次 atomic 读挡在锁外。
//
// **已知缺口:种子把一个 socket 上并存的 N 条流压成一条,而新记录不会。**
// live 按 PortKey 记,同键最后写入者胜(见 Record),seedFromLiveLocked 每键只
// 发一条记录。于是一个会议 socket 同时打 STUN(可能直连)+ TURN(可能走隧道)时:
// **订阅前**建立的只会出现在**一个**组里、连接数恒为 1;**订阅后**建立的则正确地
// 出现在**两个**组里、连接数为 N。同一个事实,按窗口打开时机给出不同答案 ——
// 而「腾讯会议为什么绕一圈」恰恰是这个功能要回答的问题。
// 今天刻意不修:相对修复前(**完全看不见**)这仍是巨大改善,用户的用例答得出来、
// 只是少一个组;真修不便宜 —— ConnClosed(port, udp) 无从知道该减哪一档,要把键
// 重新设计成能分辨同一 socket 上的不同流。当前行为由
// TestAppTrafficSeedCollapsesConcurrentFlowsOnOneSocket **明确钉住**
// (那条测试断言的是「这是已知行为」,不是「这样是对的」)。
//
// 三态刻意分开,一条都不许合并:「没人在看」/「在看但问不出来」/「在看且
// 确实没有连接」。把后两者压成一份空报告,读起来就是句自洽的假话。
type AppTraffic struct {
	src appSource
	now func() time.Time

	// active 让**字节记账**在未订阅时连锁都不碰 —— 那条路径是每次转发写一次,
	// 比 Record 热几个数量级,拿一把全局锁去发现「没人在看」正是要消灭的开销。
	// Record/ConnClosed 不再读它:活连接表无条件维护。
	active atomic.Bool

	mu      sync.Mutex
	expires time.Time
	records []appattr.ConnRecord
	next    int  // 环形缓冲写指针
	wrapped bool // 是否已经绕过一圈
	bytesUp map[appattr.PortKey]int64
	bytesDn map[appattr.PortKey]int64
	// live 是「此刻还开着的连接」。**不随订阅生灭** —— 它的边界是 ConnClosed,
	// 不是 TTL;跟着订阅清空就等于回到那个只看得见新连接的 bug。
	live map[appattr.PortKey]liveConn
}

func NewAppTraffic(src appSource, now func() time.Time) *AppTraffic {
	if now == nil {
		now = time.Now
	}
	return &AppTraffic{src: src, now: now, live: map[appattr.PortKey]liveConn{}}
}

// Subscribe 开启或续期采集。菜单每次拉取都会调它。
func (t *AppTraffic) Subscribe() {
	t.mu.Lock()
	defer t.mu.Unlock()
	// **先结算过期,再续期。** 中间没人调过 Snapshot 时,上一轮的缓冲还原样
	// 挂着;只看 active 标志就续期会把它整个带进新一轮 —— 用户看到的现象是
	// 「关掉窗口再打开,显示的还是上次那批数字」。
	t.expiredLocked()
	if !t.active.Load() {
		t.records = make([]appattr.ConnRecord, appTrafficMaxRecords)
		t.next, t.wrapped = 0, false
		t.bytesUp = make(map[appattr.PortKey]int64)
		t.bytesDn = make(map[appattr.PortKey]int64)
		// **只在一次订阅**开始时播种,续期时不播。菜单每 5 秒调一次 Subscribe,
		// 每次都播会让同一条连接每 5 秒多算一次:界面上连接数随时间线性膨胀,
		// 而没有任何一处报错。
		t.seedFromLiveLocked()
	}
	t.expires = t.now().Add(appTrafficTTL)
	t.active.Store(true)
}

// seedFromLiveLocked 把此刻所有活连接作为记录塞进刚建好的环形缓冲。
// 调用者必须持有 t.mu,且 records 必须已经是新的一份(**此时 t.active 还是
// false**,种子先于置位发生 —— 见 appendRecordLocked 的契约注释)。
//
// **种子不做容量限制,活连接超过 appTrafficMaxRecords(4096)时它自己就会把
// 缓冲绕满**,最早那批种子被后来的种子挤掉。这是刻意的:环形缓冲的语义本就是
// 「满了丢最旧的」,给种子单开一条截断规则只会多出一种要解释的行为。真机量级
// 是 66 条活连接(62 倍余量),但这个前提写在这里,别默认它永远成立 ——
// 一旦 ConnClosed 那条边界破了,泄漏出来的陈旧种子会先在这里显形(见 ConnClosed)。
//
// 排序只为让输出确定:map 迭代顺序随机,而 liveRecordsLocked 的顺序是承重的
// (Aggregate 按倒序把字节记给最近那条记录)。种子彼此的键互不相同,顺序其实
// 不影响任何数字,但一份随机顺序的输出会让将来任何一条顺序相关的断言变成 flake。
func (t *AppTraffic) seedFromLiveLocked() {
	keys := make([]appattr.PortKey, 0, len(t.live))
	for k := range t.live {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].UDP != keys[j].UDP {
			return !keys[i].UDP
		}
		return keys[i].Port < keys[j].Port
	})
	for _, k := range keys {
		c := t.live[k]
		t.appendRecordLocked(appattr.ConnRecord{
			SrcPort: k.Port,
			UDP:     k.UDP,
			Path:    c.path,
			Source:  c.source,
			Rule:    c.rule,
		})
	}
}

// expiredLocked 报告「此刻不在采集」,并在 TTL 刚过期时就地停掉采集、清空缓冲。
// 未订阅与已过期都返回 true —— 调用方要的就是「现在别干活」这一个判断。
// 调用者必须持有 t.mu。
func (t *AppTraffic) expiredLocked() bool {
	if !t.active.Load() {
		return true
	}
	if t.now().Before(t.expires) {
		return false
	}
	t.active.Store(false)
	t.records, t.bytesUp, t.bytesDn = nil, nil, nil
	t.next, t.wrapped = 0, false
	return true
}

// Record 记一条连接的判定。数据面调用,**不做任何归因**。
//
// udp 单独作为形参而不是让调用方自己拼 appattr.PortKey:拼结构体时漏填
// UDP 字段没有编译错误(零值就是 false),后果是所有连接都被当成 TCP 归因,
// 而界面上只会看到一个应用名、看不到冲突。形参漏传则编译不过。
func (t *AppTraffic) Record(srcPort uint16, udp bool, path appattr.Path, source, rule string) {
	key := appattr.PortKey{Port: srcPort, UDP: udp}
	t.mu.Lock()
	defer t.mu.Unlock()

	// **活连接表无条件维护,未订阅时也写。** 这是这次修复付出的新代价,也是
	// 它唯一能起作用的地方:窗口是在连接建好之后才打开的,那一刻若表里没有
	// 这条连接,它就永远看不见了(见类型注释上的真机 bug)。
	// 表里只有端口与判定,没有应用名 —— 隐私前提不受影响。
	c := t.live[key]
	c.path, c.source, c.rule = path, source, rule
	c.refs++
	t.live[key] = c

	if t.expiredLocked() {
		return
	}
	// **端口复用时清账,这两行是承重的 —— 但只对 TCP。**
	//
	// 下游 appattr.Aggregate 按键全局去重、只把字节记给该键**最近**的那条
	// 记录 —— 这个设计之所以成立,正是因为上游在端口被复用的这一刻就把旧账
	// 清空了:于是「这个键的字节」永远只反映当前这条连接,不存在「该分给旧
	// 连接多少」这个问题。
	//
	// 去掉 TCP 那半,Aggregate 的数字会变错,而 internal/appattr 的测试一条都
	// 抓不到 —— 它拿到的是外部传入的 map,无从知道上游有没有清账。
	//
	// **两种协议的语义在这里是相反的,别统一。**
	//   TCP:一个源端口同时只有一条活连接,同键再来一条 ⇒ 旧连接已终结,清账正确。
	//   UDP:一个 socket 服务多个对端 —— gVisor 的 forwarder 按 5 元组建流,
	//        一个应用 socket 打 STUN + TURN + 多个 peer 就产生 N 条流、N 次
	//        Record,而它们**属于同一个 socket、同一个应用**。照 TCP 那样清,
	//        每来一条新流就把这个端口已攒的字节抹掉:字节数系统性偏低而连接数
	//        完全正常,没有任何一处报错 —— 恰好命中腾讯会议的媒体流,也就是
	//        这个功能最初的用例。
	//
	// **已知代价**:某天一个 UDP 端口真被不同应用先后复用时,旧账会算给新应用。
	// 相比「媒体流字节系统性偏低」,这个方向的误差小得多、也罕见得多(UDP 端口
	// 在一个 30 秒的订阅窗口里换主人,要比一个会议 socket 同时打多个对端少见)。
	if !udp {
		delete(t.bytesUp, key)
		delete(t.bytesDn, key)
	}
	t.appendRecordLocked(appattr.ConnRecord{
		SrcPort: srcPort,
		UDP:     udp,
		Path:    path,
		Source:  source,
		Rule:    rule,
	})
}

// appendRecordLocked 往环形缓冲写一条。调用者必须持有 t.mu,且 t.records 必须
// 是有效的一份 —— **不是「t.active 为真」**:seedFromLiveLocked 在 Subscribe 里
// 置位 active **之前**就调它(缓冲刚 make 出来,种子先进去,再对外宣布在采集)。
func (t *AppTraffic) appendRecordLocked(rec appattr.ConnRecord) {
	t.records[t.next] = rec
	t.next++
	if t.next == len(t.records) {
		t.next, t.wrapped = 0, true
	}
}

// ConnClosed 报告一条连接结束。**这是活连接表唯一的边界。**
//
// **它不会涨到 OOM,别那么写** —— 键是 appattr.PortKey{uint16, bool},硬上限
// 131072 条、约 10–15MB,泄漏满了也就到此为止。**真正的后果发作得更早,而且更糟**:
// 陈旧条目累积到几千条之后,seedFromLiveLocked 一次就能把 4096 格的环形缓冲填满
// 并绕圈,**新记录被自己的陈旧种子挤掉** —— 报告从「正确但残缺」退化成「错的」,
// 而仍然没有任何一处会报错。
//
// 由 tun 引擎在 handleConn 里 defer 调用,**且必须 defer 在拨号之前**:判定
// (Record)发生在 Dial 内部,kill-switch Block 这类失败同样会留下一条活连接
// 记录,放到拨号成功之后才 defer,那些记录永远没人删。
//
// 多调一次是安全的(键不存在即无操作,refs 见底即删),这是防御性的:引擎侧
// 只要有一条路径重复 defer,这里也不能把表算成负数。
func (t *AppTraffic) ConnClosed(srcPort uint16, udp bool) {
	key := appattr.PortKey{Port: srcPort, UDP: udp}
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.live[key]
	if !ok {
		return
	}
	c.refs--
	if c.refs <= 0 {
		delete(t.live, key)
		return
	}
	t.live[key] = c
}

// liveSize 报告活连接表的条目数。**测试专用的白盒窗口** —— 「表不许无界增长」
// 这条不变量在报告里完全看不见(报告仍然正确),只能直接看表的大小。
func (t *AppTraffic) liveSize() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.live)
}

func (t *AppTraffic) AddUp(srcPort uint16, udp bool, n int64) {
	t.addBytes(srcPort, udp, n, true)
}

func (t *AppTraffic) AddDown(srcPort uint16, udp bool, n int64) {
	t.addBytes(srcPort, udp, n, false)
}

func (t *AppTraffic) addBytes(srcPort uint16, udp bool, n int64, up bool) {
	if !t.active.Load() || n <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.expiredLocked() {
		return
	}
	key := appattr.PortKey{Port: srcPort, UDP: udp}
	if up {
		t.bytesUp[key] += n
	} else {
		t.bytesDn[key] += n
	}
}

// Snapshot 现问一次内核,把攒下的连接记录 join 成按应用的报告。
//
// 三态刻意分开:subscribed=false 是「没人在看」;subscribed=true + err 是
// 「在看但问不出来」;subscribed=true + 空报告是「在看,确实还没有连接」。
//
// 问内核那一步**不持锁** —— 它要读两张 pcblist 再逐个 PID 解进程名,真机实测
// 毫秒级;持着锁去做会让数据面每条连接都排在它后面。
func (t *AppTraffic) Snapshot() (appattr.Report, bool, error) {
	t.mu.Lock()
	if t.expiredLocked() {
		t.mu.Unlock()
		// 没人在看时也给出**三组齐全**的空报告:消费方按下标取组,
		// 组数浮动会让渲染层错位。
		return appattr.Aggregate(nil, nil, nil, nil), false, nil
	}
	records := t.liveRecordsLocked()
	up := make(map[appattr.PortKey]int64, len(t.bytesUp))
	for k, v := range t.bytesUp {
		up[k] = v
	}
	dn := make(map[appattr.PortKey]int64, len(t.bytesDn))
	for k, v := range t.bytesDn {
		dn[k] = v
	}
	t.mu.Unlock()

	owners, err := t.src.OwnersByPort()
	if err != nil {
		// **不许在报错的同时再给一份看起来正常的报告。** 空报告读作
		// 「查过了,一个应用都没有」,而这里的事实是「没查出来」。
		return appattr.Report{}, true, err
	}
	return appattr.Aggregate(records, owners, up, dn), true, nil
}

// liveRecordsLocked 把环形缓冲摊平成时间序。调用者必须持有 t.mu。
//
// **这里的顺序是承重的,不是整洁问题。** Record 在端口复用时只清字节账、不删
// 旧的 ConnRecord,所以同一个键在一个窗口里可以有好几条记录并存;而
// appattr.Aggregate 按倒序遍历、只把字节记给该键**最近**的那条 —— 两个 append
// 对调就等于把「最近」判反,那笔字节会记到上一个应用/上一条路径头上。
// 由 TestAppTrafficKeepsTimeOrderAcrossRingBoundaries 用可区分的 owners 钉住
// (只比总条数的测试对顺序完全不敏感)。
func (t *AppTraffic) liveRecordsLocked() []appattr.ConnRecord {
	if !t.wrapped {
		return append([]appattr.ConnRecord(nil), t.records[:t.next]...)
	}
	out := make([]appattr.ConnRecord, 0, len(t.records))
	out = append(out, t.records[t.next:]...)
	return append(out, t.records[:t.next]...)
}
