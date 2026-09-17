package dns

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/splitdns"
	"golang.org/x/net/dns/dnsmessage"
)

// recordingForwarder 记下被问过哪些 server,并能让指定的几台**永不应答** ——
// 那正是「域控挂了」最常见的形态(不是拒绝,是没有回应)。
type recordingForwarder struct {
	mu     sync.Mutex
	asked  []string
	answer netip.Addr
	stall  map[string]bool
}

func (f *recordingForwarder) Forward(ctx context.Context, server string, query []byte) ([]byte, error) {
	f.mu.Lock()
	f.asked = append(f.asked, server)
	stalled := f.stall[server]
	f.mu.Unlock()
	if stalled {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var p dnsmessage.Parser
	h, _ := p.Start(query)
	q, _ := p.Question()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: h.ID, Response: true, RCode: dnsmessage.RCodeSuccess})
	_ = b.StartQuestions()
	_ = b.Question(q)
	_ = b.StartAnswers()
	_ = b.AResource(
		dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
		dnsmessage.AResource{A: f.answer.As4()},
	)
	out, _ := b.Finish()
	return out, nil
}

func (f *recordingForwarder) askedServers() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.asked...)
}

func newMultiServerSplit(t *testing.T, servers []string, fwd Forwarder) *Server {
	t.Helper()
	pool, err := fakeip.New("198.18.0.0/15")
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(pool, 1)
	s.SetSplit([]SplitRoute{{
		Match:   route.NewDomainSet([]string{"*.corp.example"}),
		Servers: servers,
	}}, fwd, splitdns.NewSet())
	return s
}

// —— 一台挂了不该把整个内网解析拖垮(2026-09-16)——
//
// 顺序回退**对最常见的故障形态无效**:「挂了 = 不应答」时第一台要先耗满预算,
// 而系统 resolver 早就放弃了。所以语义是并发查、先到先用 —— 内网 DNS 就在
// 局域网(真机实测往返 14ms),并发两台的成本可以忽略。
func TestSplitAsksEveryServerAtOnceAndTakesTheFirstAnswer(t *testing.T) {
	fwd := &recordingForwarder{
		answer: netip.MustParseAddr("10.0.13.45"),
		stall:  map[string]bool{"10.0.13.23:53": true}, // 第一台挂了
	}
	s := newMultiServerSplit(t, []string{"10.0.13.23:53", "10.0.13.24:53"}, fwd)

	start := time.Now()
	resp, err := s.Respond(buildQuery(t, 1, "host.corp.example.", dnsmessage.TypeA))
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	if got := firstA(t, resp); got != netip.MustParseAddr("10.0.13.45") {
		t.Fatalf("拿到的 A 是 %v —— 第二台的答案没被采用", got)
	}
	// **并发的证据是时间**:第一台一直挂着,而整轮远早于预算就返回了。
	// 顺序回退在这个输入上会花掉整个预算。
	if elapsed := time.Since(start); elapsed > splitQueryBudget/2 {
		t.Errorf("耗时 %v —— 像是等第一台超时才问第二台(顺序回退),不是并发", elapsed)
	}
	// **这条断言天生有竞态,要等一等 —— 而等待本身也是判据的一部分。**
	// 先到先用意味着 Respond 可能在挂住那一路的 goroutine 还没跑到第一行时
	// 就返回了。第一版直接断言就偶发红,而**一个会偶发红的闸门比没有闸门更糟**。
	// 有界等待:goroutine 已经 spawn 了,1 秒是极宽的余量。
	deadline := time.Now().Add(time.Second)
	for len(fwd.askedServers()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := fwd.askedServers(); len(got) != 2 {
		t.Errorf("问过的 server = %v,want 两台都问(并发才会两台都发出去)", got)
	}
}

// 全挂 ⇒ SERVFAIL,而且**在预算内**返回。这条钉的是「离网时别卡满五秒」:
// 笔记本离开内网是常态,而每一次内网域名查询都要付这个代价。
func TestSplitFailsWithinTheBudgetWhenEveryServerIsGone(t *testing.T) {
	fwd := &recordingForwarder{stall: map[string]bool{
		"10.0.13.23:53": true, "10.0.13.24:53": true,
	}}
	s := newMultiServerSplit(t, []string{"10.0.13.23:53", "10.0.13.24:53"}, fwd)

	start := time.Now()
	if _, err := s.Respond(buildQuery(t, 1, "host.corp.example.", dnsmessage.TypeA)); err != nil {
		t.Fatalf("全挂也该回 SERVFAIL 而不是错误:%v", err)
	}
	if elapsed := time.Since(start); elapsed > splitQueryBudget+2*time.Second {
		t.Errorf("耗时 %v,超出预算 %v 太多 —— 预算没起作用", elapsed, splitQueryBudget)
	}
}

// **预算必须明显短于大多数 DNS 客户端的耐心。** 原来硬编码 5 秒,而真机实测内网
// DNS 往返 14ms(2026-09-16,项目所有者的公司网络)—— 5 秒是它的 350 倍,且离网
// 时每次查询都要付满。这条不钉某个具体数值,钉的是「它没有悄悄涨回去」。
func TestSplitQueryBudgetStaysShortEnoughToBeUseful(t *testing.T) {
	if splitQueryBudget > 3*time.Second {
		t.Errorf("预算 %v —— 离网时每一次内网域名查询都要付这么久,而离网是笔记本的常态", splitQueryBudget)
	}
	if splitQueryBudget < 500*time.Millisecond {
		t.Errorf("预算 %v 太短 —— 内网 DNS 忙的时候会被误判成挂了", splitQueryBudget)
	}
}
