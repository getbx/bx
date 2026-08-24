package dialer

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/getbx/bx/internal/route"
)

// —— 活连接表的配平按构造成立(2026-08-24)——
//
// 此前:`Record` 写在 Dialer 里,而**释放**(`ConnClosed`)是调用方的事 ——
// `tun.Engine.handleConn` 里一条 defer。写在里面、释放在外面,这个不对称本身
// 就是缺陷的形状:今天只有一个调用方且它是对的,而任何新调用方都必须**记得**
// 配一条,忘了不会有编译错误、不会有测试转红,后果是活连接表里的陈旧条目攒到
// 几千条之后,一次 Subscribe 的种子就能把 4096 格的环形缓冲填满并绕圈,新记录
// 被自己的陈旧种子挤掉 —— 报告从「正确但残缺」退化成「错的」,而仍然没有
// 任何一处会报错。
//
// 现在:谁记账谁释放。拨号走单一漏斗,返回的 conn 包一层,Close 时释放;
// 返回错误时就地释放。

// releaseCall 是一次 ConnClosed。
type releaseCall struct{ flowID uint64 }

func (f *fakeAppRecorder) releases() []releaseCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]releaseCall, len(f.closed))
	copy(out, f.closed)
	return out
}

func TestDialDoesNotReleaseUntilTheConnIsClosed(t *testing.T) {
	d, rec := newAppDialer(t, true)
	conn, err := d.Dial(context.Background(), route.Meta{Domain: "a.qq.com", Port: 443, SrcPort: 51234})
	if err != nil {
		t.Fatalf("拨号: %v", err)
	}
	if got := rec.releases(); len(got) != 0 {
		t.Fatalf("连接还开着就释放了 %d 次 —— 活连接表会看不见一条正在灌流的连接", len(got))
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("关闭: %v", err)
	}
	got := rec.releases()
	if len(got) != 1 {
		t.Fatalf("关闭后释放了 %d 次, want 1: %+v", len(got), got)
	}
	if got[0].flowID != rec.onlyIssued(t) {
		t.Fatalf("释放的是流 %d,而记账那一次发的是 %d —— 配平不成立", got[0].flowID, rec.onlyIssued(t))
	}
}

func TestDialReleasesInPlaceWhenItReturnsAnError(t *testing.T) {
	// **被 kill-switch 拦下的连接已经进了表,而拨号返回错误。** 隧道挂掉时这类
	// 连接恰恰最多;没有这一条,它们的条目永远没人删。
	d, rec := newAppDialer(t, false)
	conn, err := d.Dial(context.Background(), route.Meta{Domain: "api.openai.com", Port: 443, SrcPort: 4321})
	if !errors.Is(err, ErrBlocked) {
		t.Fatalf("err = %v, want ErrBlocked(隧道不健康时必须 fail-closed)", err)
	}
	if conn != nil {
		t.Fatal("被阻断却返回了一个 conn")
	}
	got := rec.releases()
	if len(got) != 1 {
		t.Fatalf("阻断路径释放了 %d 次, want 1: %+v", len(got), got)
	}
	if got[0].flowID != rec.onlyIssued(t) {
		t.Fatalf("释放的是流 %d,而记账那一次发的是 %d", got[0].flowID, rec.onlyIssued(t))
	}
}

func TestClosingTheConnTwiceReleasesOnlyOnce(t *testing.T) {
	// 重复 Close 在 net.Conn 上是合法的,而 relay 与调用方各关一次是常见形状。
	// 释放两次会让 refs 提前归零,把一条**还开着**的连接从活连接表里抹掉。
	d, rec := newAppDialer(t, true)
	conn, err := d.Dial(context.Background(), route.Meta{Domain: "a.qq.com", Port: 443, SrcPort: 777})
	if err != nil {
		t.Fatalf("拨号: %v", err)
	}
	_ = conn.Close()
	_ = conn.Close()
	if got := rec.releases(); len(got) != 1 {
		t.Fatalf("两次 Close 释放了 %d 次, want 1: %+v", len(got), got)
	}
}

func TestTrackedConnStillPassesDeadlinesThrough(t *testing.T) {
	// **包一层不许吃掉 net.Conn 的任何方法。** relay 的空闲超时整套建立在
	// SetReadDeadline/SetWriteDeadline 上:被静默吞掉之后连接不会报错,只是
	// 再也不会因空闲而结束 —— goroutine 与 fd 一起泄漏,而界面上什么都看不出。
	d, _ := newAppDialer(t, true)
	d.Direct = countingDialer{}
	conn, err := d.Dial(context.Background(), route.Meta{Domain: "a.qq.com", Port: 443, SrcPort: 888})
	if err != nil {
		t.Fatalf("拨号: %v", err)
	}
	defer conn.Close()
	when := time.Now().Add(time.Minute)
	if err := conn.SetReadDeadline(when); err != nil {
		t.Fatalf("SetReadDeadline: %v", err)
	}
	if err := conn.SetWriteDeadline(when); err != nil {
		t.Fatalf("SetWriteDeadline: %v", err)
	}
	tracked, ok := conn.(*appTrackedConn)
	if !ok {
		t.Fatalf("返回值不是包装过的 conn(是 %T)—— 这条断言什么也没测", conn)
	}
	inner, ok := tracked.Conn.(*countingConn)
	if !ok {
		t.Fatalf("包装层里面不是测试那个会数 deadline 的 conn(是 %T)", tracked.Conn)
	}
	if n := inner.deadlines; n != 2 {
		t.Fatalf("底层 conn 只收到 %d 次 deadline, want 2 —— 包装层把它们吃掉了", n)
	}
}

func TestDialWithoutAnAppRecorderStillWorks(t *testing.T) {
	// AppRecorder 可空(没人订阅应用视图时的常态)。包装层不许因此 panic,
	// 也不许在没有记账的情况下凭空包一层。
	d, _ := newAppDialer(t, true)
	d.AppRecorder = nil
	conn, err := d.Dial(context.Background(), route.Meta{Domain: "a.qq.com", Port: 443, SrcPort: 999})
	if err != nil {
		t.Fatalf("拨号: %v", err)
	}
	if _, wrapped := conn.(*appTrackedConn); wrapped {
		t.Fatal("没有归因器却仍然包了一层 —— 那层什么也不做,只是多一次间接")
	}
	_ = conn.Close()
}

// countingDialer/countingConn 数 deadline 调用,用来证明包装层没把 net.Conn
// 的方法吃掉。
type countingDialer struct{}

func (countingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	c, _ := net.Pipe()
	return &countingConn{Conn: c}, nil
}

type countingConn struct {
	net.Conn
	deadlines int
}

func (c *countingConn) SetReadDeadline(t time.Time) error {
	c.deadlines++
	return c.Conn.SetReadDeadline(t)
}

func (c *countingConn) SetWriteDeadline(t time.Time) error {
	c.deadlines++
	return c.Conn.SetWriteDeadline(t)
}
