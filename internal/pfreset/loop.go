package pfreset

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// Driver 是对 pfctl 的四个动作。做成接口只为让循环在没有 pf 的机器上可测。
type Driver interface {
	Enable(ctx context.Context) (token string, err error)
	Load(ctx context.Context, rules string) error
	Flush(ctx context.Context) error
	Release(ctx context.Context, token string) error
}

// Options 是一次重置的全部输入。
type Options struct {
	Device       string
	RoutedAround []netip.Prefix
	// Observe 回答「此刻还有几条连接在绕过 bx」(与 appattr.StrayConnections 同一份判据)。
	// ok=false 是「问不出来」:不是 0 —— 把它读成 0 会在读不到 socket 表时编出一次成功。
	Observe  func() (n int, ok bool)
	Tick     <-chan time.Time
	Deadline time.Duration
	Log      func(format string, args ...any)
}

// Outcome 说清做了什么:没残留就 Attempted=false;做了就报初始几条、还剩几条、花了多久。
type Outcome struct {
	Attempted bool
	Initial   int
	Remaining int
	Elapsed   time.Duration
	Err       error
}

// Run 是那条循环:先看一次 —— 零条就一个 pfctl 都不调;有,则 enable → load →
// 每个 tick 看一次 → 清零 / 到 Deadline / ctx 取消即拆。**每条退出路径都拆**:
// flush 与 release 在 defer 里,装规则失败也释放引用(引用是我们加的)。
func Run(ctx context.Context, d Driver, o Options) (out Outcome) {
	if o.Log == nil {
		o.Log = func(string, ...any) {}
	}
	initial, ok := o.Observe()
	if !ok {
		out.Err = errors.New("could not read the socket table; nothing was reset")
		return out
	}
	out.Initial = initial
	out.Remaining = initial
	if initial == 0 || d == nil {
		return out
	}
	out.Attempted = true
	start := time.Now()
	defer func() { out.Elapsed = time.Since(start) }()

	token, err := d.Enable(ctx)
	if err != nil {
		out.Err = fmt.Errorf("enabling pf: %w", err)
		return out
	}
	defer func() {
		// 拆的动作不许被取消了的 ctx 拦下:那正是「用户几秒内 bx down」这条路。
		if ferr := d.Flush(context.WithoutCancel(ctx)); ferr != nil {
			out.Err = errors.Join(out.Err, fmt.Errorf("flushing the bx anchor: %w", ferr))
		}
		if rerr := d.Release(context.WithoutCancel(ctx), token); rerr != nil {
			out.Err = errors.Join(out.Err, fmt.Errorf("releasing pf: %w", rerr))
		}
	}()
	if err := d.Load(ctx, Rules(o.Device, o.RoutedAround)); err != nil {
		out.Err = fmt.Errorf("loading the reset rules: %w", err)
		return out
	}
	o.Log("pf reset: %d connection(s) opened before protection was on are being reset on %s", out.Initial, o.Device)
	deadline := time.After(o.Deadline)
	unreadable := 0
	for {
		select {
		case <-ctx.Done():
			return out
		case <-deadline:
			// 到点还在的多半是**空闲**的 socket:`return-rst` 只在它发包时才打得到,窗口里
			// 没发包的这次没重置,交给两段式去点名。这行不是失败,是如实的余额。
			o.Log("pf reset: %s elapsed, %d of %d connection(s) still outside bx (idle sockets are only reset when they next send; the menu keeps tracking them)", time.Since(start).Round(time.Millisecond), out.Remaining, out.Initial)
			if unreadable > 0 {
				out.Err = errors.Join(out.Err, fmt.Errorf("the socket table could not be read %d time(s) during the reset", unreadable))
			}
			return out
		case <-o.Tick:
			n, ok := o.Observe()
			if !ok {
				unreadable++ // 问不出来不是 0:继续等,不走「清零」那条路
				continue
			}
			out.Remaining = n
			if n == 0 {
				o.Log("pf reset: none of the %d connection(s) is outside bx any more (%s)", out.Initial, time.Since(start).Round(time.Millisecond))
				return out
			}
		}
	}
}
