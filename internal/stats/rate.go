package stats

import (
	"fmt"
	"sync"
	"time"
)

// 「这条隧道实际上跑多快」。
//
// ## 为什么是峰值而不是平均
//
// 平均值里绝大部分是**空闲**:一台机器一天里真正在传数据的时间是零头,平均下来
// 每台服务器都是「几十 KB/s」,而那个数字对「哪台快」一个字都答不了。项目所有者
// 手工做的那次 A/B(166 比 195/142 慢一个数量级,而延迟一模一样)量的正是**批量
// 下载时能到多少** —— 峰值。
//
// ## 为什么不主动测速
//
// 主动测速要下载一个大文件,那是凭空造出来的流量,还得选一个测速服务器(又一个
// 会被墙、会成为指纹的目标)。而 bx **就在数据面上**:用户已经产生的每一个字节
// 它都看见了。这与「结果计数」是同一条路子 —— 那次改动上线第一分钟就逼出一个
// 存在了很久的严重 bug,靠的正是停止扔掉已经看见的东西。

// rateSampleWindow 是两次采样之间的最小间隔。
//
// 太密会把 TCP 的突发放大成一个假的峰值(一个 64KB 的窗口在 10ms 里到齐,
// 折算成 6.4MB/s,而链路根本没那么快);太疏会把短促的下载整个错过。
const rateSampleWindow = time.Second

// rateDecayAfter 是峰值的有效期。
//
// **峰值必须会过期。** 一个三天前的数字挂在界面上,读起来像「现在就这么快」——
// 而那台服务器可能早就不行了。过期之后归零,界面据此说「还没测到」,而不是
// 拿一个陈旧的数字冒充现状。
const rateDecayAfter = 30 * time.Minute

// RateMeter 从累计字节数里算出速率。
//
// **它只做算术,不碰时钟也不碰计数器** —— 采样点由调用方喂进来,于是整个判定
// 可以在单测里逐拍推演(这个仓库里凡是自己去问时间的东西都难测,而难测的东西
// 最后都错了)。
type RateMeter struct {
	mu sync.Mutex

	lastAt    time.Time
	lastUp    int64
	lastDown  int64
	haveFirst bool

	peakBPS int64
	peakAt  time.Time
}

// Observe 喂进一次采样:此刻的累计上行、下行字节数。
//
// 第一次调用只记基线,不产出速率 —— 从零开始的累计值除以「程序启动到现在」
// 会得出一个毫无意义的数(启动时下载的那几百 KB 除以 0.2 秒)。
func (m *RateMeter) Observe(now time.Time, up, down int64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.haveFirst {
		m.lastAt, m.lastUp, m.lastDown, m.haveFirst = now, up, down, true
		return
	}
	elapsed := now.Sub(m.lastAt)
	if elapsed < rateSampleWindow {
		// 采样太密:**不更新基线**。更新了的话下一拍的间隔又会很短,
		// 于是永远在用一个会放大突发的窗口。
		return
	}
	// 计数器被换掉(热切服务器会重建吗?今天不会,但下降就是不可信)时重置基线,
	// 绝不产出一个负速率或一个天文数字。
	deltaUp, deltaDown := up-m.lastUp, down-m.lastDown
	m.lastAt, m.lastUp, m.lastDown = now, up, down
	if deltaUp < 0 || deltaDown < 0 {
		return
	}
	bps := int64(float64(deltaUp+deltaDown) / elapsed.Seconds())
	if bps > m.peakBPS || m.expiredLocked(now) {
		m.peakBPS, m.peakAt = bps, now
	}
}

// PeakBPS 返回有效期内观测到的最高速率,以及它是什么时候看到的。
//
// **过期就归零并明说没有观测**,不拿陈旧数字冒充现状。返回 ok=false 时
// 界面该说「还没测到」——「0 B/s」读起来像「这条隧道死了」。
func (m *RateMeter) PeakBPS(now time.Time) (bps int64, at time.Time, ok bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.peakBPS <= 0 || m.expiredLocked(now) {
		return 0, time.Time{}, false
	}
	return m.peakBPS, m.peakAt, true
}

func (m *RateMeter) expiredLocked(now time.Time) bool {
	return m.peakAt.IsZero() || now.Sub(m.peakAt) > rateDecayAfter
}

// HumanBPS 把字节每秒写成人读的样子。
//
// 用十进制(MB = 10^6)而不是 MiB:用户拿这个数去跟宽带套餐、跟别的测速工具比,
// 那些全是十进制。(与本包 humanBytes 的 1024 进制刻意不同 —— 那个描述的是
// 「传了多少」,这个描述的是「多快」,后者的对照物是运营商的标称速率。)
func HumanBPS(bps int64) string {
	switch {
	case bps >= 1_000_000:
		return fmt.Sprintf("%.1f MB/s", float64(bps)/1_000_000)
	case bps >= 1_000:
		return fmt.Sprintf("%.0f kB/s", float64(bps)/1_000)
	default:
		return fmt.Sprintf("%d B/s", bps)
	}
}
