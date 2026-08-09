# 控制面第一步:菜单变瘦客户端 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 菜单栏 App 停止 spawn `bx` 子进程取状态,改为直连 Guardian socket;状态推导从 UI 收回 daemon。

**Architecture:** Guardian 成为**唯一聚合点**——它已经在跟 Core 的控制 socket 说话
(`supervisor.FetchRuntimeState`),把 Core 的运行时统计并进 `/v1/status`,菜单便只剩
一个数据源。这是 `2026-08-08-control-plane-architecture-design.md` 的第一步。

**Tech Stack:** Go 1.26(`internal/guardian`)· Swift + AppKit(`apps/macos/BxMenu`)

## Global Constraints

- **不碰数据面**,不改 fail-closed 语义。
- **三条不变量每个任务都要复验**,不是最后验一次:① 隧道不健康 → Block,绝不降级直连;
  ② 停止永不依赖别的先成功;③ 菜单必须一直存在,且任何状态下都有退出入口。
- **Core 不可达不得让 `/v1/status` 失败。** Guardian 的状态是菜单唯一的数据源,
  它必须在 Core 挂掉时仍然回答,并**如实说 Core 不可达**,而不是报错或谎报健康。
  这是 `internal/observe` 那条原则的直接应用:「问不出来」与「没有」必须可区分。
- **`/v1/status` 无授权检查**(`localapi.go:83`,任何能连到 socket 的对端都能读)。
  新增字段前先问:这个字段泄漏出去要紧吗?要紧的不加。
- **绝不运行 `bx`、`sudo bx`、`launchctl`、`networksetup`、`route`**——用户机器上 bx 正在运行且在使用中。
- **绝不运行 `git stash`、`git reset --hard`、`git checkout -- .`**(仓库有一条无关的既有 stash 必须存活)。
- 不得削弱任何既有断言。CI lint 用 `gofumpt`;改过的 Go 文件跑 `gofumpt -w`。
- 每个任务结束前跑:`go build ./... && go vet ./... && go test ./... -count=1`、
  `bash scripts/test-macos-menu.sh`、`(cd apps/macos/BxMenu && swift build)`,全绿才提交。
- 中文 conventional commits,结尾 `Co-Authored-By: Claude <noreply@anthropic.com>`,直接提交 `master`。

## 现有事实(已核对)

菜单 spawn 子进程 6 种(`main.swift`):`--version`(两处)、`status --json`、
`doctor --json --skip-probe`、`logs --help`(**能力探测**)、`update --check --json`。

菜单实际用到的 14 个字段中:

| 字段 | 现状 |
|---|---|
| `phase` `protection_state` `recovery` `dns_state` `dns_managed` `dns_service` `core_version` | **Guardian `/v1/status` 已有** |
| `latency_ms` `server` `transport` `tunnel_healthy` `udp_mode` | Core stats,Guardian 未转发 |
| `checks` | `bx doctor` |
| `core_available` | CLI 推导 |

Guardian 已有端点:`/v1/status` `/v1/up` `/v1/down` `/v1/migrate` `/v1/update`
`/v1/recoveries` `/v1/recoveries/current`。

---

### Task 1: Guardian 聚合 Core 运行时统计

**Files:**
- Modify: `internal/guardian/types.go`(`Status` 加 Core 运行时子结构)
- Modify: `internal/guardian/localapi.go`(`observableStatus` 填充它)
- Test: `internal/guardian/localapi_test.go`

**Interfaces:**
- Consumes: 无
- Produces: `Status.Core *CoreRuntime`,含 `TunnelHealthy bool`、`LatencyMS int64`、
  `Server string`、`Transport string`、`UDPMode string`、`Reachable bool`

**关键背景(实施者必读):**

1. **先读 `supervisor.FetchRuntimeState` 的真实签名与返回**,以及 Guardian 今天在哪里
   调用它(协作关闭路径)。本文档不替你断言它返回什么;照它已有的用法接。
2. **Core 不可达时必须成功返回**,`Reachable: false`,其余字段留零值。菜单是它唯一的
   数据源,让 `/v1/status` 失败等于让菜单瞎掉。
3. **`Reachable` 是三态里的「问不出来」。** 不要用 `TunnelHealthy=false` 表示「Core 没
   应答」——那是把「问不出来」压成「答案是坏的」,正是 `internal/observe` 存在的理由。
4. **取 Core 状态不得阻塞 `/v1/status` 太久。** 菜单会以秒级频率打这个端点;给一个
   明确的短超时(建议 1s),超时即 `Reachable: false`,不要拖着整个响应。

- [ ] **Step 1: 写失败测试**

```go
// Core 不可达时 /v1/status 必须成功返回并如实说不可达 —— 菜单是它唯一的数据源,
// 让这个端点失败等于让菜单瞎掉。且不得用 tunnel_healthy=false 表示「没问到」。
func TestStatusReportsCoreUnreachableWithoutFailing(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Protection: ProtectionProtected}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, LocalAPIOptions{
		CoreRuntime: func(context.Context) (CoreRuntime, error) {
			return CoreRuntime{}, errors.New("dial core: connection refused")
		},
	}).ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("Core 不可达不得让 status 失败,实际 %d", recorder.Code)
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Core == nil {
		t.Fatal("Core 字段必须在场并说明不可达")
	}
	if got.Core.Reachable {
		t.Fatal("Core 拨不通时 Reachable 必须为 false")
	}
	if got.Core.TunnelHealthy {
		t.Fatal("不得用 tunnel_healthy 表达「没问到」")
	}
}

func TestStatusCarriesCoreRuntimeWhenReachable(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Protection: ProtectionProtected}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller, LocalAPIOptions{
		CoreRuntime: func(context.Context) (CoreRuntime, error) {
			return CoreRuntime{
				Reachable: true, TunnelHealthy: true, LatencyMS: 390,
				Server: "vps", Transport: "reality@vps", UDPMode: "proxy",
			}, nil
		},
	}).ServeHTTP(recorder, request)

	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Core == nil || !got.Core.Reachable || got.Core.LatencyMS != 390 || got.Core.Transport != "reality@vps" {
		t.Fatalf("Core 运行时字段 = %+v", got.Core)
	}
}

// 没有注入取数函数时(既有调用方全都如此)不得 panic,也不得凭空造 Core 字段。
func TestStatusWithoutCoreRuntimeProviderOmitsIt(t *testing.T) {
	controller := &fakeController{status: Status{SchemaVersion: 1}}
	request := httptest.NewRequest(http.MethodGet, "/v1/status", nil)
	recorder := httptest.NewRecorder()
	NewLocalAPI(controller).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	var got Status
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Core != nil {
		t.Fatalf("未注入取数函数时不该有 Core 字段,实际 %+v", got.Core)
	}
}
```

- [ ] **Step 2: 跑测试确认红**

Run: `go test ./internal/guardian -run 'TestStatus.*Core' -count=1`
Expected: 编译失败。

- [ ] **Step 3: 实现**

`types.go` 加:

```go
// CoreRuntime 是 Core 的运行时统计,由 Guardian 代取并随 Status 一起发布。
//
// Guardian 是菜单唯一的数据源(见控制面架构设计),所以这些字段必须从这里拿得到,
// 而不是让 UI 自己去 spawn 一个 CLI 把两个源合起来。
type CoreRuntime struct {
	// Reachable 区分「问到了」与「问不出来」。Core 拨不通时其余字段全为零值,
	// 不得用 TunnelHealthy=false 冒充 —— 那是把「没问到」压成「答案是坏的」。
	Reachable     bool   `json:"reachable"`
	TunnelHealthy bool   `json:"tunnel_healthy"`
	LatencyMS     int64  `json:"latency_ms"`
	Server        string `json:"server,omitempty"`
	Transport     string `json:"transport,omitempty"`
	UDPMode       string `json:"udp_mode,omitempty"`
}
```

`Status` 加 `Core *CoreRuntime \`json:"core,omitempty"\``。

`LocalAPIOptions` 加 `CoreRuntime func(context.Context) (CoreRuntime, error)`。

`observableStatus` 在返回前:取数函数为 nil 就不填(既有调用方不受影响);否则带
**1 秒超时**调用,出错或超时填 `&CoreRuntime{Reachable: false}`。

- [ ] **Step 4: 接线**

在 Guardian daemon 构造 `LocalAPIOptions` 的地方(`daemon.go`,搜 `LocalAPIOptions{`)
补上取数函数,内部用 `supervisor.FetchRuntimeState` ——**先读它的真实签名再写**。

- [ ] **Step 5: 跑测试 + 变异**

Run: `go test ./internal/guardian -count=1` → 全绿。

变异:把「取数出错 → `Reachable:false`」改成「取数出错 → 返回 500」,跑
`go test ./internal/guardian -run TestStatusReportsCoreUnreachable -count=1` → 应 FAIL。改回。

变异:把 `Reachable:false` 时的 `TunnelHealthy` 改成 `true`,同一条测试应 FAIL。改回。

- [ ] **Step 6: 提交**

```bash
gofumpt -w internal/guardian/types.go internal/guardian/localapi.go internal/guardian/localapi_test.go internal/guardian/daemon.go
git add internal/guardian/
git commit -m "$(cat <<'EOF'
feat(guardian): status 聚合 Core 运行时统计

菜单每 5 秒 spawn 两个 bx 子进程,主要只为拿 Core 的五个统计字段 —— 而
Guardian 本来就在跟 Core 的控制 socket 说话。把它们并进 /v1/status,菜单
就只剩一个数据源。

Core 不可达时 status 仍然成功返回并如实说 reachable=false:菜单是它唯一
的数据源,让这个端点失败等于让菜单瞎掉。且不用 tunnel_healthy=false 冒充
「没问到」—— 那是把问不出来压成答案是坏的。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Swift 侧取 Guardian 状态

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift`(补字段)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`(加 `.status` 端点)
- Create: `apps/macos/BxMenu/Tests/GuardianStatusFieldsTests.swift`
- Modify: `scripts/test-macos-menu.sh`

**Interfaces:**
- Consumes: Task 1 的 `/v1/status` 响应
- Produces: `GuardianClient.status() throws -> GuardianStatus`;`GuardianStatus` 补上
  `phase`、`protectionState`、`recovery`、`dnsState`、`dnsManaged`、`dnsService`、
  `coreVersion`、`core`(嵌套 `CoreRuntime`)

**关键背景:** `GuardianStatus` 今天只解 4 个字段(阶段① 加的),要补齐菜单需要的其余。
**所有新字段都用 `decodeIfPresent` + 可选类型**——Guardian 的 `omitempty` 会让字段缺席,
把缺席解成失败会让菜单在完全正常的情况下瞎掉。

`GuardianEndpoint` 已有 `.turnOn`/`.turnOff`/`.requestRecovery`/`.currentRecovery` 四态,
加 `.status`(GET `/v1/status`,期望 200,用短超时 `guardianDefaultTimeout`)。

- [ ] **Step 1: 写失败测试**(标准 `@main struct` 形式,见同目录其它测试)

覆盖三件事:完整响应解出全部字段;**Guardian 省略 `core` 时不报错、`core` 为 nil**;
`core.reachable=false` 时其余字段为零值且**不被误读成健康**。

- [ ] **Step 2: 注册进 `scripts/test-macos-menu.sh` 并确认红**

- [ ] **Step 3: 实现**

- [ ] **Step 4: 跑 `bash scripts/test-macos-menu.sh` + `swift build`**

- [ ] **Step 5: 变异验证**:把某个新字段改成非可选,确认「Guardian 省略它」那条测试转红。

- [ ] **Step 6: 提交**

---

### Task 3: 菜单状态推导改由 Guardian 提供

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`(`loadState`)
- Modify: `apps/macos/BxMenu/Sources/BxMenu/MenuRows.swift`(改吃 `GuardianStatus`)
- Modify: `internal/cli/cli_test.go`(源码守卫)
- Test: 相应 Swift 测试

**关键背景与要求:**

1. **删掉轮询路径上的 `runBx(["--version"])` 与 `runBx(["status","--json"])` 两处**
   ——它们是每次刷新都跑的。`doctor` / `update --check` / `logs --help` 属 Task 4,本任务不动。
2. `BxState` 的八个 case **保持不变**,只换数据来源。同时改状态机与数据源会让任何回归
   都无法定位是哪一半造成的。
3. **加源码守卫**(Go 侧,因为 CI 不编译 Swift 之外还有它跑不到 main.swift):
   `main.swift` 中不得再出现 `runBx(["status"` 与 `runBx(["--version"`。
4. **`checks` 与 `coreAvailable` 本任务先不接**——`checks` 归 Task 4;`coreAvailable`
   用 `core?.reachable` 顶上,并在注释里写明这是等价替换的依据。

- [ ] **Step 1–6:** 同前:先写失败测试与守卫 → 跑红 → 实现 → 跑绿 → 变异 → 提交。
      变异必须包含:把 `runBx(["status"` 加回去,守卫应转红。

---

### Task 4: doctor 与更新检查上移,删掉能力探测

**Files:** `internal/guardian/localapi.go`(新端点)· `main.swift` · `cli_test.go`

**关键背景:** 这一任务收掉剩下三种 spawn。其中 **`bx logs --help` 是能力探测**——
UI 靠解析帮助文本判断装着的 CLI 支不支持某个 flag。这是整份架构诊断里最直白的症状,
**能力应当由 daemon 在 status 里声明**(例如一个 `capabilities` 字段),而不是让 UI 去
问一个可能是旧版的二进制。

`doctor` 与 `update --check` 频率低(按需 / 每日),可作为独立端点,不必并进 `/v1/status`。
**先读 `doctor --json` 与 `update --check --json` 的真实输出结构再设计端点**,
不要凭本文档猜。

同样注意:`/v1/status` 无授权检查,新端点要各自决定授权级别——`doctor` 的输出含路径与
网络细节,**不应当无授权可读**。参照 `/v1/recoveries` 的 `authorizeOwnerPeer` 写法。

- [ ] **Step 1–6:** 同前。守卫:`main.swift` 中不得再出现任何 `runBx(`。

---

## 真机验收(由用户执行)

1. 菜单显示的状态与 `sudo bx status` 一致(节点/延迟/传输/DNS)
2. `sudo bx down` 后菜单转为「未保护」,**且仍有退出入口**(不变量 ③)
3. 停掉 Core(不停 Guardian)→ 菜单应显示 Core 不可达,**不是显示健康、也不是白屏**
4. `sudo fs_usage -w -f exec | grep bx` 期间打开/关闭菜单:**不应再看到 bx 子进程被 spawn**
   (Task 3 之后是轮询路径,Task 4 之后是全部)
5. 隧道不健康时仍然 fail-closed(不变量 ①):断开 VPS,确认流量被 Block 而非直连

## 自查

**Spec 覆盖:** 架构设计第一步的四项——菜单停止 spawn、状态推导收回 daemon、
Guardian 补端点、删掉 `logs --help` 能力探测,分别落在 Task 1–4。

**占位符:** 无 TBD。Task 1 Step 4、Task 4 都显式要求实施者**先读真实签名/输出结构**
再写——那是防止本文档的猜测覆盖代码事实,不是占位。

**类型一致:** `guardian.CoreRuntime`(Task 1)→ Swift `GuardianStatus.core`(Task 2)→
`MenuRows`/`loadState` 消费(Task 3)。三处字段名以 Task 1 的 json tag 为准。
