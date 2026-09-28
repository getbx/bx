package supervisor

import (
	"net/netip"
	"testing"
	"time"

	"github.com/getbx/bx/internal/appattr"
)

// 真机 2026-09-28:每次 `bx up` 之后菜单裂开几分钟,点名 Chrome / Mail / WeChat,
// 而这些连接几分钟内自己就没了(keep-alive 到期、推送重连)。「退出重开」对它们是
// 白要求;真需要人的只有一直不走的那几条。所以按**在 Core 眼里活了多久**分两组:
// 未满门槛的是「正在退场」,满了的才是「顽固」。同一条连接的键是四元组 + PID。
func TestStrayTrackerSplitsSettlingFromStubbornByHowLongCoreHasSeenThem(t *testing.T) {
	en0 := netip.MustParseAddr("172.20.10.2")
	chrome := appattr.PCB{LocalPort: 49528, LocalAddr: en0, RemoteAddr: netip.MustParseAddr("203.0.113.11"), RemotePort: 443, LastPID: 77335}
	mail := appattr.PCB{LocalPort: 56589, LocalAddr: en0, RemoteAddr: netip.MustParseAddr("203.0.113.12"), RemotePort: 993, LastPID: 5156}
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	tr := &strayTracker{}

	settling, stubborn := tr.classify(t0, []appattr.PCB{chrome, mail})
	if len(settling) != 2 || len(stubborn) != 0 {
		t.Fatalf("first sight: settling=%d stubborn=%d, want 2/0", len(settling), len(stubborn))
	}
	// 还没到门槛:仍是退场中。
	settling, stubborn = tr.classify(t0.Add(strayStubbornAfter-time.Second), []appattr.PCB{chrome, mail})
	if len(settling) != 2 || len(stubborn) != 0 {
		t.Fatalf("just under the threshold: settling=%d stubborn=%d, want 2/0", len(settling), len(stubborn))
	}
	// Mail 走了、Chrome 满了门槛:只有 Chrome 顽固。
	settling, stubborn = tr.classify(t0.Add(strayStubbornAfter), []appattr.PCB{chrome})
	if len(settling) != 0 || len(stubborn) != 1 || stubborn[0] != chrome {
		t.Fatalf("at the threshold: settling=%v stubborn=%v, want only chrome stubborn", settling, stubborn)
	}
	// 走了又回来的(同一个键)从头计时 —— 上一条连接的年龄不属于这一条。
	settling, stubborn = tr.classify(t0.Add(strayStubbornAfter+time.Minute), []appattr.PCB{chrome, mail})
	if len(settling) != 1 || settling[0] != mail || len(stubborn) != 1 {
		t.Fatalf("mail came back: settling=%v stubborn=%v, want mail settling, chrome stubborn", settling, stubborn)
	}
	// 全走了:表清空,再出现的又是新的。
	if s, st := tr.classify(t0.Add(time.Hour), nil); len(s)+len(st) != 0 || len(tr.firstSeen) != 0 {
		t.Fatalf("nothing stray must leave nothing tracked, got settling=%v stubborn=%v tracked=%d", s, st, len(tr.firstSeen))
	}
}

// 空追踪器(nil)也要能用:darwin 采集器在没接线时不许 panic,而是把所有连接当顽固
// —— 那是今天的行为,少接一根线只会退回旧的严格,不会把泄漏藏起来。
func TestNilStrayTrackerTreatsEverythingAsStubborn(t *testing.T) {
	var tr *strayTracker
	pcb := appattr.PCB{LocalPort: 1, LocalAddr: netip.MustParseAddr("172.20.10.2"), RemoteAddr: netip.MustParseAddr("203.0.113.11"), LastPID: 9}
	settling, stubborn := tr.classify(time.Now(), []appattr.PCB{pcb})
	if len(settling) != 0 || len(stubborn) != 1 {
		t.Fatalf("nil tracker: settling=%v stubborn=%v, want everything stubborn", settling, stubborn)
	}
}
