package supervisor

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

// appSource 现问系统「哪个端口属于哪个应用」。**注入点** —— 测试用假的。
type appSource interface {
	// OwnersByPort 现问内核一次,返回 (端口,协议) → 应用显示名。
	// 键必须是 appattr.PortKey 而不是裸 uint16 —— TCP 与 UDP 端口空间相互独立,
	// 同一个数字可能同时被两个协议占用,合并成一个键会让后写入的那个协议静默
	// 覆盖先写入的归因。
	// 查不出应用的端口**不出现在 map 里**(调用方据此判 unknown)。
	OwnersByPort() (map[appattr.PortKey]string, error)
}

var errAppSourceUnsupported = errors.New("app attribution is only available on macOS")

// appTrafficTTL 是订阅的存活期。菜单被强杀、窗口进程崩溃时不会有人来退订,
// 而「没人看的时候开销精确为零」是这个设计的隐私前提 —— 不能靠对方守规矩来保证。
const appTrafficTTL = 30 * time.Second

// appTrafficMaxRecords 是环形缓冲容量。满了就丢最旧的:界面显示的是「此刻的
// 分流构成」,几万条之前的连接对它没有意义,而无界缓冲会在订阅期间无限长。
const appTrafficMaxRecords = 4096

// AppTraffic 按 (源端口,协议) 记账,并且**只在有人订阅时**才工作。
//
// **热路径只做两件事**:一次 atomic 读判断有没有人在看,以及(有人看时)
// 往环形缓冲写一条记录 / 给两张字节表之一加个数。归因(问内核、解进程名)
// 全部发生在 Snapshot 里,不在拨号或转发路径上。
//
// 三态刻意分开,一条都不许合并:「没人在看」/「在看但问不出来」/「在看且
// 确实没有连接」。把后两者压成一份空报告,读起来就是句自洽的假话。
type AppTraffic struct {
	src appSource
	now func() time.Time

	// active 是热路径唯一要读的东西。它先于 t.mu 被读,未订阅时热路径**连锁
	// 都不碰** —— 数据面每条连接、每次转发都会走这里,拿一把全局锁去发现
	// 「没人在看」本身就是这个设计要消灭的开销。
	active atomic.Bool

	mu      sync.Mutex
	expires time.Time
	records []appattr.ConnRecord
	next    int  // 环形缓冲写指针
	wrapped bool // 是否已经绕过一圈
	bytesUp map[appattr.PortKey]int64
	bytesDn map[appattr.PortKey]int64
}

func NewAppTraffic(src appSource, now func() time.Time) *AppTraffic {
	if now == nil {
		now = time.Now
	}
	return &AppTraffic{src: src, now: now}
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
	}
	t.expires = t.now().Add(appTrafficTTL)
	t.active.Store(true)
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
	if !t.active.Load() {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.expiredLocked() {
		return
	}
	key := appattr.PortKey{Port: srcPort, UDP: udp}
	// **端口复用时清账,这两行是承重的。**
	//
	// 下游 appattr.Aggregate 按键全局去重、只把字节记给该键**最近**的那条
	// 记录 —— 这个设计之所以成立,正是因为上游在端口被复用的这一刻就把旧账
	// 清空了:于是「这个键的字节」永远只反映当前这条连接,不存在「该分给旧
	// 连接多少」这个问题。
	//
	// 去掉这两行,Aggregate 的数字会变错,而 internal/appattr 的测试一条都
	// 抓不到 —— 它拿到的是外部传入的 map,无从知道上游有没有清账。
	delete(t.bytesUp, key)
	delete(t.bytesDn, key)
	t.records[t.next] = appattr.ConnRecord{
		SrcPort: srcPort,
		UDP:     udp,
		Path:    path,
		Source:  source,
		Rule:    rule,
	}
	t.next++
	if t.next == len(t.records) {
		t.next, t.wrapped = 0, true
	}
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
func (t *AppTraffic) liveRecordsLocked() []appattr.ConnRecord {
	if !t.wrapped {
		return append([]appattr.ConnRecord(nil), t.records[:t.next]...)
	}
	out := make([]appattr.ConnRecord, 0, len(t.records))
	out = append(out, t.records[t.next:]...)
	return append(out, t.records[:t.next]...)
}
