# bx 多服务器 + 用户手动切换 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让用户配置多台服务器并手动切换,切换全程不拆路由、不破 fail-closed、绝不成环。

**Architecture:** 不造新机制。`liveMutator` + `mutationEngine`(commit-confirmed 死手)在生产里已挂着,`transportSwapper.swapTo`(建新→等健康→原子换→停旧)一直被 `runFailover` 用着,每个传输的 server host 也早已进 `serverBypass` + 静态 DNS。本计划只做四件事:给配置加服务器清单、给 UDP 槽补一个**配对原子切换**的控制面端点、把启动时捕获的 `serverBypass` 改成可更新、以及 CLI/Guardian/菜单三个入口。

**Tech Stack:** Go 1.26、`gopkg.in/yaml.v3`(yaml.Node 外科手术)、Swift 5(菜单栏)、unix socket + 极简 HTTP 控制面。

## Global Constraints

- **TDD**:先写失败测试 → 跑红 → 最小实现 → 跑绿 → 提交。每个任务多次提交。
- **提交信息**:中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`。直接在 `master` 提交。
- **验证命令**:`go build ./... && go vet ./... && go test ./... -count=1`;跨平台 `GOOS=linux/darwin/windows GOARCH=amd64/arm64 go build -o /dev/null ./...`;`gofumpt -l`(排除 `internal/embedded/assets/` 与 `internal/winfw/`)。触及 Swift 的任务另跑 `bash scripts/test-macos-menu.sh` 与 `(cd apps/macos/BxMenu && swift build)`。
- **服务器名字校验**:非空、无空白、只允许 `[A-Za-z0-9._-]`、长度 ≤ 32。违反即报错,不静默修正。
- **名字比较大小写不敏感**;**存储保留用户给的原样**。
- **匹配只按名字,不按主机**。同一主机上的第二台必须显式 `--name`。
- **`servers:` 与 `transports:` 互斥**:同时出现即**加载期报错拒绝启动**。
- **切换顺序:先热切成功,再写 `current`**。反过来会留下「config 说 A、实际跑 B」的静默背离。
- **绝不在 bypass 未落实的情况下切到新服务器**(会成环:隧道自己的流量被劫进 TUN)。
- **菜单不得 spawn 子进程**:新增的一切都走 Guardian 端点,受既有反 spawn 守卫链约束。
- **改配置一律 yaml.Node 外科手术**,禁止「读进结构体 → 改 → 整份写回」(会冲掉用户手写的 rules 与注释)。
- 命令组:**客户端占 `bx server`**,管理员(VPS 侧)那组移到 `bx vps`,`bx server install` 等旧名保留为 `Hidden: true` 别名。

---

## 文件结构

| 文件 | 职责 | 任务 |
|---|---|---|
| `internal/tunnel/serverhost.go`(新) | 从任意传输链接取 server host。**从 `supervisor/brooklink.go` 原样搬来并导出**,使 config/setup/cli 也能用(`internal/tunnel` 零 getbx 依赖,无环) | 1 |
| `internal/config/servers.go`(新) | `Server` 类型、名字校验、清单解析与 `current` 解析。与 `config.go` 分开,因为它是一整块自成体系的规则 | 2 |
| `internal/config/config.go` | `Config` 加 `Servers`/`Current`;`Parse` 调用清单解析并归一到既有 `Server`/`UDP.Transport`/`Transports` | 2 |
| `internal/supervisor/run.go` | bypass/staticA 来源从 `cfg.Transports` 扩到 `cfg.Servers` 全部链接;`liveMutator` 用可变 bypass | 3, 4 |
| `internal/supervisor/mutator.go` | `mutator` 接口加 `SetServer(link, udp string)`;`liveMutator` 的 bypass 改可更新 | 4 |
| `internal/supervisor/control.go` | 新端点 `POST /v0/server`;切换前刷新 bypass 并按需 rehijack | 5, 6 |
| `internal/supervisor/control_client.go` | `SetServerControl(sockPath, link, udp string)` | 5 |
| `internal/setup/servers.go`(新) | 清单的 yaml.Node 外科手术:加入/就地更新/从 `server:` 迁移/改 `current` | 7 |
| `internal/guardian/localapi.go` | `Status.Servers`;`POST /v1/servers/current` | 8 |
| `internal/cli/servercmd.go`(新) | `bx server list/use/rm/rename` | 9 |
| `internal/cli/cli.go` | 命令注册:`bx vps` + 隐藏别名;`bx setup --name` | 9 |
| `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift` | 解 `servers` | 10 |
| `apps/macos/BxMenu/Sources/BxMenu/ServerMenu.swift`(新) | 纯函数:清单 → 菜单行模型(可被 Swift 测试套件编译,不放 main.swift) | 10 |
| `apps/macos/BxMenu/Sources/BxMenu/main.swift` | `Servers ▸` 子菜单接线 | 10 |

---

# A 段:config + core + CLI

## Task 1: 把 server host 解析提到 `internal/tunnel`

**Files:**
- Create: `internal/tunnel/serverhost.go`
- Create: `internal/tunnel/serverhost_test.go`
- Modify: `internal/supervisor/brooklink.go`(删掉搬走的两个函数,改为调用 `tunnel.ServerHost`)

**Interfaces:**
- Produces: `func tunnel.ServerHost(link string) (string, error)` —— 认 `vless://`/`hysteria2://`/`hy2://`/`trojan://`(url host)、`ss://`(`SSHost`)、`vmess://`(`VmessHost`)、`brook://`(query 里的 endpoint)、以及裸 `host:port`。

- [ ] **Step 1: 写失败测试**

`internal/tunnel/serverhost_test.go`:

```go
package tunnel

import "testing"

func TestServerHostAcrossSchemes(t *testing.T) {
	for _, tc := range []struct{ name, link, want string }{
		{"vless", "vless://uuid@195.133.192.92:443?sni=www.cloudflare.com", "195.133.192.92"},
		{"hysteria2", "hysteria2://pw@195.133.192.92:443?obfs=salamander", "195.133.192.92"},
		{"hy2 别名", "hy2://pw@example.com:8443", "example.com"},
		{"trojan", "trojan://pw@example.com:443", "example.com"},
		{"裸 endpoint", "1.2.3.4:9999", "1.2.3.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ServerHost(tc.link)
			if err != nil {
				t.Fatalf("ServerHost(%q) 报错: %v", tc.link, err)
			}
			if got != tc.want {
				t.Fatalf("ServerHost(%q) = %q, 想要 %q", tc.link, got, tc.want)
			}
		})
	}
}

func TestServerHostRejectsLinkWithoutHost(t *testing.T) {
	if _, err := ServerHost("vless://uuid@:443"); err == nil {
		t.Fatal("缺 host 的链接必须报错 —— 它会让 bypass 少一条,而少一条就是成环")
	}
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/tunnel -run TestServerHost -count=1`
Expected: FAIL,`undefined: ServerHost`

- [ ] **Step 3: 搬迁实现**

把 `internal/supervisor/brooklink.go` 里的 `serverHostFromLink` 与 `hostFromEndpoint` **整段剪切**到 `internal/tunnel/serverhost.go`,改名为导出的 `ServerHost` 与包内的 `hostFromEndpoint`,包声明改 `package tunnel`,删掉 `tunnel.` 前缀(`tunnel.SSHost` → `SSHost`,`tunnel.VmessHost` → `VmessHost`)。**逻辑一行不改**——这是搬迁,不是重写。

在 `brooklink.go` 留下:

```go
// serverHostFromLink 已搬进 internal/tunnel(config/setup/cli 也要用它,而
// internal/tunnel 零 getbx 依赖、config 早已 import 它,搬过去无环)。
// 这里留一层薄别名,是为了不改本包内十几个调用点。
func serverHostFromLink(server string) (string, error) { return tunnel.ServerHost(server) }
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/tunnel ./internal/supervisor -count=1`
Expected: PASS(supervisor 既有测试必须**一个都不改**就通过 —— 那是「纯搬迁」的证据)

- [ ] **Step 5: 提交**

```bash
git add internal/tunnel/serverhost.go internal/tunnel/serverhost_test.go internal/supervisor/brooklink.go
git commit -m "refactor(tunnel): server host 解析提到 internal/tunnel,供 config/setup 复用"
```

---

## Task 2: 配置模型 `servers:` + `current:`

**Files:**
- Create: `internal/config/servers.go`
- Create: `internal/config/servers_test.go`
- Modify: `internal/config/config.go`(`Config` 加两个字段;`Parse` 里接上)

**Interfaces:**
- Consumes: `tunnel.ServerHost`(Task 1)
- Produces:
  - `type Server struct { Name, Link, UDP string }`(yaml 键 `name`/`link`/`udp`)
  - `Config.Servers []Server`(yaml `servers`)、`Config.Current string`(yaml `current`)
  - `func ValidateServerName(name string) error`
  - `func DeriveServerName(link string) (string, error)` —— 取 `tunnel.ServerHost` 并按名字规则校验
  - **不变量**:`Parse` 返回后,`c.Server`/`c.UDP.Transport` 一定是 `current` 那台的(已解 `bx://` 壳),`c.Transports` 一定是 `[]string{c.Server}`(单元素,故 `runFailover` 不会启动);`c.Servers` 里每一项的 `Link`/`UDP` 也已解壳。

- [ ] **Step 1: 写失败测试**

`internal/config/servers_test.go`:

```go
package config

import "strings"
import "testing"

func parseOrFail(t *testing.T, y string) *Config {
	t.Helper()
	c, err := Parse([]byte(y))
	if err != nil {
		t.Fatalf("Parse 报错: %v", err)
	}
	return c
}

func TestServersResolveCurrent(t *testing.T) {
	c := parseOrFail(t, `
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
    udp: hysteria2://p@1.1.1.1:443
  - name: tokyo
    link: vless://u@2.2.2.2:443
current: tokyo
`)
	if c.Server != "vless://u@2.2.2.2:443" {
		t.Fatalf("Server 必须是 current 那台, got %q", c.Server)
	}
	if c.UDP.Transport != "" {
		t.Fatalf("tokyo 没配 udp,UDP 槽必须为空(回落到主传输), got %q", c.UDP.Transport)
	}
	// runFailover 靠 len(Transports)>1 启动。清单绝不能喂给它 —— 那正是用户
	// 明确拒绝的「连着的时候自动切」。
	if len(c.Transports) != 1 {
		t.Fatalf("Transports 必须只有当前那一条(否则会启动自动容灾), got %v", c.Transports)
	}
}

func TestServersDefaultCurrentIsFirst(t *testing.T) {
	c := parseOrFail(t, `
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
`)
	if c.Current != "hk" || c.Server != "vless://u@1.1.1.1:443" {
		t.Fatalf("没写 current 时应取第一台, got current=%q server=%q", c.Current, c.Server)
	}
}

func TestServersCurrentMustExist(t *testing.T) {
	_, err := Parse([]byte(`
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
current: nope
`))
	if err == nil || !strings.Contains(err.Error(), "current") {
		t.Fatalf("current 指向不存在的服务器必须报错, got %v", err)
	}
}

func TestServersAndTransportsAreMutuallyExclusive(t *testing.T) {
	_, err := Parse([]byte(`
transports:
  - vless://u@1.1.1.1:443
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
`))
	if err == nil || !strings.Contains(err.Error(), "transports") {
		t.Fatalf("servers 与 transports 必须互斥 —— 两者都是「用哪条传输」的主人, got %v", err)
	}
}

func TestServersRejectDuplicateNamesCaseInsensitively(t *testing.T) {
	_, err := Parse([]byte(`
servers:
  - name: hk
    link: vless://u@1.1.1.1:443
  - name: HK
    link: vless://u@2.2.2.2:443
`))
	if err == nil {
		t.Fatal("重名(大小写不敏感)必须报错:否则 `bx server use hk` 指向哪台是掷骰子")
	}
}

func TestLegacySingleServerStillWorks(t *testing.T) {
	c := parseOrFail(t, `
server: vless://u@1.1.1.1:443
udp:
    transport: hysteria2://p@1.1.1.1:443
`)
	if c.Server != "vless://u@1.1.1.1:443" || c.UDP.Transport != "hysteria2://p@1.1.1.1:443" {
		t.Fatalf("旧配置必须一字不改照常工作, got %q / %q", c.Server, c.UDP.Transport)
	}
	if len(c.Servers) != 0 {
		t.Fatalf("旧配置不该凭空长出 servers 清单, got %v", c.Servers)
	}
}

func TestValidateServerName(t *testing.T) {
	for _, bad := range []string{"", "  ", "a b", "东京", strings.Repeat("x", 33), "a/b"} {
		if err := ValidateServerName(bad); err == nil {
			t.Fatalf("非法名字 %q 必须被拒", bad)
		}
	}
	for _, ok := range []string{"hk", "tokyo-2", "195.133.192.92", "a_b.c-1"} {
		if err := ValidateServerName(ok); err != nil {
			t.Fatalf("合法名字 %q 被拒: %v", ok, err)
		}
	}
}

func TestDeriveServerNameFromLink(t *testing.T) {
	got, err := DeriveServerName("vless://u@195.133.192.92:443")
	if err != nil {
		t.Fatal(err)
	}
	if got != "195.133.192.92" {
		t.Fatalf("没给 --name 时应取主机名, got %q", got)
	}
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/config -count=1`
Expected: FAIL,`unknown field "servers"`(`KnownFields(true)`)与 `undefined: ValidateServerName`

- [ ] **Step 3: 实现**

`internal/config/servers.go`:

```go
package config

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/getbx/bx/internal/tunnel"
)

// Server 是清单里的一台服务器:一对链接(主传输 + 可选 UDP 专用传输)。
//
// 为什么是一对而不是一条:reality 走 TCP、hysteria2 走 UDP,两条链接指向同一台
// 主机是本项目的常规部署(按类分流)。把它们拆成两个独立概念,用户就得手工保证
// 两边同步 —— 而「切了 TCP 忘了切 UDP」是个不报错、还能用、但出口不是你以为那台
// 的状态。
type Server struct {
	Name string `yaml:"name"`
	Link string `yaml:"link"`
	UDP  string `yaml:"udp"`
}

const maxServerNameLen = 32

var serverNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateServerName 校验服务器名字。非法即报错,不静默修正 —— 一个被悄悄改过的
// 名字会让 `bx server use <你写的那个>` 找不到,而用户看不出为什么。
func ValidateServerName(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("服务器名字不能为空")
	}
	if len(name) > maxServerNameLen {
		return fmt.Errorf("服务器名字过长(%d > %d): %q", len(name), maxServerNameLen, name)
	}
	if !serverNamePattern.MatchString(name) {
		return fmt.Errorf("服务器名字只允许字母数字与 . _ -,got %q", name)
	}
	return nil
}

// DeriveServerName 在用户没给 --name 时,从主链接取主机名当名字。
func DeriveServerName(link string) (string, error) {
	host, err := tunnel.ServerHost(link)
	if err != nil {
		return "", fmt.Errorf("从链接推导服务器名字: %w", err)
	}
	if err := ValidateServerName(host); err != nil {
		return "", err
	}
	return host, nil
}

// normalizeServerName 是比较用的形式(大小写不敏感);存储始终保留用户给的原样。
func normalizeServerName(name string) string { return strings.ToLower(strings.TrimSpace(name)) }

// resolveServers 校验清单、解链接壳、定位 current,并把结果归一到 c.Server /
// c.UDP.Transport / c.Transports —— 下游(bypass、dialer、status)一行都不用改。
//
// c.Transports 刻意只放**当前那一条**:run.go 用 len(cfg.Transports)>1 决定要不要
// 启动 runFailover,而自动容灾正是用户明确要求退场的东西。
func (c *Config) resolveServers() error {
	if len(c.Servers) == 0 {
		return nil
	}
	if len(c.Transports) > 0 {
		return fmt.Errorf("config: servers 与 transports 不能同时配置 —— " +
			"transports 是自动容灾优先级表(不健康就自动切),servers 是用户手选的清单," +
			"两者都是「用哪条传输」的主人;请删掉 transports")
	}
	seen := map[string]int{}
	for i := range c.Servers {
		s := &c.Servers[i]
		if err := ValidateServerName(s.Name); err != nil {
			return fmt.Errorf("config: servers[%d]: %w", i, err)
		}
		key := normalizeServerName(s.Name)
		if prev, dup := seen[key]; dup {
			return fmt.Errorf("config: servers[%d].name %q 与 servers[%d] 重名(名字比较不区分大小写)", i, s.Name, prev)
		}
		seen[key] = i
		if strings.TrimSpace(s.Link) == "" {
			return fmt.Errorf("config: servers[%d].link 不能为空", i)
		}
		decoded, err := decodeServerLink(s.Link)
		if err != nil {
			return fmt.Errorf("config: servers[%d].link: %w", i, err)
		}
		s.Link = decoded
		if strings.TrimSpace(s.UDP) != "" {
			decodedUDP, err := decodeServerLink(s.UDP)
			if err != nil {
				return fmt.Errorf("config: servers[%d].udp: %w", i, err)
			}
			s.UDP = decodedUDP
		} else {
			s.UDP = ""
		}
	}
	if strings.TrimSpace(c.Current) == "" {
		c.Current = c.Servers[0].Name
	}
	idx, ok := seen[normalizeServerName(c.Current)]
	if !ok {
		return fmt.Errorf("config: current %q 不在 servers 清单里", c.Current)
	}
	cur := c.Servers[idx]
	c.Current = cur.Name // 存回清单里的原样拼写
	c.Server = cur.Link
	c.Transports = []string{cur.Link}
	c.UDP.Transport = cur.UDP
	return nil
}

// CurrentServer 返回当前选中的那台;没有清单时返回 false。
func (c *Config) CurrentServer() (Server, bool) {
	for _, s := range c.Servers {
		if normalizeServerName(s.Name) == normalizeServerName(c.Current) {
			return s, true
		}
	}
	return Server{}, false
}
```

在 `internal/config/config.go` 的 `Config` 结构体里,紧挨 `Transports` 之后加:

```go
	// Servers 是用户手选的服务器清单(与 Transports 互斥)。一项 = 一对链接。
	Servers []Server `yaml:"servers"`
	// Current 是当前选中的服务器名字(意图,与 desired: on/off 同类)。
	Current string `yaml:"current"`
```

在 `Parse` 里,**紧接在 `owner_uid` 校验之后、传输解析之前**插入:

```go
	if err := c.resolveServers(); err != nil {
		return nil, err
	}
```

(`resolveServers` 在有清单时已经把 `c.Server`/`c.Transports` 填好,故其后既有的
「优先 transports,否则单 server」分支会走 transports 那一支、对已解壳的链接再解一次壳 ——
`decodeServerLink` 对非 `bx://`/`blink://` 的链接原样返回,幂等,无副作用。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/config -count=1 && go test ./... -count=1`
Expected: PASS,且既有 config 测试一个不改

- [ ] **Step 5: 变异验证**

把 `resolveServers` 里的 `c.Transports = []string{cur.Link}` 改成
`c.Transports = links(所有 servers)`,确认 `TestServersResolveCurrent` 转红。这条是
「清单绝不喂给自动容灾」的唯一守卫,必须证明它真在守。改回。

- [ ] **Step 6: 提交**

```bash
git add internal/config/servers.go internal/config/servers_test.go internal/config/config.go
git commit -m "feat(config): servers 清单 + current 选中项,与 transports 互斥"
```

---

## Task 3: bypass 与静态 DNS 覆盖清单里每一台

**Files:**
- Modify: `internal/supervisor/run.go:298-311`(`for _, link := range cfg.Transports` 那一段)
- Create: `internal/supervisor/serverbypass.go`(把「该给哪些链接做 bypass」抽成纯函数)
- Create: `internal/supervisor/serverbypass_test.go`

**Interfaces:**
- Consumes: `config.Server`(Task 2)
- Produces: `func bypassLinks(cfg *config.Config) []string` —— 返回**所有**需要进 `serverBypass` + 静态 DNS 的传输链接(去重前)。

- [ ] **Step 1: 写失败测试**

`internal/supervisor/serverbypass_test.go`:

```go
package supervisor

import (
	"testing"

	"github.com/getbx/bx/internal/config"
)

// 这是本设计最危险的失败模式的唯一守卫。少一条链接 = 那台服务器的 IP 不在 bypass 里
// = 切过去之后隧道自己的流量被劫进 TUN = 成环。而成环是**静默的**:连得上、
// status 显绿,流量却在绕圈或泄漏。
func TestBypassLinksCoversEveryServerBothLinks(t *testing.T) {
	cfg := &config.Config{
		Servers: []config.Server{
			{Name: "hk", Link: "vless://u@1.1.1.1:443", UDP: "hysteria2://p@1.1.1.1:443"},
			{Name: "tokyo", Link: "vless://u@2.2.2.2:443", UDP: "hysteria2://p@3.3.3.3:443"},
			{Name: "sg", Link: "vless://u@4.4.4.4:443"}, // 没有 udp
		},
		Current: "hk",
	}
	got := map[string]bool{}
	for _, l := range bypassLinks(cfg) {
		got[l] = true
	}
	for _, want := range []string{
		"vless://u@1.1.1.1:443", "hysteria2://p@1.1.1.1:443",
		"vless://u@2.2.2.2:443", "hysteria2://p@3.3.3.3:443",
		"vless://u@4.4.4.4:443",
	} {
		if !got[want] {
			t.Errorf("bypass 漏了 %s —— 切到那台就会成环(静默:连得上、status 显绿、流量绕圈)", want)
		}
	}
}

func TestBypassLinksFallsBackToTransportsAndUDPWhenNoServerList(t *testing.T) {
	cfg := &config.Config{
		Transports: []string{"vless://u@1.1.1.1:443", "brook://x@2.2.2.2:9999"},
	}
	cfg.UDP.Transport = "hysteria2://p@3.3.3.3:443"
	got := map[string]bool{}
	for _, l := range bypassLinks(cfg) {
		got[l] = true
	}
	for _, want := range []string{"vless://u@1.1.1.1:443", "brook://x@2.2.2.2:9999", "hysteria2://p@3.3.3.3:443"} {
		if !got[want] {
			t.Errorf("旧配置路径漏了 %s", want)
		}
	}
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/supervisor -run TestBypassLinks -count=1`
Expected: FAIL,`undefined: bypassLinks`

- [ ] **Step 3: 实现**

`internal/supervisor/serverbypass.go`:

```go
package supervisor

import "github.com/getbx/bx/internal/config"

// bypassLinks 列出所有必须进 serverBypass + 静态 DNS 的传输链接。
//
// 有服务器清单时是**每一台的两条链接**(不只当前那台):用户随时可能切过去,而
// 切换走的是热切路径、不重装路由。启动时一次把全部铺好,切换就一条路由都不用动。
//
// 没有清单时退回旧语义:transports(主 + 容灾备选)+ udp.transport。
func bypassLinks(cfg *config.Config) []string {
	var links []string
	if len(cfg.Servers) > 0 {
		for _, s := range cfg.Servers {
			links = append(links, s.Link)
			if s.UDP != "" {
				links = append(links, s.UDP)
			}
		}
		return links
	}
	links = append(links, cfg.Transports...)
	if cfg.UDP.Transport != "" {
		links = append(links, cfg.UDP.Transport)
	}
	return links
}
```

在 `run.go` 把这一段:

```go
	for _, link := range cfg.Transports { // 含主传输(transports[0]=cfg.Server)+ 容灾备选
		if err := addServer(link); err != nil {
			return err
		}
	}
	udpEnabled := cfg.UDP.Transport != "" && cfg.UDP.Mode == "proxy"
	if udpEnabled {
		if err := addServer(cfg.UDP.Transport); err != nil {
			return err
		}
	}
```

换成:

```go
	// 服务器清单在场时这里是**每一台的两条链接**,不只当前那台 —— 见 bypassLinks 的注释。
	for _, link := range bypassLinks(cfg) {
		if err := addServer(link); err != nil {
			return err
		}
	}
	udpEnabled := cfg.UDP.Transport != "" && cfg.UDP.Mode == "proxy"
```

(`addServer` 内部已按 host 去重,重复链接无害。`udpEnabled` 仍按当前那台算,它决定
的是要不要**挂载** UDP 专用传输,与 bypass 覆盖面无关。)

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/supervisor -count=1`
Expected: PASS

- [ ] **Step 5: 变异验证**

把 `bypassLinks` 里 `if s.UDP != ""` 那两行删掉,确认
`TestBypassLinksCoversEveryServerBothLinks` 转红并指名漏掉的是哪条 UDP 链接。改回。

- [ ] **Step 6: 提交**

```bash
git add internal/supervisor/serverbypass.go internal/supervisor/serverbypass_test.go internal/supervisor/run.go
git commit -m "feat(supervisor): bypass 与静态 DNS 覆盖清单里每一台服务器的两条链接"
```

---

## Task 4: `mutator.SetServer` 配对原子切换 + 可更新的 bypass

**Files:**
- Modify: `internal/supervisor/mutator.go`
- Modify: `internal/supervisor/run.go`(`liveMutator` 构造处加 `udpSwap`)
- Create: `internal/supervisor/mutator_server_test.go`

**Interfaces:**
- Consumes: `linkSwapper`(已有:`currentLink() string` / `swapTo(link string) error`)
- Produces:
  - `mutator` 接口新增 `SetServer(link, udp string) (apply func() error, undo func() error, err error)`
  - `liveMutator` 新增字段 `udpSwap linkSwapper`(可为 nil = 该配置没有 UDP 专用槽)
  - `liveMutator` 新增方法 `SetServerBypass(cidrs []string)`,`Rehijack()` 的 apply 读**调用时**的值

- [ ] **Step 1: 写失败测试**

`internal/supervisor/mutator_server_test.go`:

```go
package supervisor

import (
	"errors"
	"testing"
)

type fakeSwapper struct {
	link    string
	failOn  string // swapTo 到这个链接时报错
	history []string
}

func (f *fakeSwapper) currentLink() string { return f.link }
func (f *fakeSwapper) swapTo(link string) error {
	f.history = append(f.history, link)
	if link == f.failOn {
		return errors.New("建不起来")
	}
	f.link = link
	return nil
}

func TestSetServerSwapsBothSlots(t *testing.T) {
	main := &fakeSwapper{link: "vless://hk"}
	udp := &fakeSwapper{link: "hysteria2://hk"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, _, err := m.SetServer("vless://tokyo", "hysteria2://tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if main.link != "vless://tokyo" || udp.link != "hysteria2://tokyo" {
		t.Fatalf("两个槽都要换, got main=%q udp=%q", main.link, udp.link)
	}
}

// 半切状态是个**合法但没人要**的配置:bx 本来就支持 TCP/UDP 走不同主机,所以
// 它不报错、还能用、status 也显绿 —— 只是出口不是用户以为的那台。
func TestSetServerRollsBackMainWhenUDPFails(t *testing.T) {
	main := &fakeSwapper{link: "vless://hk"}
	udp := &fakeSwapper{link: "hysteria2://hk", failOn: "hysteria2://tokyo"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, _, err := m.SetServer("vless://tokyo", "hysteria2://tokyo")
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err == nil {
		t.Fatal("UDP 换失败时整个 apply 必须报错")
	}
	if main.link != "vless://hk" {
		t.Fatalf("UDP 失败必须把主传输也换回去, got %q", main.link)
	}
	if udp.link != "hysteria2://hk" {
		t.Fatalf("UDP 槽必须留在原处, got %q", udp.link)
	}
}

func TestSetServerClearsUDPSlotWhenTargetHasNone(t *testing.T) {
	main := &fakeSwapper{link: "vless://hk"}
	udp := &fakeSwapper{link: "hysteria2://hk"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, _, err := m.SetServer("vless://tokyo", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	// 目标没有 UDP 专用传输 ⇒ UDP 回落到主传输,而不是留着上一台的。
	// 留着上一台 = UDP 流量还从 hk 出去,而用户以为自己已经在 tokyo。
	if udp.link != "vless://tokyo" {
		t.Fatalf("目标没 udp 时 UDP 槽必须跟随主传输, got %q", udp.link)
	}
}

func TestSetServerUndoRestoresBothSlots(t *testing.T) {
	main := &fakeSwapper{link: "vless://hk"}
	udp := &fakeSwapper{link: "hysteria2://hk"}
	m := &liveMutator{swap: main, udpSwap: udp}

	apply, undo, _ := m.SetServer("vless://tokyo", "hysteria2://tokyo")
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if main.link != "vless://hk" || udp.link != "hysteria2://hk" {
		t.Fatalf("undo 必须把两个槽都还原, got main=%q udp=%q", main.link, udp.link)
	}
}

// serverBypass 在旧代码里是启动时捕获的定值。加**新**服务器时它不含新 IP,
// 而 Rehijack 正是为了把新 IP 装进去才被调用的 —— 读到陈旧值就等于没装,
// 切过去立刻成环。
func TestRehijackReadsBypassAtApplyTime(t *testing.T) {
	fp := &fakePlatform{}
	m := &liveMutator{plat: fp, serverBypass: []string{"1.1.1.1/32"}}
	apply, _, err := m.Rehijack()
	if err != nil {
		t.Fatal(err)
	}
	m.SetServerBypass([]string{"1.1.1.1/32", "2.2.2.2/32"})
	if err := apply(); err != nil {
		t.Fatal(err)
	}
	if len(fp.lastServerBypass) != 2 {
		t.Fatalf("Rehijack 必须用调用时的 bypass 集合(否则新服务器的 IP 装不进去,切过去成环), got %v", fp.lastServerBypass)
	}
}
```

`internal/supervisor/mutator_test.go` 里既有的 `fakePlatform` 需要记录最后一次的
bypass,加一个字段并在 `RehijackRoutes` 里赋值:

```go
	f.lastServerBypass = serverBypass
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/supervisor -run 'TestSetServer|TestRehijackReadsBypass' -count=1`
Expected: FAIL,`m.SetServer undefined` / `m.SetServerBypass undefined`

- [ ] **Step 3: 实现**

`mutator.go` 的接口加一行:

```go
	SetServer(link, udp string) (apply func() error, undo func() error, err error)
```

`nopMutator` 跟上:

```go
func (nopMutator) SetServer(string, string) (func() error, func() error, error) { return nop, nop, nil }
```

`liveMutator` 加字段与方法:

```go
type liveMutator struct {
	plat         rehijacker
	swap         linkSwapper
	udpSwap      linkSwapper // nil = 该配置没有 UDP 专用槽
	tunH         tunHandle
	mu           sync.Mutex
	serverBypass []string
	userBypass   []string
	routes       *routeReadiness
}

// SetServerBypass 更新 bypass 集合。加**新**服务器时新 IP 不在启动时算好的集合里,
// 必须先更新再 Rehijack —— 顺序反了就等于没装,而没装就成环。
func (m *liveMutator) SetServerBypass(cidrs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.serverBypass = append([]string(nil), cidrs...)
}

func (m *liveMutator) currentServerBypass() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.serverBypass...)
}

// SetServer 换一台服务器 = 换一对链接。两个槽必须一起换成功或一起留在原地:
// 半切状态(TCP 在 tokyo、UDP 还在 hk)是个合法配置,不报错、还能用,
// 但出口不是用户以为的那台。
//
// udp 为空表示目标服务器没有 UDP 专用传输 —— 此时 UDP 槽跟随主传输,
// 而不是留着上一台的(留着 = UDP 流量仍从上一台出去)。
func (m *liveMutator) SetServer(link, udp string) (apply, undo func() error, err error) {
	oldMain := m.swap.currentLink()
	var oldUDP string
	if m.udpSwap != nil {
		oldUDP = m.udpSwap.currentLink()
	}
	targetUDP := udp
	if targetUDP == "" {
		targetUDP = link
	}
	apply = func() error {
		if err := m.swap.swapTo(link); err != nil {
			return fmt.Errorf("换主传输: %w", err)
		}
		if m.udpSwap == nil {
			return nil
		}
		if err := m.udpSwap.swapTo(targetUDP); err != nil {
			// 主传输已经换过去了。留着就是半切状态,故立刻换回。
			if rerr := m.swap.swapTo(oldMain); rerr != nil {
				return fmt.Errorf("换 UDP 传输失败(%w),回退主传输也失败(%v)——两个槽可能不一致", err, rerr)
			}
			return fmt.Errorf("换 UDP 传输: %w(主传输已换回 %s)", err, transportLabel(oldMain))
		}
		return nil
	}
	undo = func() error {
		var errs []error
		if m.swap.currentLink() != oldMain {
			if err := m.swap.swapTo(oldMain); err != nil {
				errs = append(errs, err)
			}
		}
		if m.udpSwap != nil && m.udpSwap.currentLink() != oldUDP && oldUDP != "" {
			if err := m.udpSwap.swapTo(oldUDP); err != nil {
				errs = append(errs, err)
			}
		}
		return errors.Join(errs...)
	}
	return apply, undo, nil
}
```

`Rehijack` 的 apply 改为读调用时的值:

```go
	apply = func() error {
		m.setRoutesInstalled(false)
		if err := m.plat.RehijackRoutes(m.tunH, m.currentServerBypass(), m.userBypass); err != nil {
			return err
		}
		m.setRoutesInstalled(true)
		return nil
	}
```

`run.go` 的 `liveMutator` 构造处加 `udpSwap: udpSwapper`(`udpSwapper` 为 nil 时字段
自然为 nil;注意 `udpSwapper` 是 `*transportSwapper`,赋给接口字段时 nil 指针会变成
非 nil 接口 —— 必须显式判断):

```go
	mut := &liveMutator{
		plat:         plat,
		swap:         swapper,
		tunH:         tunH,
		serverBypass: serverBypass,
		userBypass:   cfg.Bypass,
		routes:       routes,
	}
	// 显式判断:*transportSwapper 的 nil 指针直接赋给 linkSwapper 接口会得到一个
	// **非 nil 的接口**(typed nil),后面 m.udpSwap != nil 就成了永真,调用即 panic。
	if udpSwapper != nil {
		mut.udpSwap = udpSwapper
	}
```

`mutator_test.go` 里既有的 fake mutator(若有)也要补上 `SetServer`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/supervisor -count=1 && go vet ./...`
Expected: PASS

- [ ] **Step 5: 变异验证**

① 把 `apply` 里 UDP 失败分支的 `m.swap.swapTo(oldMain)` 删掉 → `TestSetServerRollsBackMainWhenUDPFails` 必须转红。
② 把 `targetUDP == "" → link` 改成 `targetUDP = oldUDP` → `TestSetServerClearsUDPSlotWhenTargetHasNone` 必须转红。
③ 把 `m.currentServerBypass()` 改回 `m.serverBypass` → `TestRehijackReadsBypassAtApplyTime` 必须转红。
三处都改回。

- [ ] **Step 6: 提交**

```bash
git add internal/supervisor/mutator.go internal/supervisor/mutator_server_test.go internal/supervisor/mutator_test.go internal/supervisor/run.go
git commit -m "feat(supervisor): SetServer 配对原子切换,bypass 改为调用时读取"
```

---

## Task 5: Core 控制面 `POST /v0/server`

**Files:**
- Modify: `internal/supervisor/control.go`
- Modify: `internal/supervisor/control_client.go`
- Modify: `internal/supervisor/control_test.go`(既有测试文件)

**Interfaces:**
- Consumes: `mutator.SetServer`(Task 4)
- Produces: `func SetServerControl(sockPath, link, udp string) (string, error)` —— 返回 arm 后的 state 字符串(与 `SetTransportControl` 同形)

- [ ] **Step 1: 写失败测试**

在 `internal/supervisor/control_test.go` 追加(照既有 `handleSetTransport` 的测试写法起 server):

```go
func TestHandleSetServerArmsPairedSwap(t *testing.T) {
	rec := &recordingMutator{}
	cs := newTestControlServer(t, rec)
	body := strings.NewReader(`{"link":"vless://tokyo","udp":"hysteria2://tokyo"}`)
	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server", body))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if rec.serverLink != "vless://tokyo" || rec.serverUDP != "hysteria2://tokyo" {
		t.Fatalf("两条链接都要传到 mutator, got %q / %q", rec.serverLink, rec.serverUDP)
	}
}

func TestHandleSetServerRejectsMissingLink(t *testing.T) {
	cs := newTestControlServer(t, &recordingMutator{})
	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server", strings.NewReader(`{"udp":"hysteria2://tokyo"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("缺 link 必须 400, got %d", w.Code)
	}
}

func TestSetServerRouteIsRegistered(t *testing.T) {
	// 端点没注册时 mux 返回 404 text/plain,在客户端表现为解析错误而不是
	// 「切换失败」—— 用户看到的是一句读不懂的话,而不是「这台连不上」。
	mux := newTestControlMux(t)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v0/server", strings.NewReader(`{"link":"vless://tokyo"}`)))
	if w.Code == http.StatusNotFound {
		t.Fatal("/v0/server 没注册进 mux")
	}
}
```

若 `control_test.go` 里没有 `newTestControlServer`/`newTestControlMux`/`recordingMutator`
这些 helper,就照该文件既有测试的构造方式新建,并让 `recordingMutator` 实现完整的
`mutator` 接口(`SetTransport`/`SetServer`/`Rehijack`/`Reconnect`),`SetServer` 记录
两个参数并返回 `nop, nop, nil`。

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/supervisor -run 'SetServer' -count=1`
Expected: FAIL,`cs.handleSetServer undefined`

- [ ] **Step 3: 实现**

`control.go`:

```go
type setServerReq struct {
	Link string `json:"link"`
	UDP  string `json:"udp"`
}

// handleSetServer 换一台服务器 = 一次把主传输与 UDP 专用传输一起换过去。
//
// 为什么不是对 /v0/transport 调两次:两次调用之间没有共同的 Arm/undo,做不到原子。
// 半切状态(TCP 在新那台、UDP 还在旧那台)是个合法配置 —— 不报错、还能用、
// status 也显绿,只是出口不是用户以为的那台。
//
// 与 /v0/transport 一样是 commit-confirmed:arm 后须在窗口内 /v0/commit,
// 否则死手到点自动 revert(undo + 路由快照网)。
func (cs *controlServer) handleSetServer(w http.ResponseWriter, r *http.Request) {
	if !cs.requireOwnerOrRoot(w, r) {
		return
	}
	var req setServerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Link == "" {
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: "缺 link"})
		return
	}
	cs.mu.Lock()
	if cs.eng.State() == confirm.StateArmed {
		state := stateName(cs.eng.State())
		cs.mu.Unlock()
		writeJSON(w, http.StatusConflict, controlResponse{Status: "error", Error: "已有待确认的改动", State: state})
		return
	}
	apply, undo, merr := cs.mut.SetServer(req.Link, req.UDP)
	if merr != nil {
		cs.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: merr.Error()})
		return
	}
	armErr := cs.eng.Arm(apply, undo)
	state := stateName(cs.eng.State())
	cs.mu.Unlock()
	respondArm(w, armErr, state)
}
```

注册:在 `mux.HandleFunc("/v0/transport", …)` 那一行后面加

```go
	mux.HandleFunc("/v0/server", cs.handleSetServer)
```

`control_client.go`:

```go
// SetServerControl 请 Core 把主传输与 UDP 传输一起换到目标服务器(commit-confirmed:
// 成功后调用方须 CommitControl,否则死手到点自动 revert)。
func SetServerControl(sockPath, link, udp string) (string, error) {
	return postControlBody(sockPath, "/v0/server", map[string]string{"link": link, "udp": udp})
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/supervisor -count=1`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/control.go internal/supervisor/control_client.go internal/supervisor/control_test.go
git commit -m "feat(supervisor): /v0/server 一次原子换掉主传输与 UDP 传输"
```

---

## Task 6: 切换前刷新 bypass 并按需 rehijack

**这是全计划风险最高的一个任务。** 成环是静默的:连得上、`status` 显绿,而流量在
绕圈或直接泄漏。这里的顺序错了就是那个后果。

**Files:**
- Modify: `internal/supervisor/serverbypass.go`(把启动时那段解析抽成可复用函数)
- Modify: `internal/supervisor/run.go`(启动改调同一个函数;构造 `refreshServerBypass` 闭包)
- Modify: `internal/supervisor/control.go`(`handleSetServer` 组合 rehijack + 配对切换)
- Create: `internal/supervisor/compose.go` + `compose_test.go`
- Modify: `internal/supervisor/serverbypass_test.go`、`control_test.go`

**Interfaces:**
- Consumes: `bypassLinks`(Task 3)、`liveMutator.SetServerBypass`/`Rehijack`/`SetServer`(Task 4)、`SetServerControl`(Task 5)
- Produces:
  - `func resolveServerBypass(cfg *config.Config) (staticA map[string][]netip.Addr, addrs []netip.Addr, err error)` —— **启动与刷新走同一条路径**
  - `func composeMutations(a1, u1, a2, u2 func() error) (apply, undo func() error)`
  - `controlServer` 新增字段 `refreshBypass func() (changed bool, err error)`(可为 nil = 该部署不支持刷新,此时 `/v0/server` 只做配对切换)

- [ ] **Step 1: 写失败测试 —— 组合器**

`internal/supervisor/compose_test.go`:

```go
package supervisor

import (
	"errors"
	"testing"
)

func TestComposeMutationsRollsBackFirstWhenSecondFails(t *testing.T) {
	var log []string
	a1 := func() error { log = append(log, "a1"); return nil }
	u1 := func() error { log = append(log, "u1"); return nil }
	a2 := func() error { log = append(log, "a2"); return errors.New("boom") }
	u2 := func() error { log = append(log, "u2"); return nil }

	apply, _ := composeMutations(a1, u1, a2, u2)
	if err := apply(); err == nil {
		t.Fatal("第二步失败时整体必须失败")
	}
	want := []string{"a1", "a2", "u1"}
	if len(log) != len(want) {
		t.Fatalf("要回滚已执行的第一步, got %v", log)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("顺序不对, got %v want %v", log, want)
		}
	}
}

func TestComposeMutationsUndoRunsInReverse(t *testing.T) {
	var log []string
	nopf := func() error { return nil }
	u1 := func() error { log = append(log, "u1"); return nil }
	u2 := func() error { log = append(log, "u2"); return nil }
	_, undo := composeMutations(nopf, u1, nopf, u2)
	if err := undo(); err != nil {
		t.Fatal(err)
	}
	if len(log) != 2 || log[0] != "u2" || log[1] != "u1" {
		t.Fatalf("undo 必须逆序, got %v", log)
	}
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/supervisor -run TestComposeMutations -count=1`
Expected: FAIL,`undefined: composeMutations`

- [ ] **Step 3: 实现组合器**

`internal/supervisor/compose.go`:

```go
package supervisor

import (
	"errors"
	"fmt"
)

// composeMutations 把两对 (apply, undo) 串成一对,供 mutationEngine.Arm 使用。
//
// apply 按序执行,第二步失败即回滚第一步(不留半截 —— 半截状态在这里意味着
// 「路由已经改了但传输没换」或反过来,两种都不是任何人要的);
// undo 逆序执行,并把两边的错误都带出来。
func composeMutations(a1, u1, a2, u2 func() error) (apply, undo func() error) {
	apply = func() error {
		if err := a1(); err != nil {
			return err
		}
		if err := a2(); err != nil {
			if rerr := u1(); rerr != nil {
				return fmt.Errorf("%w(回滚前一步也失败: %v)", err, rerr)
			}
			return err
		}
		return nil
	}
	undo = func() error {
		var errs []error
		if err := u2(); err != nil {
			errs = append(errs, err)
		}
		if err := u1(); err != nil {
			errs = append(errs, err)
		}
		return errors.Join(errs...)
	}
	return apply, undo
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/supervisor -run TestComposeMutations -count=1`
Expected: PASS

- [ ] **Step 5: 写失败测试 —— 顺序与条件**

在 `control_test.go` 追加。**这两条是本任务存在的理由**:

```go
// 先换传输再装路由 = 在新服务器的 bypass 还没落实的那一小段时间里,
// 隧道自己的流量被劫进 TUN —— 成环。而成环是静默的:连得上、status 显绿。
func TestSetServerRehijacksBeforeSwappingWhenBypassChanged(t *testing.T) {
	var order []string
	rec := &recordingMutator{
		onRehijack:  func() { order = append(order, "rehijack") },
		onSetServer: func() { order = append(order, "swap") },
	}
	cs := newTestControlServer(t, rec)
	cs.refreshBypass = func() (bool, error) { return true, nil } // 集合变了

	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://new"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	if len(order) != 2 || order[0] != "rehijack" || order[1] != "swap" {
		t.Fatalf("必须先 rehijack 再换传输, got %v", order)
	}
}

func TestSetServerSkipsRehijackWhenBypassUnchanged(t *testing.T) {
	var order []string
	rec := &recordingMutator{
		onRehijack:  func() { order = append(order, "rehijack") },
		onSetServer: func() { order = append(order, "swap") },
	}
	cs := newTestControlServer(t, rec)
	cs.refreshBypass = func() (bool, error) { return false, nil } // 已知服务器之间切换

	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://known"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	// 已知服务器的 bypass 启动时就铺好了。此时重装路由是纯粹的风险:
	// 它会重探网关、拆装真实路由,而这次切换根本不需要动路由。
	if len(order) != 1 || order[0] != "swap" {
		t.Fatalf("集合没变时不该重装路由, got %v", order)
	}
}

func TestSetServerRefusesWhenBypassRefreshFails(t *testing.T) {
	cs := newTestControlServer(t, &recordingMutator{})
	cs.refreshBypass = func() (bool, error) { return false, errors.New("解析不了新服务器的 IP") }

	w := httptest.NewRecorder()
	cs.handleSetServer(w, httptest.NewRequest(http.MethodPost, "/v0/server",
		strings.NewReader(`{"link":"vless://new"}`)))
	if w.Code == http.StatusOK {
		t.Fatal("bypass 刷新失败必须拒绝切换 —— 落实不了就绝不切过去,这是成环与不成环的分界")
	}
}
```

`recordingMutator` 加两个可选回调字段 `onRehijack`/`onSetServer`,在对应方法的
**apply 闭包里**调用(不是方法体里 —— `mutator` 的契约是方法本身无副作用)。

- [ ] **Step 6: 跑它,确认失败**

Run: `go test ./internal/supervisor -run 'TestSetServer(Rehijacks|Skips|Refuses)' -count=1`
Expected: FAIL,`cs.refreshBypass undefined`

- [ ] **Step 7: 实现**

`serverbypass.go` 把 run.go 里那段 `staticA`/`addServer` 循环抽出来(**run.go 启动时
改调它**,这样刷新与启动是同一条代码路径,不会走漂):

```go
// resolveServerBypass 解析清单里全部服务器的 host → IP,产出静态 DNS 表与地址集合。
// 启动与「切换前刷新」共用它:两条路径分头实现,迟早有一条会漏掉某类链接,
// 而漏掉的那一条就是成环。
func resolveServerBypass(cfg *config.Config) (map[string][]netip.Addr, []netip.Addr, error) {
	staticA := map[string][]netip.Addr{}
	var addrs []netip.Addr
	for _, link := range bypassLinks(cfg) {
		h, err := tunnel.ServerHost(link)
		if err != nil {
			return nil, nil, fmt.Errorf("取传输服务器: %w", err)
		}
		if _, ok := staticA[h]; ok {
			continue
		}
		a := hostToAddrs(h)
		if len(a) == 0 {
			return nil, nil, fmt.Errorf("无法解析传输服务器 %q 为 IP(bypass 必需,否则成环)", h)
		}
		staticA[h] = a
		addrs = append(addrs, a...)
	}
	if len(addrs) == 0 {
		return nil, nil, fmt.Errorf("无法解析任何传输服务器 IP(bypass 必需)")
	}
	return staticA, addrs, nil
}
```

`run.go` 在构造完 `mut` 之后建闭包,并把它传给控制面:

```go
	// refreshServerBypass 重读配置、重算 bypass 集合并写进 mutator。
	// 加**新**服务器后切过去时必须先调它:新服务器的 IP 不在启动时算好的集合里,
	// 而没在集合里就意味着隧道自己连服务器的流量会被劫进 TUN(成环)。
	refreshServerBypass := func() (bool, error) {
		if opts.ConfigPath == "" {
			return false, fmt.Errorf("未知配置路径,无法刷新 bypass")
		}
		raw, err := os.ReadFile(opts.ConfigPath)
		if err != nil {
			return false, fmt.Errorf("重读配置: %w", err)
		}
		fresh, err := config.Parse(raw)
		if err != nil {
			return false, fmt.Errorf("重读配置: %w", err)
		}
		_, addrs, err := resolveServerBypass(fresh)
		if err != nil {
			return false, err
		}
		next := mergeBypassCIDRs(addrsToCIDRs(addrs), tailscaleBootstrapBypassCIDRs(ctx, direct))
		changed := !equalStringSets(mut.currentServerBypass(), next)
		if changed {
			mut.SetServerBypass(next)
		}
		return changed, nil
	}
```

(`equalStringSets` 是个八行的集合比较,写在 `serverbypass.go` 并单测:顺序无关、
长度不同即不等。)

把 `refreshServerBypass` 经 `serveControlWithPathRecovery` 传进 `controlServer`,
存为 `refreshBypass` 字段。

`handleSetServer` 改为(替换 Task 5 里那版的 `apply, undo, merr := cs.mut.SetServer(...)` 一段):

```go
	changed := false
	if cs.refreshBypass != nil {
		var rerr error
		changed, rerr = cs.refreshBypass()
		if rerr != nil {
			cs.mu.Unlock()
			// 落实不了新服务器的 bypass 就绝不切过去。切过去 = 隧道自己的流量
			// 被劫进 TUN = 成环,而成环是静默的(连得上、status 显绿、流量绕圈)。
			writeJSON(w, http.StatusInternalServerError,
				controlResponse{Status: "error", Error: "刷新 bypass 失败,已拒绝切换: " + rerr.Error()})
			return
		}
	}
	swapApply, swapUndo, merr := cs.mut.SetServer(req.Link, req.UDP)
	if merr != nil {
		cs.mu.Unlock()
		writeJSON(w, http.StatusBadRequest, controlResponse{Status: "error", Error: merr.Error()})
		return
	}
	apply, undo := swapApply, swapUndo
	if changed {
		// 顺序是硬要求:先把新服务器的 bypass 路由装上,再换传输。
		// 反过来会在两步之间留下一个成环窗口。
		rhApply, rhUndo, rerr := cs.mut.Rehijack()
		if rerr != nil {
			cs.mu.Unlock()
			writeJSON(w, http.StatusInternalServerError, controlResponse{Status: "error", Error: rerr.Error()})
			return
		}
		apply, undo = composeMutations(rhApply, rhUndo, swapApply, swapUndo)
	}
	armErr := cs.eng.Arm(apply, undo)
```

- [ ] **Step 8: 跑测试确认通过**

Run: `go test ./internal/supervisor -count=1 && go build ./... && go vet ./...`
Expected: PASS,且 run.go 既有的启动路径测试**一个都不用改**

- [ ] **Step 9: 变异验证**

① 把 `composeMutations(rhApply, rhUndo, swapApply, swapUndo)` 的两对参数对调(变成先换传输再装路由)→ `TestSetServerRehijacksBeforeSwappingWhenBypassChanged` 必须转红。
② 把 `if changed` 改成 `if true` → `TestSetServerSkipsRehijackWhenBypassUnchanged` 必须转红。
③ 把刷新失败那一支改成「只记日志、继续切」→ `TestSetServerRefusesWhenBypassRefreshFails` 必须转红。
三处都改回。

- [ ] **Step 10: 提交**

```bash
git add internal/supervisor/
git commit -m "feat(supervisor): 切到新服务器前先刷新 bypass 并 rehijack,顺序反了就成环"
```

---

## Task 7: 配置写入 —— 加入 / 就地更新 / 迁移 / 改 current

**Files:**
- Create: `internal/setup/servers.go`
- Create: `internal/setup/servers_test.go`

**Interfaces:**
- Consumes: `config.ValidateServerName`、`config.DeriveServerName`(Task 2)
- Produces:
  - `func UpsertServer(path, name, link, udp string) (added bool, err error)` —— 名字已存在则就地更新那一项,否则追加;两种情况都把 `current` 设成它。首次调用若配置是旧式 `server:`/`udp.transport:`,先把旧的迁成 `servers[0]`。
  - `func SetCurrentServer(path, name string) error`
  - `func RemoveServer(path, name string) error`
  - `func RenameServer(path, oldName, newName string) error`
  - `func ListServers(path string) ([]config.Server, string, error)` —— 返回清单与 current

**全部实现必须用 `yaml.Node` 外科手术**(照 `internal/setup/update.go` 的 `UpdateTransports`),禁止整份重写。

- [ ] **Step 1: 写失败测试**

`internal/setup/servers_test.go`:

```go
package setup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCfg(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// 2026-08-06 真机事故:换服务器的那条路整份重写配置,把用户手写的
// apple/steam 直连策略连同注释一起冲掉了。current 每次切换都要改,这条比以往更要紧。
func TestUpsertServerPreservesHandWrittenConfig(t *testing.T) {
	p := writeCfg(t, `# 我手写的注释
server: vless://u@1.1.1.1:443
killswitch: true
rules:
    - direct:
        - '*.icloud.com'   # 苹果推送必须直连
`)
	if _, err := UpsertServer(p, "tokyo", "vless://u@2.2.2.2:443", "hysteria2://p@2.2.2.2:443"); err != nil {
		t.Fatal(err)
	}
	out := read(t, p)
	for _, want := range []string{"# 我手写的注释", "'*.icloud.com'", "# 苹果推送必须直连", "killswitch: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("外科手术必须保住 %q,实际:\n%s", want, out)
		}
	}
}

func TestUpsertServerMigratesLegacySingleServer(t *testing.T) {
	p := writeCfg(t, `
server: vless://u@1.1.1.1:443
udp:
    transport: hysteria2://p@1.1.1.1:443
`)
	added, err := UpsertServer(p, "tokyo", "vless://u@2.2.2.2:443", "")
	if err != nil {
		t.Fatal(err)
	}
	if !added {
		t.Fatal("tokyo 是新的,应报 added=true")
	}
	servers, current, err := ListServers(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(servers) != 2 {
		t.Fatalf("旧的那台必须被迁进清单而不是丢掉, got %v", servers)
	}
	if servers[0].Name != "1.1.1.1" || servers[0].UDP != "hysteria2://p@1.1.1.1:443" {
		t.Fatalf("迁移必须带上旧的 udp.transport, got %+v", servers[0])
	}
	if current != "tokyo" {
		t.Fatalf("加入后应切过去, got %q", current)
	}
	out := read(t, p)
	if strings.Contains(out, "\nserver:") {
		t.Errorf("迁移后顶层 server: 必须删掉(与 servers 并存会有两个主人):\n%s", out)
	}
}

func TestUpsertServerUpdatesInPlaceOnSameName(t *testing.T) {
	p := writeCfg(t, `
servers:
    - name: tokyo
      link: vless://u@2.2.2.2:443
current: tokyo
`)
	added, err := UpsertServer(p, "tokyo", "vless://u@9.9.9.9:443", "")
	if err != nil {
		t.Fatal(err)
	}
	if added {
		t.Fatal("同名应就地更新,不新增")
	}
	servers, _, _ := ListServers(p)
	if len(servers) != 1 || servers[0].Link != "vless://u@9.9.9.9:443" {
		t.Fatalf("就地更新失败, got %v", servers)
	}
}

func TestRemoveServerRefusesCurrentAndLast(t *testing.T) {
	p := writeCfg(t, `
servers:
    - name: hk
      link: vless://u@1.1.1.1:443
    - name: tokyo
      link: vless://u@2.2.2.2:443
current: tokyo
`)
	if err := RemoveServer(p, "tokyo"); err == nil {
		t.Fatal("删当前选中的那台必须报错(先 use 别的),否则 current 会指向不存在的名字、下次启动直接起不来")
	}
	if err := RemoveServer(p, "hk"); err != nil {
		t.Fatal(err)
	}
	if err := RemoveServer(p, "tokyo"); err == nil {
		t.Fatal("删到一台不剩必须报错")
	}
}

func TestRenameServerFollowsCurrent(t *testing.T) {
	p := writeCfg(t, `
servers:
    - name: tokyo
      link: vless://u@2.2.2.2:443
current: tokyo
`)
	if err := RenameServer(p, "tokyo", "jp"); err != nil {
		t.Fatal(err)
	}
	servers, current, _ := ListServers(p)
	if servers[0].Name != "jp" || current != "jp" {
		t.Fatalf("改名后 current 必须跟着改, got name=%q current=%q", servers[0].Name, current)
	}
}

func TestSetCurrentServerRejectsUnknownName(t *testing.T) {
	p := writeCfg(t, `
servers:
    - name: hk
      link: vless://u@1.1.1.1:443
current: hk
`)
	if err := SetCurrentServer(p, "nope"); err == nil {
		t.Fatal("切到不存在的名字必须报错,而不是写进去让下次启动失败")
	}
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/setup -count=1`
Expected: FAIL,`undefined: UpsertServer`

- [ ] **Step 3: 实现**

`internal/setup/servers.go`。复用 `update.go` 里已有的 `documentRoot`、`mappingValue`、
`scalarValue`、`setScalar`、`removeKey`、`appendKey` 这些 helper。新增两个:

```go
// serverSeq 返回 servers 序列节点;没有则返回 nil。
func serverSeq(root *yaml.Node) *yaml.Node

// serverEntry 在序列里按名字(大小写不敏感)找一项;找不到返回 nil。
func serverEntry(seq *yaml.Node, name string) *yaml.Node
```

`UpsertServer` 的流程:

1. 读文件、`yaml.Unmarshal` 进 `yaml.Node`、取 `documentRoot`。
2. **迁移**:若没有 `servers` 键而有 `server` 键,新建 `servers` 序列,把旧的
   `server:` 与 `udp.transport:` 组成第一项(名字用 `config.DeriveServerName(旧链接)`),
   然后 `removeKey(root, "server")`,并把 `udp` 映射里的 `transport` 键删掉
   (`udp` 映射为空时整个 `udp` 键也删掉)。
3. `config.ValidateServerName(name)`;`serverEntry` 找同名项:
   - 找到 → `setScalar` 改它的 `link`/`udp`(udp 为空则 `removeKey`),`added=false`
   - 没找到 → 追加一项,`added=true`
4. `setScalar(root, "current", name)`。
5. `yaml.Marshal` 回写(`0o600`,先写临时文件再 `os.Rename`,避免半截文件)。

`SetCurrentServer`/`RemoveServer`/`RenameServer`/`ListServers` 同样在 `yaml.Node` 上操作。
`RemoveServer` 先判「是不是 current」与「是不是最后一台」,两条都报错返回,**不改文件**。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/setup -count=1`
Expected: PASS

- [ ] **Step 5: 交叉验证 —— 写出来的东西 config 要吃得下**

在 `servers_test.go` 加一条,把 `UpsertServer` 之后的文件喂给 `config.Parse`:

```go
func TestUpsertServerOutputParses(t *testing.T) {
	p := writeCfg(t, "server: vless://u@1.1.1.1:443\n")
	if _, err := UpsertServer(p, "tokyo", "vless://u@2.2.2.2:443", "hysteria2://p@2.2.2.2:443"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	c, err := config.Parse(b)
	if err != nil {
		t.Fatalf("写出来的配置 config.Parse 读不了 —— 那是一台起不来的机器: %v\n%s", err, b)
	}
	if c.Server != "vless://u@2.2.2.2:443" {
		t.Fatalf("current 应是新加的那台, got %q", c.Server)
	}
}
```

(`internal/setup` 今天**不** import `internal/config`,本任务新加这条 import;`config` 的依赖只有 `tunnel`/`blink`,不反向依赖 `setup`,故无环。)

- [ ] **Step 6: 提交**

```bash
git add internal/setup/servers.go internal/setup/servers_test.go
git commit -m "feat(setup): 服务器清单的 yaml.Node 外科手术(加入/就地更新/迁移/改 current)"
```

---

## Task 8: Guardian —— status 带清单 + `POST /v1/servers/current`

**Files:**
- Modify: `internal/guardian/types.go`(`Status` 加 `Servers`)
- Modify: `internal/guardian/localapi.go`(新 handler + 路由)
- Modify: `internal/guardian/localapi_test.go`

**Interfaces:**
- Consumes: `setup.SetCurrentServer`/`ListServers`(Task 7)、`supervisor.SetServerControl`/`CommitControl`/`RollbackControl`(Task 5)
- Produces:
  - `type ServerEntry struct { Name string \`json:"name"\`; Host string \`json:"host"\`; Current bool \`json:"current"\` }`
  - `Status.Servers []ServerEntry \`json:"servers"\`` —— **刻意无 `omitempty`**(与 `Capabilities` 同理:键缺席是「旧版 Guardian」的唯一信号)
  - `POST /v1/servers/current`,body `{"name":"tokyo"}`,授权 `authorizeOwnerPeer`

- [ ] **Step 1: 写失败测试**

在 `internal/guardian/localapi_test.go` 追加:

```go
func TestServersCurrentRequiresOwnerPeer(t *testing.T) {
	// 与 /v1/up、/v1/down 同级:它改的是网络出口。
	// 用既有的 non-owner peer 构造方式(照本文件 TestLocalAPIMutationsRequireRootPeer 写)。
}

func TestStatusAlwaysCarriesServersKey(t *testing.T) {
	b, err := json.Marshal(Status{})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte(`"servers"`)) {
		t.Fatal("servers 键必须恒在:菜单靠「键缺席」判断这是不认识清单的旧 Guardian," +
			"加 omitempty 会让新旧两种 Guardian 在线上无法区分")
	}
}

func TestStatusServersTagHasNoOmitempty(t *testing.T) {
	f, ok := reflect.TypeOf(Status{}).FieldByName("Servers")
	if !ok {
		t.Fatal("Status 没有 Servers 字段")
	}
	if strings.Contains(string(f.Tag.Get("json")), "omitempty") {
		t.Fatal("Servers 刻意不带 omitempty —— 空清单与「旧 Guardian 根本不认识这个字段」" +
			"必须可区分。只断言键存在挡不住这个改动(今天清单非空,断言照样绿)")
	}
}
```

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/guardian -run 'Servers' -count=1`
Expected: FAIL,`Status 没有 Servers 字段`

- [ ] **Step 3: 实现**

`types.go`:

```go
// ServerEntry 是发布给菜单的一项服务器。Host 已经以 server 字段暴露在 status 里,
// 不是新泄漏面;链接(含凭据)绝不外发。
type ServerEntry struct {
	Name    string `json:"name"`
	Host    string `json:"host"`
	Current bool   `json:"current"`
}
```

`Status` 加:

```go
	// Servers 恒在(刻意无 omitempty):键缺席是「旧版 Guardian」的唯一信号。
	Servers []ServerEntry `json:"servers"`
```

`localapi.go` 新 handler,**顺序是先热切、成功了再写 current**:

```go
// serversCurrentHandler 切换当前服务器。
//
// 顺序:先请 Core 热切,成功了再把 current 写进配置。反过来(先写意图再执行)
// 会在切换失败时留下「config 说 tokyo、实际跑 hk」的静默背离,而能消化这种背离的
// 调谐循环是阶段③的东西,今天还没有。
//
// Core 不在跑时只改配置并回报「下次启动生效」—— 那不是失败,没有事实可背离。
func (a *LocalAPI) serversCurrentHandler(w http.ResponseWriter, r *http.Request) { … }
```

实现要点:
1. `authorizeOwnerPeer` 鉴权(与 `/v1/up`、`/v1/down` 同一函数)。
2. 解析 `{"name":"…"}`,空名 → 400。
3. `setup.ListServers(configPath)` 找到目标项;不存在 → 400。
4. Core 不在跑(`supervisor.FetchRuntimeState` 拿不到)→ 只 `setup.SetCurrentServer`,
   回 200 并在响应里标明 `applied:false`。
5. Core 在跑 → `supervisor.SetServerControl(sock, target.Link, target.UDP)`:
   - 失败 → 不改配置,回 500 + 失败码 `server_switch_failed`(**完整错误只进 Guardian
     日志**,响应体只带码 —— 与既有四个 handler 同一条规则)。
   - 成功 → `supervisor.CommitControl(sock)` 确认(不 commit 的话死手会在窗口内自动
     revert 掉这次切换);commit 失败 → `supervisor.RollbackControl(sock)` 并回 500。
   - commit 成功 → `setup.SetCurrentServer` 写配置 → 回 200 `applied:true`。
6. 路由注册:`mux.HandleFunc("/v1/servers/current", a.serversCurrentHandler)`。
7. `statusWithVersions`(或 status 组装处)填 `Servers`:遍历 `setup.ListServers` 的结果,
   `Host` 用 `tunnel.ServerHost(s.Link)`,取不到就留空字符串**而不是**报错整个 status 失败。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/guardian -count=1 && go test ./... -count=1`
Expected: PASS

- [ ] **Step 5: 变异验证**

给 `Servers` 加 `,omitempty` → `TestStatusServersTagHasNoOmitempty` 必须转红
(`TestStatusAlwaysCarriesServersKey` 在清单非空时抓不到,这正是要两条断言的原因)。改回。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/
git commit -m "feat(guardian): status 发布服务器清单,/v1/servers/current 切换"
```

---

## Task 9: CLI —— `bx server`(客户端)+ `bx vps`(管理员)

**Files:**
- Create: `internal/cli/servercmd.go`
- Create: `internal/cli/servercmd_test.go`
- Modify: `internal/cli/cli.go`(命令注册、`setup` 加 `--name`)

**Interfaces:**
- Consumes: Task 7 的 `setup.*`、Task 8 的 Guardian 端点
- Produces:
  - `bx server list` / `bx server use <name>` / `bx server rm <name>` / `bx server rename <old> <new>`
  - `bx vps`:承接原 `bx server` 的全部子命令(`install`/`share`/…)
  - `bx server install` 等旧路径以 `Hidden: true` 保留为别名
  - `bx setup <link> [--udp <link>] [--name X]`:调 `setup.UpsertServer`

- [ ] **Step 1: 写失败测试**

`internal/cli/servercmd_test.go`:

```go
package cli

import (
	"strings"
	"testing"
)

// bx server 现在是**客户端**命令组(选哪台连);VPS 侧管理移到 bx vps。
// 一个名词只能有一个意思 —— 否则 `bx server list` 到底列「我配了哪几台」
// 还是「我这台 VPS 上的东西」,只能靠猜。
func TestServerGroupIsClientSideAndVPSGroupExists(t *testing.T) {
	app := newApp()
	server := findCommand(t, app, "server")
	for _, want := range []string{"list", "use", "rm", "rename"} {
		if findSub(server, want) == nil {
			t.Errorf("bx server 缺子命令 %q", want)
		}
	}
	if sub := findSub(server, "install"); sub == nil || !sub.Hidden {
		t.Error("bx server install 应保留为**隐藏**别名(旧文档与肌肉记忆),但不再是主路径")
	}
	vps := findCommand(t, app, "vps")
	for _, want := range []string{"install", "share"} {
		if findSub(vps, want) == nil {
			t.Errorf("bx vps 缺子命令 %q", want)
		}
	}
}

func TestSetupHasNameFlag(t *testing.T) {
	app := newApp()
	setup := findCommand(t, app, "setup")
	var found bool
	for _, f := range setup.Flags {
		for _, n := range f.Names() {
			if n == "name" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("bx setup 需要 --name:同一主机上的第二台服务器只能靠它区分" +
			"(匹配只按名字,不给 --name 会就地覆盖第一台)")
	}
}

func TestRenderServerListMarksCurrentAndDoesNotInventLatency(t *testing.T) {
	out := renderServerList([]serverRow{
		{Name: "hk", Host: "1.1.1.1", Current: false},
		{Name: "tokyo", Host: "2.2.2.2", Current: true, LatencyMS: 452},
	})
	if !strings.Contains(out, "tokyo") || !strings.Contains(out, "452") {
		t.Fatalf("当前那台要显示实测延迟:\n%s", out)
	}
	if strings.Contains(out, "hk") && strings.Contains(out, "0ms") {
		t.Fatalf("没连的那台绝不能显示延迟数字 —— 没测过就说没测过:\n%s", out)
	}
	if !strings.Contains(out, "not connected") {
		t.Fatalf("没连的那台要明说 not connected:\n%s", out)
	}
}
```

`findCommand`/`findSub` 若不存在则在本测试文件里就地实现(遍历 `app.Commands`
按 `Name` 找,找不到 `t.Fatalf`)。

- [ ] **Step 2: 跑它,确认失败**

Run: `go test ./internal/cli -run 'TestServerGroup|TestSetupHasName|TestRenderServerList' -count=1`
Expected: FAIL

- [ ] **Step 3: 实现**

`servercmd.go`:

```go
// serverRow 是 bx server list 的一行。LatencyMS 只对当前那台有意义 ——
// 其余那些没有活隧道,任何数字都是编的。
type serverRow struct {
	Name      string
	Host      string
	Current   bool
	LatencyMS int
}

func renderServerList(rows []serverRow) string { … }
```

`renderServerList` 输出形如:

```
  NAME     HOST              STATUS
* tokyo    2.2.2.2           452ms
  hk       1.1.1.1           not connected
```

`bx server use <name>` 走 Guardian `/v1/servers/current`;Guardian 不可达时退回
`setup.SetCurrentServer` 并打印「已改配置,下次 `sudo bx up` 生效」。

`cli.go` 的命令注册:

```go
			// bx server 现在是**客户端**命令组(配了哪几台、当前用哪台)。
			// VPS 侧管理(install/share/…)移到 bx vps —— 一个名词只能有一个意思。
			{Name: "server", Usage: "管理已配置的服务器(列出/切换)", Subcommands: clientServerCommands()},
			{Name: "vps", Usage: "管理 bx 服务端(在 VPS 上运行)", Subcommands: serverCommands()},
```

`clientServerCommands()` 返回 `list`/`use`/`rm`/`rename`,**再把 `serverCommands()`
的每一项以 `Hidden: true` 追加进来**(旧路径 `bx server install` 继续能跑)。

`setupFlags()` 加:

```go
		&cli.StringFlag{Name: "name", Usage: "给这台服务器起个名字(不给则用主机名)"},
```

`setupAction` 里,写配置那一步改调 `setup.UpsertServer(cfgPath, name, link, udpLink)`;
`name` 为空时用 `config.DeriveServerName(link)`。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/cli -count=1 && go build ./... && go vet ./...`
Expected: PASS

- [ ] **Step 5: README 与帮助文本**

在 `README.md` 里 `bx setup` 一节后面加一小节「多台服务器」,写清四条:
`bx setup` 是加入并切过去、匹配只按名字、同主机第二台要 `--name`、切换会断掉既有连接。

- [ ] **Step 6: 提交**

```bash
git add internal/cli/servercmd.go internal/cli/servercmd_test.go internal/cli/cli.go README.md
git commit -m "feat(cli): bx server 改为客户端服务器管理,VPS 侧移到 bx vps"
```

---

# B 段:菜单

## Task 10: 菜单 `Servers ▸` 子菜单

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/ServerMenu.swift`
- Create: `apps/macos/BxMenu/Tests/ServerMenuTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift`(解 `servers`)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`(加 `.selectServer(name:)` 端点)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(接线)
- Modify: `scripts/test-macos-menu.sh`(注册新套件)
- Modify: `internal/cli/cli_test.go`(守卫)

**Interfaces:**
- Consumes: Task 8 的 `Status.Servers` 与 `POST /v1/servers/current`
- Produces:
  - `struct ServerRow { let name: String; let host: String; let isCurrent: Bool }`
  - `func serverMenuRows(_ servers: [GuardianServer]?) -> [ServerRow]`
  - `GuardianEndpoint.selectServer(name: String)` → `POST /v1/servers/current`

**判定逻辑放 `ServerMenu.swift` 而不是 `main.swift`**:`main.swift` 编不进 Swift 测试
套件,里面的逻辑只能靠 Go 侧文本匹配来守,而这类守卫在阶段①被攻破了八次。

- [ ] **Step 1: 写失败测试**

`apps/macos/BxMenu/Tests/ServerMenuTests.swift`(照既有套件的 `expect`/`fail` 写法):

```swift
// 清单缺席(旧 Guardian)与清单为空是两回事:前者应当整个不显示 Servers 子菜单,
// 后者说明配置坏了。把两者压成一个,用户会看见一个空的、点不动的子菜单。
expect(serverMenuRows(nil).isEmpty, "旧 Guardian 不发 servers 时不该渲染任何行")

let rows = serverMenuRows([
    GuardianServer(name: "hk", host: "1.1.1.1", current: false),
    GuardianServer(name: "tokyo", host: "2.2.2.2", current: true),
])
expect(rows.count == 2, "两台都要列出来")
expect(rows[1].isCurrent, "当前那台要打勾")
expect(rows.filter { $0.isCurrent }.count == 1, "只能有一个当前项")
```

- [ ] **Step 2: 跑它,确认失败**

Run: `bash scripts/test-macos-menu.sh`
Expected: FAIL,`cannot find 'serverMenuRows' in scope`

- [ ] **Step 3: 实现**

`ServerMenu.swift` 写纯函数;`GuardianStatus.swift` 加

```swift
struct GuardianServer: Decodable {
    let name: String
    let host: String
    let current: Bool
}
```

并在 `GuardianStatus` 里加 `let servers: [GuardianServer]?`(**可选** —— 旧 Guardian
不发这个键,可选性把「没发」与「发了空清单」分开)。

`GuardianClient.swift` 的 `GuardianEndpoint` 加一项:

```swift
    case selectServer(name: String)
```

在 `guardianRequest(for:)` 的 switch 里:

```swift
    case .selectServer(let name):
        method = "POST"
        path = "/v1/servers/current"
        body = try? JSONEncoder().encode(["name": name])
```

`main.swift` 的 `rebuildMenu()` 里,在状态行之后插入子菜单(**只在
`rows.isEmpty == false` 时插入**),每项 `state = row.isCurrent ? .on : .off`,
点击调 `guardianClient.selectServer(name:)`,**在后台队列**执行(不阻塞主线程),
失败沿用既有的 toggle 失败提示路径。

- [ ] **Step 4: 跑测试确认通过**

Run: `bash scripts/test-macos-menu.sh && (cd apps/macos/BxMenu && swift build)`
Expected: PASS,收尾横幅 `macOS menu tests passed` 打印

- [ ] **Step 5: 加 Go 侧守卫并变异验证**

在 `internal/cli/cli_test.go`:
- 把 `/v1/servers/current` 加进 `TestMenuGuardianPathsAreServedByTheDaemon` 的覆盖
  (Task 8 已注册该路由,这条会自动生效;确认 `GuardianEndpoint` 的端点计数下限也跟着 +1)。
- 新增一条守卫:`main.swift` 里选服务器的动作**不得**出现在既有 spawn 白名单链上
  ——即它必须走 `guardianClient`,不能 spawn `bx`。断言 `selectServer(` 出现在
  `main.swift` 且其所在函数不在 `runBx` 链的白名单里。

变异:把点击动作改成 `runBx(["server", "use", name])` → `TestMacMenuSpawnsOnlyFromTheActionPath`
必须转红。改回。

- [ ] **Step 6: 提交**

```bash
git add apps/macos/BxMenu internal/cli/cli_test.go scripts/test-macos-menu.sh
git commit -m "feat(menu): Servers 子菜单,切换走 Guardian 端点而非 spawn"
```

---

## 真机验收(用户执行,不由 agent 跑)

A 段完成后先跑 1-3,B 段完成后跑 4:

1. **两台之间来回切**:`bx server use tokyo` → `curl https://api.ipify.org` 出口 == tokyo 的 IP;切回 hk 同理。(**别用 `ifconfig.me`/`ip.sb`**,它们在 china 直连列表里。)
2. **切到已关掉的服务器**:必须**留在原地**并报错,`bx status` 仍显示原来那台、隧道健康。
3. **运行中加第三台**:`sudo bx setup '<第三台>' --name sg` → 出口正确,且 `netstat -rn | grep <第三台IP>` 能看到它的 bypass 路由(**没有这条就是成环**)。
4. **菜单**:`Servers ▸` 列出三台、当前项打勾、点另一台能切过去;切换期间 `sudo fs_usage -w -f exec | grep bx` **不得**出现新的 bx 子进程。
