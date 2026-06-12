# split-DNS 实现计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让匹配 split 规则的内网域名经指定内网 DNS 解析、并强制直连出物理网卡(绕开代理隧道),同时把 config 解析改严格模式杜绝「配了但静默失效」。

**Architecture:** split 判定收敛在 DNS 层:匹配域名 → 经 DirectDialer 转发到内网 DNS 拿真实 IP → 把真实 IP 注册进共享 `splitdns.Set` 旁路集 → 原样返回应答。客户端连真实 IP 回到 TUN 时,dialer 查 `Set` 命中即强制 Direct。纯逻辑 Router 不受污染。仅 v4(AAAA→NODATA)。

**Tech Stack:** Go 1.26、`golang.org/x/net/dns/dnsmessage`、`gopkg.in/yaml.v3`、`net/netip`。设计见 `docs/superpowers/specs/2026-06-12-bx-split-dns-design.md`。

---

## 文件结构

- 新建 `internal/splitdns/set.go` + `set_test.go` — 并发安全的直连 IP 旁路集(DNS 填、dialer 查)。
- 改 `internal/config/config.go` + `config_test.go` — `DNS.Split` schema + 校验 + `KnownFields` 严格模式。
- 改 `internal/dns/server.go` + 新建 `internal/dns/split.go` + `split_test.go` — `SplitRoute`/`Forwarder` 类型、UDP forwarder、`Server` 的 split 分支。
- 改 `internal/dialer/dialer.go` + `dialer_test.go` — `SplitDirect` 字段 + Dial 钩子。
- 改 `internal/supervisor/run.go` — 接线(从 config 建 SplitRoute、建共享 Set、注入 dns+dialer)。

---

## Task 1: config schema + 严格模式

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试**

追加到 `internal/config/config_test.go`:

```go
func TestParseSplitDNS(t *testing.T) {
	c, err := Parse([]byte(`
server: "brook://abc"
dns:
  split:
    - domains: ["*.shanghai-electric.com"]
      server: 10.0.13.23
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(c.DNS.Split) != 1 {
		t.Fatalf("want 1 split rule, got %d", len(c.DNS.Split))
	}
	r := c.DNS.Split[0]
	if len(r.Domains) != 1 || r.Domains[0] != "*.shanghai-electric.com" {
		t.Fatalf("bad domains: %+v", r.Domains)
	}
	if r.Server != "10.0.13.23:53" { // 无端口时补 :53
		t.Fatalf("want server 10.0.13.23:53, got %q", r.Server)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	// 严格模式:未知字段必须报错(就是 dns.split 这次该报而没报的根因)。
	_, err := Parse([]byte(`
server: "brook://abc"
totally_unknown_field: 1
`))
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
}

func TestParseRejectsSplitMissingServer(t *testing.T) {
	_, err := Parse([]byte(`
server: "brook://abc"
dns:
  split:
    - domains: ["*.x.com"]
`))
	if err == nil {
		t.Fatal("expected error for split rule without server")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -run 'SplitDNS|UnknownField|SplitMissingServer' -v`
Expected: 编译失败(`c.DNS.Split` 未定义)/ FAIL。

- [ ] **Step 3: 最小实现**

在 `internal/config/config.go` 的 `DNS` 结构体加字段,新增 `SplitRule` 类型,并把 `Parse` 改严格模式 + 校验。

`DNS` 结构体改为:
```go
type DNS struct {
	China      string      `yaml:"china"`
	FakeipCIDR string      `yaml:"fakeip_cidr"`
	Split      []SplitRule `yaml:"split"`
}

// SplitRule:把匹配域名交给指定内网 DNS 解析(并由分流层强制直连)。
type SplitRule struct {
	Domains []string `yaml:"domains"` // 支持 *.suffix 通配
	Server  string   `yaml:"server"`  // 内网 DNS;无端口时补 :53
}
```

`import` 块补 `"bytes"`、`"net"`、`"strings"`(若未引入)。`Parse` 改为:
```go
func Parse(b []byte) (*Config, error) {
	var c Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true) // 未知字段直接报错,杜绝「配了但静默失效」
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if c.Server == "" {
		return nil, fmt.Errorf("config: server 不能为空")
	}
	if c.DNS.China == "" {
		c.DNS.China = "223.5.5.5"
	}
	if c.DNS.FakeipCIDR == "" {
		c.DNS.FakeipCIDR = "198.18.0.0/15"
	}
	for i := range c.DNS.Split {
		r := &c.DNS.Split[i]
		if len(r.Domains) == 0 {
			return nil, fmt.Errorf("config: dns.split[%d].domains 不能为空", i)
		}
		if strings.TrimSpace(r.Server) == "" {
			return nil, fmt.Errorf("config: dns.split[%d].server 不能为空", i)
		}
		if _, _, err := net.SplitHostPort(r.Server); err != nil {
			r.Server = net.JoinHostPort(r.Server, "53") // 无端口补 :53
		}
	}
	if c.DataDir == "" {
		c.DataDir = "/var/lib/bx"
	}
	return &c, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config/ -v`
Expected: 全部 PASS(含既有 `TestLoadValid` 等 —— 其 config 仅用已知字段,严格模式不影响)。

- [ ] **Step 5: 提交**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "feat(config): dns.split schema + KnownFields 严格模式(未知字段报错)"
```

---

## Task 2: splitdns.Set 旁路集

**Files:**
- Create: `internal/splitdns/set.go`
- Test: `internal/splitdns/set_test.go`

- [ ] **Step 1: 写失败测试**

`internal/splitdns/set_test.go`:
```go
package splitdns

import (
	"net/netip"
	"testing"
)

func TestSetAddContains(t *testing.T) {
	s := NewSet()
	ip := netip.MustParseAddr("10.0.13.45")
	if s.Contains(ip) {
		t.Fatal("空集不应命中")
	}
	s.Add(ip)
	if !s.Contains(ip) {
		t.Fatal("Add 后应命中")
	}
	if s.Contains(netip.MustParseAddr("8.8.8.8")) {
		t.Fatal("未加的 IP 不应命中")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/splitdns/ -v`
Expected: 编译失败(包/`NewSet` 不存在)。

- [ ] **Step 3: 最小实现**

`internal/splitdns/set.go`:
```go
// Package splitdns 提供 split-DNS 的「强制直连 IP」旁路集:DNS 层把内网 DNS 解析出的
// 真实 IP 注册进来,dialer 在分流时查它命中即强制 Direct。共享、跨 Router 热重载存活。
package splitdns

import (
	"net/netip"
	"sync"
)

// Set 是并发安全的 netip.Addr 集合(不淘汰:内网 IP 少而稳)。
type Set struct {
	mu sync.RWMutex
	m  map[netip.Addr]struct{}
}

func NewSet() *Set {
	return &Set{m: make(map[netip.Addr]struct{})}
}

func (s *Set) Add(ip netip.Addr) {
	s.mu.Lock()
	s.m[ip] = struct{}{}
	s.mu.Unlock()
}

func (s *Set) Contains(ip netip.Addr) bool {
	s.mu.RLock()
	_, ok := s.m[ip]
	s.mu.RUnlock()
	return ok
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/splitdns/ -v`
Expected: PASS。`go vet ./internal/splitdns/` 干净。

- [ ] **Step 5: 提交**

```bash
git add internal/splitdns/
git commit -m "feat(splitdns): 强制直连 IP 旁路集(Add/Contains,并发安全)"
```

---

## Task 3: DNS forwarder(转发到内网 DNS)

**Files:**
- Create: `internal/dns/split.go`
- Test: `internal/dns/split_test.go`

- [ ] **Step 1: 写失败测试**

`internal/dns/split_test.go`(用本地 UDP echo 服务器验证转发,免 root):
```go
package dns

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestUDPForwarderRoundTrip(t *testing.T) {
	// 起一个本地 UDP 服务器,把收到的查询字节原样加个标记回送。
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	go func() {
		buf := make([]byte, 512)
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		resp := append([]byte{0xAB}, buf[:n]...) // 标记 + 回显
		_, _ = pc.WriteTo(resp, addr)
	}()

	fwd := NewUDPForwarder(&net.Dialer{Timeout: 2 * time.Second})
	resp, err := fwd.Forward(context.Background(), pc.LocalAddr().String(), []byte("query!"))
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	if len(resp) != len("query!")+1 || resp[0] != 0xAB {
		t.Fatalf("bad resp: %v", resp)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dns/ -run UDPForwarder -v`
Expected: 编译失败(`NewUDPForwarder`/`Forward` 不存在)。

- [ ] **Step 3: 最小实现**

`internal/dns/split.go`:
```go
package dns

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/getbx/bx/internal/route"
)

// SplitRoute 是一条编译好的 split 路由:域名匹配器 + 目标内网 DNS(host:port)。
type SplitRoute struct {
	Match  *route.DomainSet
	Server string
}

// Forwarder 把原始 DNS 查询字节转发到指定 server 并返回应答字节。
type Forwarder interface {
	Forward(ctx context.Context, server string, query []byte) ([]byte, error)
}

// contextDialer 是 Forward 拨号所需的最小接口(*net.Dialer 满足;生产注入 DirectDialer 防环)。
type contextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type udpForwarder struct {
	d contextDialer
}

// NewUDPForwarder 用给定拨号器(生产=DirectDialer)构造 UDP DNS 转发器。
func NewUDPForwarder(d contextDialer) Forwarder { return &udpForwarder{d: d} }

func (f *udpForwarder) Forward(ctx context.Context, server string, query []byte) ([]byte, error) {
	conn, err := f.d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, fmt.Errorf("拨内网 DNS %s: %w", server, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(query); err != nil {
		return nil, fmt.Errorf("发查询: %w", err)
	}
	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("读应答: %w", err)
	}
	return buf[:n], nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dns/ -run UDPForwarder -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/dns/split.go internal/dns/split_test.go
git commit -m "feat(dns): split UDP forwarder(经注入拨号器转发到内网 DNS)"
```

---

## Task 4: dns.Server 的 split 分支

**Files:**
- Modify: `internal/dns/server.go`
- Test: `internal/dns/split_test.go`(追加)

- [ ] **Step 1: 写失败测试**

把 `internal/dns/split_test.go`(Task 3 创建,现有 import:`context`/`net`/`testing`/`time`)的 import 块**扩成**(import 必须在文件顶部单块内,不能在 func 后另起):
```go
import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/getbx/bx/internal/fakeip"
	"github.com/getbx/bx/internal/route"
	"github.com/getbx/bx/internal/splitdns"
	"golang.org/x/net/dns/dnsmessage"
)
```
然后追加下列内容到文件末尾:
```go
// fakeForwarder 返回固定的 A 应答(10.0.13.45),并记录是否被调用。
type fakeForwarder struct {
	called bool
	answer netip.Addr
	fail   bool
}

func (f *fakeForwarder) Forward(_ context.Context, _ string, query []byte) ([]byte, error) {
	f.called = true
	if f.fail {
		return nil, fmt.Errorf("内网 DNS 不可达")
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

func newSplitServer(fwd Forwarder, set *splitdns.Set) *Server {
	pool, _ := fakeip.New("198.18.0.0/15")
	s := NewServer(pool, 1)
	s.SetSplit([]SplitRoute{{
		Match:  route.NewDomainSet([]string{"*.shanghai-electric.com"}),
		Server: "10.0.13.23:53",
	}}, fwd, set)
	return s
}

func TestRespondSplitMatchForwardsAndRegisters(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{answer: netip.MustParseAddr("10.0.13.45")}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "app.shanghai-electric.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if !fwd.called {
		t.Fatal("匹配域名应调用 forwarder")
	}
	if !set.Contains(netip.MustParseAddr("10.0.13.45")) {
		t.Fatal("解析出的真实 IP 应注册进 splitDirect 集")
	}
	if firstA(t, resp) != netip.MustParseAddr("10.0.13.45") {
		t.Fatal("应原样返回内网 DNS 的 A 记录")
	}
}

func TestRespondSplitMissDoesNotForward(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{answer: netip.MustParseAddr("10.0.13.45")}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "www.google.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.called {
		t.Fatal("非匹配域名不应调用 forwarder")
	}
	// 非匹配走 fake-IP:A 记录应落在 198.18/15。
	a := firstA(t, resp)
	if !netip.PrefixFrom(netip.MustParseAddr("198.18.0.0"), 15).Contains(a) {
		t.Fatalf("非匹配应得 fake-IP,实得 %v", a)
	}
}

func TestRespondSplitAAAAIsNoData(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{answer: netip.MustParseAddr("10.0.13.45")}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "app.shanghai-electric.com.", dnsmessage.TypeAAAA))
	if err != nil {
		t.Fatal(err)
	}
	if fwd.called {
		t.Fatal("split AAAA 不应转发(逼 v4)")
	}
	if answerCount(t, resp) != 0 {
		t.Fatal("AAAA 应为 NODATA(无答案)")
	}
}

func TestRespondSplitForwardFailIsServFail(t *testing.T) {
	set := splitdns.NewSet()
	fwd := &fakeForwarder{fail: true}
	s := newSplitServer(fwd, set)

	resp, err := s.Respond(buildQuery(t, 1, "app.shanghai-electric.com.", dnsmessage.TypeA))
	if err != nil {
		t.Fatal(err)
	}
	if rcode(t, resp) != dnsmessage.RCodeServerFailure {
		t.Fatal("转发失败应返回 SERVFAIL")
	}
}

// --- 解析辅助 ---

func firstA(t *testing.T, msg []byte) netip.Addr {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		t.Fatal(err)
	}
	_ = p.SkipAllQuestions()
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			t.Fatal("无 A 记录")
		}
		if h.Type == dnsmessage.TypeA {
			a, _ := p.AResource()
			return netip.AddrFrom4(a.A)
		}
		_ = p.SkipAnswer()
	}
}

func answerCount(t *testing.T, msg []byte) int {
	t.Helper()
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		t.Fatal(err)
	}
	_ = p.SkipAllQuestions()
	n := 0
	for {
		if _, err := p.AnswerHeader(); err != nil {
			return n
		}
		_ = p.SkipAnswer()
		n++
	}
}

func rcode(t *testing.T, msg []byte) dnsmessage.RCode {
	t.Helper()
	var p dnsmessage.Parser
	h, err := p.Start(msg)
	if err != nil {
		t.Fatal(err)
	}
	return h.RCode
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dns/ -run 'RespondSplit' -v`
Expected: 编译失败(`Server.SetSplit` 不存在)。

- [ ] **Step 3: 最小实现**

在 `internal/dns/server.go`:给 `Server` 加字段 + `SetSplit` + 在 `Respond` 顶部插 split 分支。

`Server` 结构体改为:
```go
type Server struct {
	pool   *fakeip.Pool
	ttl    uint32
	splits []SplitRoute
	fwd    Forwarder
	direct *splitdns.Set
}
```

`import` 块补 `"context"`、`"net/netip"`、`"github.com/getbx/bx/internal/splitdns"`。

新增方法:
```go
// SetSplit 配置 split 路由(匹配域名转发到内网 DNS 并把真实 IP 注册进 direct 集)。
func (s *Server) SetSplit(splits []SplitRoute, fwd Forwarder, direct *splitdns.Set) {
	s.splits = splits
	s.fwd = fwd
	s.direct = direct
}

// matchSplit 返回命中的 split 路由(无则 nil)。
func (s *Server) matchSplit(domain string) *SplitRoute {
	for i := range s.splits {
		if s.splits[i].Match.Match(domain) {
			return &s.splits[i]
		}
	}
	return nil
}
```

在 `Respond` 中,解析出 `q` 之后、构造应答 builder 之前(即拿到 question 后),插入 split 分支。具体:把现有从 `b := dnsmessage.NewBuilder(...)` 到 `return b.Finish()` 的逻辑保留作「默认 fake-IP 路径」,但在其之前加:

```go
	domain := strings.ToLower(strings.TrimSuffix(q.Name.String(), "."))
	// split 命中且非 AAAA:转发到内网 DNS,注册真实 IP,原样返回。
	if rt := s.matchSplit(domain); rt != nil && q.Type != dnsmessage.TypeAAAA {
		resp, err := s.fwd.Forward(context.Background(), rt.Server, query)
		if err != nil {
			return s.servfail(query)
		}
		s.registerA(resp)
		return resp, nil
	}
	// split 命中 AAAA → 落到下面默认路径,因 q.Type!=TypeA 而成 NODATA(逼 v4)。
```

> 注:现有 `Respond` 已有从 `q.Name` 取 domain 的代码(在 TypeA 分支内,第 53 行)。把取 `domain` 提前到此处共用,删掉 TypeA 分支里重复的那行。

新增两个辅助方法:
```go
// registerA 把应答里所有 A 记录注册进 direct 集(强制直连)。
func (s *Server) registerA(msg []byte) {
	if s.direct == nil {
		return
	}
	var p dnsmessage.Parser
	if _, err := p.Start(msg); err != nil {
		return
	}
	if err := p.SkipAllQuestions(); err != nil {
		return
	}
	for {
		h, err := p.AnswerHeader()
		if err != nil {
			return
		}
		if h.Type == dnsmessage.TypeA {
			a, err := p.AResource()
			if err != nil {
				return
			}
			s.direct.Add(netip.AddrFrom4(a.A))
			continue
		}
		if err := p.SkipAnswer(); err != nil {
			return
		}
	}
}

// servfail 构造一个 RCode=SERVFAIL 的应答(转发失败时返回)。
func (s *Server) servfail(query []byte) ([]byte, error) {
	var p dnsmessage.Parser
	h, err := p.Start(query)
	if err != nil {
		return nil, err
	}
	q, err := p.Question()
	if err != nil {
		return nil, err
	}
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
		ID:                 h.ID,
		Response:           true,
		OpCode:             h.OpCode,
		RecursionDesired:   h.RecursionDesired,
		RecursionAvailable: true,
		RCode:              dnsmessage.RCodeServerFailure,
	})
	if err := b.StartQuestions(); err != nil {
		return nil, err
	}
	if err := b.Question(q); err != nil {
		return nil, err
	}
	return b.Finish()
}
```

确保 `server.go` 的 `import` 含 `"strings"`(现有已用)、并补 `"context"`、`"net/netip"`、`splitdns`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dns/ -v`
Expected: 全部 PASS(含 Task 3 的 forwarder 测试与既有 fake-IP 测试)。

- [ ] **Step 5: 提交**

```bash
git add internal/dns/server.go internal/dns/split_test.go
git commit -m "feat(dns): Server split 分支(匹配转发内网 DNS+注册直连 IP,AAAA→NODATA,失败 SERVFAIL)"
```

---

## Task 5: dialer SplitDirect 钩子

**Files:**
- Modify: `internal/dialer/dialer.go`
- Test: `internal/dialer/dialer_test.go`

- [ ] **Step 1: 写失败测试**

`internal/dialer/dialer_test.go` 已存在(`package dialer`,import 已含 `context`/`net`/`net/netip`/`testing`/`route`)。**只需**:① 在其 import 块补一行 `"github.com/getbx/bx/internal/splitdns"`;② 追加下列内容到文件末尾:

```go
// recordDialer 记录被调用,返回一端 pipe conn。
type recordDialer struct{ used bool }

func (r *recordDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	r.used = true
	c, _ := net.Pipe()
	return c, nil
}

func TestDialSplitDirectForcesDirect(t *testing.T) {
	set := splitdns.NewSet()
	ip := netip.MustParseAddr("10.0.13.45")
	set.Add(ip)

	direct, proxy := &recordDialer{}, &recordDialer{}
	d := &Dialer{Proxy: proxy, Direct: direct, SplitDirect: set}
	d.SetRouter(&route.Router{GlobalProxy: true}) // global:默认本会判 Proxy

	conn, err := d.Dial(context.Background(), route.Meta{IP: ip, Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !direct.used || proxy.used {
		t.Fatalf("split 命中应强制 Direct(direct.used=%v proxy.used=%v)", direct.used, proxy.used)
	}
}

func TestDialNonSplitPublicGoesProxy(t *testing.T) {
	direct, proxy := &recordDialer{}, &recordDialer{}
	d := &Dialer{Proxy: proxy, Direct: direct, SplitDirect: splitdns.NewSet()}
	d.SetRouter(&route.Router{GlobalProxy: true})

	conn, err := d.Dial(context.Background(), route.Meta{IP: netip.MustParseAddr("1.1.1.1"), Port: 443})
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if !proxy.used || direct.used {
		t.Fatalf("未命中 split 的公网 IP 应走 Proxy(direct.used=%v proxy.used=%v)", direct.used, proxy.used)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/dialer/ -run 'SplitDirect|NonSplitPublic' -v`
Expected: 编译失败(`Dialer.SplitDirect` 字段不存在)。

- [ ] **Step 3: 最小实现**

`internal/dialer/dialer.go`:`import` 补 `"github.com/getbx/bx/internal/splitdns"`。`Dialer` 结构体加字段:
```go
	// SplitDirect 可空:split-DNS 解析出的内网真实 IP 集,命中即强制直连(绕 Router)。
	SplitDirect *splitdns.Set
```

在 `Dial` 里,把现有的 `dec := rt.Decide(m)`(第 67 行)替换为:
```go
	var dec route.Decision
	if m.Domain == "" && d.SplitDirect != nil && d.SplitDirect.Contains(m.IP) {
		dec = route.Direct // split 解析出的内网真实 IP:强制直连,跳过 Router
	} else {
		dec = rt.Decide(m)
	}
```

(其后的 `NeedResolve` 处理、`switch dec` 均不变;Direct 分支在 `m.Domain==""` 时直接用 `m.IP` 经 `d.Direct` 拨号,正合所需。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/dialer/ -v`
Expected: 全部 PASS(含既有 dialer 测试,零回归)。

- [ ] **Step 5: 提交**

```bash
git add internal/dialer/dialer.go internal/dialer/dialer_test.go
git commit -m "feat(dialer): SplitDirect 钩子(split 内网 IP 强制直连,绕过 Router)"
```

---

## Task 6: supervisor 接线

**Files:**
- Modify: `internal/supervisor/run.go`

- [ ] **Step 1: 实现接线**

在 `internal/supervisor/run.go` 第 124-147 行区间(fake-IP 池 + DNS + Dialer 构造处)接线。

`import` 块补 `"github.com/getbx/bx/internal/splitdns"`(`bxdns`、`route`、`dialer` 已在)。

在 `dnsSrv := bxdns.NewServer(pool, 1)` 之后、Dialer 构造之前,插入:
```go
	// split-DNS:匹配域名转发到内网 DNS 解析,真实 IP 注册进 splitDirect 强制直连。
	splitDirect := splitdns.NewSet()
	if len(cfg.DNS.Split) > 0 {
		var routes []bxdns.SplitRoute
		for _, r := range cfg.DNS.Split {
			routes = append(routes, bxdns.SplitRoute{
				Match:  route.NewDomainSet(r.Domains),
				Server: r.Server,
			})
		}
		dnsSrv.SetSplit(routes, bxdns.NewUDPForwarder(plat.DirectDialer()), splitDirect)
		log.Printf("split-DNS 已启用:%d 条规则", len(routes))
	}
```

> 注:`direct := plat.DirectDialer()` 当前在第 133 行(Dialer 构造段)。上面 forwarder 用 `plat.DirectDialer()` 单独取一个即可(每次调用返回新 `*net.Dialer`,无共享状态问题)。

在 `d := &dialer.Dialer{...}` 字面量里加一行字段:
```go
		SplitDirect: splitDirect,
```

- [ ] **Step 2: 构建 + 全量验证**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: 全绿。

Run: `GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...`
Expected: darwin 交叉编译通过(supervisor 接线无平台分支)。

- [ ] **Step 3: 提交**

```bash
git add internal/supervisor/run.go
git commit -m "feat(supervisor): 接线 split-DNS(从 config 建路由+共享 splitDirect 注入 dns/dialer)"
```

---

## 验证清单(真机,非本计划范围)

纯逻辑测试覆盖命令/分支构造,**不证明真机行为**。落地后需在目标机手测:

- [ ] `*.shanghai-electric.com` 解析返回内网真实 IP(`dig @127.0.0.1 app.shanghai-electric.com`,经 bx)。
- [ ] 该连接实际走 eno1(`ip route get <真实IP>` / 抓包),不进隧道。
- [ ] 隧道挂时(killswitch)内网域名仍通(Direct 豁免)。
- [ ] 内网 DNS 不可达时返回 SERVFAIL,不静默走代理。
- [ ] 公网域名零回归(仍 fake-IP + 隧道分流)。
