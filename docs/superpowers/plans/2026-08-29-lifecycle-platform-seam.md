# Guardian 生命周期平台缝(终局第 2 步)实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 把 Guardian 散落的平台选择收进一份类型可见的清单(`lifecyclePlatform`),让 `RunDaemon` 变成读不出 OS 的组装,并给终局第 3 步(Linux 适配器进集成台)留下一张编译器背书的检查表。

**Architecture:** 实测(2026-08-29)推翻了 spec 里「接口 + 意图方法」的草图——`Barrier`/`DNSManager`/`CoreRunner`/`daemonNetworkObserver` **已经是接口**,Manager 全程接口消费;平台选择只剩 `RunDaemon`/`StartDaemon` 里五处对 build-tag 自由函数的直接引用。故本计划**不发明新接口**,只做两件事:① 把那五处收进一个 struct-of-funcs 清单(`lifecyclePlatform`),平台解析仍走既有 build tag(与数据面 `platform_<os>.go` 同一机制);② 反射 `validate()` + 三平台 CI 腿上的测试钉住「清单不许有洞」。**darwin 行为零改动,既有测试一个断言不动**。

**Tech Stack:** Go 1.26,纯 stdlib(reflect),无新依赖。

**Spec:** `docs/superpowers/specs/2026-08-29-control-plane-endgame-design.md`(第 2 步)

## Global Constraints

- **TDD**:先写失败测试→跑红→最小实现→跑绿→提交;refactor 任务(Task 2)例外——无新行为,网是既有全套测试,计划里明写。
- **darwin 行为保真是本计划的全部风险所在**:Task 1–2 结束时 `git diff --stat -- '*_test.go'` 除新建文件外必须为空(spec 判据:「既有 darwin 测试原样全绿、一个断言不动」)。
- **验证一律 `bash scripts/verify.sh`(判据是退出码)**;单任务迭代期可用 `go test ./internal/guardian/ -count=1`,最终必须全量。
- **绝不启动 bx / 改路由**;本计划纯代码摆放,不碰任何系统状态。
- **绝不并行派两个会写盘的子代理进同一个 checkout**。
- 提交:中文 conventional commits,结尾 `Co-Authored-By: Claude …`,默认分支直接提交。
- **两条已记档的纪律在此复述,实现时不许违反**:①「谁移植 Guardian 必须先实现 `scanRunningCores`,不能只放开 `requireDaemonPlatform`」——本计划保持 `!darwin` 全部 stub 的 fail-closed 行为原样;② `reason=lifecycle|observe` 扫描标签是「我允许了一个 Core 启动」的唯一审计线索,不许在任何转发层丢掉。

## 实测缝清单(2026-08-29,计划的事实依据)

进 bundle 的(guardian 包内 build-tag 对 + 被 daemon 组装直接选用):

| 符号 | darwin | !darwin | 组装调用点 |
|---|---|---|---|
| `requireDaemonPlatform()` | daemon_darwin.go 恒 nil | daemon_other.go 恒 ErrUnsupported | daemon.go:459 |
| `NewBarrier(CommandRunner)` | barrier_darwin.go | barrier_other.go(unsupportedBarrier) | daemon.go:475 |
| `DiscoverDefaultGateway(ctx)` | barrier_darwin.go | barrier_other.go | daemon.go:480(GatewayProviderFunc) |
| `newPlatformNetworkObserver(...)` | network_observer_darwin.go | network_observer_other.go | startRecoveredDaemon(daemon.go:525) |
| `localPeerCredentials(conn)` | peercred_darwin.go | peercred_other.go 恒 (0,false) | daemon.go:150(options.PeerCredentials 为 nil 时的默认) |

**刻意不进 bundle 的,每条有理由:**

- `scanRunningCores(reason)`:`ExecCoreRunner.ScanRunningCores` 注入钩子是**无参**的
  (process.go:128),经 bundle 无参字段转发会把 `reason=lifecycle|observe` 审计标签
  弄丢——那是 CLAUDE.md 记档的唯一放行审计线索。缝留在编译期自由函数上,第 3 步
  Linux 直接加 `procscan_linux.go`(/proc 读 cmdline,比 darwin 的 procargs2 简单)。
- `inspectProcess`/`processConfirmedGone`:前者已有 process_unix.go 的 Linux 实现,
  后者只被 darwin-tagged 代码调用,无缝可画。
- `NewDNSManager`:guardian 侧无 tag,平台差异住在 `internal/install` 的
  DNSContext 三函数里——那是 install 的缝,不是 guardian 的。
- `systemLegacyCoreLifecycle`:同上,`install.LegacyCore*` 自带 darwin/other 对。
- `RemoveBlockingBarrierRoutes`:唯一生产调用方是 CLI 逃生口
  (internal/cli/guardian.go:274),而逃生口按不变量必须独立于 daemon 工作,
  不许经 daemon 的组装清单。
- **不给 DaemonOptions 加 platform 注入字段**:options.PeerCredentials 与
  options.networkObserver 这两个既有注入缝已满足全部测试需要,第 3 步 Linux 走的
  也是编译期选择;多一个没有消费者的注入面就是多一处「实现里没有就静默不计」的
  风险形状。

## 与 spec 草图的偏离(记档,执行者不必回读 spec 争论)

spec 第 2 步草图写的是 `SpawnCore/StopCore/ObserveNetwork/ManageDNS/Barrier` 这样的
**意图方法接口**。实测:SpawnCore/StopCore 已住在 `ExecCoreRunner`(经 `ProcessOperations`
接口注入),ManageDNS/Barrier 已是接口。spec 自己写了「方法数以抽取时实测为准」——
本计划就是那次实测的结果:**清单是构造器不是动词,选择机制保持编译期**。两个恒等的
per-OS 构造器文件会谎报「选择发生在这里」,故 `newLifecyclePlatform()` 是**无 tag 单份**,
靠字段引用的 tagged 符号在编译期落到本 OS 的实现——与数据面同构,且 CI 三条腿
(ubuntu/macos/windows)天然各验一份。

---

### Task 1: `lifecyclePlatform` 清单 + 三平台完整性守卫

**Files:**
- Create: `internal/guardian/lifecycle.go`
- Create: `internal/guardian/lifecycle_test.go`
- Create: `internal/guardian/lifecycle_darwin_test.go`
- Create: `internal/guardian/lifecycle_other_test.go`

**Interfaces:**
- Consumes: 既有 tagged 符号 `requireDaemonPlatform`、`NewBarrier`、`DiscoverDefaultGateway`、`newPlatformNetworkObserver`、`localPeerCredentials`(全部已存在,本任务零改动)。
- Produces: `newLifecyclePlatform() lifecyclePlatform`(Task 2 的唯一消费入口)与 `(lifecyclePlatform).validate() error`。

- [ ] **Step 1: 写失败测试(三个文件一起)**

`internal/guardian/lifecycle_test.go`(无 tag,三平台都跑):

```go
package guardian

import "testing"

// 平台清单不许有洞:每个字段都必须被本 OS 接线(接到真实现或 fail-closed stub
// 都行,nil 不行——nil 在使用点是 panic,而 panic 在 daemon 里是崩溃循环)。
// 这条测试在 CI 的 ubuntu/macos/windows 三条腿上各跑一遍,分别证明三个 OS
// 的清单完整;新加字段忘了某个平台,先红的是这里,不是生产的 nil deref。
func TestLifecyclePlatformHasNoHoles(t *testing.T) {
	if err := newLifecyclePlatform().validate(); err != nil {
		t.Fatalf("平台清单有洞: %v", err)
	}
}
```

`internal/guardian/lifecycle_darwin_test.go`:

```go
//go:build darwin

package guardian

import "testing"

// darwin 清单接的必须是放行的门——这不是重复 daemon_darwin.go 的测试,
// 是钉住「bundle 字段指向的确实是本平台的那份实现」:把字段接错到 stub 上,
// 编译器不会抗议(签名相同),只有行为测试抓得到。
func TestLifecyclePlatformAllowsDaemonOnDarwin(t *testing.T) {
	if err := newLifecyclePlatform().RequireDaemon(); err != nil {
		t.Fatalf("darwin 上 RequireDaemon 必须放行,got %v", err)
	}
}
```

`internal/guardian/lifecycle_other_test.go`:

```go
//go:build !darwin

package guardian

import (
	"context"
	"errors"
	"testing"
)

// !darwin 的清单必须保持 fail-closed:门是关的、屏障是 unsupported。
// 「谁移植 Guardian 必须先实现 scanRunningCores,不能只放开 requireDaemonPlatform」
// ——本清单不许成为绕开那条纪律的新入口。
func TestLifecyclePlatformStaysFailClosedOffDarwin(t *testing.T) {
	p := newLifecyclePlatform()
	if err := p.RequireDaemon(); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("!darwin 上 RequireDaemon 必须拒绝,got %v", err)
	}
	if err := p.NewBarrier(nil).Install(context.Background(), BarrierContext{}); err == nil {
		t.Fatal("!darwin 屏障必须 fail-closed 拒绝安装")
	}
}
```

- [ ] **Step 2: 跑红**

Run: `go test ./internal/guardian/ -run TestLifecyclePlatform -count=1`
Expected: 编译失败,`undefined: newLifecyclePlatform`(编译失败即本步的「红」)。

- [ ] **Step 3: 最小实现**

`internal/guardian/lifecycle.go`:

```go
package guardian

import (
	"context"
	"fmt"
	"net"
	"reflect"
)

// lifecyclePlatform 是 Guardian 的平台缝清单:daemon 组装(RunDaemon/StartDaemon)
// 对平台可变实现的每一次选用,都必须经这里的一个字段,而不是直接点名 build-tag
// 自由函数——这样「Guardian 在这个平台上需要什么」是一份类型可见的检查表,
// 终局第 3 步给 Linux 供货时照单实现即可。
//
// 选择机制刻意保持编译期(字段引用的符号由 build tag 落到本 OS 实现),
// 与数据面 platform_<os>.go 同构;这里不做运行时注入——options.PeerCredentials
// 与 options.networkObserver 两个既有注入缝已覆盖全部测试需要,多一个没有
// 消费者的注入面只是多一处静默失配的机会。
//
// 刻意不在清单里的缝(别往里加,每条有记档的理由,见对应计划文档):
//   - scanRunningCores:注入钩子无参,经转发会丢 reason=lifecycle|observe 审计标签;
//   - inspectProcess:process_unix.go 已覆盖 Linux,无缝可画;
//   - NewDNSManager / LegacyCore:平台差异住在 internal/install,不在 guardian;
//   - RemoveBlockingBarrierRoutes:CLI 逃生口专用,按不变量独立于 daemon。
type lifecyclePlatform struct {
	RequireDaemon      func() error
	NewBarrier         func(CommandRunner) Barrier
	DiscoverGateway    func(context.Context) (string, error)
	NewNetworkObserver func(networkRecoveryRequester) daemonNetworkObserver
	PeerCredentials    func(net.Conn) (uint32, bool)
}

func newLifecyclePlatform() lifecyclePlatform {
	return lifecyclePlatform{
		RequireDaemon:      requireDaemonPlatform,
		NewBarrier:         NewBarrier,
		DiscoverGateway:    DiscoverDefaultGateway,
		NewNetworkObserver: newPlatformNetworkObserver,
		PeerCredentials:    localPeerCredentials,
	}
}

// validate 用反射穷举字段:新加的字段自动进检查范围,加字段的人不需要记得
// 回来改这里——与 statusdigest 嵌套穷举守卫同一条「默认参与」纪律。
func (p lifecyclePlatform) validate() error {
	v := reflect.ValueOf(p)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() != reflect.Func {
			return fmt.Errorf("lifecyclePlatform.%s 不是函数字段:清单只收平台构造器", t.Field(i).Name)
		}
		if f.IsNil() {
			return fmt.Errorf("lifecyclePlatform.%s 未接线:平台清单不许有洞", t.Field(i).Name)
		}
	}
	return nil
}
```

- [ ] **Step 4: 跑绿**

Run: `go test ./internal/guardian/ -run TestLifecyclePlatform -count=1 -v`
Expected: 本 OS(darwin)上 `TestLifecyclePlatformHasNoHoles` 与 `TestLifecyclePlatformAllowsDaemonOnDarwin` PASS;
再跑 `GOOS=linux go vet ./internal/guardian/` 确认 !darwin 侧编译通过(other 测试由 CI ubuntu/windows 腿执行)。

- [ ] **Step 5: 提交**

```bash
git add internal/guardian/lifecycle.go internal/guardian/lifecycle_test.go internal/guardian/lifecycle_darwin_test.go internal/guardian/lifecycle_other_test.go
git commit -m "feat(guardian): lifecyclePlatform 平台缝清单 —— 反射完整性守卫 + 三平台行为测试"
```

---

### Task 2: `RunDaemon`/`StartDaemon` 改为经清单选平台(纯摆放,零行为改动)

**Files:**
- Modify: `internal/guardian/daemon.go:150`(PeerCredentials 默认)、`:459`(门)、`:475`(Barrier)、`:480`(GatewayProvider)、`:525`(networkObserver 默认)——行号为 2026-08-29 快照,执行时以符号定位。

**Interfaces:**
- Consumes: Task 1 的 `newLifecyclePlatform()`。
- Produces: 无新 API。交付物是「`RunDaemon` 读不出 OS」这个性质。

**这是 refactor 任务,TDD 的例外条款生效**:无新行为,网是既有全套测试。
每一处改动都是 `直接点名 tagged 符号` → `newLifecyclePlatform().字段`,值在 darwin 上
逐函数恒等,故任何既有断言变红都意味着改错了地方,不是测试要更新。

- [ ] **Step 1: 记录基线**

Run: `go test ./internal/guardian/ -count=1 2>&1 | tail -1`
Expected: `ok`(记下来;之后每步对照)。

- [ ] **Step 2: 改 RunDaemon(门 + 三个构造)**

`daemon.go` 的 `RunDaemon` 开头与 `NewManager` 调用改为:

```go
func RunDaemon(ctx context.Context, options DaemonOptions) error {
	platform := newLifecyclePlatform()
	if err := platform.validate(); err != nil {
		return err
	}
	if err := platform.RequireDaemon(); err != nil {
		return err
	}
	// …(中间原样)…
	manager, err := NewManager(ManagerOptions{
		// …其余字段原样…
		Barrier:         platform.NewBarrier(nil),
		GatewayProvider: GatewayProviderFunc(platform.DiscoverGateway),
		// …
	})
```

- [ ] **Step 3: 改两处默认注入**

`daemon.go:150` 一带:

```go
	credentials := options.PeerCredentials
	if credentials == nil {
		credentials = newLifecyclePlatform().PeerCredentials
	}
```

`startRecoveredDaemon` 里:

```go
	if options.networkObserver == nil {
		options.networkObserver = newLifecyclePlatform().NewNetworkObserver(controller)
	}
```

(两处保持「注入优先、平台默认兜底」的既有形状,只换默认值的来源。)

- [ ] **Step 4: 跑绿 + 保真核对**

Run: `go test ./internal/guardian/ -count=1 && GOOS=linux go vet ./internal/guardian/ && GOOS=windows go vet ./internal/guardian/`
Expected: 全绿。
Run: `git diff --stat -- 'internal/guardian/*_test.go'`
Expected: 空输出(一个既有断言都没动;lifecycle_*_test.go 是 Task 1 已提交的新文件,不在 diff 里)。

- [ ] **Step 5: 确认清单外无残留直呼**

Run: `grep -rn 'requireDaemonPlatform()\|newPlatformNetworkObserver(\|= localPeerCredentials$' internal/guardian/*.go | grep -v _test | grep -v lifecycle`
Expected: 仅剩各符号自己的定义文件(daemon_darwin/other、network_observer_darwin/other、peercred_darwin/other)。这是一次性人工核对,**不做成常驻文本守卫**——本仓库已为「文本守卫被绕过 20+ 次」付足学费,常驻保证由「组装只有 RunDaemon/StartDaemon 一处」这个结构承担。

- [ ] **Step 6: 提交**

```bash
git add internal/guardian/daemon.go
git commit -m "refactor(guardian): daemon 组装经 lifecyclePlatform 选平台 —— RunDaemon 读不出 OS"
```

---

### Task 3: 全量验证 + 记档(spec 偏离与 CLAUDE.md)

**Files:**
- Modify: `docs/superpowers/specs/2026-08-29-control-plane-endgame-design.md`(第 2 步一节)
- Modify: `CLAUDE.md`(架构一节末尾追加一段)

**Interfaces:** 无代码;交付物是「关于代码的陈述与代码一致」。

- [ ] **Step 1: 全量验证**

Run: `bash scripts/verify.sh`
Expected: 退出码 0(判据是退出码,不是输出里有没有好看的字)。

- [ ] **Step 2: spec 第 2 步落账**

在 spec 第 2 步末尾追加(替换「接口草案」代码块下那句「方法数以抽取时实测为准」的段落):

```markdown
> **2026-08-29 实施落账(`docs/superpowers/plans/2026-08-29-lifecycle-platform-seam.md`)**:
> 实测后草图按事实修正——清单是**构造器不是动词**(SpawnCore/StopCore 已住在
> ExecCoreRunner 的 ProcessOperations 缝里,ManageDNS/Barrier 已是接口),选择机制
> 保持**编译期**(与数据面同构,两个恒等的 per-OS 构造器文件会谎报选择发生地)。
> `scanRunningCores` 刻意不进清单:注入钩子无参,转发会丢 reason=lifecycle|observe
> 审计标签;第 3 步 Linux 直接加 procscan_linux.go。
```

- [ ] **Step 3: CLAUDE.md 记档**

在「平台抽象(重要:跨平台的接缝)」一节末尾追加一段(与该节既有条目同风格):

```markdown
- **Guardian 侧的平台缝自 2026-08-29 起有清单**:`internal/guardian/lifecycle.go` 的
  `lifecyclePlatform`(RequireDaemon/NewBarrier/DiscoverGateway/NewNetworkObserver/
  PeerCredentials 五个构造器字段),daemon 组装只经它选平台,反射 `validate()` +
  三平台 CI 腿各一条行为测试钉住「清单无洞且接的是本平台那份」。**`scanRunningCores`
  刻意不在清单里**(注入钩子无参,转发丢 reason= 审计标签,缝留在编译期自由函数);
  `RemoveBlockingBarrierRoutes` 也不在(CLI 逃生口专用,独立于 daemon)。给 Linux
  移植 Guardian 时照 lifecycle.go 的字段清单供货,procscan/peercred/barrier 各加
  `_linux.go`,`requireDaemonPlatform` 最后放开——顺序不许反。
```

- [ ] **Step 4: 提交**

```bash
git add docs/superpowers/specs/2026-08-29-control-plane-endgame-design.md CLAUDE.md
git commit -m "docs: lifecyclePlatform 落账 —— spec 草图按实测修正,CLAUDE.md 记缝清单"
```

---

## Self-Review 记录

- **Spec coverage**:spec 第 2 步的三个承诺——缝正规化(Task 1)、组装平台盲(Task 2)、
  行为保真纪律(Global Constraints + Task 2 Step 4 的空 diff 判据)——各有任务对应。
  spec 草图与实测的偏离在 Task 3 落账,不留「文档说的比代码大」。
- **Placeholder scan**:全部代码块是可粘贴的实文;唯一的「执行时以符号定位」是行号
  免责,不是内容缺席。
- **Type consistency**:`newLifecyclePlatform()`/`validate()` 在 Task 1 定义、Task 2 消费、
  Task 3 记档,名称一致;五个字段签名逐一取自 2026-08-29 的 grep 实测
  (见「实测缝清单」表)。
