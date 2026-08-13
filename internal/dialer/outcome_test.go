package dialer

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"testing"

	"github.com/getbx/bx/internal/route"
)

// recordingCounter 记下数据面报上来的每一次决策与结果。
type recordingCounter struct {
	mu       sync.Mutex
	direct   int
	proxy    int
	dFail    int
	pFail    int
	attempts []string // "source|rule"
	failures []string
}

func (c *recordingCounter) Proxy()        { c.mu.Lock(); c.proxy++; c.mu.Unlock() }
func (c *recordingCounter) Direct()       { c.mu.Lock(); c.direct++; c.mu.Unlock() }
func (c *recordingCounter) Blocked()      {}
func (c *recordingCounter) UDPBlocked()   {}
func (c *recordingCounter) DirectFailed() { c.mu.Lock(); c.dFail++; c.mu.Unlock() }
func (c *recordingCounter) ProxyFailed()  { c.mu.Lock(); c.pFail++; c.mu.Unlock() }
func (c *recordingCounter) RuleAttempt(source, rule string) {
	c.mu.Lock()
	c.attempts = append(c.attempts, source+"|"+rule)
	c.mu.Unlock()
}

func (c *recordingCounter) RuleFailure(source, rule string) {
	c.mu.Lock()
	c.failures = append(c.failures, source+"|"+rule)
	c.mu.Unlock()
}

type failingDialer struct{ err error }

func (f failingDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	return nil, f.err
}

type okDialer struct{}

func (okDialer) DialContext(context.Context, string, string) (net.Conn, error) {
	c, _ := net.Pipe()
	return c, nil
}

type fixedResolver struct{}

func (fixedResolver) Resolve(context.Context, string) (netip.Addr, error) {
	return netip.MustParseAddr("203.0.113.9"), nil
}

// **这是整件事的落点:失败必须被记下来,并归因到逼出它的那条规则。**
//
// 真实场景:用户 config 里 `'*.steamstatic.com'` 强制直连,而那条直连路已经
// 完全不通 —— 每一次都在几毫秒内失败。bx 在数据面上**每一次都看见了**,
// 却只把它打进 debug 日志,于是 `bx status` 报 `direct 26186` 而对失败只字不提。
func TestDirectDialFailureIsCountedAndAttributedToTheRule(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{
		Resolver: fixedResolver{},
		Direct:   failingDialer{err: errors.New("connection reset")},
		Stats:    counter,
	}
	d.SetRouter(&route.Router{
		GlobalProxy: true,
		UserDirect:  route.NewDomainSet([]string{"*.steamstatic.com"}),
	})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "cdn.akamai.steamstatic.com", Port: 443}); err == nil {
		t.Fatal("这次拨号本该失败")
	}

	if counter.direct != 1 || counter.dFail != 1 {
		t.Errorf("direct=%d direct_failed=%d, want 1/1 —— 失败被扔掉了正是本次要修的",
			counter.direct, counter.dFail)
	}
	want := "user_direct|*.steamstatic.com"
	if len(counter.attempts) != 1 || counter.attempts[0] != want {
		t.Errorf("判定归因 = %v, want [%s]", counter.attempts, want)
	}
	if len(counter.failures) != 1 || counter.failures[0] != want {
		t.Errorf("失败归因 = %v, want [%s] —— 归因的全部价值就是点名该删哪一行",
			counter.failures, want)
	}
}

// 成功的拨号只记判定,不记失败。少了这一条,上面那条可以靠「什么都算失败」满足。
func TestSuccessfulDialIsNotCountedAsFailure(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter}
	d.SetRouter(&route.Router{
		GlobalProxy: true,
		UserDirect:  route.NewDomainSet([]string{"*.steamstatic.com"}),
	})
	d.SetTransport(&Transport{Proxy: okDialer{}, Healthy: func() bool { return true }})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "a.steamstatic.com", Port: 443}); err != nil {
		t.Fatalf("这次拨号本该成功:%v", err)
	}
	if counter.dFail != 0 || len(counter.failures) != 0 {
		t.Errorf("成功的拨号被记成了失败:dFail=%d failures=%v", counter.dFail, counter.failures)
	}
}

// 走隧道那一侧同样要记 —— 隧道坏了的表现是**代理失败率飙升**,
// 而那是与「某条直连规则失效」完全不同的故障,处置也不同。
func TestProxyDialFailureIsCountedToo(t *testing.T) {
	counter := &recordingCounter{}
	d := &Dialer{Resolver: fixedResolver{}, Direct: okDialer{}, Stats: counter}
	d.SetRouter(&route.Router{GlobalProxy: true})
	d.SetTransport(&Transport{
		Proxy:   failingDialer{err: errors.New("socks5 refused")},
		Healthy: func() bool { return true },
	})

	if _, err := d.Dial(context.Background(), route.Meta{Domain: "example.org", Port: 443}); err == nil {
		t.Fatal("这次拨号本该失败")
	}
	if counter.proxy != 1 || counter.pFail != 1 {
		t.Errorf("proxy=%d proxy_failed=%d, want 1/1", counter.proxy, counter.pFail)
	}
	if len(counter.failures) != 1 || counter.failures[0] != "default|" {
		t.Errorf("内建来源也要归因(只是没有规则原文可点名):%v", counter.failures)
	}
}
