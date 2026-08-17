# Guardian 状态 watch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让菜单栏图标在 `bx up` / `bx down` 之后**立刻**变,而不是等一个 30 秒的常量。

**Architecture:** Guardian 的 `/v1/status` 长出一个长轮询形态 `?wait=<generation>`。
代际号**由内容派生**:唯一的发布点重算 `Status`、取一个排除了易变字段的投影、
与上次比,不同才 `++`。广播(只有 `/v1/up`、`/v1/down` 两处)不携带数据、只是
「现在就重算」,所以漏一个广播只是慢几秒。菜单改走 watch 循环,并保留一条与
watch 健康判断无关的慢速兜底轮询。

**Tech Stack:** Go 1.26(`crypto/sha256`、`encoding/json`、`reflect`、`sync`)、
Swift(`apps/macos/BxMenu`,raw socket + 手拼 HTTP)、既有的 `Capabilities`
能力声明与 `beginShutdown` 关机范式。

**设计依据:** `docs/superpowers/specs/2026-08-17-guardian-status-watch-design.md`。
**读那份 spec 的「投影」与「失效模式」两节** —— 本计划的每个判断都从那里来。

## Global Constraints

以下每一条都是全局要求,每个 task 的验收隐含包含:

- **投影(digest)= 整个 `Status` 减一张明确的排除名单,不是白名单。** 新字段
  **默认参与**。方向是刻意的:将来加一个易变字段会让 watch 疯狂触发(吵、当场
  可见);默认不参与则是菜单静默地不再对新信号反应(安静、只有用户抱怨时才发现)。
- **排除名单里每一条都要写明它为什么易变。** 名单:`StatusGeneration`(自激)、
  `Core.LatencyMS`、`Core.FailingRules[].Attempts`/`.Failures`(**只零计数,
  保留 `Kind`/`Rule`**)、`Reconcile.At`、`Recovery.UpdatedAt`。
- **深拷贝是承重的。** `Core`/`Reconcile` 是指针、`FailingRules` 是切片 ——
  在「副本」里清零会改到同一个底层数组,让**真正发布出去的** `Status` 计数变成 0。
- **`!=` 而不是 `>`** 比较代际号:Guardian 重启后代际号从头开始,`>` 会让请求
  永久挂住。
- **关机不许被 parked 请求拖住。** `server.Shutdown` 会等 handler 返回,而升级时
  Guardian 要被 bootout —— 这个项目在「关机慢」上栽过 71 分钟。
- **兜底轮询与 watch 的健康判断无关。** 一个会被 watch 自己的健康判断影响的兜底,
  在那个判断错的时候恰好也是坏的。
- **绝不「试着拨一下看看」**:走 `Capabilities` 能力门控(既有 `/v1/rules`、
  `/v1/servers` 已是此形状)。
- 具体数字(互相约束,不许各猜一个):服务端挂住 **25s**、`timeout` 参数钳到
  **[1s, 25s]**、客户端 watch 超时 **40s**、重算兵底 **3s**、菜单兜底轮询 **60s**、
  无 watch 时的关闭档轮询**保持 30s**。
- **绝不启动 bx、绝不改路由、不要 sudo。** 全部测试免 root。
- 中文 conventional commits,结尾 `Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>`。
  在 `master` 直接提交(单人项目,项目所有者已授权)。
- 每个 task 收尾跑 `bash scripts/verify.sh --quick`,**前台跑并显式收退出码**
  (`bash scripts/verify.sh --quick; echo "EXIT=$?"`)—— 后台跑实测会中途变孤儿。
  最后一个 task 跑全量 `bash scripts/verify.sh`。

---

## File Structure

**新建:**

| 文件 | 责任 |
|---|---|
| `internal/guardian/statusdigest.go` | `statusDigest(Status) (string, error)` + 排除名单(每条带理由)。纯函数。 |
| `internal/guardian/statusdigest_test.go` | 反射遍历顶层字段的守卫 + 嵌套易变字段的表驱动测试 + `representativeStatus()` fixture |
| `internal/guardian/statuswatch.go` | `statusPublisher`:代际号、`publish`(带合并)、`wait`、`poke`、`beginShutdown` |
| `internal/guardian/statuswatch_test.go` | 并发行为:立刻返回 / 挂住 / poke 唤醒 / 超时 / 关机立刻返回 / `wait` 大于当前不挂死 |
| `apps/macos/BxMenu/Sources/BxMenu/StatusWatch.swift` | watch 循环的**纯判据**部分(下一步该拨什么、退避多久),好让它编得进测试套件 |
| `apps/macos/BxMenu/Tests/StatusWatchTests.swift` | 上者的测试 |

**修改:**

| 文件 | 改什么 |
|---|---|
| `internal/guardian/types.go` | `Status.StatusGeneration uint64`(**无 omitempty**)、`CapabilityStatusWatch`、进 `GuardianCapabilities()` |
| `internal/guardian/localapi.go` | `localAPI` 加 `watch *statusPublisher`;`/v1/status` handler 解析 `wait`/`timeout`;`mutationHandler` 落定后 `poke`;`beginShutdown` 唤醒 parked waiter |
| `internal/guardian/client.go` | `func (c *Client) StatusWatch(ctx, generation uint64) (Status, error)` |
| `internal/cli/cli.go` | `statusFlags()` 加 `--watch`;`statusAction` 的 watch 循环 |
| `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift` | `.statusWatch(generation:)` endpoint + 40s 超时 + `guardianRequest(for:)` 里的 path |
| `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift` | 解 `status_generation` |
| `apps/macos/BxMenu/Sources/BxMenu/MenuCadence.swift` | 兜底轮询常量;`menuPollInterval` 保持不变(无 watch 时的降级路径) |
| `apps/macos/BxMenu/Sources/BxMenu/main.swift` | watch 循环接线;rules/servers 改按需拉 |
| `apps/macos/BxMenu/Tests/MenuCadenceTests.swift` | 重写 `closed >= 20` 那条守卫(它钉住的理由已被证伪) |
| `scripts/test-macos-menu.sh` | 注册 `status-watch` 套件 |
| `CLAUDE.md` | 收尾那一节 |

---

## Task 1: `statusDigest` —— 投影与排除名单

**Files:**
- Create: `internal/guardian/statusdigest.go`
- Test: `internal/guardian/statusdigest_test.go`

**Interfaces:**
- Consumes: `Status`、`CoreRuntime`、`FailingRule`、`ReconcileReport`、`RecoverySnapshot`(全在 `internal/guardian/types.go`)
- Produces:
  - `func statusDigest(s Status) (string, error)` —— 返回 sha256 hex;`error` 非 nil 时调用方**必须当作「变了」**(见下)
  - `func representativeStatus() Status`(测试文件里,供 Task 2 复用)

- [ ] **Step 1: 写失败测试 —— 反射守卫 + 嵌套易变字段**

创建 `internal/guardian/statusdigest_test.go`:

```go
package guardian

import (
	"reflect"
	"testing"
	"time"
)

// representativeStatus 是一份**每个字段都非零**的 Status。
//
// 非零是承重的:反射守卫靠「改一个字段、看投影变不变」工作,而从零值改成非零
// 与从非零改成另一个非零,在一个写错了的 statusDigest 面前不等价
// (比如一个只在指针非 nil 时才纳入嵌套内容的实现)。
func representativeStatus() Status {
	at := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	return Status{
		SchemaVersion:     1,
		Desired:           DesiredOn,
		Phase:             Phase("protected"),
		CorePID:           4321,
		CoreVersion:       "0.3.0",
		Protection:        ProtectionProtected,
		NetworkGeneration: "gen-7",
		Recovery: RecoverySnapshot{
			ID: "rec-1", State: "idle", Stage: "idle", Reason: "manual",
			Generation: "gen-7", ErrorCode: "", Detail: "d",
			Attempt: 2, StartedAt: at, UpdatedAt: at,
		},
		LastError:       "boom",
		GuardianVersion: "g1",
		RuntimeVersion:  "r1",
		DNSState:        DNSState("managed"),
		DNSManaged:      true,
		DNSService:      "Wi-Fi",
		DNSServers:      []string{"127.0.0.1"},
		Core: &CoreRuntime{
			Reachable: true, TunnelHealthy: true, LatencyMS: 390,
			Server: "vps", Transport: "reality", UDPMode: "proxy",
			UDPTransport: "hysteria2://x", DNSUpstream: "223.5.5.5",
			FailingRules: []FailingRule{{Kind: "direct", Rule: "*.qq.com", Attempts: 100, Failures: 99}},
		},
		Capabilities:    []string{CapabilityRules},
		Reconcile:       &ReconcileReport{At: at, Actions: []string{"start_core"}},
		MaintenanceHold: &MaintenanceHoldStatus{Reason: "upgrade", ExpiresAt: at},
	}
}

// digestExclusions 是**顶层**字段的排除名单。值是理由 —— 一条没有理由的排除,
// 下一个人无从判断它还该不该在名单里。
//
// 嵌套的易变字段(Core.LatencyMS 等)不在这里,由
// TestVolatileNestedFieldsDoNotMoveTheDigest 单独钉。
var digestExclusions = map[string]string{
	"StatusGeneration": "代际号自己进投影就会让每次 bump 都让下一次比对不同,永久自激",
}

// **本测试是整个 watch 功能里最重要的一条。**
//
// 它钉住的不是「投影算得对」,而是「**没有字段被静默排除在 watch 之外**」:
// 将来谁往 Status 加一个字段,不改任何测试它也自动参与投影;要排除它就必须
// 显式加进 digestExclusions 并写明理由。
//
// 方向是刻意的(见 spec「投影」一节):默认参与,则加一个易变字段会让 watch
// 疯狂触发 —— 吵,但当场看得见;默认不参与,则加一个要紧字段会让菜单静默地
// 不再对它反应 —— 安静,只有用户抱怨时才发现。代价不对称。
func TestEveryStatusFieldParticipatesInTheDigest(t *testing.T) {
	base := representativeStatus()
	baseline, err := statusDigest(base)
	if err != nil {
		t.Fatalf("基准投影算不出来: %v", err)
	}

	typ := reflect.TypeOf(base)
	if typ.NumField() < 15 {
		t.Fatalf("Status 只有 %d 个字段,少得反常 —— 这条守卫可能已经读不到它要守的东西了", typ.NumField())
	}
	for i := range typ.NumField() {
		field := typ.Field(i)
		mutated := representativeStatus()
		mutateFieldForDigest(t, field.Name, reflect.ValueOf(&mutated).Elem().Field(i))

		got, err := statusDigest(mutated)
		if err != nil {
			t.Fatalf("改了 %s 之后投影算不出来: %v", field.Name, err)
		}
		if why, excluded := digestExclusions[field.Name]; excluded {
			if got != baseline {
				t.Errorf("Status.%s 在排除名单里(%s),但改它改变了投影 —— 名单与实现不一致", field.Name, why)
			}
			continue
		}
		if got == baseline {
			t.Errorf("改了 Status.%s 而投影没变 —— 这个字段被**静默排除**在 watch 之外了。"+
				"要么它该参与(修 statusDigest),要么它是易变字段(加进 digestExclusions 并写明理由)。"+
				"静默不参与意味着菜单永远不会因为这个字段的变化而更新,而没有任何东西会报错", field.Name)
		}
	}
}

// mutateFieldForDigest 把一个字段改成一个与 representativeStatus 不同的值。
//
// **读不懂的类型必须响亮失败**,不许静默跳过:一个遇到新 Kind 就放过的守卫,
// 在最需要它的时候(有人加了个新类型的字段)恰好是失效的。
func mutateFieldForDigest(t *testing.T, name string, v reflect.Value) {
	t.Helper()
	switch v.Kind() {
	case reflect.String:
		v.SetString(v.String() + "-changed")
	case reflect.Bool:
		v.SetBool(!v.Bool())
	case reflect.Int, reflect.Int64, reflect.Int32:
		v.SetInt(v.Int() + 1)
	case reflect.Uint, reflect.Uint64, reflect.Uint32:
		v.SetUint(v.Uint() + 1)
	case reflect.Slice:
		if v.Type().Elem().Kind() != reflect.String {
			t.Fatalf("字段 %s 是 %s 的切片,本守卫的变异器读不懂它 —— 请连同它一起扩写,别让它静默通过", name, v.Type().Elem())
		}
		v.Set(reflect.Append(v, reflect.ValueOf("digest-probe")))
	case reflect.Pointer:
		if v.IsNil() {
			t.Fatalf("字段 %s 在 representativeStatus 里是 nil —— fixture 必须每个字段都非零,否则这条守卫在它身上是空转的", name)
		}
		mutateFieldForDigest(t, name+".(elem)", v.Elem())
	case reflect.Struct:
		if t0, ok := v.Interface().(time.Time); ok {
			v.Set(reflect.ValueOf(t0.Add(time.Hour)))
			return
		}
		for i := range v.NumField() {
			f := v.Field(i)
			if f.Kind() == reflect.String && f.CanSet() {
				mutateFieldForDigest(t, name+"."+v.Type().Field(i).Name, f)
				return
			}
		}
		t.Fatalf("字段 %s 是结构体但找不到可改的字符串字段 —— 请扩写变异器", name)
	default:
		t.Fatalf("字段 %s 的类型 %s 本守卫读不懂 —— 请扩写变异器,别让它静默通过", name, v.Kind())
	}
}

// 易变字段单独改动时,投影**必须不动**。
//
// 每一条都是真机上会持续变的东西;不排除它们,watch 触发得比今天 30 秒轮询
// 还频,而这个坑在别的测试里发现不了(那些测试的 latency 是固定 fixture)。
func TestVolatileNestedFieldsDoNotMoveTheDigest(t *testing.T) {
	baseline, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("基准投影: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Status)
		why    string
	}{
		{"Core.LatencyMS", func(s *Status) { s.Core.LatencyMS = 412 }, "每次健康探测都抖"},
		{"Core.FailingRules[].Attempts", func(s *Status) { s.Core.FailingRules[0].Attempts = 12345 }, "每条连接都在涨"},
		{"Core.FailingRules[].Failures", func(s *Status) { s.Core.FailingRules[0].Failures = 12344 }, "每条连接都在涨"},
		{"Reconcile.At", func(s *Status) { s.Reconcile.At = time.Now() }, "recordReconcileRound 每轮都盖时间戳"},
		{"Recovery.UpdatedAt", func(s *Status) { s.Recovery.UpdatedAt = time.Now() }, "恢复进行中每次轮询都换"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := representativeStatus()
			tc.mutate(&s)
			got, err := statusDigest(s)
			if err != nil {
				t.Fatalf("投影: %v", err)
			}
			if got != baseline {
				t.Errorf("只改了 %s(%s)投影就变了 —— watch 会跟着它持续触发,比 30 秒轮询更差", tc.name, tc.why)
			}
		})
	}
}

// 反过来:同一条规则**开始**成片失败是真事件,必须让投影变。
// 只零计数、保留 Kind/Rule,就是为了这个。
func TestANewFailingRuleMovesTheDigest(t *testing.T) {
	baseline, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("基准投影: %v", err)
	}
	s := representativeStatus()
	s.Core.FailingRules = append(s.Core.FailingRules, FailingRule{Kind: "proxy", Rule: "*.example.com"})
	got, err := statusDigest(s)
	if err != nil {
		t.Fatalf("投影: %v", err)
	}
	if got == baseline {
		t.Error("多了一条成片失败的规则而投影没变 —— 只该零掉计数,不该把整条规则也排除掉")
	}
}

// **statusDigest 绝不许改到调用方那份 Status。**
//
// Core/Reconcile 是指针、FailingRules 是切片:复制 Status 只复制切片头,
// 在「副本」里清零改的是同一个底层数组,于是**真正发布出去的**那份 Status 里
// 计数变成 0。一个会污染它所要度量的东西的 digest,比没有 digest 更糟。
func TestStatusDigestDoesNotMutateItsInput(t *testing.T) {
	s := representativeStatus()
	if _, err := statusDigest(s); err != nil {
		t.Fatalf("投影: %v", err)
	}
	if s.Core.LatencyMS != 390 {
		t.Errorf("Core.LatencyMS 被 statusDigest 改成了 %d —— 它污染了调用方的 Status", s.Core.LatencyMS)
	}
	if s.Core.FailingRules[0].Attempts != 100 || s.Core.FailingRules[0].Failures != 99 {
		t.Errorf("FailingRules 的计数被清零了(%+v)—— 切片只复制了头,清零改的是同一个底层数组",
			s.Core.FailingRules[0])
	}
	if s.Reconcile.At.IsZero() {
		t.Error("Reconcile.At 被清零了 —— 指针指向的是调用方那份")
	}
	if s.Recovery.UpdatedAt.IsZero() {
		t.Error("Recovery.UpdatedAt 被清零了")
	}
}

// 同一份 Status 算两次必须相同 —— 否则代际号会每次重算都 bump。
func TestStatusDigestIsStable(t *testing.T) {
	a, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("投影: %v", err)
	}
	b, err := statusDigest(representativeStatus())
	if err != nil {
		t.Fatalf("投影: %v", err)
	}
	if a != b {
		t.Fatalf("同一份 Status 两次投影不同(%s vs %s)—— watch 会永久自激", a, b)
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/guardian/ -run 'TestEveryStatusField|TestVolatileNested|TestANewFailingRule|TestStatusDigestDoes|TestStatusDigestIsStable' -v`
Expected: FAIL —— `undefined: statusDigest`(`Status` 若还没有 `StatusGeneration` 字段,`digestExclusions` 那条会在反射循环里找不到同名字段而不生效;本 task 先只加 `statusDigest`,`StatusGeneration` 字段在 Task 3 加 —— **所以这一步预期还会看到 `TestEveryStatusFieldParticipatesInTheDigest` 通过而排除名单未被用到**。见 Step 3 的处置。)

- [ ] **Step 3: 加 `Status.StatusGeneration`(本 task 就加,别拖到 Task 3)**

排除名单如果指向一个不存在的字段,那条守卫就是空转的。所以字段现在就加 ——
它此刻还没有人填,值恒为 0,不影响任何既有行为。

在 `internal/guardian/types.go` 的 `Status` 结构体里,`SchemaVersion` 之后加:

```go
	// StatusGeneration 是**内容派生**的单调计数器:Guardian 每次发现自己发布的
	// Status(减去易变字段的投影,见 statusDigest)与上一次不同,它就 +1。
	//
	// 客户端把手上这个值经 `GET /v1/status?wait=<gen>` 发回来,Guardian 在
	// `current != wait` 时立刻应答、相同则挂住。**比较用 `!=` 而不是 `>`**:
	// Guardian 重启后这个计数器从头开始,`>` 会让客户端手上那个较大的值
	// 永久挂住。
	//
	// **刻意没有 omitempty。** 键缺席 = 这一版 Guardian 没有 watch 这个概念
	// (升级窗口里的旧 Guardian);键在而值为 0 = 有这个概念、还没发布过。
	// 两者对客户端意味着不同的行为(降级轮询 vs 正常 watch),而 omitempty 会
	// 把它们压成同一个形状(与 Capabilities 同一条纪律)。
	StatusGeneration uint64 `json:"status_generation"`
```

- [ ] **Step 4: 写 `statusdigest.go`**

```go
package guardian

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// statusDigest 是 Status 的**投影**:除掉一批持续在变的字段之后的内容指纹。
// 代际号由它派生 —— 投影不同才 bump(见 statusPublisher)。
//
// **投影 = 整个 Status 减一张排除名单,不是一份白名单。** 方向是刻意的:
// 新加的字段默认参与,于是将来谁加一个易变字段会让 watch 疯狂触发(吵、当场
// 看得见);默认不参与则是菜单静默地不再对新信号反应(安静,只有用户抱怨时
// 才发现)。代价不对称,默认值站在「吵」那边 —— 与 Class 零值取 ClassRisky、
// leakcheck.Section 零值取 SectionPath 同一条纪律。
//
// **本函数绝不改动调用方那份 Status。** Core/Reconcile 是指针、FailingRules 是
// 切片,所以下面每一处都显式复制:在「副本」里清零会改到同一个底层数组,让
// 真正发布出去的 Status 计数变成 0 —— 一个污染它所要度量的东西的 digest,
// 比没有 digest 更糟。(GuardianCapabilities 头上那句注释说的是同一条纪律。)
//
// error 非 nil 时调用方**必须当作「变了」**:那意味着 Status 里有编不了码的
// 东西,是编程错误。当作「变了」会让 watch 每个兵底拍都触发 —— 吵、且有日志,
// 而当作「没变」会让菜单**静默冻住**。同一条不对称。
func statusDigest(s Status) (string, error) {
	// 代际号自己不进投影:进了就每次 bump 都让下一次比对不同,永久自激。
	s.StatusGeneration = 0

	if s.Core != nil {
		core := *s.Core
		// 每次健康探测都抖:390 → 412 → 388。
		core.LatencyMS = 0
		if len(core.FailingRules) != 0 {
			// **必须新分配。** copy 到一条新切片上再清零,否则改的是调用方的数组。
			rules := make([]FailingRule, len(core.FailingRules))
			copy(rules, core.FailingRules)
			for i := range rules {
				// 每条连接都在涨。**只零计数,保留 Kind/Rule** ——
				// 「这条规则开始成片失败」是真事件,「它又多失败了 3 次」不是。
				rules[i].Attempts = 0
				rules[i].Failures = 0
			}
			core.FailingRules = rules
		}
		s.Core = &core
	}

	if s.Reconcile != nil {
		report := *s.Reconcile
		// recordReconcileRound 是唯一写入口且**每轮都盖时间戳**。不排除它,
		// watch 会跟着调谐环每 30 秒到 10 分钟触发一次。
		// 只排 At:Actions/Held 变了是真事件。
		report.At = time.Time{}
		s.Reconcile = &report
	}

	// 恢复进行中每次轮询都换。只排它:State/Stage/Attempt/ErrorCode 变了都是
	// 真事件,而菜单那个「Connecting — N 秒」计数器由它自己的本地 toggleTicker
	// 驱动,不靠推送走字。
	s.Recovery.UpdatedAt = time.Time{}

	encoded, err := json.Marshal(s)
	if err != nil {
		return "", fmt.Errorf("投影 Status: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
```

- [ ] **Step 5: 跑测试,确认通过**

Run: `go test ./internal/guardian/ -run 'TestEveryStatusField|TestVolatileNested|TestANewFailingRule|TestStatusDigestDoes|TestStatusDigestIsStable' -v`
Expected: PASS(5 组,`TestVolatileNestedFieldsDoNotMoveTheDigest` 有 5 个子测试)

若 `TestEveryStatusFieldParticipatesInTheDigest` 在某个字段上失败,**先判断是哪一类**:
该字段确实是易变的 ⇒ 加进 `digestExclusions` 或 `statusDigest` 并写明理由;
不是 ⇒ `statusDigest` 漏了它,修实现。**不要为了让测试变绿而放宽断言。**

- [ ] **Step 6: 变异验证守卫真的会红(两个方向)**

**方向一 —— 静默排除会被抓到。** 在 `statusDigest` 里 `s.StatusGeneration = 0`
之后临时加一行 `s.DNSService = ""`(模拟「某个字段被静默排除」):

Run: `go test ./internal/guardian/ -run TestEveryStatusFieldParticipatesInTheDigest -v`
Expected: FAIL,信息含 `改了 Status.DNSService 而投影没变` 与 `被**静默排除**`

删掉那行,重跑 ⇒ PASS。

**方向二 —— 污染输入会被抓到。** 把 `rules := make(...)` + `copy(...)` 那两行
临时改成 `rules := core.FailingRules`:

Run: `go test ./internal/guardian/ -run TestStatusDigestDoesNotMutateItsInput -v`
Expected: FAIL,信息含 `切片只复制了头`

改回来,重跑 ⇒ PASS。

- [ ] **Step 7: verify + 提交**

```bash
bash scripts/verify.sh --quick; echo "EXIT=$?"
git add internal/guardian/statusdigest.go internal/guardian/statusdigest_test.go internal/guardian/types.go
git commit -m "$(cat <<'EOF'
feat(guardian): Status 的投影 —— watch 的代际号由内容派生

代际号不能由各个事件点自己数(「新加一条改状态的路忘了 bump」不会有编译错误、
也不会有测试转红,正是这个仓库反复栽的形状),所以它由内容派生:算一个投影、
与上次比,不同才 +1。

**投影 = 整个 Status 减一张排除名单,不是白名单。** 默认参与的方向是刻意的:
将来加一个易变字段会让 watch 疯狂触发(吵、当场看得见),默认不参与则是菜单
静默地不再对新信号反应(安静,只有用户抱怨时才发现)。

排除名单每条都带理由:StatusGeneration(自激)、Core.LatencyMS(每次探测都抖)、
FailingRules 的计数(每条连接都涨,但保留 Kind/Rule —— 「开始成片失败」是真事件)、
Reconcile.At(每轮都盖时间戳)、Recovery.UpdatedAt(恢复中每次轮询都换)。

两条守卫都变异验证过:反射遍历顶层字段抓「静默排除」,另一条抓「污染输入」——
FailingRules 是切片,在「副本」里清零改的是同一个底层数组,发布出去的计数会变成
0,那比没有 digest 更糟。变异器读不懂的类型一律 t.Fatal:一个遇到新 Kind 就放过的
守卫,在最需要它的时候恰好是失效的。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: `statusPublisher` —— 代际号、挂住、叫醒、关机

**Files:**
- Create: `internal/guardian/statuswatch.go`
- Test: `internal/guardian/statuswatch_test.go`

**Interfaces:**
- Consumes: Task 1 的 `statusDigest`、`representativeStatus`(测试用)
- Produces:
  - `func newStatusPublisher(compute func() Status) *statusPublisher`
  - `func (p *statusPublisher) current() (Status, uint64)` —— 重算(带合并)并返回
  - `func (p *statusPublisher) poke()` —— 强制重算,绕过合并
  - `func (p *statusPublisher) wait(ctx context.Context, clientGen uint64, timeout time.Duration) (Status, uint64)`
  - `func (p *statusPublisher) beginShutdown()`
  - 常量 `watchRecomputeInterval = 3 * time.Second`、`watchMaxHold = 25 * time.Second`、`watchMinHold = time.Second`

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/statuswatch_test.go`:

```go
package guardian

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// 代际号只在**内容**变了的时候动。
func TestGenerationMovesOnlyWhenContentChanges(t *testing.T) {
	var version atomic.Int64
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.CoreVersion = "v" + string(rune('a'+version.Load()))
		return s
	})

	_, first := p.current()
	p.poke()
	_, again := p.current()
	if again != first {
		t.Fatalf("内容没变而代际号从 %d 动到了 %d —— 这会让 watch 永久自激", first, again)
	}

	version.Add(1)
	p.poke()
	_, third := p.current()
	if third == first {
		t.Fatalf("内容变了而代际号没动(仍是 %d)—— 客户端永远收不到这次变化", third)
	}
}

// 易变字段变了不算变化:这一条把 Task 1 的投影与本层的代际号连起来。
func TestVolatileChangeDoesNotMoveTheGeneration(t *testing.T) {
	var latency atomic.Int64
	latency.Store(390)
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.Core.LatencyMS = latency.Load()
		return s
	})
	_, first := p.current()
	for _, ms := range []int64{412, 388, 401} {
		latency.Store(ms)
		p.poke()
	}
	_, last := p.current()
	if last != first {
		t.Fatalf("只有 latency 在抖,代际号却从 %d 动到了 %d —— watch 会比 30 秒轮询更频", first, last)
	}
}

// 客户端手上的代际号与当前不同 ⇒ **立刻**返回,一秒都不挂。
func TestWaitReturnsImmediatelyWhenGenerationDiffers(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	start := time.Now()
	_, got := p.wait(context.Background(), gen+1, watchMaxHold)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("代际号不同却挂了 %v", elapsed)
	}
	if got != gen {
		t.Fatalf("返回的代际号是 %d,want %d", got, gen)
	}
}

// **Guardian 重启的形状:客户端手上的号比当前大。**
// 用 `>` 比较会让这个请求永久挂住 —— 重启后代际号从头开始。
func TestWaitReturnsImmediatelyWhenClientGenerationIsAhead(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	done := make(chan uint64, 1)
	go func() {
		_, g := p.wait(context.Background(), gen+1000, watchMaxHold)
		done <- g
	}()
	select {
	case g := <-done:
		if g != gen {
			t.Fatalf("返回 %d, want %d", g, gen)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("客户端代际号比当前大时挂住了 —— 比较用的是 `>` 而不是 `!=`," +
			"Guardian 重启后这个请求永远不会返回")
	}
}

// 相同 ⇒ 挂住;poke ⇒ 醒。
func TestWaitParksAndWakesOnPoke(t *testing.T) {
	var version atomic.Int64
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.CoreVersion = "v" + string(rune('a'+version.Load()))
		return s
	})
	_, gen := p.current()

	woke := make(chan uint64, 1)
	go func() {
		_, g := p.wait(context.Background(), gen, watchMaxHold)
		woke <- g
	}()

	// 给 waiter 一点时间真的挂上去。
	time.Sleep(200 * time.Millisecond)
	select {
	case g := <-woke:
		t.Fatalf("内容没变却提前返回了(代际号 %d)", g)
	default:
	}

	version.Add(1)
	p.poke()

	select {
	case g := <-woke:
		if g == gen {
			t.Fatalf("醒了但代际号没变(%d)", g)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("poke 之后 waiter 没醒")
	}
}

// 超时 ⇒ 返回当前 Status,代际号**不变**。这一条同时是「通道还活着」的证据。
func TestWaitTimesOutWithUnchangedGeneration(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	start := time.Now()
	_, got := p.wait(context.Background(), gen, 300*time.Millisecond)
	if got != gen {
		t.Fatalf("超时返回的代际号是 %d,want %d(不变)", got, gen)
	}
	if elapsed := time.Since(start); elapsed < 200*time.Millisecond {
		t.Fatalf("只挂了 %v,比要求的超时短", elapsed)
	}
}

// **关机必须立刻唤醒全部 parked waiter。**
//
// server.Shutdown 会等在跑的 handler 返回,而升级时 Guardian 要被 bootout ——
// 一个挂 25 秒的 watch 会让关机慢 25 秒。这个项目在「关机慢」上栽过 71 分钟,
// 所以这一条钉的是不变量,不是性能。
func TestBeginShutdownWakesParkedWaitersImmediately(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()

	done := make(chan struct{})
	go func() {
		p.wait(context.Background(), gen, watchMaxHold)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)

	start := time.Now()
	p.beginShutdown()
	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("关机后 waiter 过了 %v 才返回", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("beginShutdown 没有唤醒 parked 的 waiter —— Guardian 关机会被它拖住 25 秒")
	}
}

// ctx 取消(客户端断开)⇒ 立刻返回,别把 goroutine 漏在那里。
func TestWaitReturnsWhenContextIsCanceled(t *testing.T) {
	p := newStatusPublisher(representativeStatus)
	_, gen := p.current()
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		p.wait(ctx, gen, watchMaxHold)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("ctx 取消后 waiter 没返回")
	}
}

// **合并:多个 waiter 不该把重算放大。**
//
// /v1/status 是无鉴权的读端点,而 compute 里有一次 Core socket 往返 ——
// 没有合并,任何本地进程都能开一百条 watch 把往返放大一百倍。
func TestConcurrentWaitersDoNotMultiplyRecomputes(t *testing.T) {
	var computes atomic.Int64
	p := newStatusPublisher(func() Status {
		computes.Add(1)
		return representativeStatus()
	})
	_, gen := p.current()
	before := computes.Load()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for range 20 {
		go p.wait(ctx, gen, watchMaxHold)
	}
	// 一个兵底间隔多一点:20 个 waiter 各自醒一次,但合并之后重算次数应当
	// 与「一个 waiter」同量级,而不是 20 倍。
	time.Sleep(watchRecomputeInterval + 500*time.Millisecond)
	cancel()

	if extra := computes.Load() - before; extra > 6 {
		t.Fatalf("20 个 waiter 在一个兵底间隔里触发了 %d 次重算 —— 合并没生效,"+
			"一百条 watch 就能把 Core 往返放大一百倍", extra)
	}
}

// poke 必须绕过合并:真事件要立刻见效,不能被「刚算过」挡住。
func TestPokeBypassesCoalescing(t *testing.T) {
	var version atomic.Int64
	p := newStatusPublisher(func() Status {
		s := representativeStatus()
		s.CoreVersion = "v" + string(rune('a'+version.Load()))
		return s
	})
	_, gen := p.current() // 刚算过,合并窗口里

	version.Add(1)
	p.poke()
	_, got := p.current()
	if got == gen {
		t.Fatal("poke 被合并挡住了 —— 用户敲完 bx down 要等一个兵底间隔才看到变化," +
			"而 poke 存在的全部理由就是消掉那段等待")
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/guardian/ -run 'TestGeneration|TestVolatileChange|TestWait|TestBeginShutdownWakes|TestConcurrentWaiters|TestPokeBypasses' -v`
Expected: FAIL —— `undefined: newStatusPublisher`

- [ ] **Step 3: 写 `statuswatch.go`**

```go
package guardian

import (
	"context"
	"log"
	"sync"
	"time"
)

const (
	// watchRecomputeInterval 是**重算兵底**:parked 的 waiter 每隔这么久自己
	// 醒一次、重算一遍、比一次投影。
	//
	// 它存在的理由不是「怕漏广播」这么抽象 —— 有一个具体的、没有任何广播点
	// 会覆盖的变化:**维护挂起到期**。挂起是读取时判过期的(沿用
	// internal/toolkeys 那个不设定时器的先例),到期那一刻没有任何代码在跑,
	// 只有下一次重算会发现 MaintenanceHold 从非 nil 变成了 nil。
	watchRecomputeInterval = 3 * time.Second
	// watchMaxHold 是服务端挂住的上限,也是「通道还活着」那条心跳的节拍。
	// **客户端超时必须比它长**(菜单侧 40 秒),否则客户端拿到的永远是自己的
	// 超时,而服务端这个上限一次都不会生效(switchServer/probeServers 的注释
	// 里已经踩过并写下过同一个坑)。
	watchMaxHold = 25 * time.Second
	// watchMinHold 是 timeout 参数的下限。0 会让客户端把 watch 变成满速轮询。
	watchMinHold = time.Second
)

// statusPublisher 是**唯一**发布 Status 与代际号的地方。
//
// 代际号由内容派生:重算 → 取投影(statusDigest)→ 与上次比 → 不同才 ++。
// 于是「新加一条改状态的路、忘了 bump」在构造上不存在 —— 而那正是这个仓库
// 反复出现的形状(判据只长在一条路上,而它要保护的状态可以从别的路进来)。
//
// 广播(poke)**不携带任何数据**,只是「现在就重算,别等下一个兵底拍」。
// 所以一次广播不可能是错的,漏一个的代价只是慢到下一个兵底拍。
type statusPublisher struct {
	compute func() Status

	mu         sync.Mutex
	generation uint64
	digest     string
	status     Status
	computedAt time.Time
	// changed 每次 bump 时被 close 并换一条新的 —— Go 里的标准广播手法。
	// parked 的 waiter 在解锁前先抓住当前这一条。
	changed chan struct{}

	shutdownOnce sync.Once
	shutdown     chan struct{}
}

func newStatusPublisher(compute func() Status) *statusPublisher {
	return &statusPublisher{
		compute:  compute,
		changed:  make(chan struct{}),
		shutdown: make(chan struct{}),
	}
}

// current 返回当前 Status 与代际号,必要时重算。
// 「必要」= 距上次重算已超过一个兵底间隔(合并,见 recomputeLocked)。
func (p *statusPublisher) current() (Status, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recomputeLocked(false)
	return p.status, p.generation
}

// poke 强制重算并在内容变了时唤醒全部 waiter。**绕过合并** ——
// 用户敲完 bx down 正等着看图标变,不能让「刚算过」把它挡一个兵底间隔。
func (p *statusPublisher) poke() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recomputeLocked(true)
}

// recomputeLocked 重算一次并按投影决定要不要 bump。调用方必须持锁。
//
// force=false 时做**合并**:距上次重算不足一个兵底间隔就直接返回缓存。
// 没有这一条,N 个并发 waiter 就是 N 倍的 compute,而 compute 里有一次 Core
// socket 往返、/v1/status 又是无鉴权的读端点 —— 一百条 watch 能把它放大一百倍。
func (p *statusPublisher) recomputeLocked(force bool) {
	if !force && !p.computedAt.IsZero() && time.Since(p.computedAt) < watchRecomputeInterval {
		return
	}
	status := p.compute()
	p.computedAt = time.Now()

	digest, err := statusDigest(status)
	if err != nil {
		// 当作「变了」:那意味着 Status 里有编不了码的东西,是编程错误。
		// 当作「变了」会让 watch 每个兵底拍都触发 —— 吵、且这里有日志;
		// 当作「没变」会让菜单**静默冻住**。同一条不对称(见 statusDigest 的注释)。
		log.Printf("guardian_status_digest_failed err=%v", err)
		digest = ""
	}
	if err == nil && digest == p.digest {
		p.status = status
		return
	}
	p.digest = digest
	p.generation++
	status.StatusGeneration = p.generation
	p.status = status
	// 广播:close 当前这一条,换一条新的。
	close(p.changed)
	p.changed = make(chan struct{})
}

// wait 是长轮询的核心。
//
// **比较用 `!=` 而不是 `>`。** Guardian 重启后代际号从头开始,客户端手上那个
// 较大的值在 `>` 下永远不满足,请求会永久挂住。
func (p *statusPublisher) wait(ctx context.Context, clientGen uint64, timeout time.Duration) (Status, uint64) {
	if timeout < watchMinHold {
		timeout = watchMinHold
	}
	if timeout > watchMaxHold {
		timeout = watchMaxHold
	}
	deadline := time.Now().Add(timeout)
	for {
		p.mu.Lock()
		p.recomputeLocked(false)
		status, generation, changed := p.status, p.generation, p.changed
		p.mu.Unlock()

		if generation != clientGen {
			return status, generation
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return status, generation
		}
		// 兵底不是一条独立的 goroutine,也不需要订阅者计数:每个 waiter 的
		// select 自带这个分支。于是「没人 watch 时开销精确为零」是构造出来的,
		// 不是维护出来的。
		nap := watchRecomputeInterval
		if remaining < nap {
			nap = remaining
		}
		timer := time.NewTimer(nap)
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return status, generation
		case <-p.shutdown:
			timer.Stop()
			return status, generation
		}
	}
}

// beginShutdown 立刻唤醒全部 parked waiter。
//
// **这是不变量,不是性能。** http.Server.Shutdown 会等在跑的 handler 返回,
// 而升级时 Guardian 要被 launchctl bootout —— 一个挂 25 秒的 watch 会让关机慢
// 25 秒。这个项目在「关机慢」上栽过 71 分钟(2026-08-04),纪律是:
// 停止路径不许因为别的事没做完而变慢或失败。
//
// 用 sync.Once 是因为 Daemon.Shutdown 可能被调用多次(它自己有
// shutdownStarted 保护,但这一层不该依赖调用方的纪律)。
func (p *statusPublisher) beginShutdown() {
	p.shutdownOnce.Do(func() { close(p.shutdown) })
}
```

- [ ] **Step 4: 跑测试,确认通过**

Run: `go test ./internal/guardian/ -run 'TestGeneration|TestVolatileChange|TestWait|TestBeginShutdownWakes|TestConcurrentWaiters|TestPokeBypasses' -v`
Expected: PASS(9 条)

- [ ] **Step 5: 跑 race 检测(本 task 是唯一带并发的一块)**

Run: `go test ./internal/guardian/ -run 'TestWait|TestConcurrentWaiters|TestBeginShutdownWakes' -race -count=2`
Expected: PASS,无 race 报告

- [ ] **Step 6: 变异验证两条最要紧的守卫**

**`!=` 那条**:把 `if generation != clientGen` 临时改成 `if generation > clientGen`:

Run: `go test ./internal/guardian/ -run TestWaitReturnsImmediatelyWhenClientGenerationIsAhead -v`
Expected: FAIL,信息含 `比较用的是 \`>\` 而不是 \`!=\``

改回来 ⇒ PASS。

**关机那条**:把 `case <-p.shutdown:` 那一支临时删掉:

Run: `go test ./internal/guardian/ -run TestBeginShutdownWakesParkedWaitersImmediately -v`
Expected: FAIL,信息含 `Guardian 关机会被它拖住 25 秒`

改回来 ⇒ PASS。

- [ ] **Step 7: verify + 提交**

```bash
bash scripts/verify.sh --quick; echo "EXIT=$?"
git add internal/guardian/statuswatch.go internal/guardian/statuswatch_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): statusPublisher —— 代际号、挂住、叫醒、关机

唯一的发布点:重算 Status → 取投影 → 与上次比 → 不同才 ++ 并唤醒全部 waiter。
广播(poke)不携带数据,只是「现在就重算」,所以一次广播不可能是错的。

三处判断值得记:

· **`!=` 而不是 `>`**:Guardian 重启后代际号从头开始,客户端手上那个较大的值
  在 `>` 下永远不满足、请求永久挂住。变异验证过。
· **关机立刻唤醒全部 parked waiter**,而且这是不变量不是性能:server.Shutdown
  会等 handler 返回,升级时 Guardian 要被 bootout —— 这个项目在「关机慢」上
  栽过 71 分钟。变异验证过。
· **兵底不是独立 goroutine,也不需要订阅者计数**:每个 waiter 的 select 自带
  3 秒分支。于是「没人 watch 时零开销」是构造出来的而不是维护出来的。代价是
  N 个 waiter 会 N 倍重算,故 recomputeLocked 做合并;poke 绕过合并(真事件要
  立刻见效)。没有合并,任何本地进程都能开一百条 watch 把 Core 往返放大一百倍
  —— /v1/status 是无鉴权的读端点。

兵底最具体的存在理由是**维护挂起到期**:它是读取时判过期的(沿用 toolkeys 那个
不设定时器的先例),到期那一刻没有任何代码在跑,只有下一次重算会发现
MaintenanceHold 从非 nil 变成了 nil。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: `/v1/status?wait=` —— 端点、能力声明、广播点、关机接线

**Files:**
- Modify: `internal/guardian/localapi.go`(`localAPI` 结构、`NewLocalAPI`、`/v1/status` handler、`mutationHandler`、`beginShutdown`)
- Modify: `internal/guardian/types.go`(`CapabilityStatusWatch` + `GuardianCapabilities()`)
- Test: `internal/guardian/localapi_watch_test.go`(新建)

**Interfaces:**
- Consumes: Task 2 的 `newStatusPublisher`/`wait`/`current`/`poke`/`beginShutdown`、`watchMaxHold`、`watchMinHold`;既有 `observableStatus(controller, recoveries, options) Status`
- Produces: `CapabilityStatusWatch = "status_watch"`;`/v1/status` 认 `wait` 与 `timeout` 两个 query 参数

- [ ] **Step 1: 写失败测试**

创建 `internal/guardian/localapi_watch_test.go`。用既有测试里那套 controller 替身
(**先 `grep -n "func.*fakeController\|type.*[Cc]ontroller struct" internal/guardian/*_test.go`
找到现成的替身类型并复用,不要新造一个** —— 本包已有多套,新造一个会让下一个人
不知道该用哪个):

```go
package guardian

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// 不带 wait 的 GET 与今天行为相同,**但多一个 status_generation 键**。
// 「加个字段而已」正是会顺手把旧客户端弄坏的那类改动,所以两头都钉:
// 键必须在(客户端得先有代际号才能发回来),而且旧解码器不该因此失败。
func TestStatusWithoutWaitStillAnswersImmediatelyAndCarriesTheGeneration(t *testing.T) {
	api := newTestLocalAPI(t)

	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("解码: %v", err)
	}
	if _, ok := raw["status_generation"]; !ok {
		t.Fatal("应答里没有 status_generation —— 客户端拿不到代际号就没法发起 watch")
	}
	// 旧客户端(不认识这个键的解码器)仍应成功:Go 的 json.Decode 默认忽略未知键。
	var status Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("既有解码路径读不动新应答了: %v", err)
	}
	if status.StatusGeneration == 0 {
		t.Error("代际号是 0 —— 发布过至少一次之后它应当 >= 1")
	}
}

// 能力声明:菜单靠它决定走 watch 还是降级轮询。**绝不「试着拨一下看看」。**
func TestStatusWatchIsDeclaredAsACapability(t *testing.T) {
	var found bool
	for _, c := range GuardianCapabilities() {
		if c == CapabilityStatusWatch {
			found = true
		}
	}
	if !found {
		t.Fatalf("GuardianCapabilities() 里没有 %q —— 菜单无从知道这一版支持 watch,"+
			"只能去试拨,而试拨拿到的普通应答与「立刻返回因为变了」无法区分,"+
			"于是会退化成一个满速轮询", CapabilityStatusWatch)
	}
}

// wait 与当前不同 ⇒ 立刻返回。
func TestWaitWithStaleGenerationReturnsImmediately(t *testing.T) {
	api := newTestLocalAPI(t)
	start := time.Now()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status?wait=999999", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d", rec.Code)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("代际号不同却挂了 %v —— 比较可能用了 `>` 而不是 `!=`", elapsed)
	}
}

// timeout 参数要被钳住:客户端不许要求比服务端上限更长的挂住 ——
// 那会把关机延迟的上限交给客户端决定。
func TestWaitTimeoutIsClampedToTheServerCeiling(t *testing.T) {
	if got := clampWatchTimeout(9999 * time.Second); got != watchMaxHold {
		t.Errorf("超长 timeout 被钳到 %v,want %v", got, watchMaxHold)
	}
	if got := clampWatchTimeout(0); got != watchMinHold {
		t.Errorf("0 被钳到 %v,want %v(0 会让 watch 变成满速轮询)", got, watchMinHold)
	}
	if got := clampWatchTimeout(-5 * time.Second); got != watchMinHold {
		t.Errorf("负数被钳到 %v,want %v", got, watchMinHold)
	}
	if got := clampWatchTimeout(5 * time.Second); got != 5*time.Second {
		t.Errorf("合法值被改成了 %v", got)
	}
}

// 读不懂的 wait 值不许当成 0(那会让每次请求都立刻返回 = 满速轮询),
// 也不许 500(菜单会以为 Guardian 坏了)。**当成「没带 wait」** —— 立刻返回一次
// 当前状态,客户端拿到真代际号后自然会用对。
func TestUnparsableWaitIsTreatedAsNoWait(t *testing.T) {
	api := newTestLocalAPI(t)
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status?wait=abc", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("状态码 %d,want 200 —— 读不懂的参数不该让菜单以为 Guardian 坏了", rec.Code)
	}
}
```

> `newTestLocalAPI(t)` 是你要写的小 helper:用本包**既有**的 controller 替身
> 构造 `NewLocalAPI(...)`。写它之前先看既有测试怎么构造的,照抄那一份。

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/guardian/ -run 'TestStatusWithoutWait|TestStatusWatchIsDeclared|TestWaitWithStale|TestWaitTimeoutIsClamped|TestUnparsableWait' -v`
Expected: FAIL —— `undefined: CapabilityStatusWatch` / `undefined: clampWatchTimeout`

- [ ] **Step 3: 加能力常量**

`internal/guardian/types.go`,在 `CapabilityServers` 之后:

```go
// CapabilityStatusWatch 表示这一版 Guardian 的 GET /v1/status 认 `wait=<generation>`
// 长轮询,于是客户端可以在状态真的变了的那一刻收到,而不是靠一个轮询常量。
//
// 菜单**靠这个键决定走 watch 还是降级轮询,绝不去试拨** ——
// 旧 Guardian 会忽略未知 query 参数、回一份普通应答,而那与「立刻返回因为状态
// 变了」在客户端看来一模一样,于是 watch 循环会退化成一个满速轮询。
// (与 /v1/rules、/v1/servers 同一条门控纪律。)
const CapabilityStatusWatch = "status_watch"
```

并把它加进 `GuardianCapabilities()`:

```go
func GuardianCapabilities() []string {
	return []string{CapabilityDiagnosticsArchive, CapabilityReconcileReport, CapabilityMaintenanceHold, CapabilityRules, CapabilityServers, CapabilityStatusWatch}
}
```

- [ ] **Step 4: 接线 —— publisher 进 `localAPI`,handler 认 `wait`**

`internal/guardian/localapi.go`:

① `localAPI` 结构加一个字段:

```go
type localAPI struct {
	handler        http.Handler
	mutations      *acceptedMutations
	recoveries     recoveryLifecycle
	pathRecoveries pathRecoveryLifecycle
	// watch 是 Status 与代际号的唯一发布点。parked 的 watch 请求由
	// beginShutdown 唤醒 —— 见那个方法。
	watch *statusPublisher
}
```

② `NewLocalAPI` 里,在 `mux := http.NewServeMux()` **之前**建 publisher,
并把 `/v1/status` handler 换掉:

```go
	mutations := &acceptedMutations{accepting: true, drained: make(chan struct{})}
	// **Status 的唯一发布点。** 连不带 wait 的那条路也走它 —— 否则应答里的
	// status_generation 与 watch 那条路发布的会是两个互不相干的数,而客户端
	// 正是拿前者发回给后者的。
	watch := newStatusPublisher(func() Status {
		return observableStatus(controller, pathRecoveryControllerFor(controller), options)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeGuardianJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		// 读不懂的 wait **当成「没带」**:当成 0 会让每次请求都立刻返回
		// (= 满速轮询),回 4xx/5xx 会让菜单以为 Guardian 坏了。立刻返回一次
		// 当前状态最无害 —— 客户端拿到真代际号之后自然会用对。
		clientGen, parked := parseWatchGeneration(r)
		if !parked {
			status, _ := watch.current()
			writeGuardianJSON(w, http.StatusOK, status)
			return
		}
		status, _ := watch.wait(r.Context(), clientGen, parseWatchTimeout(r))
		writeGuardianJSON(w, http.StatusOK, status)
	})
```

③ 文件末尾加三个小函数:

```go
// parseWatchGeneration 取出 `wait` 参数。第二个返回值 = 「这是一次长轮询」。
//
// 缺席或读不懂都返回 false(当成普通 GET),理由见调用点。
func parseWatchGeneration(r *http.Request) (uint64, bool) {
	raw := r.URL.Query().Get("wait")
	if raw == "" {
		return 0, false
	}
	value, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return value, true
}

// parseWatchTimeout 取出可选的 `timeout`(秒),并钳进服务端允许的区间。
func parseWatchTimeout(r *http.Request) time.Duration {
	raw := r.URL.Query().Get("timeout")
	if raw == "" {
		return watchMaxHold
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return watchMaxHold
	}
	return clampWatchTimeout(time.Duration(seconds) * time.Second)
}

// clampWatchTimeout 把客户端要求的挂住时长钳进 [watchMinHold, watchMaxHold]。
//
// **上限不能交给客户端决定**:它同时是 Guardian 关机可能被拖住的上限。
// 下限不能是 0:那会让 watch 退化成满速轮询。
func clampWatchTimeout(d time.Duration) time.Duration {
	if d < watchMinHold {
		return watchMinHold
	}
	if d > watchMaxHold {
		return watchMaxHold
	}
	return d
}
```

(import 加 `"strconv"`、`"time"`,若尚未存在。)

④ `return &localAPI{...}` 那一行带上 publisher:

```go
	return &localAveraging{...}  // ← 不要照抄这一行,它是故意写错的占位
```

**上面那行是错的,不要照抄。** 正确做法:把既有那句
`return &localAPI{handler: mux, mutations: mutations, recoveries: recoveries, pathRecoveries: pathRecoveries}`
末尾加上 `, watch: watch`。

⑤ `beginShutdown` 加一句:

```go
func (a *localAPI) beginShutdown() {
	a.mutations.stopAccepting()
	// **parked 的 watch 必须立刻放开。** server.Shutdown 会等在跑的 handler
	// 返回,而这个方法正是在 server.Shutdown 之前被 Daemon.Shutdown 调的
	// (daemon.go:319)。不唤醒它们,升级时 Guardian 关机会慢到 25 秒 ——
	// 而这个项目在「关机慢」上栽过 71 分钟。
	if a.watch != nil {
		a.watch.beginShutdown()
	}
}
```

⑥ **广播点**:`mutationHandler` 成功之后 poke。它今天的签名是
`mutationHandler(controller, controller.Up, mutations, options, "/v1/up")` ——
加一个 `watch *statusPublisher` 形参(**必填,不是可选**:形参必填意味着漏传就
编不过,而这是接线正确的唯一硬凭据),在写应答之前调 `watch.poke()`。

两个调用点都要改:

```go
	mux.HandleFunc("/v1/up", mutationHandler(controller, controller.Up, mutations, options, "/v1/up", watch))
	mux.HandleFunc("/v1/down", markMaintenanceStop(mutationHandler(controller, controller.Down, mutations, options, "/v1/down", watch)))
```

**只有这两处 poke。** 刻意不在 Core 意外退出、路径恢复迁移那些地方也 poke ——
那些逻辑住在 `Manager` 里,要把 publisher 穿进去,而换来的只是把 3 秒缩短到 0。
广播按设计只是加速器;选 up/down 是因为那是**用户正站在旁边等反馈**的两处,
也正是这个 bug 的原始现场。

- [ ] **Step 5: 跑测试,确认通过**

Run: `go test ./internal/guardian/ -run 'TestStatusWithoutWait|TestStatusWatchIsDeclared|TestWaitWithStale|TestWaitTimeoutIsClamped|TestUnparsableWait' -v`
Expected: PASS(5 条)

Run: `go test ./internal/guardian/ -count=1`
Expected: PASS —— **既有测试一条都不许改**。有转红的先判断是「新行为正确、
旧断言过期」还是「我改坏了」;前者要在报告里逐条说明,后者要修实现。

- [ ] **Step 6: 写「poke 真的接上了」的守卫**

追加到 `internal/guardian/localapi_watch_test.go`:

```go
// **接线守卫。** publisher 全对而没人在 up/down 之后 poke,与没有广播在输出上
// 完全一样(只是慢 3 秒),而这个功能的原始现场正是那两处。
//
// 这里不查源码文本 —— 那类守卫在本仓库被绕过过八次 —— 而是真的打一次
// POST /v1/down,断言代际号动了。
func TestDownPokesTheStatusGeneration(t *testing.T) {
	api := newTestLocalAPI(t)

	before := statusGenerationVia(t, api)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/down", strings.NewReader("{}"))
	api.ServeHTTP(rec, withTestOwnerPeer(req))
	if rec.Code != http.StatusOK {
		t.Fatalf("/v1/down 回了 %d: %s", rec.Code, rec.Body.String())
	}
	after := statusGenerationVia(t, api)
	if after == before {
		t.Fatalf("/v1/down 之后代际号仍是 %d —— poke 没接上,"+
			"于是用户敲完 bx down 要等一个兵底间隔(3 秒)才看到图标变", after)
	}
}

func statusGenerationVia(t *testing.T, api http.Handler) uint64 {
	t.Helper()
	rec := httptest.NewRecorder()
	api.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	var status Status
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatalf("解码 status: %v", err)
	}
	return status.StatusGeneration
}
```

> `withTestOwnerPeer(req)` 与 `newTestLocalAPI` 一样:`/v1/down` 走
> `authorizeOwnerPeer`,请求需要带 peer 凭据。**先看既有的 mutation 测试是怎么
> 造这个请求的并复用那一份** —— 本包已有现成手法(`withPeerCredentials`)。
> 若这条测试因为替身 controller 的 `Down` 不改变任何被 `observableStatus` 读到的
> 状态而无法让代际号动,**那说明替身太空**:让替身的 `Down` 真的把它自己的
> `Status()` 从 protected 改成 off(那才是 `bx down` 的语义),而不是放宽断言。

- [ ] **Step 7: 变异验证接线守卫 + 关机接线**

把两个 `mutationHandler(...)` 调用末尾的 `, watch` 换成 `, nil`(并让
`mutationHandler` 对 nil publisher 跳过 poke):

Run: `go test ./internal/guardian/ -run TestDownPokesTheStatusGeneration -v`
Expected: FAIL,信息含 `poke 没接上`

改回来 ⇒ PASS。**然后把 `mutationHandler` 里那个 nil 判断也删掉** ——
形参必填、不许 nil,漏传就编不过,那是接线正确的唯一硬凭据。

- [ ] **Step 8: verify + 提交**

```bash
bash scripts/verify.sh --quick; echo "EXIT=$?"
git add internal/guardian/localapi.go internal/guardian/types.go internal/guardian/localapi_watch_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): GET /v1/status?wait=<gen> 长轮询 + 能力声明 + 两个广播点

不带 wait 时行为与今天相同,但应答多一个 status_generation 键(客户端得先有
代际号才能发回来)。向后兼容有测试专门钉:Go 默认忽略未知键 —— 「加个字段而已」
正是会顺手把旧客户端弄坏的那类改动。

三处判断:

· **读不懂的 wait 当成「没带」**,不是 0、也不是 4xx。当成 0 会让每次请求立刻
  返回(满速轮询),报错会让菜单以为 Guardian 坏了。
· **timeout 上限不交给客户端**:它同时是 Guardian 关机可能被拖住的上限。
· **能力声明,绝不试拨**:旧 Guardian 会忽略未知 query 参数回一份普通应答,
  而那与「立刻返回因为状态变了」在客户端看来一模一样,试拨会退化成满速轮询。

**只在 /v1/up 与 /v1/down 两处 poke。** Core 意外退出与路径恢复迁移住在 Manager
里,要把 publisher 穿进去,而换来的只是把 3 秒缩短到 0;广播按设计只是加速器。
选这两处是因为那是用户正站在旁边等反馈的两处,也正是这个 bug 的原始现场。

publisher 形参**必填**(不是可选、不认 nil):漏传就编不过,那是接线正确的唯一
硬凭据。另有一条走真 HTTP 的守卫断言 /v1/down 之后代际号真的动了,变异验证过。

localAPI.beginShutdown 唤醒 parked 的 watch —— 它正是在 server.Shutdown 之前被
Daemon.Shutdown 调的那个方法,不唤醒会让升级时关机慢 25 秒。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: `bx status --watch` —— 这个功能唯一的只读真机验证手段

**Files:**
- Modify: `internal/guardian/client.go`
- Modify: `internal/cli/cli.go`(`statusFlags()`、`statusAction`)
- Create: `internal/cli/statuswatch.go`(循环的**纯判据**部分)
- Test: `internal/cli/statuswatch_test.go`

**Interfaces:**
- Consumes: `Client.request`(`client.go:201`,URL 由 `"http://local"+path` 拼成 —— query 由调用方附在 path 上)、`readClientStatusReport()`(`cli.go:4115`)
- Produces:
  - `func (c *Client) StatusWatch(ctx context.Context, generation uint64) (Status, error)`
  - `func watchBackoff(consecutiveFailures int) time.Duration`
  - `--watch` flag

**为什么它不是附赠品:** 菜单那一半要重装 App 才能验;这一条只要一个终端。
项目所有者能在**不动网络**的前提下亲眼看到「敲 `bx down` 那一瞬间它就吐了」,
而且它是唯一能长时间观察「投影够不够安静」的工具(spec 说那是本设计唯一真正的
验收)。

- [ ] **Step 1: 写失败测试**

创建 `internal/cli/statuswatch_test.go`:

```go
package cli

import (
	"testing"
	"time"
)

// 退避:连续失败要越等越久,但有上限 —— 无上限的指数退避在 int64 上会溢出回绕
// (阶段③a 那条退避上限断言就是被溢出架空的),而回绕成 0 意味着满速重连。
func TestWatchBackoffGrowsThenCaps(t *testing.T) {
	if got := watchBackoff(0); got != 0 {
		t.Errorf("第一次不该等,got %v", got)
	}
	first := watchBackoff(1)
	second := watchBackoff(2)
	if !(first > 0 && second > first) {
		t.Errorf("退避没有增长:%v → %v", first, second)
	}
	// **上限必须在极大轮次上也成立。** 用一个大到会让未加保护的实现溢出的值。
	if got := watchBackoff(1000); got > watchBackoffMax {
		t.Errorf("第 1000 次退避是 %v,超过上限 %v —— 无上限的指数退避会溢出回绕成 0,"+
			"而那意味着满速重连", got, watchBackoffMax)
	}
	if got := watchBackoff(1000); got <= 0 {
		t.Errorf("第 1000 次退避是 %v —— 已经回绕了", got)
	}
}
```

- [ ] **Step 2: 跑测试,确认失败**

Run: `go test ./internal/cli/ -run TestWatchBackoffGrowsThenCaps -v`
Expected: FAIL —— `undefined: watchBackoff`

- [ ] **Step 3: 写 `internal/cli/statuswatch.go`**

```go
package cli

import "time"

// watchBackoffMax 是重连退避的上限。
//
// **上限不是礼貌,是正确性。** 无上限的指数退避在 int64 上会溢出回绕
// (阶段③a 那条退避断言就是被溢出架空的:round 54 回绕成 0),而回绕成 0
// 意味着满速重连。
const watchBackoffMax = 30 * time.Second

// watchBackoff 是连续第 n 次失败之后该等多久。n==0(刚成功)不等。
//
// 先乘后钳会溢出,所以**先判轮次**:超过阈值直接返回上限,不去算那个会回绕的
// 乘法。
func watchBackoff(consecutiveFailures int) time.Duration {
	if consecutiveFailures <= 0 {
		return 0
	}
	if consecutiveFailures > 10 {
		return watchBackoffMax
	}
	delay := time.Second << (consecutiveFailures - 1)
	if delay > watchBackoffMax {
		return watchBackoffMax
	}
	return delay
}
```

- [ ] **Step 4: 跑测试,确认通过**

Run: `go test ./internal/cli/ -run TestWatchBackoffGrowsThenCaps -v`
Expected: PASS

- [ ] **Step 5: Go 客户端加 `StatusWatch`**

`internal/guardian/client.go`,在 `Status` 之后:

```go
// StatusWatch 是 Status 的长轮询形态:Guardian 在自己的代际号与 generation
// 不同时立刻应答,相同则挂住到它变了或服务端超时。
//
// query 直接拼在 path 上 —— request 只做 "http://local"+path,本包没有
// url.Values 那一层(见 request)。
func (c *Client) StatusWatch(ctx context.Context, generation uint64) (Status, error) {
	return c.request(ctx, http.MethodGet, "/v1/status?wait="+strconv.FormatUint(generation, 10), nil)
}
```

(import 加 `"strconv"`。)

- [ ] **Step 6: CLI 加 `--watch`**

`internal/cli/cli.go` 的 `statusFlags()`:

```go
func statusFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "输出机器可读 JSON"},
		&cli.BoolFlag{Name: "watch", Usage: "挂住等状态变化,每次变化打印一次(Ctrl-C 退出;只读,不改任何东西)"},
	}
}
```

`statusAction` 开头分流(**把 watch 循环放在单独的函数里,别塞进 statusAction**):

```go
func statusAction(c *cli.Context) error {
	if c.Bool("watch") {
		return statusWatchLoop(c.Context, os.Stdout, c.Bool("json"))
	}
	...既有代码不动...
}
```

`internal/cli/statuswatch.go` 追加循环本体:

```go
// statusWatchLoop 挂在 Guardian 的 /v1/status?wait= 上,每次状态变化打印一次。
//
// **它是这个功能唯一的只读真机验证手段**:菜单那一半要重装 App 才能验,
// 而这一条只要一个终端。它也是唯一能长时间观察「投影够不够安静」的工具 ——
// 稳态下它应当几乎不吐。
func statusWatchLoop(ctx context.Context, out io.Writer, asJSON bool) error {
	client := guardian.Client{SocketPath: guardian.DefaultSocketPath}
	var generation uint64
	failures := 0
	for {
		if delay := watchBackoff(failures); delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return nil
			}
		}
		// 客户端超时必须比服务端挂住上限长,否则拿到的永远是自己的超时
		// (switchServer 的注释里踩过同一个坑)。
		callCtx, cancel := context.WithTimeout(ctx, watchClientTimeout)
		status, err := client.StatusWatch(callCtx, generation)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			failures++
			fmt.Fprintf(out, "watch 断开(第 %d 次):%v\n", failures, err)
			continue
		}
		failures = 0
		if status.StatusGeneration == generation {
			// 服务端超时,状态没变。**这一条不打印** —— 它是心跳,
			// 打出来会把真正的变化淹掉。
			continue
		}
		generation = status.StatusGeneration
		if err := printWatchedStatus(out, status, asJSON); err != nil {
			return err
		}
	}
}
```

`printWatchedStatus` 与 `watchClientTimeout`(= 40 秒)也写在这个文件里。
JSON 模式一行一份(NDJSON),人面模式打一行摘要:代际号 + `protection_state`
+ `desired` + 时刻。

> `guardian.Client` 的字段名与 `DefaultSocketPath` 常量名**先 grep 确认**
> (`grep -n "type Client struct" -A 8 internal/guardian/client.go`),
> 不要照抄上面那一行。

- [ ] **Step 7: 手工跑一次(只读,不碰网络)**

```bash
go build ./... && echo BUILD_OK
go run ./cmd/bx status --watch --json
```

Expected:若本机 Guardian 在跑,它会打一行当前状态然后**安静挂住**。
Ctrl-C 退出。**若持续每 3 秒吐一行,那就是投影漏了一个易变字段** ——
停下来,把吐出来的相邻两份 JSON diff 一下,把变动的那个字段加进排除名单
(带理由),再回 Task 1 补一条对应的测试。

若 Guardian 没在跑,它会打「watch 断开」并退避重连 —— 那也是对的。

**这一步的输出要逐字贴进报告。**

- [ ] **Step 8: verify + 提交**

```bash
bash scripts/verify.sh --quick; echo "EXIT=$?"
git add internal/guardian/client.go internal/cli/cli.go internal/cli/statuswatch.go internal/cli/statuswatch_test.go
git commit -m "$(cat <<'EOF'
feat(cli): bx status --watch —— watch 的第二个消费方,也是它的验证手段

菜单那一半要重装 App 才能验;这一条只要一个终端,而且不动网络、不改任何东西。
它是唯一能长时间观察「投影够不够安静」的工具 —— 而 spec 说那是本设计唯一真正的
验收:稳态下它应当几乎不吐。

「服务端超时、状态没变」那一条**不打印**:它是心跳,打出来会把真正的变化淹掉。

退避上限不是礼貌而是正确性:无上限的指数退避在 int64 上会回绕成 0
(阶段③a 那条退避断言正是被溢出架空的),而回绕成 0 意味着满速重连。
实现先判轮次再算乘法,测试用 round=1000 探它。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: 菜单走 watch

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/StatusWatch.swift`
- Create: `apps/macos/BxMenu/Tests/StatusWatchTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `scripts/test-macos-menu.sh`

**Interfaces:**
- Consumes: Task 3 的 `/v1/status?wait=`、`status_generation`、`CapabilityStatusWatch`(字符串 `"status_watch"`)
- Produces:
  - Swift `GuardianEndpoint.statusWatch(generation: UInt64)`
  - `func statusWatch(generation: UInt64) throws -> GuardianStatus`
  - `GuardianStatus.statusGeneration: UInt64?`
  - `func watchIsAvailable(capabilities: [String]?) -> Bool`
  - `func watchBackoffSeconds(consecutiveFailures: Int) -> TimeInterval`

**为什么判据要单独成文件:** `main.swift` 是 AppKit、**编不进 Swift 测试套件**,
里面的逻辑只能靠 Go 侧读源码文本的守卫钉住 —— 而那类守卫在本仓库被攻破过八次。
凡能做成纯函数的都放 `StatusWatch.swift`,`main.swift` 只剩起线程与拨 socket。

- [ ] **Step 1: 写失败测试**

创建 `apps/macos/BxMenu/Tests/StatusWatchTests.swift`,照本仓库既有 Swift 测试的
形状(`@main struct XTests`、`static var failures`、`expect(_:_:)`、结尾
`exit(failures == 0 ? 0 : 1)` —— **先读 `Tests/MenuCadenceTests.swift` 照抄那个
骨架**):

```swift
import Foundation

@main
struct StatusWatchTests {
    static var failures = 0

    static func expect(_ condition: Bool, _ message: String) {
        if !condition {
            failures += 1
            FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
        }
    }

    static func main() {
        // **能力缺席 ≠ 能力为空。** nil 是「这一版 Guardian 压根没声明过能力」
        // (旧版,键缺席),[] 是「声明了、一个都没有」。两者都不能走 watch,
        // 但它们不是同一件事,而 GuardianStatus.capabilities 刻意保留了这个区分。
        expect(!watchIsAvailable(capabilities: nil),
               "能力缺席时不该走 watch —— 旧 Guardian 会忽略未知 query 参数回一份普通应答,而那与「立刻返回因为变了」无法区分,watch 循环会退化成满速轮询")
        expect(!watchIsAvailable(capabilities: []), "能力为空时不该走 watch")
        expect(!watchIsAvailable(capabilities: ["rules", "servers"]), "没有 status_watch 时不该走 watch")
        expect(watchIsAvailable(capabilities: ["rules", "status_watch"]), "声明了 status_watch 就该走 watch")

        // 退避:增长 + 有上限。上限不是礼貌 —— 没有上限的话极大轮次会溢出,
        // 而溢出之后的值意味着满速重连。
        expect(watchBackoffSeconds(consecutiveFailures: 0) == 0, "刚成功不该等")
        let first = watchBackoffSeconds(consecutiveFailures: 1)
        let second = watchBackoffSeconds(consecutiveFailures: 2)
        expect(first > 0 && second > first, "退避没有增长:\(first) → \(second)")
        expect(watchBackoffSeconds(consecutiveFailures: 1000) <= watchBackoffMaxSeconds,
               "第 1000 次退避是 \(watchBackoffSeconds(consecutiveFailures: 1000)),超过上限")
        expect(watchBackoffSeconds(consecutiveFailures: 1000) > 0,
               "第 1000 次退避不是正数 —— 已经溢出了,而那意味着满速重连")

        // **客户端超时必须大于服务端挂住上限(25 秒)。**
        // 小于它的话客户端拿到的永远是自己的超时,服务端那个上限一次都不生效
        // —— switchServer/probeServers 的注释里已经踩过这个坑。
        expect(guardianStatusWatchTimeout > 25,
               "watch 的客户端超时是 \(guardianStatusWatchTimeout) 秒,不大于服务端的 25 秒上限")

        // 兜底轮询与 watch 的健康判断**无关**:一个会被 watch 自己的健康判断
        // 影响的兜底,在那个判断错的时候恰好也是坏的。
        expect(menuWatchBackstopSeconds >= 30,
               "兜底轮询 \(menuWatchBackstopSeconds) 秒太密 —— 它只是「watch 哑了」的保险,不是取数据的手段")

        exit(failures == 0 ? 0 : 1)
    }
}
```

- [ ] **Step 2: 注册套件并跑,确认失败**

`scripts/test-macos-menu.sh` 里加一个 `run_test` 块(位置随意,每个 `run_test`
互相独立):

```bash
run_test status-watch \
  "$MENU/Sources/BxMenu/MenuCadence.swift" \
  "$MENU/Sources/BxMenu/StatusWatch.swift" \
  "$MENU/Tests/StatusWatchTests.swift"
```

Run: `bash scripts/test-macos-menu.sh; echo "EXIT=$?"`
Expected: 非 0,`StatusWatch.swift` 不存在导致编译失败

- [ ] **Step 3: 写 `StatusWatch.swift`**

```swift
import Foundation

/// Guardian 侧 watchMaxHold = 25 秒。**客户端必须比它长**,否则拿到的永远是
/// 自己的超时,而服务端那个上限一次都不会生效 —— 与 switchServer/probeServers
/// 注释里记下的是同一个坑。
let guardianStatusWatchTimeout: TimeInterval = 40

/// 重连退避上限。**上限不是礼貌,是正确性**:没有上限的指数退避在极大轮次上
/// 会溢出,而溢出之后的值意味着满速重连。
let watchBackoffMaxSeconds: TimeInterval = 30

/// 兜底轮询间隔。
///
/// **它与 watch 的健康判断无关,而且刻意如此。** watch 有一类失效是静默的
/// (连接半开、循环自己死掉),此时没有任何东西会报错,菜单就停在最后一次收到
/// 的状态上而看起来完全正常。一个会被 watch 自己的健康判断影响的兜底,
/// 在那个判断错的时候恰好也是坏的 —— 所以它是个常量,永远在跑。
///
/// 「指示灯不再指示」是这个组件最坏的失效模式(这个项目为此删掉过 Quit Menu)。
let menuWatchBackstopSeconds: TimeInterval = 60

/// watchIsAvailable 判断这一版 Guardian 认不认 `/v1/status?wait=`。
///
/// **nil 与 [] 是两件事**:nil 是「这一版压根没声明过能力」(旧 Guardian,
/// 键缺席),[] 是「声明了、一个都没有」。两者都不走 watch,但不是同一件事,
/// 而 GuardianStatus.capabilities 刻意保留了这个区分。
///
/// **绝不「试着拨一下看看」**:旧 Guardian 会忽略未知 query 参数、回一份普通
/// 应答,而那与「立刻返回因为状态变了」在客户端看来一模一样 —— 于是 watch
/// 循环会退化成一个满速轮询。
func watchIsAvailable(capabilities: [String]?) -> Bool {
    guard let capabilities else { return false }
    return capabilities.contains("status_watch")
}

/// 连续第 n 次失败之后该等多久。n == 0(刚成功)不等。
///
/// **先判轮次再算乘法**:先乘后钳会在极大轮次上溢出。
func watchBackoffSeconds(consecutiveFailures: Int) -> TimeInterval {
    guard consecutiveFailures > 0 else { return 0 }
    if consecutiveFailures > 10 { return watchBackoffMaxSeconds }
    let delay = pow(2.0, Double(consecutiveFailures - 1))
    return min(delay, watchBackoffMaxSeconds)
}
```

- [ ] **Step 4: 跑测试,确认通过**

Run: `bash scripts/test-macos-menu.sh; echo "EXIT=$?"`
Expected: `EXIT=0`,且输出里有 `macOS menu tests passed`
(**收尾横幅要确认打印过** —— 脚本提前 `exit 0` 时退出码仍是 0,只有横幅抓得住)

- [ ] **Step 5: `GuardianStatus` 解代际号**

`GuardianStatus.swift`:字段、`CodingKeys`、`init(from:)` **三处都要加**
(这个结构体是手写 `init(from:)` 的 —— Swift 合成的 `Decodable` 不用属性默认值,
这个坑本项目在 `RulesModel` 上踩过):

```swift
    /// Guardian 的内容派生代际号。**nil = 这一版没有 watch 这个概念**
    /// (键缺席),不是 0。判「支不支持 watch」要看 capabilities,不看这个键 ——
    /// 与 maintenanceHold 同一条纪律。
    let statusGeneration: UInt64?
```
```swift
        case statusGeneration = "status_generation"
```
```swift
        statusGeneration = try container.decodeIfPresent(UInt64.self, forKey: .statusGeneration)
```

- [ ] **Step 6: `GuardianClient` 加 endpoint**

`GuardianClient.swift` 三处:

① `GuardianEndpoint` 加 case(带关联值,与 `.switchServer(name:)` 同形):
```swift
    /// 长轮询:Guardian 在自己的代际号与 generation 不同时立刻应答,相同则挂住。
    case statusWatch(generation: UInt64)
```
② `expectedStatus` 的 200 那一支加上 `.statusWatch`。
③ `timeout` 加一支 —— **不要复用 `guardianDefaultTimeout`(5 秒)**,那比服务端
挂住上限短,会让每次 watch 都拿到自己的超时:
```swift
        // 服务端最长挂 25 秒。客户端必须更长,否则拿到的永远是自己的超时,
        // 而服务端那个上限一次都不会生效。
        case .statusWatch: return guardianStatusWatchTimeout
```
④ `guardianRequest(for:)` 的 switch 加一支:
```swift
    case let .statusWatch(generation):
        method = "GET"
        path = "/v1/status?wait=\(generation)"
        body = nil
```
(`generation` 是 `UInt64`,插值不引入注入面 —— 与 `changeRule` 那里用
`JSONSerialization` 的理由不冲突:那里的输入是用户写的任意文本。)

⑤ `GuardianClient` 加方法:
```swift
    func statusWatch(generation: UInt64) throws -> GuardianStatus {
        try perform(endpoint: .statusWatch(generation: generation), as: GuardianStatus.self)
    }
```

- [ ] **Step 7: `main.swift` 接线**

在 `AppDelegate` 里加:一个后台串行队列上的 watch 循环、一个 `watchGeneration`、
一个 `watchLoopRunning` 标记。要点:

- **引导序列**:启动时先照旧 `refresh` 一次(那次会拿到 `capabilities`);
  `applyRefresh` 里若 `watchIsAvailable(capabilities:)` 且循环还没起,就起它。
- **循环体**:`client.statusWatch(generation:)` → 回主线程走**既有的**
  `applyRefresh` 路径(别新写一条状态落定路径:那会变成第二个控制面)→
  更新 `watchGeneration` → 继续。失败则退避重连。
- **兜底**:`rescheduleRefreshTimer` 在 watch 起来之后改用
  `menuWatchBackstopSeconds`;**watch 没起来时一个字不动**(保持今天的
  `menuPollInterval`)。
- **`menuWillOpen` / `menuDidClose` 的调频保持不变**:菜单开着时用户在读数据行,
  2 秒那一档仍然有它的理由(rules/servers 在 Task 6 变成按需拉,但 status 行
  仍受益)。

- [ ] **Step 8: 加 Go 侧接线守卫**

`internal/cli/cli_test.go` 追加(**这是 CI 里唯一真正跑的 macOS 保护** ——
`main.swift` 编不进 Swift 套件):

```go
// main.swift 的 watch 接线只能靠读源码守住。**守语义,不守拼法。**
//
// 钉两件事:① 循环必须经既有的 applyRefresh 落定状态(新写一条落定路径就是
// 第二个控制面);② 能力门控必须是 watchIsAvailable,不是某处手抄的字符串比较。
func TestMacMenuWatchLoopUsesCapabilityGateAndExistingApplyPath(t *testing.T) {
	source := readMacMenuMainSource(t) // 既有 helper;没有就照本文件其它守卫的做法写一个
	if !strings.Contains(source, "watchIsAvailable(capabilities:") {
		t.Error("watch 的启用判据不是 watchIsAvailable —— 手抄一份字符串比较会与 StatusWatch.swift 漂开,而那一份才有测试")
	}
	if strings.Contains(source, `contains("status_watch")`) {
		t.Error("main.swift 里直接比对了 \"status_watch\" 字面量 —— 判据只该有一份,在 StatusWatch.swift")
	}
}
```

> 这条守卫**弱于它想守的东西**,commit message 里要说明:它证明的是「判据没有
> 被手抄第二份」,不是「循环真的跑起来了」。后者只能真机点一遍。

- [ ] **Step 9: 全量 verify + 提交**

```bash
bash scripts/verify.sh; echo "EXIT=$?"
```
(全量会跑 `swift build --package-path apps/macos/BxMenu` 与
`scripts/test-macos-menu.sh` —— Swift 侧的编译错误只有它抓得住,CI 里
`go vet`/`go test` 一律绿。)

```bash
git add apps/macos/BxMenu/ scripts/test-macos-menu.sh internal/cli/cli_test.go
git commit -m "$(cat <<'EOF'
feat(menu): 菜单栏改走 watch —— 图标跟着事实变,不跟着常量变

引导序列:启动先照旧拨一次 /v1/status 拿 capabilities,声明了 status_watch 才转入
watch 循环。**绝不试拨** —— 旧 Guardian 会忽略未知 query 参数回一份普通应答,
而那与「立刻返回因为变了」在客户端看来一模一样,试拨会退化成满速轮询。

判据全部放 StatusWatch.swift 而不是 main.swift:后者是 AppKit、编不进 Swift 测试
套件,里面的逻辑只能靠 Go 侧读源码文本的守卫钉住,而那类守卫在本仓库被攻破过
八次。能进纯函数的就不留在 main.swift 里。

三个数字互相约束,各有一条断言:客户端超时 40 秒必须 > 服务端挂住上限 25 秒
(否则拿到的永远是自己的超时,服务端那个上限一次都不生效);退避有上限
(无上限的指数退避会溢出,而溢出之后意味着满速重连);兜底轮询 60 秒**与 watch
的健康判断无关** —— watch 有一类失效是静默的,而一个会被 watch 自己的健康判断
影响的兜底,在那个判断错的时候恰好也是坏的。

statusGeneration 的 nil 与 0 是两件事(键缺席 = 这版没有 watch 这个概念),
且字段/CodingKeys/init(from:) 三处都加了 —— Swift 合成的 Decodable 不用属性
默认值,这个坑本项目在 RulesModel 上踩过。

Go 侧那条守卫弱于它想守的东西:它证明「判据没被手抄第二份」,不证明循环真的
跑起来了。后者只能真机点一遍。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: rules / servers 改按需拉 + 重写那条被证伪的守卫

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(`loadState`、子菜单/窗口打开处)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/MenuCadence.swift`(注释;常量不变)
- Modify: `apps/macos/BxMenu/Tests/MenuCadenceTests.swift`

**Interfaces:**
- Consumes: Task 5 的 watch 循环;既有 `GuardianClient().listRules()` / `listServers()`、
  `lastRules` / `lastServers` 缓存
- Produces: 无新公开面 —— 这一 task 只把「什么时候拉」改掉

**为什么它在 watch 之后从可选变成承重:** 今天一次刷新是 3 次 socket 往返,
后两者各**读并 YAML 解析一遍 `/etc/bx/config.yaml`**,而图标只依赖
`/v1/status`(`menuRowsNow()` 一个字都不碰那两样)。轮询时代这是浪费;
**watch 时代刷新从「每 30 秒一次」变成「每次状态变化都有一次」**,若每次都带
两份 config 解析,总开销可能反而上升。

- [ ] **Step 1: 改 `MenuCadenceTests.swift` 里那条被证伪的守卫(先改测试)**

把:
```swift
        expect(closed >= 20, "菜单关着时只有图标要更新,间隔应显著放宽,实际 \(closed) 秒")
```
换成:
```swift
        // **这条守卫的旧措辞把因果说反了。** 它原本写的是「菜单关着时只有图标
        // 要更新,间隔应显著放宽」—— 而菜单关着的时候,**图标恰恰是唯一在被读
        // 的东西**。用「没人在看数据行」当理由去放宽间隔,放宽掉的正是唯一有人
        // 在看的那一样;项目所有者看到的现象就是「敲完 bx down 图标不变,点一下
        // 才变」。
        //
        // 30 秒本身没有被改小:延迟由 watch(StatusWatch.swift)消掉,而这个
        // 常量只在**旧 Guardian 不支持 watch** 时作为降级路径生效。它的理由
        // 现在是「降级路径要保持既有行为」,不再是那句被证伪的话。
        expect(closed == 30, "无 watch 时的降级轮询应保持既有的 30 秒,实际 \(closed) 秒")

        // 兜底轮询与关闭档轮询是**两件不同的东西**:前者在 watch 健康时也照跑,
        // 是「watch 已经哑了」的保险;后者是完全没有 watch 时的取数手段。
        expect(menuWatchBackstopSeconds > closed,
               "兜底轮询(\(menuWatchBackstopSeconds) 秒)不该比降级轮询(\(closed) 秒)还密 —— 它不是取数据的手段")
```

`scripts/test-macos-menu.sh` 里 `menu-cadence` 那个块要加上
`StatusWatch.swift`(它现在引用了 `menuWatchBackstopSeconds`)。

- [ ] **Step 2: 跑测试,确认失败**

Run: `bash scripts/test-macos-menu.sh; echo "EXIT=$?"`
Expected: 非 0(`menu-cadence` 套件里 `menuWatchBackstopSeconds` 未定义,
或断言不成立)—— 按报错把 `StatusWatch.swift` 加进那个块。

- [ ] **Step 3: `loadState` 不再拉 rules / servers**

`main.swift` 的 `loadState` 里,删掉这两段(`main.swift:472-481` 附近):

```swift
        if rulesEditingAvailable(capabilities: maintenanceReport?.capabilities) {
            rules = try? GuardianClient().listRules()
        }
        if serverSwitchingAvailable(capabilities: maintenanceReport?.capabilities) {
            servers = try? GuardianClient().listServers()
        }
```

`RefreshOutcome` 的 `rules` / `servers` 字段**保留**(`applyRefresh` 里
「这一轮没读到就保留上一轮的」那段逻辑仍然有用),但刷新路径现在恒传 nil。

新增两个按需取数的方法,在**打开规则子菜单 / 打开服务器窗口**时调用:

```swift
    /// 按需拉一次规则。**只在用户真的要看规则时拨** ——
    /// 图标不依赖它(menuRowsNow 一个字都不碰 rules),而每次拨都让一个 root
    /// 守护进程读并 YAML 解析一遍 /etc/bx/config.yaml。
    ///
    /// 在轮询时代这是浪费;watch 时代刷新变成「每次状态变化都有一次」,
    /// 带着它就会变成更糟。
    private func fetchRulesOnDemand() { ... }
    private func fetchServersOnDemand() { ... }
```

**接线点要确认清楚**:先 `grep -n "lastRules\|lastServers" main.swift` 找到全部
读它们的地方(`main.swift:719`、`:834` 附近),在**那些界面被打开之前**触发一次
按需取数。**读不到就照既有逻辑说读不到,不许摆一个空列表** ——
`RulesModel` 那一期已经定过这条纪律。

- [ ] **Step 4: 跑测试与构建**

Run: `bash scripts/test-macos-menu.sh; echo "EXIT=$?"` → `EXIT=0` + 收尾横幅
Run: `swift build --package-path apps/macos/BxMenu 2>&1 | tail -5; echo "EXIT=$?"` → `EXIT=0`

- [ ] **Step 5: 全量 verify + 提交**

```bash
bash scripts/verify.sh; echo "EXIT=$?"
git add apps/macos/BxMenu/ scripts/test-macos-menu.sh
git commit -m "$(cat <<'EOF'
refactor(menu): rules/servers 改按需拉;重写那条被证伪的轮询守卫

今天一次刷新是 3 次 socket 往返,后两者各读并 YAML 解析一遍 /etc/bx/config.yaml,
而图标只依赖 /v1/status(menuRowsNow 一个字都不碰那两样)。轮询时代这是浪费;
**watch 时代刷新从「每 30 秒一次」变成「每次状态变化都有一次」**,带着它反而
可能让总开销上升 —— 所以它从可选变成了承重。

MenuCadenceTests 那条 `closed >= 20` 守卫的旧措辞把因果说反了:它写的是「菜单
关着时只有图标要更新,间隔应显著放宽」,而**菜单关着的时候图标恰恰是唯一在被读
的东西**。30 秒本身不改小 —— 延迟由 watch 消掉,这个常量只在旧 Guardian 不支持
watch 时作为降级路径生效,它的理由现在是「降级路径保持既有行为」。

另钉住兜底轮询与降级轮询是两件不同的东西:前者在 watch 健康时也照跑、是
「watch 已经哑了」的保险;后者是完全没有 watch 时的取数手段。

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
EOF
)"
```

---

## 收尾:文档与真机验收

- [ ] **Step 1: 更新 CLAUDE.md**

新增一节,要点:代际号由内容派生、广播只是叫醒(所以漏一个只慢几秒);
投影 = 整个 Status 减排除名单、**默认参与**的方向与理由、名单五条各自为什么易变;
**深拷贝承重**(切片只复制头,清零会污染发布出去的那份);`!=` 而非 `>`
(Guardian 重启);关机必须唤醒 parked waiter(接 71 分钟那条不变量);
兜底轮询与 watch 健康判断无关;只有 up/down 两个广播点及其理由;
`bx status --watch` 是唯一的只读真机验证手段;**「投影够不够安静」真机未验**。

- [ ] **Step 2: 提交文档**

```bash
git add CLAUDE.md
git commit -m "docs: Guardian 状态 watch —— 代际号由内容派生,广播只是叫醒

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>"
```

- [ ] **Step 3: 真机验收(交给项目所有者,分两半)**

**第一半:只读,不动网络,不重装任何东西。**

```bash
go run ./cmd/bx status --watch
```
挂着别管它。**要盯的是它多久吐一行:**
- 稳态下**应当几乎不吐**。若每 3 秒吐一行 → 投影漏了一个易变字段。
  用 `--json` 再跑一次,把相邻两份 diff 一下就知道是哪个字段。
- 另开一个终端敲 `sudo bx down`(**这一下会动网络,由你决定**)——
  它应当**当场**吐一行,而不是 3 秒后。那一行是 poke 生效的证据。

**第二半:菜单,要重装 App。**
```bash
bash scripts/package-macos-release.sh
# 然后以**普通用户身份**跑 dist/release/bx-macos-arm64/install.sh(不要 sudo)
```
装完 `bx up` / `bx down`,**别碰菜单栏**,看图标是否当场变。

**升级时另量一件事**:Guardian 关机有没有变慢(`beginShutdown` 那条不变量)。
`install.sh` 的「重启保护服务」那一步若明显比以往久,停下来查 parked watch。

---

## 已知缺口(写在计划里,免得被当成做完了)

- **「投影够不够安静」只能真机验。** 单测里 latency 是固定 fixture,
  测不出吵不吵。Task 4 的 `bx status --watch` 就是为此存在的。
- **`main.swift` 的 watch 循环没有真正的测试。** 它编不进 Swift 套件,
  Go 侧那条守卫只证明「判据没被手抄第二份」。
- **Linux / Windows 不在范围内。** Guardian 只在 darwin 跑;Windows 托盘每
  3 秒 spawn 一次自己刷,没有这个问题。
