# bx 地基 + 分流脑 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 搭好 bx 项目骨架与配置解析,并实现完全可单测的"分流脑"(Router + 规则/CIDR/域名匹配 + fake-IP 分配器)。

**Architecture:** 决策(纯逻辑)与 IO 分离。本计划只产出纯逻辑层:给定 `{域名/IP/端口}` 判定 `直连/代理/阻断`,并管理 fake-IP↔域名映射。无 TUN、无网络、无 root,全部表驱动单测。后续计划再接 TUN/brook/DNS。

**Tech Stack:** Go 1.26、urfave/cli/v2(CLI)、gopkg.in/yaml.v3(配置)、标准库 `net/netip`(CIDR/IP)。模块路径 `github.com/getbx/bx`。

---

### Task 1: 项目骨架 + CLI 框架

**Files:**
- Create: `go.mod`
- Create: `main.go`
- Create: `internal/cli/cli.go`

- [ ] **Step 1: 初始化 go.mod**

Run:
```bash
cd /home/nategu/Documents/bx
go mod init github.com/getbx/bx
go get github.com/urfave/cli/v2@latest
```
Expected: `go.mod` 生成,含 urfave/cli/v2 依赖。

- [ ] **Step 2: 写 CLI 骨架** `internal/cli/cli.go`

```go
package cli

import (
	"fmt"

	"github.com/urfave/cli/v2"
)

// New 返回配置好子命令的 bx App。
func New() *cli.App {
	return &cli.App{
		Name:  "bx",
		Usage: "基于 brook 的透明全局代理",
		Commands: []*cli.Command{
			{Name: "up", Usage: "启动全局代理", Action: notImpl("up")},
			{Name: "down", Usage: "停止", Action: notImpl("down")},
			{Name: "status", Usage: "查看状态", Action: notImpl("status")},
			{Name: "reload", Usage: "热重载规则", Action: notImpl("reload")},
			{Name: "install", Usage: "安装 systemd 自启服务", Action: notImpl("install")},
		},
	}
}

func notImpl(name string) cli.ActionFunc {
	return func(*cli.Context) error { return fmt.Errorf("%s: 尚未实现", name) }
}
```

- [ ] **Step 3: 写 main.go**

```go
package main

import (
	"log"
	"os"

	"github.com/getbx/bx/internal/cli"
)

func main() {
	if err := cli.New().Run(os.Args); err != nil {
		log.Fatal(err)
	}
}
```

- [ ] **Step 4: 编译验证**

Run: `cd /home/nategu/Documents/bx && go build -o bx . && ./bx --help`
Expected: 打印 bx 用法,列出 up/down/status/reload/install 子命令。

- [ ] **Step 5: 提交**

```bash
git add -A && git commit -m "feat: bx CLI 骨架"
```

---

### Task 2: 配置解析与校验

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试** `internal/config/config_test.go`

```go
package config

import "testing"

func TestLoadValid(t *testing.T) {
	yaml := []byte(`
server: "brook://abc"
killswitch: true
dns:
  china: 223.5.5.5
  fakeip_cidr: 198.18.0.0/15
rules:
  - direct: ["*.internal.com", "10.0.0.0/8"]
  - proxy: ["*.openai.com"]
lists:
  china_domain: /tmp/china_domain.txt
  china_cidr: /tmp/china_cidr4.txt
`)
	c, err := Parse(yaml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if c.Server != "brook://abc" || !c.Killswitch {
		t.Fatalf("bad scalar fields: %+v", c)
	}
	if c.DNS.China != "223.5.5.5" || c.DNS.FakeipCIDR != "198.18.0.0/15" {
		t.Fatalf("bad dns: %+v", c.DNS)
	}
	if len(c.Rules) != 2 || c.Rules[0].Direct[0] != "*.internal.com" {
		t.Fatalf("bad rules: %+v", c.Rules)
	}
}

func TestParseRejectsEmptyServer(t *testing.T) {
	if _, err := Parse([]byte(`killswitch: true`)); err == nil {
		t.Fatal("expected error for missing server")
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `cd /home/nategu/Documents/bx && go test ./internal/config/`
Expected: FAIL（`Parse` undefined）。

- [ ] **Step 3: 写实现** `internal/config/config.go`

```go
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

type DNS struct {
	China      string `yaml:"china"`
	FakeipCIDR string `yaml:"fakeip_cidr"`
}

type Rule struct {
	Direct []string `yaml:"direct"`
	Proxy  []string `yaml:"proxy"`
}

type Lists struct {
	ChinaDomain string `yaml:"china_domain"`
	ChinaCIDR   string `yaml:"china_cidr"`
}

type Config struct {
	Server     string `yaml:"server"`
	Password   string `yaml:"password"`
	Killswitch bool   `yaml:"killswitch"`
	DNS        DNS    `yaml:"dns"`
	Rules      []Rule `yaml:"rules"`
	Lists      Lists  `yaml:"lists"`
}

// Parse 解析并校验配置字节。
func Parse(b []byte) (*Config, error) {
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
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
	return &c, nil
}
```

Run: `go get gopkg.in/yaml.v3`

- [ ] **Step 4: 运行,确认通过**

Run: `cd /home/nategu/Documents/bx && go test ./internal/config/ -v`
Expected: PASS（两个用例）。

- [ ] **Step 5: 提交**

```bash
git add -A && git commit -m "feat: 配置解析与校验"
```

---

### Task 3: 决策与元数据类型

**Files:**
- Create: `internal/route/types.go`

- [ ] **Step 1: 写类型** `internal/route/types.go`

```go
package route

import "net/netip"

// Decision 是分流判定结果。
type Decision int

const (
	Direct Decision = iota // 直连
	Proxy                  // 走 brook 代理
	Block                  // kill-switch 阻断
)

func (d Decision) String() string {
	switch d {
	case Direct:
		return "direct"
	case Proxy:
		return "proxy"
	case Block:
		return "block"
	default:
		return "unknown"
	}
}

// Meta 描述一条待判定的连接。Domain 可能为空(裸 IP 连接)。
type Meta struct {
	Domain string
	IP     netip.Addr
	Port   uint16
	UDP    bool
}
```

- [ ] **Step 2: 编译验证**

Run: `cd /home/nategu/Documents/bx && go build ./internal/route/`
Expected: 无错误。

- [ ] **Step 3: 提交**

```bash
git add -A && git commit -m "feat: 分流决策与元数据类型"
```

---

### Task 4: CIDR 集合匹配(geoip-cn)

**Files:**
- Create: `internal/route/cidrset.go`
- Test: `internal/route/cidrset_test.go`

- [ ] **Step 1: 写失败测试** `internal/route/cidrset_test.go`

```go
package route

import (
	"net/netip"
	"testing"
)

func TestCIDRSet(t *testing.T) {
	s, err := NewCIDRSet([]string{"1.2.0.0/16", "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]bool{
		"1.2.3.4":   true,
		"1.3.0.1":   false,
		"10.0.1.1": true,
		"8.8.8.8":   false,
	}
	for ip, want := range cases {
		got := s.Contains(netip.MustParseAddr(ip))
		if got != want {
			t.Errorf("Contains(%s)=%v want %v", ip, got, want)
		}
	}
}

func TestCIDRSetSkipsBadLines(t *testing.T) {
	s, err := NewCIDRSet([]string{"", "# comment", "1.2.0.0/16", "garbage"})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Contains(netip.MustParseAddr("1.2.3.4")) {
		t.Fatal("valid CIDR should still match")
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `cd /home/nategu/Documents/bx && go test ./internal/route/ -run CIDRSet`
Expected: FAIL（`NewCIDRSet` undefined）。

- [ ] **Step 3: 写实现** `internal/route/cidrset.go`

```go
package route

import (
	"net/netip"
	"strings"
)

// CIDRSet 是 IP 前缀集合,Contains 判断 IP 是否落入任一前缀。
type CIDRSet struct {
	prefixes []netip.Prefix
}

// NewCIDRSet 从 CIDR 字符串构建;空行、# 注释、非法行自动跳过。
func NewCIDRSet(lines []string) (*CIDRSet, error) {
	s := &CIDRSet{}
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "#") {
			continue
		}
		p, err := netip.ParsePrefix(ln)
		if err != nil {
			continue // 容错:跳过坏行
		}
		s.prefixes = append(s.prefixes, p)
	}
	return s, nil
}

func (s *CIDRSet) Contains(ip netip.Addr) bool {
	for _, p := range s.prefixes {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: 运行,确认通过**

Run: `cd /home/nategu/Documents/bx && go test ./internal/route/ -run CIDRSet -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add -A && git commit -m "feat: CIDR 集合匹配(geoip-cn)"
```

> 注:线性遍历对 6000+ 段足够快(每连接一次)。若后续成瓶颈,再换 `netipx.IPSet`/前缀树,届时单测不变。

---

### Task 5: 域名集合匹配(后缀 + 通配)

**Files:**
- Create: `internal/route/domainset.go`
- Test: `internal/route/domainset_test.go`

- [ ] **Step 1: 写失败测试** `internal/route/domainset_test.go`

```go
package route

import "testing"

func TestDomainSet(t *testing.T) {
	s := NewDomainSet([]string{"openai.com", "*.google.com", "baidu.com"})
	cases := map[string]bool{
		"openai.com":        true, // 精确
		"api.openai.com":    true, // 后缀匹配子域
		"google.com":        true, // *.google.com 也覆盖裸域
		"maps.google.com":   true,
		"notgoogle.com":     false,
		"baidu.com.evil.com": false, // 不能被 baidu.com 误匹配
	}
	for d, want := range cases {
		if got := s.Match(d); got != want {
			t.Errorf("Match(%s)=%v want %v", d, got, want)
		}
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `cd /home/nategu/Documents/bx && go test ./internal/route/ -run DomainSet`
Expected: FAIL（`NewDomainSet` undefined）。

- [ ] **Step 3: 写实现** `internal/route/domainset.go`

```go
package route

import "strings"

// DomainSet 做域名后缀匹配。规则 "a.com" 匹配 a.com 及其子域;
// "*.a.com" 等价处理(去掉前缀 *. 后按后缀匹配,同时覆盖裸域)。
type DomainSet struct {
	suffixes map[string]struct{}
}

func NewDomainSet(patterns []string) *DomainSet {
	s := &DomainSet{suffixes: make(map[string]struct{})}
	for _, p := range patterns {
		p = strings.TrimSpace(strings.ToLower(p))
		p = strings.TrimPrefix(p, "*.")
		if p == "" || strings.HasPrefix(p, "#") {
			continue
		}
		s.suffixes[p] = struct{}{}
	}
	return s
}

// Match 判断域名是否命中:自身或任一父域在集合中。
func (s *DomainSet) Match(domain string) bool {
	domain = strings.ToLower(strings.TrimSuffix(domain, "."))
	for {
		if _, ok := s.suffixes[domain]; ok {
			return true
		}
		i := strings.IndexByte(domain, '.')
		if i < 0 {
			return false
		}
		domain = domain[i+1:]
	}
}
```

- [ ] **Step 4: 运行,确认通过**

Run: `cd /home/nategu/Documents/bx && go test ./internal/route/ -run DomainSet -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add -A && git commit -m "feat: 域名后缀集合匹配"
```

---

### Task 6: Router —— 分流脑核心

**Files:**
- Create: `internal/route/router.go`
- Test: `internal/route/router_test.go`

判定优先级(与 spec 第 5 节一致):
1. 用户显式规则(direct/proxy,域名 DomainSet + 网段 CIDRSet)
2. china_domain 命中 → 直连
3. 有域名但未命中 → 交由调用方"解析后再判 geoip"(Router 返回 `NeedResolve`);Router 自身只对**已知 IP** 判 geoip
4. 裸 IP(无域名)→ geoip-cn:中国直连,否则代理

为保持 Router 纯逻辑(不解析 DNS),把"未命中域名需解析"显式表达为第四种结果 `NeedResolve`,由上层 DNS/Dialer 解析后用 `DecideIP` 二次判定。

- [ ] **Step 1: 写失败测试** `internal/route/router_test.go`

```go
package route

import (
	"net/netip"
	"testing"
)

func newTestRouter() *Router {
	cn, _ := NewCIDRSet([]string{"1.2.0.0/16"}) // 假装 1.2.0.0/16 是中国
	return &Router{
		UserDirect:   NewDomainSet([]string{"*.internal.com"}),
		UserProxy:    NewDomainSet([]string{"*.openai.com"}),
		UserDirectIP: mustSet([]string{"10.0.0.0/8"}),
		ChinaDomain:  NewDomainSet([]string{"baidu.com"}),
		ChinaCIDR:    cn,
	}
}

func mustSet(l []string) *CIDRSet { s, _ := NewCIDRSet(l); return s }

func TestDecide(t *testing.T) {
	r := newTestRouter()
	tests := []struct {
		name string
		meta Meta
		want Decision
	}{
		{"用户强制直连域名", Meta{Domain: "a.internal.com"}, Direct},
		{"用户强制代理域名", Meta{Domain: "api.openai.com"}, Proxy},
		{"china_domain 直连", Meta{Domain: "x.baidu.com"}, Direct},
		{"用户直连网段(裸IP)", Meta{IP: netip.MustParseAddr("10.5.5.5")}, Direct},
		{"中国IP裸连直连", Meta{IP: netip.MustParseAddr("1.2.3.4")}, Direct},
		{"外国IP裸连代理", Meta{IP: netip.MustParseAddr("8.8.8.8")}, Proxy},
	}
	for _, tc := range tests {
		if got := r.Decide(tc.meta); got != tc.want {
			t.Errorf("%s: Decide=%v want %v", tc.name, got, tc.want)
		}
	}
}

func TestDecideNeedResolve(t *testing.T) {
	r := newTestRouter()
	// 未命中任何域名规则,且无 IP → 需解析
	if got := r.Decide(Meta{Domain: "unknown-foreign.com"}); got != NeedResolve {
		t.Fatalf("want NeedResolve got %v", got)
	}
}

func TestDecideIP(t *testing.T) {
	r := newTestRouter()
	if r.DecideIP(netip.MustParseAddr("1.2.3.4")) != Direct {
		t.Fatal("china ip should be direct")
	}
	if r.DecideIP(netip.MustParseAddr("8.8.8.8")) != Proxy {
		t.Fatal("foreign ip should be proxy")
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `cd /home/nategu/Documents/bx && go test ./internal/route/ -run Decide`
Expected: FAIL（`Router`/`NeedResolve` undefined）。

- [ ] **Step 3: 写实现** `internal/route/router.go`

先在 `types.go` 追加 `NeedResolve`：把 `internal/route/types.go` 的 const 块改为：

```go
const (
	Direct      Decision = iota // 直连
	Proxy                       // 走 brook 代理
	Block                       // kill-switch 阻断
	NeedResolve                 // 有域名但未命中规则,需解析后用 DecideIP 再判
)
```

并在 `String()` 的 switch 增加：
```go
	case NeedResolve:
		return "need-resolve"
```

然后写 `internal/route/router.go`：

```go
package route

import "net/netip"

// Router 是纯逻辑分流脑。所有字段由上层从配置构建后注入。
type Router struct {
	UserDirect   *DomainSet // 用户强制直连域名
	UserProxy    *DomainSet // 用户强制代理域名
	UserDirectIP *CIDRSet   // 用户强制直连网段
	UserProxyIP  *CIDRSet   // 用户强制代理网段(可选)
	ChinaDomain  *DomainSet // 国内域名列表
	ChinaCIDR    *CIDRSet   // 国内 IP 段(geoip-cn)
}

// Decide 按优先级判定。返回 NeedResolve 表示有域名但未命中,
// 上层应解析出 IP 后调用 DecideIP。
func (r *Router) Decide(m Meta) Decision {
	if m.Domain != "" {
		switch {
		case r.UserDirect != nil && r.UserDirect.Match(m.Domain):
			return Direct
		case r.UserProxy != nil && r.UserProxy.Match(m.Domain):
			return Proxy
		case r.ChinaDomain != nil && r.ChinaDomain.Match(m.Domain):
			return Direct
		default:
			return NeedResolve
		}
	}
	if m.IP.IsValid() {
		return r.DecideIP(m.IP)
	}
	return Proxy // 信息不足时保守走代理
}

// DecideIP 仅按 IP 判定:用户网段 > geoip-cn > 默认代理。
func (r *Router) DecideIP(ip netip.Addr) Decision {
	if r.UserDirectIP != nil && r.UserDirectIP.Contains(ip) {
		return Direct
	}
	if r.UserProxyIP != nil && r.UserProxyIP.Contains(ip) {
		return Proxy
	}
	if r.ChinaCIDR != nil && r.ChinaCIDR.Contains(ip) {
		return Direct
	}
	return Proxy
}
```

- [ ] **Step 4: 运行,确认通过**

Run: `cd /home/nategu/Documents/bx && go test ./internal/route/ -v`
Expected: 全部 PASS（含 Task 4/5 用例）。

- [ ] **Step 5: 提交**

```bash
git add -A && git commit -m "feat: Router 分流脑核心(优先级判定)"
```

---

### Task 7: Fake-IP 分配器

**Files:**
- Create: `internal/fakeip/pool.go`
- Test: `internal/fakeip/pool_test.go`

- [ ] **Step 1: 写失败测试** `internal/fakeip/pool_test.go`

```go
package fakeip

import (
	"net/netip"
	"testing"
)

func TestPoolAllocAndLookup(t *testing.T) {
	p, err := New("198.18.0.0/24")
	if err != nil {
		t.Fatal(err)
	}
	ip1 := p.Alloc("google.com")
	ip2 := p.Alloc("baidu.com")
	if ip1 == ip2 {
		t.Fatal("different domains must get different IPs")
	}
	if !netip.MustParsePrefix("198.18.0.0/24").Contains(ip1) {
		t.Fatalf("ip %v not in pool range", ip1)
	}
	// 同域名再次分配返回同一 IP
	if p.Alloc("google.com") != ip1 {
		t.Fatal("same domain must be stable")
	}
	// 反查
	if d, ok := p.Domain(ip1); !ok || d != "google.com" {
		t.Fatalf("reverse lookup failed: %q %v", d, ok)
	}
	if _, ok := p.Domain(netip.MustParseAddr("9.9.9.9")); ok {
		t.Fatal("unknown ip should not resolve")
	}
}
```

- [ ] **Step 2: 运行,确认失败**

Run: `cd /home/nategu/Documents/bx && go test ./internal/fakeip/`
Expected: FAIL（`New` undefined）。

- [ ] **Step 3: 写实现** `internal/fakeip/pool.go`

```go
package fakeip

import (
	"fmt"
	"net/netip"
	"sync"
)

// Pool 从一段 CIDR 里给域名分配稳定的 fake IP,并支持反查。
type Pool struct {
	mu       sync.Mutex
	prefix   netip.Prefix
	next     netip.Addr
	d2ip     map[string]netip.Addr
	ip2d     map[netip.Addr]string
}

func New(cidr string) (*Pool, error) {
	pfx, err := netip.ParsePrefix(cidr)
	if err != nil {
		return nil, fmt.Errorf("fakeip cidr: %w", err)
	}
	return &Pool{
		prefix: pfx,
		next:   pfx.Addr().Next(), // 跳过网络地址
		d2ip:   make(map[string]netip.Addr),
		ip2d:   make(map[netip.Addr]string),
	}, nil
}

// Alloc 返回域名对应的 fake IP(已存在则复用)。
func (p *Pool) Alloc(domain string) netip.Addr {
	p.mu.Lock()
	defer p.mu.Unlock()
	if ip, ok := p.d2ip[domain]; ok {
		return ip
	}
	ip := p.next
	p.next = ip.Next()
	if !p.prefix.Contains(p.next) {
		// 用尽则回绕(覆盖最早的映射);MVP 简单处理
		p.next = p.prefix.Addr().Next()
	}
	p.d2ip[domain] = ip
	p.ip2d[ip] = domain
	return ip
}

// Domain 反查 fake IP 对应的域名。
func (p *Pool) Domain(ip netip.Addr) (string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	d, ok := p.ip2d[ip]
	return d, ok
}
```

- [ ] **Step 4: 运行,确认通过**

Run: `cd /home/nategu/Documents/bx && go test ./internal/fakeip/ -v`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add -A && git commit -m "feat: fake-IP 分配器(域名↔IP 双向映射)"
```

---

### Task 8: 全量测试 + 里程碑收尾

- [ ] **Step 1: 跑全部测试**

Run: `cd /home/nategu/Documents/bx && go test ./... && go vet ./...`
Expected: 全部 PASS,vet 无告警。

- [ ] **Step 2: 提交收尾**

```bash
git add -A && git commit -m "test: 分流脑里程碑全测试通过" --allow-empty
```

---

## 本计划完成后的状态

地基(CLI 骨架 + 配置)与"分流脑"(Router + CIDR/域名匹配 + fake-IP 映射)完成且全单测覆盖,
无需 root/网络即可验证整套分流决策逻辑。

**后续独立计划(各自再写)**：
- Plan 2：brook 隧道管理器(子进程 + 健康检查 + 自动重连)
- Plan 3：TUN 引擎(gVisor netstack)+ Dialer 接线,打通全局透明(先全量代理)
- Plan 4：DNS 处理器(fake-IP 应答 + 防污染)接入 Router/Pool,中国直连落地
- Plan 5：断线保护 + kill-switch + 退出还原
- Plan 6：status 面板 + systemd 自启(`bx install`)
