package supervisor

import (
	"net/netip"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

// strayStubbornAfter 是一条绕过 bx 的连接从「正在退场」变成「顽固」的门槛。
//
// 真机 2026-09-28:每次 `bx up` 之后菜单裂开几分钟,点名 Chrome / Mail / WeChat,
// 而那些是保护关着那十几秒里开的 keep-alive 与推送连接,几分钟内自己就没了 ——
// 「退出重开」对它们是白要求,而每次日常操作都裂一次图标,人很快学会「裂了也没事」。
// 五分钟取 Chrome 空闲连接池的回收期这个量级;超过它还在的,才是真不会自己走、
// 重开才是出路的那几条。这**不改变泄漏本身**:那五分钟里真实 IP 照样在用,只是
// 不再把一件用户做不了什么的事画成事故。
const strayStubbornAfter = 5 * time.Minute

type strayKey struct {
	localPort, remotePort uint16
	remote                netip.Addr
	pid                   int32
}

// strayTracker 记住每条绕过 bx 的连接**第一次被 Core 看见**的时刻。Core 在 `bx up`
// 时启动,所以「被 Core 看见多久」就是「保护开着之后它还活了多久」。走了的键当场
// 忘掉:同一个键再出现是另一条连接,年龄从头算。
type strayTracker struct {
	firstSeen map[strayKey]time.Time
}

// classify 把此刻的绕过连接分成两组:未满门槛的 settling(正在退场)与满了的
// stubborn(顽固)。nil 追踪器把全部当顽固 —— 那是接线之前的行为,少一根线只会退回
// 旧的严格,不会把泄漏藏起来。
func (t *strayTracker) classify(now time.Time, stray []appattr.PCB) (settling, stubborn []appattr.PCB) {
	if t == nil {
		return nil, stray
	}
	if t.firstSeen == nil {
		t.firstSeen = map[strayKey]time.Time{}
	}
	live := make(map[strayKey]bool, len(stray))
	for _, p := range stray {
		k := strayKey{localPort: p.LocalPort, remotePort: p.RemotePort, remote: p.RemoteAddr.Unmap(), pid: p.LastPID}
		live[k] = true
		first, seen := t.firstSeen[k]
		if !seen {
			first = now
			t.firstSeen[k] = now
		}
		if now.Sub(first) >= strayStubbornAfter {
			stubborn = append(stubborn, p)
		} else {
			settling = append(settling, p)
		}
	}
	for k := range t.firstSeen {
		if !live[k] {
			delete(t.firstSeen, k)
		}
	}
	return settling, stubborn
}
