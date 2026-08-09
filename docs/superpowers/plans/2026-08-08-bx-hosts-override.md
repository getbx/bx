# bx hosts 覆盖 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `/etc/bx/config.yaml` 能把某个域名钉到某个 IPv4,由 bx 的 DNS 在 fake-IP 之前直接应答。

**Architecture:** 机制已存在——`internal/dns` 的 `staticA` 是 DNS 处理第一跳(`server.go:119`),今天只从传输服务器域名填充(防环)。本计划只加配置面:config 解析期校验并归一化,supervisor 合并进 `staticA`(**传输服务器优先**),`bx status` 常驻显示生效中的覆盖。

**Tech Stack:** Go 1.26 · `internal/config` · `internal/supervisor` · `internal/stats`

## Global Constraints

- **传输服务器的静态 A 不可被用户覆盖。** 那些条目是防环用的:隧道子进程靠它们解析到服务器真 IP 再走 bypass 路由。用户覆盖了会让隧道**静默**连到错误的地方——status 照样绿。冲突时以传输服务器为准,并把被忽略的条目报出来。这是本功能最危险的失败模式,必须有回归守卫。
- **非法值在 `config.Parse` 就报错拒绝启动**,不在运行期静默丢弃。`staticA` 是 DNS 第一跳且不受 `rules` 影响,一个悄悄没生效的覆盖,用户会以为生效了——正是本功能要消灭的困惑。这也与 `Parse` 既有的 `dec.KnownFields(true)`(「未知字段直接报错,杜绝『配了但静默失效』」)一脉相承。
- **只支持「一个域名 → 一个 IPv4 字面量」。** 不做通配符、不做多值、不做 CNAME。
- **域名归一化**:小写 + 去尾点。`Torchfun.com.` 与 `torchfun.com` 必须是同一条。
- **绝不运行 `bx`、`sudo bx`、`launchctl`、`networksetup`、`route`**,不得启动或安装 bx。**用户机器上 bx 正在运行且在使用中。**
- **绝不运行 `git stash`、`git reset --hard`、`git checkout -- .`**(仓库有一条无关的既有 stash 必须存活)。
- 不得削弱任何既有断言。
- CI lint 用 `gofumpt`;改过的文件跑 `gofumpt -w`。
- 每个任务结束前跑 `go build ./... && go vet ./... && go test ./... -count=1`,全绿才提交。
- 中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`,直接提交 `master`。

## 现有事实(已核对,别再猜)

| 位置 | 内容 |
|---|---|
| `internal/dns/server.go:119` | `staticA[domain]` 命中即返回固定 A,**先于 fake-IP** |
| `internal/dns/server.go:71` | `SetStaticA(records map[string][]netip.Addr, direct *splitdns.Set)` |
| `internal/supervisor/run.go:285` | `staticA := map[string][]netip.Addr{}`,只从传输 server 填 |
| `internal/supervisor/run.go:325` | `dnsSrv.SetStaticA(staticA, splitDirect)` |
| `internal/config/config.go:109` | `Parse` 是唯一校验入口,已有 `KnownFields(true)` |
| `internal/supervisor/control.go:399` | `stats.Report{...}` 构造点(在 `report := func()` 闭包里) |
| `internal/stats/render.go:60` | `bx status` 文本渲染 |

---

### Task 1: config 的 hosts 字段与解析期校验

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/config_test.go`

**Interfaces:**
- Consumes: 无
- Produces:
  - `Config.Hosts map[string]string`(yaml `hosts`)
  - `func (c *Config) HostOverrides() map[string]netip.Addr` —— 归一化后的域名 → IPv4,`Parse` 已保证合法

- [ ] **Step 1: 写失败测试**

在 `internal/config/config_test.go` 末尾追加:

```go
func TestParseHostsOverrides(t *testing.T) {
	cfg, err := Parse([]byte(`
server: brook://example.com:9999
hosts:
  Torchfun.com.: 127.0.0.1
  api.example.com: 192.168.50.10
`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	got := cfg.HostOverrides()
	// 归一化:小写 + 去尾点。Torchfun.com. 与 torchfun.com 必须是同一条,
	// 否则用户按其中一种写法配、DNS 按另一种查,覆盖静默不生效。
	if a, ok := got["torchfun.com"]; !ok || a.String() != "127.0.0.1" {
		t.Fatalf("torchfun.com = %v ok=%v", a, ok)
	}
	if a, ok := got["api.example.com"]; !ok || a.String() != "192.168.50.10" {
		t.Fatalf("api.example.com = %v ok=%v", a, ok)
	}
	if len(got) != 2 {
		t.Fatalf("条目数 = %d, want 2", len(got))
	}
}

// 非法值必须在解析期就拒绝启动。
//
// staticA 是 DNS 第一跳且不受 rules 影响 —— 一个运行期被静默丢弃的覆盖,
// 用户会以为它生效了,而这正是本功能要消灭的那种困惑。
func TestParseRejectsBadHostOverrides(t *testing.T) {
	for _, tt := range []struct{ name, value string }{
		{"域名而非 IP", "another.example.com"},
		{"IPv6", "::1"},
		{"空值", ""},
		{"未指定地址", "0.0.0.0"},
		{"带端口", "127.0.0.1:53"},
		{"CIDR", "127.0.0.0/8"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte("server: brook://example.com:9999\nhosts:\n  bad.example.com: \"" + tt.value + "\"\n"))
			if err == nil {
				t.Fatalf("值 %q 必须在解析期报错", tt.value)
			}
			if !strings.Contains(err.Error(), "bad.example.com") {
				t.Fatalf("错误必须点名是哪条 hosts 出问题,实际 = %v", err)
			}
		})
	}
}

// 空域名同样是配置错误,不能默默跳过。
func TestParseRejectsEmptyHostKey(t *testing.T) {
	if _, err := Parse([]byte("server: brook://example.com:9999\nhosts:\n  \"\": 127.0.0.1\n")); err == nil {
		t.Fatal("空域名必须报错")
	}
}

// 没配 hosts 时不得凭空造出条目。
func TestParseWithoutHostsYieldsNone(t *testing.T) {
	cfg, err := Parse([]byte("server: brook://example.com:9999\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.HostOverrides()) != 0 {
		t.Fatalf("未配 hosts 时应为空,实际 %v", cfg.HostOverrides())
	}
}
```

- [ ] **Step 2: 跑测试确认全红**

Run: `go test ./internal/config -run 'TestParseHosts|TestParseRejects|TestParseWithoutHosts' -count=1`
Expected: 编译失败(`Hosts` / `HostOverrides` 不存在)。

- [ ] **Step 3: 实现**

`internal/config/config.go` 的 `Config` 结构体加字段(放在 `Rules` 之后、`Lists` 之前):

```go
	// Hosts 把域名钉到固定 IPv4,由 bx 自己的 DNS 在 fake-IP 之前直接应答。
	//
	// 存在的理由:bx 开着时,被它接管的应用走系统 DNS(已指向 bx),/etc/hosts
	// 根本不在这条链路上 —— 用户改了却不生效,而且看不出为什么。
	//
	// 只支持「一个域名 → 一个 IPv4 字面量」。通配符/多值/CNAME 的语义需要单独
	// 想清楚,现在加进来只会造成误解。
	Hosts map[string]string `yaml:"hosts"`
```

在 `Parse` 里、`return &c, nil` 之前加校验(位置在 DNS 默认值那一段之后):

```go
	// hosts 覆盖:解析期校验,非法值直接拒绝启动。
	//
	// 不在运行期静默丢弃,理由与上面 KnownFields(true) 相同:staticA 是 DNS
	// 第一跳且不受 rules 影响,一个悄悄没生效的覆盖,用户会以为它生效了。
	normalizedHosts := make(map[string]netip.Addr, len(c.Hosts))
	for name, value := range c.Hosts {
		host := normalizeHostName(name)
		if host == "" {
			return nil, fmt.Errorf("config: hosts 的域名不能为空")
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("config: hosts[%s] 的值 %q 不是合法 IP 字面量(只支持 IPv4)", host, value)
		}
		if !addr.Is4() {
			return nil, fmt.Errorf("config: hosts[%s] 只支持 IPv4,得到 %q", host, value)
		}
		if addr.IsUnspecified() {
			return nil, fmt.Errorf("config: hosts[%s] 不能是 0.0.0.0", host)
		}
		if _, dup := normalizedHosts[host]; dup {
			return nil, fmt.Errorf("config: hosts 里 %s 出现多次(大小写/尾点归一后相同)", host)
		}
		normalizedHosts[host] = addr
	}
	c.hostOverrides = normalizedHosts
```

结构体再加一个非导出字段(不参与 yaml):

```go
	hostOverrides map[string]netip.Addr `yaml:"-"`
```

以及两个辅助:

```go
// normalizeHostName 归一化域名:小写 + 去尾点。
//
// Torchfun.com. 与 torchfun.com 必须是同一条 —— 用户按其中一种写法配、DNS 按
// 另一种查,覆盖就静默不生效,而这类静默失效正是本功能要消灭的东西。
func normalizeHostName(name string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(name)), ".")
}

// HostOverrides 返回归一化并校验过的 hosts 覆盖。Parse 已保证每个值都是合法 IPv4。
func (c *Config) HostOverrides() map[string]netip.Addr {
	return c.hostOverrides
}
```

补 import:`net/netip`(`strings`/`fmt` 已在)。

- [ ] **Step 4: 跑测试**

Run: `go test ./internal/config -count=1`
Expected: 全绿。

- [ ] **Step 5: 变异验证**

把 `normalizeHostName` 改成直接 `return name`,跑
`go test ./internal/config -run TestParseHostsOverrides -count=1`
Expected: FAIL(`Torchfun.com.` 查不到)。确认后改回。

把 `!addr.Is4()` 那条去掉,跑
`go test ./internal/config -run TestParseRejectsBadHostOverrides -count=1`
Expected: FAIL(IPv6 用例)。确认后改回。

- [ ] **Step 6: 提交**

```bash
gofumpt -w internal/config/config.go internal/config/config_test.go
git add internal/config/config.go internal/config/config_test.go
git commit -m "$(cat <<'EOF'
feat(config): hosts 覆盖字段与解析期校验

bx 开着时被它接管的应用走系统 DNS(已指向 bx),/etc/hosts 根本不在这条
链路上 —— 用户改了却不生效,而且看不出为什么。这个字段是唯一能做这件事的
地方。

非法值在解析期就拒绝启动,不在运行期静默丢弃:staticA 是 DNS 第一跳且不受
rules 影响,一个悄悄没生效的覆盖用户会以为它生效了 —— 与 Parse 既有的
KnownFields(true)「杜绝配了但静默失效」是同一条理由。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: 合并进 staticA,传输服务器优先

**Files:**
- Create: `internal/supervisor/hostsmerge.go`
- Create: `internal/supervisor/hostsmerge_test.go`
- Modify: `internal/supervisor/run.go`(`staticA` 构建之后、`SetStaticA` 之前)

**Interfaces:**
- Consumes: `cfg.HostOverrides() map[string]netip.Addr`(Task 1)
- Produces: `func mergeHostOverrides(serverStatic map[string][]netip.Addr, user map[string]netip.Addr) (merged map[string][]netip.Addr, applied map[string]netip.Addr, ignored []string)`

**关键背景:** `serverStatic` 里的条目是**防环**用的——bx 的隧道子进程靠它们解析到服务器真 IP,再由 bypass 路由送出去。用户若覆盖了服务器域名,隧道会连到错误的地方,**而且是静默的**:`bx status` 照样报健康,因为隧道自己那条 socks 健康检查走的是同一条错路。所以冲突时**一律以传输服务器为准**,并把被忽略的域名报出来。

- [ ] **Step 1: 写失败测试**

新建 `internal/supervisor/hostsmerge_test.go`:

```go
package supervisor

import (
	"net/netip"
	"testing"
)

func addrs(ss ...string) []netip.Addr {
	out := make([]netip.Addr, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

func TestMergeHostOverridesAddsUserEntries(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	user := map[string]netip.Addr{"torchfun.com": netip.MustParseAddr("127.0.0.1")}

	merged, applied, ignored := mergeHostOverrides(server, user)

	if got := merged["torchfun.com"]; len(got) != 1 || got[0].String() != "127.0.0.1" {
		t.Fatalf("torchfun.com = %v", got)
	}
	if got := merged["vps.example.com"]; len(got) != 1 || got[0].String() != "203.0.113.20" {
		t.Fatalf("服务器条目不该被动过,实际 %v", got)
	}
	if len(applied) != 1 || applied["torchfun.com"].String() != "127.0.0.1" {
		t.Fatalf("applied = %v", applied)
	}
	if len(ignored) != 0 {
		t.Fatalf("没有冲突时 ignored 应为空,实际 %v", ignored)
	}
}

// 传输服务器的静态 A 不可被用户覆盖。
//
// 那些条目是防环用的:隧道子进程靠它们解析到服务器真 IP 再走 bypass 路由。
// 用户覆盖了会让隧道静默连到错误的地方 —— bx status 照样报健康,因为隧道
// 自己那条 socks 健康检查走的是同一条错路。这是本功能最危险的失败模式。
func TestMergeHostOverridesNeverOverridesTransportServer(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	user := map[string]netip.Addr{"vps.example.com": netip.MustParseAddr("127.0.0.1")}

	merged, applied, ignored := mergeHostOverrides(server, user)

	if got := merged["vps.example.com"]; len(got) != 1 || got[0].String() != "203.0.113.20" {
		t.Fatalf("服务器域名必须保持真 IP,实际 %v —— 隧道会静默连错地方", got)
	}
	if _, ok := applied["vps.example.com"]; ok {
		t.Fatal("被忽略的条目不得出现在 applied 里(否则 status 会谎报它生效了)")
	}
	if len(ignored) != 1 || ignored[0] != "vps.example.com" {
		t.Fatalf("必须报出被忽略的域名,实际 %v", ignored)
	}
}

// 不改传入的 map:调用方还要用 serverStatic 做别的事(bypass 路由)。
func TestMergeHostOverridesDoesNotMutateInputs(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	user := map[string]netip.Addr{"torchfun.com": netip.MustParseAddr("127.0.0.1")}

	mergeHostOverrides(server, user)

	if len(server) != 1 {
		t.Fatalf("serverStatic 被改动了:%v", server)
	}
	if len(user) != 1 {
		t.Fatalf("user 被改动了:%v", user)
	}
}

func TestMergeHostOverridesWithNoUserEntries(t *testing.T) {
	server := map[string][]netip.Addr{"vps.example.com": addrs("203.0.113.20")}
	merged, applied, ignored := mergeHostOverrides(server, nil)
	if len(merged) != 1 || len(applied) != 0 || len(ignored) != 0 {
		t.Fatalf("merged=%v applied=%v ignored=%v", merged, applied, ignored)
	}
}
```

- [ ] **Step 2: 跑测试确认红**

Run: `go test ./internal/supervisor -run TestMergeHostOverrides -count=1`
Expected: 编译失败。

- [ ] **Step 3: 实现**

新建 `internal/supervisor/hostsmerge.go`:

```go
package supervisor

import (
	"net/netip"
	"sort"
)

// mergeHostOverrides 把用户配置的 hosts 覆盖并进传输服务器的静态 A 记录。
//
// **传输服务器一律优先。** serverStatic 里的条目是防环用的:bx 的隧道子进程靠
// 它们解析到服务器真 IP,再由 bypass 路由送出去。用户若把服务器域名映射到别处,
// 隧道会连到错误的地方 —— 而且是静默的,bx status 照样报健康,因为隧道自己那条
// socks 健康检查走的是同一条错路。所以冲突时忽略用户那条,并把它报出来。
//
// 返回三样:合并后的表、真正生效的用户覆盖(供 status 显示,不含被忽略的)、
// 被忽略的域名(已排序,供日志与 status 提示)。
func mergeHostOverrides(
	serverStatic map[string][]netip.Addr,
	user map[string]netip.Addr,
) (merged map[string][]netip.Addr, applied map[string]netip.Addr, ignored []string) {
	merged = make(map[string][]netip.Addr, len(serverStatic)+len(user))
	for host, a := range serverStatic {
		merged[host] = a
	}
	applied = make(map[string]netip.Addr, len(user))
	for host, addr := range user {
		if _, taken := serverStatic[host]; taken {
			ignored = append(ignored, host)
			continue
		}
		merged[host] = []netip.Addr{addr}
		applied[host] = addr
	}
	sort.Strings(ignored)
	return merged, applied, ignored
}
```

- [ ] **Step 4: 接进 run.go**

在 `internal/supervisor/run.go` 里,`dnsSrv.SetStaticA(staticA, splitDirect)` **之前**插入:

```go
	staticA, appliedHosts, ignoredHosts := mergeHostOverrides(staticA, cfg.HostOverrides())
	for _, host := range ignoredHosts {
		// 只记不停:配置里那条是错的,但传输服务器的真 IP 已经用上了,
		// 隧道是安全的。停在这里对用户没有好处。
		log.Printf("hosts_override_ignored host=%s reason=transport_server", host)
	}
	for host, addr := range appliedHosts {
		log.Printf("hosts_override_applied host=%s addr=%s", host, addr)
	}
```

`appliedHosts` 之后 Task 3 还要用来填 `stats.Report`,所以变量留着。

- [ ] **Step 5: 跑测试 + 变异验证**

Run: `go test ./internal/supervisor -count=1`
Expected: 全绿。

把 `if _, taken := serverStatic[host]; taken` 那个分支去掉(即允许用户覆盖服务器),跑
`go test ./internal/supervisor -run TestMergeHostOverridesNeverOverrides -count=1`
Expected: FAIL。确认后改回。

- [ ] **Step 6: 提交**

```bash
gofumpt -w internal/supervisor/hostsmerge.go internal/supervisor/hostsmerge_test.go internal/supervisor/run.go
git add internal/supervisor/hostsmerge.go internal/supervisor/hostsmerge_test.go internal/supervisor/run.go
git commit -m "$(cat <<'EOF'
feat(supervisor): hosts 覆盖并进 staticA,传输服务器一律优先

serverStatic 里的条目是防环用的:隧道子进程靠它们解析到服务器真 IP 再走
bypass 路由。用户覆盖了会让隧道静默连到错误的地方 —— bx status 照样报健康,
因为隧道自己那条 socks 健康检查走的是同一条错路。这是本功能最危险的失败
模式,故冲突时一律忽略用户那条并报出来。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: `bx status` 常驻显示生效中的覆盖

**Files:**
- Modify: `internal/stats/render.go`(`Report` 结构体 + 渲染)
- Modify: `internal/supervisor/control.go`(`stats.Report{...}` 构造点,约 :399)
- Modify: `internal/supervisor/run.go`(把 `appliedHosts` 传到控制面)
- Test: `internal/stats/render_test.go`

**Interfaces:**
- Consumes: `appliedHosts map[string]netip.Addr`(Task 2)
- Produces: `stats.Report.HostOverrides map[string]string`(json `host_overrides,omitempty`)

**关键背景:** 这一行不是锦上添花。`staticA` 是 DNS 第一跳、**不受任何 `rules` 影响**——写错一个域名就把它整个劫持了,没有任何规则能救回来。一个写完就忘的隐形开关是这类功能最常见的事故源:用户几个月后发现某网站打不开,不会想到是自己当初加的一行。

**没有覆盖时不显示这一行**——字段缺席是诚实的「没有」,不是每次都提醒「你没配」。

- [ ] **Step 1: 写失败测试**

在 `internal/stats/render_test.go` 追加:

```go
func TestRenderShowsHostOverrides(t *testing.T) {
	out := Render(Report{
		Server:        "vps",
		TunnelHealthy: true,
		HostOverrides: map[string]string{
			"torchfun.com":    "127.0.0.1",
			"api.example.com": "192.168.50.10",
		},
	})
	for _, must := range []string{"覆盖", "torchfun.com", "127.0.0.1", "api.example.com"} {
		if !strings.Contains(out, must) {
			t.Fatalf("status 必须显示生效中的 hosts 覆盖(缺 %q):\n%s", must, out)
		}
	}
	// 多条时必须稳定排序,否则每次 status 输出顺序都不同,diff 没法用
	if strings.Index(out, "api.example.com") > strings.Index(out, "torchfun.com") {
		t.Fatalf("覆盖列表必须按域名排序:\n%s", out)
	}
}

// 没配覆盖时不显示这一行 —— 字段缺席是诚实的「没有」,
// 不是每次都提醒用户「你没配」。
func TestRenderOmitsHostOverridesWhenNone(t *testing.T) {
	out := Render(Report{Server: "vps", TunnelHealthy: true})
	if strings.Contains(out, "覆盖") {
		t.Fatalf("没有覆盖时不该出现这一行:\n%s", out)
	}
}
```

(渲染函数的真实名字以 `render.go` 为准,不一定叫 `Render`;先读再写。)

- [ ] **Step 2: 跑测试确认红**

Run: `go test ./internal/stats -run TestRenderShowsHostOverrides -count=1`
Expected: 编译失败(`HostOverrides` 字段不存在)。

- [ ] **Step 3: 加字段与渲染**

`internal/stats/render.go` 的 `Report` 结构体加(放在 `UDPTransport` 之后):

```go
	// HostOverrides 是生效中的 hosts 覆盖(域名 → IPv4),不含被传输服务器压过的。
	//
	// 常驻显示是有意的:staticA 是 DNS 第一跳且不受 rules 影响,写错一个域名就把
	// 它整个劫持了,没有任何规则能救回来。写完就忘的隐形开关是这类功能最常见的
	// 事故源。
	HostOverrides map[string]string `json:"host_overrides,omitempty"`
```

渲染:在传输那一段之后加

```go
	if len(r.HostOverrides) > 0 {
		names := make([]string, 0, len(r.HostOverrides))
		for name := range r.HostOverrides {
			names = append(names, name)
		}
		sort.Strings(names) // 稳定顺序,否则 status 的 diff 没法用
		parts := make([]string, 0, len(names))
		for _, name := range names {
			parts = append(parts, fmt.Sprintf("%s → %s", name, r.HostOverrides[name]))
		}
		fmt.Fprintf(&b, "  覆盖    %s\n", strings.Join(parts, "  "))
	}
```

需要 import `sort`(`fmt`/`strings` 已在)。

- [ ] **Step 4: 接线**

构造点在 `internal/supervisor/control.go:390` 的
`serveControlWithPathRecovery(...)` 里的 `report := func() stats.Report` 闭包
(`serveControl`(`:386`)是它的薄包装,两处签名都要跟着改)。

**已核对的现实,请带着它决定怎么传**:这个函数**已经有 13 个位置参数**
(`ctx, c, t, server, mode, udpMode, transportInfo, runtime, eng, mut, reload, shutdown,
ownerUID`),再加第 14 个不好看。三条路自己选并说明理由:

1. 直接加第 14 个 `hostOverrides map[string]string`——与既有风格一致,改动最小,
   但把一个已经过长的签名推得更长。
2. 仿 `transportInfo func() (string, []string, string)` 的形状加一个闭包——一致性更好,
   但覆盖在进程生命周期内不会变(本期不做热重载),用闭包是没必要的间接。
3. 把这一串参数收进 options struct——最干净,但**超出本计划范围**,会碰到所有调用点
   和它们的测试,风险大于收益。**不建议在本任务里做。**

`run.go` 把 Task 2 得到的 `appliedHosts`(`map[string]netip.Addr`)转成
`map[string]string` 再传进去——`stats.Report` 是要序列化成 JSON 的,`netip.Addr`
在那里没有好处。

- [ ] **Step 5: 跑全部测试**

Run: `go build ./... && go vet ./... && go test ./... -count=1`
Expected: 全绿。

- [ ] **Step 6: 变异验证**

把渲染那段的 `sort.Strings(names)` 去掉,跑
`go test ./internal/stats -run TestRenderShowsHostOverrides -count=1 -count=5`
Expected: 至少一次 FAIL(map 遍历顺序随机)。确认后改回。

把 `if len(r.HostOverrides) > 0` 改成无条件渲染,跑
`go test ./internal/stats -run TestRenderOmitsHostOverridesWhenNone -count=1`
Expected: FAIL。确认后改回。

- [ ] **Step 7: 提交**

```bash
gofumpt -w internal/stats/render.go internal/stats/render_test.go internal/supervisor/control.go internal/supervisor/run.go
git add internal/stats/render.go internal/stats/render_test.go internal/supervisor/control.go internal/supervisor/run.go
git commit -m "$(cat <<'EOF'
feat(status): 常驻显示生效中的 hosts 覆盖

staticA 是 DNS 第一跳且不受 rules 影响 —— 写错一个域名就把它整个劫持了,
没有任何规则能救回来。写完就忘的隐形开关是这类功能最常见的事故源:用户
几个月后发现某网站打不开,不会想到是自己当初加的一行。

没有覆盖时不显示这一行:字段缺席是诚实的「没有」,不是每次提醒「你没配」。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## 真机验收(由用户执行)

1. `/etc/bx/config.yaml` 加 `hosts: {torchfun.com: 127.0.0.1}`,`sudo bx down && sudo bx up`
2. `dig torchfun.com @127.0.0.1 +short` → `127.0.0.1`(不是 `198.18.x.x` 的 fake-IP)
3. `dig -t AAAA torchfun.com @127.0.0.1` → NODATA(不给应用绕过覆盖的路)
4. `bx status` 里能看到 `覆盖  torchfun.com → 127.0.0.1`
5. app 的语音落到本地 ASR 服务器
6. `curl https://api.ipify.org` 出口仍是 VPS(其他域名完全不受影响)
7. **故意配错一次**:把传输服务器的域名也写进 hosts,`bx up` 应正常起来、日志有
   `hosts_override_ignored`、`bx status` 的覆盖行里**没有**它

## 自查

**Spec 覆盖:** 配置面 → Task 1;传输服务器优先 + 被忽略要报出来 → Task 2;
可见性 → Task 3;解析期严格校验 → Task 1;AAAA 仍 NODATA → 无需改动(`staticA`
只处理 `TypeA`),由真机验收第 3 项确认。

**占位符:** 无 TBD。Task 3 Step 1 与 Step 4 显式要求实施者以现有代码为准确认渲染函数名
与 `Serve` 的传参方式——那是防止本文档的猜测覆盖代码事实,不是占位。

**类型一致:** `Config.HostOverrides() map[string]netip.Addr`(Task 1)→
`mergeHostOverrides` 的 `user` 参数(Task 2)→ `appliedHosts` 转 `map[string]string` 填
`stats.Report.HostOverrides`(Task 3)。三处类型转换点都已写明。
