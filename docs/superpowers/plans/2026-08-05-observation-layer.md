# 观测层与不变量基线 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 bx 第一次能向系统现问「保护是否真的生效」,并把观测与信念并列发布,使二者的差异成为可见、可操作的诊断信号。

**Architecture:** 新增只读的 `internal/observe` 包:纯逻辑核心(三态、观测结构、差异计算)+ 注入式 Observer。观测原语从既有代码导出复用,不重写。全程不改任何控制流,不给任何组件写权限。

**Tech Stack:** Go 1.26,复用 `internal/supervisor` 的路由查询、`internal/install` 的 DNS 探测、`internal/guardian` 的屏障网段定义。

设计文档:`docs/superpowers/specs/2026-08-05-observation-layer-design.md`

## Global Constraints

- **观测只读**:本计划不得因观测结果改动系统任何状态。不新增任何 `route add`/`networksetup set`/`pfctl` 类调用。
- **不改控制流**:`internal/guardian` 的 `Manager` 状态字段与 `needsAttention` 调用点一个不动。允许新增导出函数,不允许改既有行为。
- **不碰用户运行中的实例**:绝不执行 `bx`、`launchctl`、`networksetup`、`pfctl`、`route` 等改动系统的命令。用户机器上有正在运行的 bx 与菜单栏。
- **「观测不到」必须与「观测到否」可区分**:一律用三态,禁止用 `bool` 表示观测结果。
- **观测失败不得让调用方失败**:任一项观测出错即记为 `Unknown` 并附错误,继续观测其余项。
- 纯逻辑测试免 root,用假依赖注入,不碰真实路由/DNS/socket。
- **测试不得钉交错**:禁止用 `time.Sleep` 当同步、禁止精确调用计数断言、禁止整串调用序列字符串相等。断言必须是「给定输入 → 必须产出什么」。
- TDD:先写失败测试→跑红→最小实现→跑绿。中文 conventional commits,结尾带 `Co-Authored-By: Claude …`。
- 验证:`go build ./... && go vet ./... && go test ./...`;跨平台 `GOOS=linux/windows GOARCH=amd64 go build -o /dev/null ./...`。

---

### Task 1: 三态、观测结构与差异计算(纯逻辑)

**Files:**
- Create: `internal/observe/state.go`
- Create: `internal/observe/diverge.go`
- Test: `internal/observe/state_test.go`、`internal/observe/diverge_test.go`

**Interfaces:**
- Consumes: 无(纯逻辑,零依赖)
- Produces:
  - `type Tristate uint8`,常量 `Unknown`/`True`/`False`,方法 `String() string`
  - `type ObserveError struct { Item string; Err string }`
  - `type ObservedState struct { ... }`(字段见下)
  - `type Intent struct { Desired string }`
  - `type Believed struct { Protection, Phase, LastError string }`
  - `type Divergence struct { Field, Believed, Observed, Note string }`
  - `func Diverge(intent Intent, observed ObservedState, believed Believed) []Divergence`

- [ ] **Step 1: 写失败测试(三态)**

创建 `internal/observe/state_test.go`:

```go
package observe

import "testing"

// 「观测不到」与「观测到否」必须可区分——用 bool 会把「问不出来」压成 false,
// 而这正是当前架构骗人的方式之一(RoutesInstalled 是个只置位不复查的 atomic.Bool)。
func TestTristateDistinguishesUnknownFromFalse(t *testing.T) {
	if Unknown == False {
		t.Fatal("Unknown 必须与 False 不同")
	}
	if Unknown == True {
		t.Fatal("Unknown 必须与 True 不同")
	}
	cases := map[Tristate]string{Unknown: "unknown", True: "true", False: "false"}
	for value, want := range cases {
		if got := value.String(); got != want {
			t.Errorf("Tristate(%d).String() = %q, want %q", value, got, want)
		}
	}
}

// 零值必须是 Unknown:未填写的观测项不得冒充"正常"。
func TestTristateZeroValueIsUnknown(t *testing.T) {
	var zero Tristate
	if zero != Unknown {
		t.Errorf("零值 = %v, want Unknown——未观测的项绝不能默认为 True/False", zero)
	}
	var state ObservedState
	if state.CaptureOK != Unknown || state.BarrierPresent != Unknown ||
		state.DNSManaged != Unknown || state.CoreSocket != Unknown || state.TunnelHealthy != Unknown {
		t.Errorf("ObservedState 零值的所有三态字段必须是 Unknown,实际 = %+v", state)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/observe/ -run TestTristate -count=1`
Expected: FAIL,`undefined: Unknown`(包尚不存在)

- [ ] **Step 3: 实现三态与观测结构**

创建 `internal/observe/state.go`:

```go
// Package observe 向系统现问 bx 的保护是否真的生效。
//
// 本包只读:它从不改动系统任何状态。它存在的理由是——bx 的生命周期层长期用
// 内存记忆表达"保护装没装",而记忆与内核里的事实会分叉(真实事故:Guardian 被
// bootout 后内存记录蒸发,内核里的 /2 reject 路由却留着,整机断网且无人能删)。
package observe

import "time"

// Tristate 区分「观测到是」「观测到否」「观测不到」。
//
// 零值必须是 Unknown:用 bool 会把"问不出来"压成 false,让未观测的项冒充正常。
type Tristate uint8

const (
	Unknown Tristate = iota
	True
	False
)

func (t Tristate) String() string {
	switch t {
	case True:
		return "true"
	case False:
		return "false"
	default:
		return "unknown"
	}
}

// FromBool 把一个确定的布尔判定转成三态。仅在观测成功时使用。
func FromBool(value bool) Tristate {
	if value {
		return True
	}
	return False
}

// ObserveError 记录单项观测为何失败。观测失败不让调用方失败,只让该项为 Unknown。
type ObserveError struct {
	Item string `json:"item"`
	Err  string `json:"err"`
}

// ObservedState 是某一时刻向系统现问得到的事实,不含任何记忆。
type ObservedState struct {
	ObservedAt       time.Time      `json:"observed_at"`
	CaptureInterface string         `json:"capture_interface,omitempty"`
	CaptureOK        Tristate       `json:"capture_ok"`
	BarrierPresent   Tristate       `json:"barrier_present"`
	DNSServers       []string       `json:"dns_servers,omitempty"`
	DNSManaged       Tristate       `json:"dns_managed"`
	CoreSocket       Tristate       `json:"core_socket"`
	TunnelHealthy    Tristate       `json:"tunnel_healthy"`
	Errors           []ObserveError `json:"errors,omitempty"`
}

// Intent 是用户/agent 声明的意图。观测层只读它,永不改写。
type Intent struct {
	Desired string `json:"desired"` // "on" | "off"
}

// Believed 是生命周期层的内存信念,原样透出以便与观测对照。
type Believed struct {
	Protection string `json:"protection"`
	Phase      string `json:"phase,omitempty"`
	LastError  string `json:"last_error,omitempty"`
}
```

`Tristate` 的 JSON 表示由 Task 4 处理(需要 `MarshalJSON` 输出字符串而非数字),本任务不做。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/observe/ -run TestTristate -count=1`
Expected: PASS

- [ ] **Step 5: 写差异计算的失败测试**

创建 `internal/observe/diverge_test.go`:

```go
package observe

import (
	"strings"
	"testing"
)

// 不变量 3:声称 protected 就必须三项观测皆为 True。Unknown 不满足——
// 不确定不等于正常。真实事故里"status 显绿而流量明文直连"正是这么产生的。
func TestDivergeFlagsProtectedWithoutCapture(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: False, DNSManaged: True, TunnelHealthy: True},
		Believed{Protection: "protected"},
	)
	if !hasField(got, "capture_ok") {
		t.Errorf("声称 protected 但劫持未生效必须产出 divergence,实际 = %+v", got)
	}
}

func TestDivergeTreatsUnknownAsNotSatisfyingProtected(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: Unknown, DNSManaged: True, TunnelHealthy: True},
		Believed{Protection: "protected"},
	)
	if !hasField(got, "capture_ok") {
		t.Errorf("观测不到时不得当作满足 protected,实际 = %+v", got)
	}
}

// 不变量 1:desired=off 时不得残留屏障路由。真实事故:强制拆除后 8 条 /2
// reject 路由成为无主孤儿,整机断网且 bx 报绿。
func TestDivergeFlagsBarrierResidueWhenDesiredOff(t *testing.T) {
	got := Diverge(
		Intent{Desired: "off"},
		ObservedState{BarrierPresent: True},
		Believed{Protection: "off"},
	)
	if !hasField(got, "barrier_present") {
		t.Errorf("desired=off 却观测到屏障残留必须产出 divergence,实际 = %+v", got)
	}
}

// 不变量 2:desired=off 时系统 DNS 不得仍是 bx 的。真实事故:强制拆除后
// DNS 仍指 127.0.0.1 而监听者已退出,用户"网页打不开"。
func TestDivergeFlagsDNSResidueWhenDesiredOff(t *testing.T) {
	got := Diverge(
		Intent{Desired: "off"},
		ObservedState{DNSManaged: True},
		Believed{Protection: "off"},
	)
	if !hasField(got, "dns_managed") {
		t.Errorf("desired=off 却观测到 DNS 仍被接管必须产出 divergence,实际 = %+v", got)
	}
}

// 一致时必须安静:否则 divergence 会被噪声淹没,失去诊断价值。
func TestDivergeSilentWhenConsistent(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: True, DNSManaged: True, TunnelHealthy: True, BarrierPresent: False},
		Believed{Protection: "protected"},
	)
	if len(got) != 0 {
		t.Errorf("观测与信念一致时不应产出 divergence,实际 = %+v", got)
	}
}

// 不变量 4:每条 divergence 必须自解释——agent 没有直觉,note 是它唯一的上下文。
func TestDivergenceCarriesSelfExplainingNote(t *testing.T) {
	got := Diverge(
		Intent{Desired: "on"},
		ObservedState{CaptureOK: False, DNSManaged: True, TunnelHealthy: True},
		Believed{Protection: "protected"},
	)
	for _, d := range got {
		if strings.TrimSpace(d.Note) == "" {
			t.Errorf("divergence 必须带自解释的 note,实际 = %+v", d)
		}
		if d.Observed == "" || d.Believed == "" {
			t.Errorf("divergence 必须同时给出观测值与信念值,实际 = %+v", d)
		}
	}
}

func hasField(list []Divergence, field string) bool {
	for _, d := range list {
		if d.Field == field {
			return true
		}
	}
	return false
}
```

- [ ] **Step 6: 跑测试确认失败**

Run: `go test ./internal/observe/ -run TestDiverge -count=1`
Expected: FAIL,`undefined: Diverge`

- [ ] **Step 7: 实现差异计算**

创建 `internal/observe/diverge.go`:

```go
package observe

import "fmt"

// Divergence 是一条「信念与事实不符」的记录。
//
// 它是本包给 agent(含用户自己的 AI)的核心产物:自解释、可操作。今天这个信号
// 存在于内核里但无人读取。
type Divergence struct {
	Field    string `json:"field"`
	Believed string `json:"believed"`
	Observed string `json:"observed"`
	Note     string `json:"note"`
}

// Diverge 比对意图、观测与信念,产出所有不一致。一致时返回空切片。
//
// 刻意不做的事:不修正信念、不改动系统、不排序优先级。它只陈述差异。
func Diverge(intent Intent, observed ObservedState, believed Believed) []Divergence {
	var out []Divergence

	if believed.Protection == "protected" {
		// 声称已保护时,三项观测必须皆为 True。Unknown 不满足:不确定不等于正常。
		for _, check := range []struct {
			field string
			value Tristate
			note  string
		}{
			{"capture_ok", observed.CaptureOK, "保护状态声称已保护,但未观测到流量被劫持进 bx 的 TUN"},
			{"dns_managed", observed.DNSManaged, "保护状态声称已保护,但未观测到系统 DNS 由 bx 接管"},
			{"tunnel_healthy", observed.TunnelHealthy, "保护状态声称已保护,但未观测到隧道健康"},
		} {
			if check.value != True {
				out = append(out, Divergence{
					Field:    check.field,
					Believed: "protected",
					Observed: check.value.String(),
					Note:     check.note,
				})
			}
		}
	}

	if intent.Desired == "off" {
		if observed.BarrierPresent == True {
			out = append(out, Divergence{
				Field:    "barrier_present",
				Believed: "off",
				Observed: "true",
				Note:     "意图是关闭,但内核里仍有 bx 装的阻断路由;它们会让整机断网且不随 bx 退出而消失",
			})
		}
		if observed.DNSManaged == True {
			out = append(out, Divergence{
				Field:    "dns_managed",
				Believed: "off",
				Observed: "true",
				Note:     "意图是关闭,但系统 DNS 仍指向 bx;若 bx 已退出,域名解析会全部失败",
			})
		}
	}

	for _, e := range observed.Errors {
		out = append(out, Divergence{
			Field:    e.Item,
			Believed: believed.Protection,
			Observed: "unknown",
			Note:     fmt.Sprintf("该项无法观测:%s", e.Err),
		})
	}

	return out
}
```

- [ ] **Step 8: 跑测试确认通过并提交**

```bash
go test ./internal/observe/ -count=1
go build ./... && go vet ./...
git add internal/observe/
git commit -m "feat(observe): 三态、观测结构与差异计算"
```

---

### Task 2: 导出观测原语

**Files:**
- Create: `internal/supervisor/route_lookup_darwin.go`、`internal/supervisor/route_lookup_other.go`
- Create: `internal/guardian/barrier_cidrs.go`
- Test: `internal/supervisor/route_lookup_test.go`、`internal/guardian/barrier_cidrs_test.go`

**Interfaces:**
- Consumes: 既有 `darwinRouteLookup`(`supervisor/underlay_darwin.go:180`)、`darwinRouteSelection`(`:23`)、`publicIPv4Blocks`/`publicIPv6Blocks`(`guardian/barrier.go:49-50`)
- Produces:
  - `type supervisor.RouteSelection struct { Gateway, Interface string; Reject bool }`
  - `func supervisor.LookupRoute(ctx context.Context, destination string, ipv6 bool) (RouteSelection, error)`
  - `func guardian.BlockingBarrierCIDRs() (ipv4, ipv6 []string)`

**为什么要导出:** 观测层要复用这两处既有能力,但它们目前都是未导出的。**只加导出包装,不改既有实现**——`darwinRouteLookup` 的解析已在生产使用,风险由既有代码承担。

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/barrier_cidrs_test.go`:

```go
package guardian

import "testing"

// 观测层要问"内核里有没有这些 reject 路由",必须与屏障实际装的是同一份清单。
// 若二者漂移,观测会问错网段,得出错误结论。
func TestBlockingBarrierCIDRsMatchesInstalledBlocks(t *testing.T) {
	ipv4, ipv6 := BlockingBarrierCIDRs()
	if len(ipv4) != len(publicIPv4Blocks) {
		t.Fatalf("IPv4 网段数 = %d, want %d", len(ipv4), len(publicIPv4Blocks))
	}
	for i, want := range publicIPv4Blocks {
		if ipv4[i] != want {
			t.Errorf("IPv4[%d] = %q, want %q", i, ipv4[i], want)
		}
	}
	for i, want := range publicIPv6Blocks {
		if ipv6[i] != want {
			t.Errorf("IPv6[%d] = %q, want %q", i, ipv6[i], want)
		}
	}
}

// 返回的必须是副本:调用方改动不得影响屏障实际使用的清单。
func TestBlockingBarrierCIDRsReturnsCopy(t *testing.T) {
	ipv4, _ := BlockingBarrierCIDRs()
	if len(ipv4) == 0 {
		t.Fatal("IPv4 网段不应为空")
	}
	original := publicIPv4Blocks[0]
	ipv4[0] = "203.0.113.0/24"
	if publicIPv4Blocks[0] != original {
		t.Error("修改返回值污染了屏障实际使用的清单")
	}
}
```

创建 `internal/supervisor/route_lookup_test.go`:

```go
package supervisor

import (
	"context"
	"runtime"
	"testing"
)

// 非 darwin 平台必须返回明确的不支持错误,而不是零值冒充"查到了"。
func TestLookupRouteUnsupportedOutsideDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("本用例断言非 darwin 行为")
	}
	if _, err := LookupRoute(context.Background(), "1.1.1.1", false); err == nil {
		t.Error("非 darwin 平台必须返回错误,不得静默返回零值")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

```bash
go test ./internal/guardian/ -run TestBlockingBarrierCIDRs -count=1
GOOS=linux GOARCH=amd64 go vet ./internal/supervisor/
```
Expected: FAIL / 编译错误,`undefined: BlockingBarrierCIDRs` 与 `undefined: LookupRoute`

- [ ] **Step 3: 实现导出**

创建 `internal/guardian/barrier_cidrs.go`:

```go
package guardian

// BlockingBarrierCIDRs 返回屏障实际装载的阻断网段副本,供只读观测复用。
//
// 观测层需要问内核"这些 reject 路由在不在"。让它复用同一份清单,避免二者漂移
// 导致观测问错网段。
func BlockingBarrierCIDRs() (ipv4, ipv6 []string) {
	return append([]string(nil), publicIPv4Blocks...), append([]string(nil), publicIPv6Blocks...)
}
```

创建 `internal/supervisor/route_lookup_darwin.go`:

```go
//go:build darwin

package supervisor

import "context"

// RouteSelection 是一次路由查询的结果:内核会把发往该目的地的包交给谁。
type RouteSelection struct {
	Gateway   string
	Interface string
	Reject    bool
}

// LookupRoute 问内核"发往 destination 的包现在走哪里"。
//
// 这是观测的基石:它对"谁装的这条路由"完全不关心,因此天然免疫所有权簿记问题。
func LookupRoute(ctx context.Context, destination string, ipv6 bool) (RouteSelection, error) {
	selection, err := darwinRouteLookup(ctx, destination, ipv6)
	if err != nil {
		return RouteSelection{}, err
	}
	return RouteSelection{
		Gateway:   selection.Gateway,
		Interface: selection.Interface,
		Reject:    selection.Reject,
	}, nil
}
```

创建 `internal/supervisor/route_lookup_other.go`:

```go
//go:build !darwin

package supervisor

import (
	"context"
	"errors"
)

// RouteSelection 是一次路由查询的结果:内核会把发往该目的地的包交给谁。
type RouteSelection struct {
	Gateway   string
	Interface string
	Reject    bool
}

var errRouteLookupUnsupported = errors.New("route lookup is only implemented on darwin")

// LookupRoute 在非 darwin 平台返回明确的不支持错误,绝不返回零值冒充查询成功。
func LookupRoute(context.Context, string, bool) (RouteSelection, error) {
	return RouteSelection{}, errRouteLookupUnsupported
}
```

- [ ] **Step 4: 跑测试确认通过并提交**

```bash
go test ./internal/guardian/ ./internal/supervisor/ -count=1
go build ./... && go vet ./...
GOOS=linux GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
git add internal/guardian/barrier_cidrs.go internal/guardian/barrier_cidrs_test.go \
        internal/supervisor/route_lookup_darwin.go internal/supervisor/route_lookup_other.go \
        internal/supervisor/route_lookup_test.go
git commit -m "feat(observe): 导出路由查询与屏障网段供只读观测复用"
```

---

### Task 3: Observer——把原语组合成观测

**Files:**
- Create: `internal/observe/observer.go`
- Test: `internal/observe/observer_test.go`

**Interfaces:**
- Consumes: Task 1 的 `ObservedState`/`Tristate`;Task 2 的 `supervisor.LookupRoute`/`guardian.BlockingBarrierCIDRs`
- Produces:
  - `type Deps struct { LookupRoute func(ctx, string, bool) (RouteResult, error); BarrierCIDRs func() (v4, v6 []string); InspectDNS func(ctx) (DNSResult, error); FetchRuntime func() (RuntimeResult, error); TunName func() string; Now func() time.Time }`
  - `type RouteResult struct { Interface string; Reject bool }`
  - `type DNSResult struct { Servers []string; Enabled bool }`
  - `type RuntimeResult struct { TunnelHealthy bool }`
  - `func Observe(ctx context.Context, deps Deps) ObservedState`

**为什么用注入而非直接调用:** 观测要能在 CI 里免 root 测试。本包定义自己的窄结果类型,由调用方做适配——这样 `internal/observe` 不 import `supervisor`/`install`/`guardian`,避免循环并保持可测。

- [ ] **Step 1: 写失败测试**

创建 `internal/observe/observer_test.go`:

```go
package observe

import (
	"context"
	"errors"
	"testing"
	"time"
)

func fixedDeps() Deps {
	return Deps{
		TunName:      func() string { return "utun4" },
		BarrierCIDRs: func() ([]string, []string) { return []string{"0.0.0.0/2"}, nil },
		LookupRoute: func(context.Context, string, bool) (RouteResult, error) {
			return RouteResult{Interface: "utun4"}, nil
		},
		InspectDNS: func(context.Context) (DNSResult, error) {
			return DNSResult{Servers: []string{"127.0.0.1"}, Enabled: true}, nil
		},
		FetchRuntime: func() (RuntimeResult, error) { return RuntimeResult{TunnelHealthy: true}, nil },
		Now:          func() time.Time { return time.Unix(1700000000, 0) },
	}
}

func TestObserveReportsHealthySystem(t *testing.T) {
	got := Observe(context.Background(), fixedDeps())
	if got.CaptureOK != True {
		t.Errorf("CaptureOK = %v, want True", got.CaptureOK)
	}
	if got.DNSManaged != True {
		t.Errorf("DNSManaged = %v, want True", got.DNSManaged)
	}
	if got.TunnelHealthy != True {
		t.Errorf("TunnelHealthy = %v, want True", got.TunnelHealthy)
	}
	if got.CoreSocket != True {
		t.Errorf("CoreSocket = %v, want True", got.CoreSocket)
	}
	if got.ObservedAt.IsZero() {
		t.Error("必须带观测时刻,消费者要据此判断新鲜度")
	}
}

// 劫持没生效时必须观测到 False,而不是沿用上次的记忆。
func TestObserveDetectsCaptureNotEffective(t *testing.T) {
	deps := fixedDeps()
	deps.LookupRoute = func(context.Context, string, bool) (RouteResult, error) {
		return RouteResult{Interface: "en0"}, nil
	}
	got := Observe(context.Background(), deps)
	if got.CaptureOK != False {
		t.Errorf("包走 en0 而非 TUN 时 CaptureOK 必须为 False,实际 = %v", got.CaptureOK)
	}
	if got.CaptureInterface != "en0" {
		t.Errorf("必须如实报告实际接口,实际 = %q", got.CaptureInterface)
	}
}

// 单项观测失败必须记为 Unknown 并附错误,且不得影响其余项。
func TestObserveIsolatesFailurePerItem(t *testing.T) {
	deps := fixedDeps()
	deps.InspectDNS = func(context.Context) (DNSResult, error) {
		return DNSResult{}, errors.New("networksetup timed out")
	}
	got := Observe(context.Background(), deps)
	if got.DNSManaged != Unknown {
		t.Errorf("DNS 观测失败时必须为 Unknown,实际 = %v", got.DNSManaged)
	}
	if got.CaptureOK != True {
		t.Errorf("一项失败不得影响其余项,CaptureOK = %v, want True", got.CaptureOK)
	}
	if len(got.Errors) == 0 {
		t.Error("必须记录失败原因,否则 Unknown 无从解释")
	}
}

// Core socket 不应答时,隧道健康必须是 Unknown 而不是 False——
// 问不出来不等于不健康。
func TestObserveUnknownTunnelWhenSocketSilent(t *testing.T) {
	deps := fixedDeps()
	deps.FetchRuntime = func() (RuntimeResult, error) { return RuntimeResult{}, errors.New("no such file") }
	got := Observe(context.Background(), deps)
	if got.CoreSocket != False {
		t.Errorf("socket 不应答时 CoreSocket = %v, want False", got.CoreSocket)
	}
	if got.TunnelHealthy != Unknown {
		t.Errorf("socket 不应答时隧道健康应为 Unknown(问不出来),实际 = %v", got.TunnelHealthy)
	}
}

// TUN 不存在(bx 未运行)时,劫持观测为 False 而非 Unknown:
// 没有 TUN 就是确定没劫持。
func TestObserveCaptureFalseWhenNoTun(t *testing.T) {
	deps := fixedDeps()
	deps.TunName = func() string { return "" }
	got := Observe(context.Background(), deps)
	if got.CaptureOK != False {
		t.Errorf("无 TUN 时 CaptureOK = %v, want False", got.CaptureOK)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/observe/ -run TestObserve -count=1`
Expected: FAIL,`undefined: Deps`

- [ ] **Step 3: 实现 Observer**

创建 `internal/observe/observer.go`:

```go
package observe

import (
	"context"
	"time"
)

// RouteResult 是本包对一次路由查询所需信息的窄视图。
type RouteResult struct {
	Interface string
	Reject    bool
}

// DNSResult 是本包对系统 DNS 状态所需信息的窄视图。
type DNSResult struct {
	Servers []string
	Enabled bool // resolver 是否为 bx
}

// RuntimeResult 是本包对 Core 自报状态所需信息的窄视图。
type RuntimeResult struct {
	TunnelHealthy bool
}

// Deps 是观测所需的外部能力。用注入而非直接 import,使本包免 root 可测,
// 且不与 supervisor/install/guardian 产生依赖循环。
type Deps struct {
	TunName      func() string
	BarrierCIDRs func() (ipv4, ipv6 []string)
	LookupRoute  func(ctx context.Context, destination string, ipv6 bool) (RouteResult, error)
	InspectDNS   func(ctx context.Context) (DNSResult, error)
	FetchRuntime func() (RuntimeResult, error)
	Now          func() time.Time
}

// captureProbes 是用于判定劫持是否生效的探测目的地。两个地址分别落在
// 0.0.0.0/1 与 128.0.0.0/1,覆盖 split-default 的两半。
var captureProbes = []string{"1.1.1.1", "129.1.1.1"}

// Observe 向系统现问,返回某一时刻的事实。
//
// 它绝不改动系统,绝不因某项失败而中断:任一项出错即记为 Unknown 并附原因,
// 继续观测其余项。这是刻意的——观测失败不该让保护中断,也不该让调用方失败。
func Observe(ctx context.Context, deps Deps) ObservedState {
	state := ObservedState{}
	if deps.Now != nil {
		state.ObservedAt = deps.Now()
	}

	state.CaptureOK, state.CaptureInterface = observeCapture(ctx, deps, &state)
	state.BarrierPresent = observeBarrier(ctx, deps, &state)
	state.DNSManaged, state.DNSServers = observeDNS(ctx, deps, &state)
	state.CoreSocket, state.TunnelHealthy = observeCore(deps, &state)

	return state
}

func observeCapture(ctx context.Context, deps Deps, state *ObservedState) (Tristate, string) {
	if deps.TunName == nil || deps.LookupRoute == nil {
		return Unknown, ""
	}
	tun := deps.TunName()
	if tun == "" {
		// 没有 TUN 就是确定没有劫持,不是"问不出来"。
		return False, ""
	}
	firstInterface := ""
	for _, destination := range captureProbes {
		selection, err := deps.LookupRoute(ctx, destination, false)
		if err != nil {
			state.Errors = append(state.Errors, ObserveError{Item: "capture_ok", Err: err.Error()})
			return Unknown, firstInterface
		}
		if firstInterface == "" {
			firstInterface = selection.Interface
		}
		if selection.Interface != tun {
			return False, selection.Interface
		}
	}
	return True, firstInterface
}

func observeBarrier(ctx context.Context, deps Deps, state *ObservedState) Tristate {
	if deps.BarrierCIDRs == nil || deps.LookupRoute == nil {
		return Unknown
	}
	ipv4, _ := deps.BarrierCIDRs()
	if len(ipv4) == 0 {
		return Unknown
	}
	// 用首个阻断网段的网络地址代表整组:它们同装同拆,查一条即可判定在位与否。
	selection, err := deps.LookupRoute(ctx, barrierProbeAddress(ipv4[0]), false)
	if err != nil {
		state.Errors = append(state.Errors, ObserveError{Item: "barrier_present", Err: err.Error()})
		return Unknown
	}
	return FromBool(selection.Reject)
}

// barrierProbeAddress 从 CIDR 取出可用于 route -n get 的地址。
func barrierProbeAddress(cidr string) string {
	for i := 0; i < len(cidr); i++ {
		if cidr[i] == '/' {
			return cidr[:i]
		}
	}
	return cidr
}

func observeDNS(ctx context.Context, deps Deps, state *ObservedState) (Tristate, []string) {
	if deps.InspectDNS == nil {
		return Unknown, nil
	}
	result, err := deps.InspectDNS(ctx)
	if err != nil {
		state.Errors = append(state.Errors, ObserveError{Item: "dns_managed", Err: err.Error()})
		return Unknown, nil
	}
	return FromBool(result.Enabled), result.Servers
}

func observeCore(deps Deps, state *ObservedState) (socket, tunnel Tristate) {
	if deps.FetchRuntime == nil {
		return Unknown, Unknown
	}
	result, err := deps.FetchRuntime()
	if err != nil {
		state.Errors = append(state.Errors, ObserveError{Item: "core_socket", Err: err.Error()})
		// socket 不应答是确定的观测;但隧道健康问不出来,必须是 Unknown。
		return False, Unknown
	}
	return True, FromBool(result.TunnelHealthy)
}
```

- [ ] **Step 4: 跑测试确认通过并提交**

```bash
go test ./internal/observe/ -count=1
go build ./... && go vet ./...
git add internal/observe/observer.go internal/observe/observer_test.go
git commit -m "feat(observe): 组合观测原语,单项失败不连累其余"
```

---

### Task 4: 接线到 `bx status --json` 并加入第五条不变量

**Files:**
- Create: `internal/observe/json.go`(`Tristate` 的 JSON 表示)
- Create: `internal/observe/wire.go`(把 supervisor/install/guardian 适配成 `Deps`)
- Modify: `internal/cli/cli.go`(`clientStatusReport` 增加 `observed`/`divergence` 字段)
- Test: `internal/observe/json_test.go`、`internal/cli/cli_test.go`(追加)
- Test: `internal/observe/invariants_test.go`(第五条不变量)

**Interfaces:**
- Consumes: Task 1-3 的全部产出
- Produces:
  - `func (t Tristate) MarshalJSON() ([]byte, error)`
  - `func LiveDeps(tunName string, socketPath string) Deps`
  - `clientStatusReport` 新增字段 `Observed *observe.ObservedState`、`Divergence []observe.Divergence`

**只增不改:** 现有 `clientStatusReport` 的所有字段与取值逻辑一个不动,只追加两个字段。这样人类的 `bx status` 与既有测试全部不受影响。

- [ ] **Step 1: 写失败测试(JSON 表示)**

创建 `internal/observe/json_test.go`:

```go
package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

// 三态必须序列化成字符串。数字对 agent 无意义,而本包的全部价值在于让 agent 读懂。
func TestTristateMarshalsAsString(t *testing.T) {
	payload, err := json.Marshal(map[string]Tristate{"a": True, "b": False, "c": Unknown})
	if err != nil {
		t.Fatal(err)
	}
	got := string(payload)
	for _, want := range []string{`"true"`, `"false"`, `"unknown"`} {
		if !strings.Contains(got, want) {
			t.Errorf("序列化结果缺少 %s,实际 = %s", want, got)
		}
	}
	if strings.Contains(got, ":0") || strings.Contains(got, ":1") || strings.Contains(got, ":2") {
		t.Errorf("三态不得序列化为数字,实际 = %s", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败** → **Step 3: 实现 `MarshalJSON`**

创建 `internal/observe/json.go`:

```go
package observe

import "encoding/json"

// MarshalJSON 让三态在 JSON 里是 "true"/"false"/"unknown" 而非数字。
// 数字对 agent 无意义,而本包的全部价值在于让 agent 读懂。
func (t Tristate) MarshalJSON() ([]byte, error) {
	return json.Marshal(t.String())
}
```

- [ ] **Step 4: 写第五条不变量的测试(本期已知失败)**

创建 `internal/observe/invariants_test.go`:

```go
package observe

import "testing"

// 不变量 5:任何「减少 bx 足迹」的操作,在任何前置条件缺失下都必须能执行。
//
// 本期不实现该不变量(属控制面期次)。此测试刻意留作**已知失败**,让"控制面还没修"
// 成为 CI 里可见的事实,而不是待办清单里的一行字。
//
// 真实事故:bx down 因解析不到默认网关、Guardian 起不来、recoveryBlocked 三种前置
// 缺失而拒绝执行,用户被锁在断网状态,最终只能 uninstall 脱身。
//
// 实现该不变量后,删除 t.Skip 并补上真实断言。
func TestInvariantTeardownNeverRefuses(t *testing.T) {
	t.Skip("控制面期次未实现:bx down 仍会因前置条件缺失而拒绝执行。见 " +
		"docs/superpowers/specs/2026-08-05-observation-layer-design.md 不变量 5")
}
```

**注意:** 设计文档要求「标记而非跳过」。Go 没有原生的 known-failure 标记,`t.Skip` 配合
**明确的原因字符串**是本仓库可用的最接近形式——`go test -v` 会打印该原因,CI 日志里可见。
实现时不要把这条测试删掉或改成通过。

- [ ] **Step 5: 实现生产接线**

创建 `internal/observe/wire.go`,把三个包适配成 `Deps`:

```go
package observe

import (
	"context"
	"time"

	"github.com/getbx/bx/internal/guardian"
	"github.com/getbx/bx/internal/install"
	"github.com/getbx/bx/internal/supervisor"
)

// LiveDeps 把生产环境的真实能力适配成 Deps。
//
// 适配层放在本包而非各自包里,是为了让 internal/observe 的核心保持零依赖、可测;
// 只有这一个文件 import 外部包。
func LiveDeps(tunName, socketPath string) Deps {
	return Deps{
		TunName:      func() string { return tunName },
		BarrierCIDRs: guardian.BlockingBarrierCIDRs,
		Now:          time.Now,
		LookupRoute: func(ctx context.Context, destination string, ipv6 bool) (RouteResult, error) {
			selection, err := supervisor.LookupRoute(ctx, destination, ipv6)
			if err != nil {
				return RouteResult{}, err
			}
			return RouteResult{Interface: selection.Interface, Reject: selection.Reject}, nil
		},
		InspectDNS: func(ctx context.Context) (DNSResult, error) {
			status, err := install.InspectDNSContext(ctx, "")
			if err != nil {
				return DNSResult{}, err
			}
			return DNSResult{Servers: status.Servers, Enabled: status.Enabled}, nil
		},
		FetchRuntime: func() (RuntimeResult, error) {
			state, err := supervisor.FetchRuntimeState(socketPath)
			if err != nil {
				return RuntimeResult{}, err
			}
			return RuntimeResult{TunnelHealthy: state.TunnelHealthy}, nil
		},
	}
}
```

- [ ] **Step 6: 接进 `bx status --json`**

在 `internal/cli/cli.go` 的 `clientStatusReport` 结构体**末尾追加**两个字段(不改任何既有字段):

```go
	Observed   *observe.ObservedState `json:"observed,omitempty"`
	Divergence []observe.Divergence   `json:"divergence,omitempty"`
```

在组装该报告的函数里,于返回前追加观测(观测失败不得让 status 失败):

```go
	// 观测只读且尽力而为:任何失败都不该让 bx status 失败。
	observed := observe.Observe(ctx, observe.LiveDeps(report.TunName, statusSocketPath()))
	report.Observed = &observed
	report.Divergence = observe.Diverge(
		observe.Intent{Desired: desiredForReport},
		observed,
		observe.Believed{Protection: report.ProtectionState, Phase: report.Phase, LastError: report.LastError},
	)
```

**实现者注意:** `report.TunName`、`desiredForReport`、`report.ProtectionState`/`Phase`/`LastError`
的确切字段名与取得方式需按 `internal/cli/cli.go` 实际结构对齐——本仓库该文件超过 4700 行,
**请先读实际代码再动手**,不要照抄上面的名字。若某个值在该函数里拿不到,**停下来报告
NEEDS_CONTEXT**,不要为了凑合而改动既有控制流。

- [ ] **Step 7: 写接线的回归测试**

在 `internal/cli/cli_test.go` 追加一条:断言 `bx status --json` 的输出中,当观测与信念不一致时
`divergence` 非空;一致时为空。用假的 `Deps` 注入,**不得**调用真实系统。若当前 `clientStatusReport`
的组装函数不可注入,先做最小重构使其可测。

- [ ] **Step 8: 全量验证并提交**

```bash
go test ./... -count=1
go build ./... && go vet ./...
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
git add internal/observe/ internal/cli/
git commit -m "feat(observe): status 并列发布观测与信念,暴露二者差异"
```

---

### Task 5: 文档与全量验证

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: 记录设计支点与不变量**

在 macOS 段落补记:

- **意图 / 事实 / 代码 三分**:意图只由用户/agent 显式声明式改动;事实只由 reconcile 改动
  (本期尚无 reconcile,观测只读);代码由人经 PR 评审改动。**agent 只声明意图,从不直接驱动动作。**
- `internal/observe` 是只读观测层:向系统现问劫持是否生效、屏障是否在位、DNS 归谁、Core 是否活着。
  三态区分「观测不到」与「观测到否」,零值为 Unknown。
- `bx status --json` 现同时发布 `observed` 与 `divergence`,**不用观测覆盖信念**——二者的 diff
  本身就是最高价值的诊断信号。
- 首批四条不变量已由 `internal/observe` 的表驱动测试钉住;第五条(拆除永不拒绝)**本期未实现**,
  以已知失败的测试形式留在 `invariants_test.go`。

**注明本期全部为纯逻辑与单测,真机未验。**

- [ ] **Step 2: 全量验证**

```bash
go build ./... && go vet ./... && go test ./... -count=1
go test -race ./internal/observe ./internal/guardian ./internal/cli -count=1
GOOS=linux   GOARCH=amd64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
GOOS=darwin  GOARCH=arm64 go build -o /dev/null ./...
scripts/test-macos-menu.sh
git diff --check
```
Expected: 全部 rc=0

- [ ] **Step 3: 提交并停在重建之前**

```bash
git add CLAUDE.md
git commit -m "docs: 记录观测层与意图/事实/代码三分"
```

**不要重建、不要重装、不要重启用户机器上运行中的 bx。** 向用户报告验证结果,并说明下次重建后
可以拿到的东西:

```bash
# 重建安装后,第一次看到真机上"信念"与"事实"的实际差异
bx status --json | python3 -m json.tool | grep -A 30 '"observed"'
bx status --json | python3 -m json.tool | grep -A 20 '"divergence"'

# 预期:保护正常时 divergence 为空;
# 若非空,每一条都会写明哪个字段、信念是什么、观测是什么、以及为什么要紧
```
