# Task 9a — 真实 Linux 路由快照器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 实现 `confirm.Snapshotter` 的 Linux 版——精确还原(diff-reconcile)`ip rule` + table-100 路由(v4+v6),正确性大头压进免-root 纯单测,IO 往返用 netns 集成测验证。

**Architecture:** 四层切分:纯解析(`parseRules`/`parseRoutes`)+ 纯 diff(`diffRules`)+ 纯命令重建(`ruleArgs`/`routeAddArgs`)放无 build-tag 的 `routesnapshot.go`(在 Mac 上原生 TDD);IO 薄壳 `Capture()`/`Restore()` 放 `systemsnapshot_linux.go`(`//go:build linux`),跑 `ip` 命令调上述纯函数。**不接 live 路径**(`newSystemSnapshotter()` 仍返回 nop,留 9b 同 commit 切真)。

**Tech Stack:** Go 1.26.3;`iproute2`(`ip` 命令);`golang.org/x/sys/unix`(netns 测试);复用 `internal/supervisor` 现有 `runIP`/`ipv6Enabled`;实现 `internal/confirm.Snapshotter`。

## Global Constraints

- 模块 `github.com/getbx/bx`,Go 1.26.3,`CGO_ENABLED=0`。
- TDD:失败测试→红→实现→绿→提交。纯逻辑测试免 root,在 macOS 上原生跑(无 build tag → `go test ./internal/supervisor/` 直接纳入)。
- 提交信息中文 conventional + 结尾 `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`。在 master 直接提交。
- **不接 live 路径**:本 plan 不改 `newSystemSnapshotter()`(仍 nop)、不接控制 socket、不动死手位置、不 wire MCP ops。那些是 9b/9c,且受"快照器与真实 mutation 同 commit"硬约束——9a 只交付被测组件。
- **快照覆盖**:`ip rule`(全,v4+v6)+ `ip route show table 100`(v4+v6)。**不抓主表路由**(内核/DHCP 自管)。
- **Restore 语义**:rules 走 diff-reconcile(共享表,精确增删);table 100 走 flush+replay(bx 独占)。顺序:先删多余 rule → flush+replay table 100(v4/v6)→ 加缺失 rule。
- **权威规则集**(快照器必须能往返的):见 `internal/supervisor/platform_linux.go` 的 `upSteps()`——pref 100 `fwmark 0x162 table main`、pref 149 `to 100.64.0.0/10 table 52`、pref 150 `to <私网cidr> table main`、pref 200 `table 100`;table 100 路由 `default dev <tun>`、bypass `<cidr> via <gw> dev <dev>`、v6 `unreachable default`。
- **netns 验证基建已就绪**(`docs/...task9-validation-harness`):`unshare(CLONE_NEWNET)`+`LockOSThread`+dummy 网卡;CI `integration` job + SKIP 守卫。本机 macOS 跑不了 netns 测,真跑在 CI/Colima。

---

### Task 1: 纯类型 + `parseRules` + `parseRoutes`

**Files:**
- Create: `internal/supervisor/routesnapshot.go`(无 build tag)
- Create: `internal/supervisor/routesnapshot_test.go`(无 build tag)

**Interfaces:**
- Produces:
  - `type ipFamily int`;`const ( familyV4 ipFamily = iota; familyV6 )`
  - `type ruleSpec struct { family ipFamily; pref int; fwmark string; toCIDR string; table string }`(全可比较字段,后续用作 map key)
  - `type routeSpec struct { family ipFamily; typ string; dst string; via string; dev string }`
  - `func parseRules(out string, fam ipFamily) []ruleSpec` —— 解析 `ip [-6] rule list` 文本。
  - `func parseRoutes(out string, fam ipFamily) []routeSpec` —— 解析 `ip [-6] route show table 100` 文本。

- [ ] **Step 1: 写失败测试**

Create `internal/supervisor/routesnapshot_test.go`:
```go
package supervisor

import (
	"reflect"
	"testing"
)

func TestParseRulesV4(t *testing.T) {
	out := `0:	from all lookup local
100:	from all fwmark 0x162 lookup main
149:	from all to 100.64.0.0/10 lookup 52
150:	from all to 10.0.0.0/8 lookup main
200:	from all lookup 100
32766:	from all lookup main
32767:	from all lookup default
`
	got := parseRules(out, familyV4)
	want := []ruleSpec{
		{familyV4, 0, "", "", "local"},
		{familyV4, 100, "0x162", "", "main"},
		{familyV4, 149, "", "100.64.0.0/10", "52"},
		{familyV4, 150, "", "10.0.0.0/8", "main"},
		{familyV4, 200, "", "", "100"},
		{familyV4, 32766, "", "", "main"},
		{familyV4, 32767, "", "", "default"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRules\n got=%#v\nwant=%#v", got, want)
	}
}

func TestParseRulesV6Empty(t *testing.T) {
	if got := parseRules("", familyV6); len(got) != 0 {
		t.Fatalf("空输入应得 0 条,got=%#v", got)
	}
}

func TestParseRoutesTable100(t *testing.T) {
	out := `default dev bx0 
10.1.2.3 via 192.168.1.1 dev eth0 
`
	got := parseRoutes(out, familyV4)
	want := []routeSpec{
		{familyV4, "", "default", "", "bx0"},
		{familyV4, "", "10.1.2.3", "192.168.1.1", "eth0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRoutes\n got=%#v\nwant=%#v", got, want)
	}
}

func TestParseRoutesV6Unreachable(t *testing.T) {
	out := "unreachable default dev lo metric 1024 \n"
	got := parseRoutes(out, familyV6)
	want := []routeSpec{{familyV6, "unreachable", "default", "", "lo"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseRoutes v6\n got=%#v\nwant=%#v", got, want)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run 'ParseRules|ParseRoutes' -v`
Expected: 编译失败,`parseRules`/`parseRoutes`/`ruleSpec`/`routeSpec` undefined。

- [ ] **Step 3: 写实现**

Create `internal/supervisor/routesnapshot.go`:
```go
// routesnapshot.go 是 Linux 路由快照器的纯逻辑层(无 build tag,可在任何平台原生单测):
// 解析 `ip rule`/`ip route` 文本、diff 规则、把 spec 重建回 `ip` 命令参数。
// IO 薄壳(真跑 ip 命令)在 systemsnapshot_linux.go。
package supervisor

import (
	"strconv"
	"strings"
)

type ipFamily int

const (
	familyV4 ipFamily = iota
	familyV6
)

// ruleSpec 是一条策略路由规则的可比较表示,足以重建 `ip [-6] rule add/del`。
type ruleSpec struct {
	family ipFamily
	pref   int
	fwmark string // "" 表示无;否则如 "0x162"
	toCIDR string // "" 表示无 to 选择子
	table  string // "main"/"local"/"default"/"100"/"52"...
}

// routeSpec 是 table 100 一条路由的可比较表示,足以重建 `ip [-6] route add ... table 100`。
type routeSpec struct {
	family ipFamily
	typ    string // "" 普通;或 "unreachable"
	dst    string // "default" 或 CIDR/IP
	via    string // "" 表示无
	dev    string // "" 表示无
}

// parseRules 解析 `ip [-6] rule list` 输出。无法表示的选择子尽力提取(pref+table 必有)。
func parseRules(out string, fam ipFamily) []ruleSpec {
	var specs []ruleSpec
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 形如 "100:\tfrom all fwmark 0x162 lookup main"
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		pref, err := strconv.Atoi(strings.TrimSpace(line[:colon]))
		if err != nil {
			continue
		}
		rest := strings.Fields(line[colon+1:])
		r := ruleSpec{family: fam, pref: pref}
		for i := 0; i < len(rest); i++ {
			switch rest[i] {
			case "fwmark":
				if i+1 < len(rest) {
					r.fwmark = stripMask(rest[i+1])
					i++
				}
			case "to":
				if i+1 < len(rest) {
					r.toCIDR = rest[i+1]
					i++
				}
			case "lookup":
				if i+1 < len(rest) {
					r.table = rest[i+1]
					i++
				}
			}
		}
		specs = append(specs, r)
	}
	return specs
}

// stripMask 去掉 fwmark 的掩码后缀(有些 iproute2 打 "0x162/0xffffffff")。
func stripMask(s string) string {
	if i := strings.IndexByte(s, '/'); i >= 0 {
		return s[:i]
	}
	return s
}

// parseRoutes 解析 `ip [-6] route show table 100` 输出(只取重建所需:typ/dst/via/dev)。
func parseRoutes(out string, fam ipFamily) []routeSpec {
	var specs []routeSpec
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		r := routeSpec{family: fam}
		i := 0
		if f[0] == "unreachable" || f[0] == "blackhole" || f[0] == "prohibit" {
			r.typ = f[0]
			i = 1
		}
		if i >= len(f) {
			continue
		}
		r.dst = f[i]
		i++
		for ; i < len(f); i++ {
			switch f[i] {
			case "via":
				if i+1 < len(f) {
					r.via = f[i+1]
					i++
				}
			case "dev":
				if i+1 < len(f) {
					r.dev = f[i+1]
					i++
				}
			}
		}
		specs = append(specs, r)
	}
	return specs
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/supervisor/ -run 'ParseRules|ParseRoutes' -v && go vet ./internal/supervisor/`
Expected: 4 个测试 PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/routesnapshot.go internal/supervisor/routesnapshot_test.go
git commit -m "feat(supervisor): 路由快照器纯解析层(parseRules/parseRoutes)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: 纯 diff —— `diffRules`

**Files:**
- Modify: `internal/supervisor/routesnapshot.go`(追加 `diffRules`)
- Modify: `internal/supervisor/routesnapshot_test.go`(追加测试)

**Interfaces:**
- Consumes: `ruleSpec`(Task 1)。
- Produces: `func diffRules(current, target []ruleSpec) (toDel, toAdd []ruleSpec)` —— `toDel = current∖target`(现有但快照没有,如 bx mutation 加的规则),`toAdd = target∖current`(快照有但现在没有,被删的基线规则)。保持各自输入顺序。

- [ ] **Step 1: 写失败测试**

在 `routesnapshot_test.go` 追加:
```go
func TestDiffRules(t *testing.T) {
	base := []ruleSpec{
		{familyV4, 0, "", "", "local"},
		{familyV4, 32766, "", "", "main"},
	}
	// 当前 = 基线 + bx 装的 3 条(pref 100/150/200)
	current := append(append([]ruleSpec{}, base...),
		ruleSpec{familyV4, 100, "0x162", "", "main"},
		ruleSpec{familyV4, 150, "", "10.0.0.0/8", "main"},
		ruleSpec{familyV4, 200, "", "", "100"},
	)
	toDel, toAdd := diffRules(current, base)
	wantDel := []ruleSpec{
		{familyV4, 100, "0x162", "", "main"},
		{familyV4, 150, "", "10.0.0.0/8", "main"},
		{familyV4, 200, "", "", "100"},
	}
	if !reflect.DeepEqual(toDel, wantDel) {
		t.Fatalf("toDel\n got=%#v\nwant=%#v", toDel, wantDel)
	}
	if len(toAdd) != 0 {
		t.Fatalf("toAdd 应空(基线没被删),got=%#v", toAdd)
	}
}

func TestDiffRulesReAddsDeletedBaseline(t *testing.T) {
	base := []ruleSpec{{familyV4, 150, "", "192.168.0.0/16", "main"}}
	current := []ruleSpec{} // 一条基线规则被(异常)删了
	toDel, toAdd := diffRules(current, base)
	if len(toDel) != 0 {
		t.Fatalf("toDel 应空,got=%#v", toDel)
	}
	if !reflect.DeepEqual(toAdd, base) {
		t.Fatalf("toAdd 应重加被删基线,got=%#v want=%#v", toAdd, base)
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run Diff -v`
Expected: `diffRules` undefined。

- [ ] **Step 3: 写实现**

在 `routesnapshot.go` 追加:
```go
// diffRules 算集合差:toDel = current 中不在 target 的(需删除),
// toAdd = target 中不在 current 的(需补回)。ruleSpec 全字段可比较,用作 map key。
func diffRules(current, target []ruleSpec) (toDel, toAdd []ruleSpec) {
	inTarget := make(map[ruleSpec]bool, len(target))
	for _, r := range target {
		inTarget[r] = true
	}
	inCurrent := make(map[ruleSpec]bool, len(current))
	for _, r := range current {
		inCurrent[r] = true
	}
	for _, r := range current {
		if !inTarget[r] {
			toDel = append(toDel, r)
		}
	}
	for _, r := range target {
		if !inCurrent[r] {
			toAdd = append(toAdd, r)
		}
	}
	return toDel, toAdd
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/supervisor/ -run Diff -v && go vet ./internal/supervisor/`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/routesnapshot.go internal/supervisor/routesnapshot_test.go
git commit -m "feat(supervisor): 路由快照器 diffRules(集合差 toDel/toAdd)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 3: 纯命令重建 —— `ruleArgs` / `routeAddArgs`(最易藏 bug 的一层)

**Files:**
- Modify: `internal/supervisor/routesnapshot.go`(追加)
- Modify: `internal/supervisor/routesnapshot_test.go`(追加)

**Interfaces:**
- Consumes: `ruleSpec`/`routeSpec`(Task 1)。
- Produces:
  - `func ruleArgs(verb string, r ruleSpec) []string` —— verb 为 "add"/"del";返回传给 `ip` 的参数(v6 以 "-6" 开头)。形如 `["rule","add","to","10.0.0.0/8","pref","150","table","main"]`。
  - `func routeAddArgs(r routeSpec) []string` —— 返回 `["route","add",...,"table","100"]`(v6 以 "-6" 开头)。

**重建规则(对齐 `upSteps()`/`downSteps()` 的真实语法):**
- `to` 规则:`[-6] rule <verb> to <cidr> pref <pref> table <table>`
- fwmark 规则:`[-6] rule <verb> pref <pref> fwmark <fwmark> table <table>`
- 纯规则:`[-6] rule <verb> pref <pref> table <table>`
- 路由:`[-6] route add [<typ>] <dst> [via <via>] [dev <dev>] table 100`

- [ ] **Step 1: 写失败测试**

在 `routesnapshot_test.go` 追加:
```go
func TestRuleArgs(t *testing.T) {
	cases := []struct {
		name string
		verb string
		r    ruleSpec
		want []string
	}{
		{"fwmark-add", "add", ruleSpec{familyV4, 100, "0x162", "", "main"},
			[]string{"rule", "add", "pref", "100", "fwmark", "0x162", "table", "main"}},
		{"to-add", "add", ruleSpec{familyV4, 150, "", "10.0.0.0/8", "main"},
			[]string{"rule", "add", "to", "10.0.0.0/8", "pref", "150", "table", "main"}},
		{"to-tailscale-add", "add", ruleSpec{familyV4, 149, "", "100.64.0.0/10", "52"},
			[]string{"rule", "add", "to", "100.64.0.0/10", "pref", "149", "table", "52"}},
		{"plain-del", "del", ruleSpec{familyV4, 200, "", "", "100"},
			[]string{"rule", "del", "pref", "200", "table", "100"}},
		{"v6-add", "add", ruleSpec{familyV6, 200, "", "", "100"},
			[]string{"-6", "rule", "add", "pref", "200", "table", "100"}},
	}
	for _, c := range cases {
		if got := ruleArgs(c.verb, c.r); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: ruleArgs\n got=%v\nwant=%v", c.name, got, c.want)
		}
	}
}

func TestRouteAddArgs(t *testing.T) {
	cases := []struct {
		name string
		r    routeSpec
		want []string
	}{
		{"default-dev", routeSpec{familyV4, "", "default", "", "bx0"},
			[]string{"route", "add", "default", "dev", "bx0", "table", "100"}},
		{"bypass", routeSpec{familyV4, "", "10.1.2.3", "192.168.1.1", "eth0"},
			[]string{"route", "add", "10.1.2.3", "via", "192.168.1.1", "dev", "eth0", "table", "100"}},
		{"v6-unreachable", routeSpec{familyV6, "unreachable", "default", "", ""},
			[]string{"-6", "route", "add", "unreachable", "default", "table", "100"}},
	}
	for _, c := range cases {
		if got := routeAddArgs(c.r); !reflect.DeepEqual(got, c.want) {
			t.Fatalf("%s: routeAddArgs\n got=%v\nwant=%v", c.name, got, c.want)
		}
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/supervisor/ -run 'RuleArgs|RouteAddArgs' -v`
Expected: undefined。

- [ ] **Step 3: 写实现**

在 `routesnapshot.go` 追加:
```go
import "strconv" // 已 import,确认存在

// ruleArgs 把 ruleSpec 重建成 `ip [-6] rule <verb> ...` 的参数(verb: "add"|"del")。
func ruleArgs(verb string, r ruleSpec) []string {
	var a []string
	if r.family == familyV6 {
		a = append(a, "-6")
	}
	a = append(a, "rule", verb)
	switch {
	case r.toCIDR != "":
		a = append(a, "to", r.toCIDR, "pref", strconv.Itoa(r.pref))
	case r.fwmark != "":
		a = append(a, "pref", strconv.Itoa(r.pref), "fwmark", r.fwmark)
	default:
		a = append(a, "pref", strconv.Itoa(r.pref))
	}
	a = append(a, "table", r.table)
	return a
}

// routeAddArgs 把 table-100 routeSpec 重建成 `ip [-6] route add ... table 100`。
func routeAddArgs(r routeSpec) []string {
	var a []string
	if r.family == familyV6 {
		a = append(a, "-6")
	}
	a = append(a, "route", "add")
	if r.typ != "" {
		a = append(a, r.typ)
	}
	a = append(a, r.dst)
	if r.via != "" {
		a = append(a, "via", r.via)
	}
	if r.dev != "" {
		a = append(a, "dev", r.dev)
	}
	a = append(a, "table", "100")
	return a
}
```
注:若 `strconv` 已在 Task 1 import,删掉重复 import 行;确保文件顶部 import 块含 `strconv` 与 `strings`。

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/supervisor/ -run 'RuleArgs|RouteAddArgs' -v && go vet ./internal/supervisor/`
Expected: PASS。

- [ ] **Step 5: 提交**

```bash
git add internal/supervisor/routesnapshot.go internal/supervisor/routesnapshot_test.go
git commit -m "feat(supervisor): 路由快照器命令重建(ruleArgs/routeAddArgs)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 4: IO 薄壳 —— `Capture`/`Restore` + `linuxSnapshot` + `NewSystemSnapshotter`(`//go:build linux`)

**Files:**
- Create: `internal/supervisor/systemsnapshot_linux.go`(`//go:build linux`)
- (本任务无新纯测试;验证靠 `GOOS=linux go vet` 编译 + Task 5 的 netns 实测)

**Interfaces:**
- Consumes: `parseRules`/`parseRoutes`/`diffRules`/`ruleArgs`/`routeAddArgs`(Task 1-3);现有 `runIPQuiet`(platform_linux.go,`exec.Command("ip", ...).Run()`)、`ipv6Enabled()`(platform_linux.go)。
- Produces:
  - `type linuxSnapshot struct {...}` 实现 `confirm.Snapshot`(`ID() string`)。
  - `func NewSystemSnapshotter() confirm.Snapshotter` —— 返回 Linux 实现。
  - 方法 `Capture() (confirm.Snapshot, error)` / `Restore(confirm.Snapshot) error`。

- [ ] **Step 1: 写实现**

Create `internal/supervisor/systemsnapshot_linux.go`:
```go
//go:build linux

// systemsnapshot_linux.go 是路由快照器的 IO 薄壳:跑 `ip` 抓状态 / 还原,
// 纯逻辑(解析/diff/重建)在 routesnapshot.go。实现 confirm.Snapshotter。
// 本文件不接 live 路径(newSystemSnapshotter 仍 nop,见 mcp/server.go);9b 同 commit 切真。
package supervisor

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/getbx/bx/internal/confirm"
)

// linuxSnapshot 是一次 last-known-good 路由状态快照,实现 confirm.Snapshot。
type linuxSnapshot struct {
	id      string
	v4Rules []ruleSpec
	v6Rules []ruleSpec
	v4T100  []routeSpec
	v6T100  []routeSpec
}

func (s *linuxSnapshot) ID() string { return s.id }

type linuxSnapshotter struct{ seq int }

// NewSystemSnapshotter 返回 Linux 路由快照器(实现 confirm.Snapshotter)。
func NewSystemSnapshotter() confirm.Snapshotter { return &linuxSnapshotter{} }

// ipShow 跑 `ip <args...>` 抓 stdout 文本。
func ipShow(args ...string) (string, error) {
	out, err := exec.Command("ip", args...).Output()
	if err != nil {
		return "", fmt.Errorf("ip %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (s *linuxSnapshotter) Capture() (confirm.Snapshot, error) {
	v4r, err := ipShow("rule", "list")
	if err != nil {
		return nil, err
	}
	v4t, err := ipShow("route", "show", "table", "100")
	if err != nil {
		return nil, err
	}
	snap := &linuxSnapshot{
		v4Rules: parseRules(v4r, familyV4),
		v4T100:  parseRoutes(v4t, familyV4),
	}
	if ipv6Enabled() {
		v6r, err := ipShow("-6", "rule", "list")
		if err != nil {
			return nil, err
		}
		v6t, err := ipShow("-6", "route", "show", "table", "100")
		if err != nil {
			return nil, err
		}
		snap.v6Rules = parseRules(v6r, familyV6)
		snap.v6T100 = parseRoutes(v6t, familyV6)
	}
	s.seq++
	snap.id = fmt.Sprintf("lkg-%d", s.seq)
	return snap, nil
}

// Restore 精确还原到快照:rules diff-reconcile,table 100 flush+replay。
// 尽力做完所有步骤(单步出错记录但继续),聚合返回。顺序:删多余 rule → flush+replay
// table 100 → 加缺失 rule。
func (s *linuxSnapshotter) Restore(snap confirm.Snapshot) error {
	ls, ok := snap.(*linuxSnapshot)
	if !ok {
		return fmt.Errorf("快照类型不符: %T", snap)
	}
	var errs []error
	run := func(args ...string) {
		if err := runIPQuiet(args...); err != nil {
			errs = append(errs, fmt.Errorf("ip %s: %w", strings.Join(args, " "), err))
		}
	}

	// 1) rules:重抓当前 → diff → 删多余(此步)。
	curV4 := parseCurrentRules(familyV4)
	delV4, addV4 := diffRules(curV4, ls.v4Rules)
	var delV6, addV6 []ruleSpec
	if ipv6Enabled() {
		curV6 := parseCurrentRules(familyV6)
		delV6, addV6 = diffRules(curV6, ls.v6Rules)
	}
	for _, r := range delV4 {
		run(ruleArgs("del", r)...)
	}
	for _, r := range delV6 {
		run(ruleArgs("del", r)...)
	}

	// 2) table 100:flush + replay(bx 独占)。
	run("route", "flush", "table", "100")
	for _, rt := range ls.v4T100 {
		run(routeAddArgs(rt)...)
	}
	if ipv6Enabled() {
		run("-6", "route", "flush", "table", "100")
		for _, rt := range ls.v6T100 {
			run(routeAddArgs(rt)...)
		}
	}

	// 3) rules:加缺失(防御性,bx mutation 一般不删基线规则)。
	for _, r := range addV4 {
		run(ruleArgs("add", r)...)
	}
	for _, r := range addV6 {
		run(ruleArgs("add", r)...)
	}
	return errors.Join(errs...)
}

// parseCurrentRules 抓当前 `ip [-6] rule list` 并解析(抓失败返回 nil,让 diff 退化为"全加")。
func parseCurrentRules(fam ipFamily) []ruleSpec {
	args := []string{"rule", "list"}
	if fam == familyV6 {
		args = append([]string{"-6"}, args...)
	}
	out, err := ipShow(args...)
	if err != nil {
		return nil
	}
	return parseRules(out, fam)
}
```

- [ ] **Step 2: 本机交叉编译验证**

Run:
```bash
cd /Users/nategu_mac_company/Documents/bx
GOOS=linux go vet ./internal/supervisor/
go test ./internal/supervisor/ 2>&1 | tail -3
go build ./...
```
Expected: `GOOS=linux go vet` 干净(linux 文件编得过、引用的 `runIPQuiet`/`ipv6Enabled`/`confirm.Snapshotter`/纯函数都存在);Mac 上现有 supervisor 测试照常(linux 文件被排除);build 绿。
若 `confirm` import 造成循环或 `runIPQuiet` 签名不符,打开 `internal/supervisor/platform_linux.go` 与 `internal/confirm/snapshot.go` 核对真实签名并改正。

- [ ] **Step 3: 提交**

```bash
git add internal/supervisor/systemsnapshot_linux.go
git commit -m "feat(supervisor): Linux 路由快照器 IO 薄壳(Capture/Restore,未接 live)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 5: netns 往返集成测试(`//go:build integration && linux`)

**Files:**
- Create: `internal/supervisor/systemsnapshot_netns_linux_test.go`(`//go:build integration && linux`)

**Interfaces:**
- Consumes: `NewSystemSnapshotter()`(Task 4)、`netConf`(platform_linux.go)、`route.DefaultPrivateCIDRs`;netns 模式同验证基建(`unshare`+`LockOSThread`+dummy)。

- [ ] **Step 1: 写测试**

Create `internal/supervisor/systemsnapshot_netns_linux_test.go`:
```go
//go:build integration && linux

package supervisor

import (
	"os"
	"os/exec"
	"runtime"
	"testing"

	"github.com/getbx/bx/internal/route"
	"golang.org/x/sys/unix"
)

// TestSnapshotterRoundTripInNetns:netns 内 Capture 基线 → netConf.up() 制造改动
// → Restore(基线) → 断言 ip rule/route table 100 回到基线。需 root,门控 build tag。
func TestSnapshotterRoundTripInNetns(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("需要 root(Colima VM 或 CI 里 sudo)")
	}
	if _, err := exec.LookPath("ip"); err != nil {
		t.Skip("缺 ip 命令")
	}
	runtime.LockOSThread() // 不 Unlock:goroutine 结束销毁线程,临时 netns 随之消失
	if err := unix.Unshare(unix.CLONE_NEWNET); err != nil {
		t.Skipf("unshare(CLONE_NEWNET) 失败: %v", err)
	}
	mustIP2 := func(args ...string) {
		if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
			t.Fatalf("ip %v: %v\n%s", args, err, out)
		}
	}
	ruleList := func() string {
		out, err := exec.Command("ip", "rule", "list").CombinedOutput()
		if err != nil {
			t.Fatalf("ip rule list: %v\n%s", err, out)
		}
		return string(out)
	}
	mustIP2("link", "set", "lo", "up")
	mustIP2("link", "add", "bx0", "type", "dummy")

	snapper := NewSystemSnapshotter()
	base, err := snapper.Capture()
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	baseRules := ruleList()

	// 制造改动:跑 bx 现有 netConf.up() 装一堆策略路由。
	nc := &netConf{tunName: "bx0", tunAddr: "198.51.100.1/30", mainLookup: route.DefaultPrivateCIDRs}
	if err := nc.up(); err != nil {
		t.Fatalf("netConf.up(): %v", err)
	}
	if ruleList() == baseRules {
		t.Fatal("up() 后规则应已变化(测试前提不成立)")
	}

	// 还原并断言回到基线。
	if err := snapper.Restore(base); err != nil {
		t.Fatalf("Restore 报错: %v", err)
	}
	if got := ruleList(); got != baseRules {
		t.Fatalf("Restore 未回到基线:\n--- base ---\n%s\n--- got ---\n%s", baseRules, got)
	}
	// table 100 应被清空(基线时为空)。
	if out, _ := exec.Command("ip", "route", "show", "table", "100").CombinedOutput(); len(out) != 0 {
		t.Fatalf("Restore 后 table 100 应空,得到:\n%s", out)
	}
}
```

- [ ] **Step 2: 本机门控验证**

Run:
```bash
cd /Users/nategu_mac_company/Documents/bx
GOOS=linux go vet -tags integration ./internal/supervisor/
go test ./internal/supervisor/ 2>&1 | tail -3
```
Expected: `GOOS=linux go vet -tags integration` 编得过;Mac 上 `go test`(无 tag)照常、不含本测试。**本机 macOS 跑不了真测**(需 Linux+root+netns),真跑在 CI/Colima。

- [ ] **Step 3:(可选)真机/容器实跑**

如有 Linux:`sudo "$(which go)" test -tags integration -run TestSnapshotterRoundTripInNetns ./internal/supervisor/ -v` → Expected PASS。报告注明本机未跑。

- [ ] **Step 4: 提交**

```bash
git add internal/supervisor/systemsnapshot_netns_linux_test.go
git commit -m "test(supervisor): 快照器 netns 往返集成测(Capture→up→Restore→断言基线)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review(对照 spec)

**Spec 覆盖:**
- 位置(`internal/supervisor`,`confirm` 保持纯,实现 `confirm.Snapshotter`)→ Task 4。
- 四层切分(解析/diff/命令重建纯 + IO 薄壳)→ Task 1(解析)、Task 2(diff)、Task 3(命令重建)、Task 4(IO)。
- Capture 抓 ip rule(v4/v6)+ table 100(v4/v6)→ Task 4。
- Restore:rules diff-reconcile + table 100 flush+replay,顺序删 rule→flush/replay→加 rule → Task 4。
- 失败尽力做完 + 聚合返回(`errors.Join`)→ Task 4。
- v6 未启用跳过(`ipv6Enabled()`)→ Task 4。
- 不接 live 路径(不改 `newSystemSnapshotter()`)→ Global Constraints + 无该改动。
- 测试分层(纯单测 + netns 往返)→ Task 1-3 纯测、Task 5 netns。
- 权威规则集往返(pref 100/149/150/200 + table 100 + v6 unreachable)→ Task 1/3 fixtures + Task 5 用真实 `netConf.up()` 制造改动。

**占位扫描:** 无 TBD;每段代码完整。Task 4 无独立纯测(IO 层)、Task 5 本机不跑真测——均为对 macOS 环境的诚实记录(靠 GOOS=linux vet 编译 + CI/Colima 真跑),非占位。

**类型一致性:** `ruleSpec`/`routeSpec`/`ipFamily`/`familyV4`/`familyV6`(T1)被 T2/T3/T4/T5 一致引用;`parseRules`/`parseRoutes`(T1)、`diffRules`(T2)、`ruleArgs`/`routeAddArgs`(T3)签名与 T4 调用一致;`linuxSnapshot`/`NewSystemSnapshotter`/`Capture`/`Restore`(T4)被 T5 一致引用;复用的 `runIPQuiet`/`ipv6Enabled`(platform_linux.go)、`route.DefaultPrivateCIDRs`、`confirm.Snapshotter`/`confirm.Snapshot` 均与现状一致(已核对)。
