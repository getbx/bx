# 菜单栏阶段① 实施计划:owner_uid 授权 + 开关走 socket + 异步化

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让菜单栏的开/关不再弹管理员密码框、不再阻塞主线程,并在动作进行中持续显示已用时与失败原因。

**Architecture:** Guardian 侧把 `/v1/up`、`/v1/down` 的授权判据从「必须 root」换成既有的
「root 或 config 里的 `owner_uid`」——该判据已存在(`authorizeRecoveryPeer`)且已守着
`/v1/recoveries`。菜单侧改为经已有的 `GuardianClient`(AF_UNIX + HTTP)在后台队列发起变更,
纯逻辑(进度文案、逾时阈值、失败码指引)抽进可单测的 `ToggleController.swift`,`main.swift`
只做接线。

**Tech Stack:** Go 1.26(`internal/guardian`)· Swift + AppKit(`apps/macos/BxMenu`)·
测试:`go test ./...` 与 `scripts/test-macos-menu.sh`(用 `swiftc` 直编直跑,非 XCTest)

## Global Constraints

- **只放开 `mutationHandler`**(服务 `/v1/up` 与 `/v1/down`)。`updateHandler`(`/v1/update`)
  与 `migrationHandler`(`/v1/migrate`)**保持 root only**,必须有独立测试钉住。
- 授权判据统一为 `credentials.got && (credentials.uid == 0 || (ownerUID != 0 && credentials.uid == ownerUID))`。
  **`ownerUID == 0` 时退化为 root-only**,不得放宽。
- **绝不阻塞主线程**:任何 Guardian 变更调用都在后台队列执行,回主线程只更新 UI。
- **绝不放宽既有断言**。本计划不需要修改任何现有测试的断言;若有测试转红,先弄清原因再动。
- **绝不运行 `bx`、`sudo bx`、`launchctl`、`networksetup`、`route`**,不得启动或安装 bx。
- Guardian 响应体只带失败码,不外传原始错误串;完整错误只进 Guardian 日志。
- 提交信息用中文 conventional commits,结尾带 `Co-Authored-By: Claude <noreply@anthropic.com>`。
  按 CLAUDE.md,本项目**直接在 `master` 提交**。
- 每个任务结束前跑:`go build ./... && go vet ./... && go test ./... -count=1` 与
  `bash scripts/test-macos-menu.sh`,全绿才提交。

---

## 文件结构

| 文件 | 职责 | 动作 |
|---|---|---|
| `internal/guardian/localapi.go` | Guardian HTTP 控制面 | 改:判据重命名 + `mutationHandler` 收 ownerUID |
| `internal/guardian/localapi_test.go` | 同上测试 | 加:owner 放行 / update·migrate 仍 root-only |
| `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift` | Guardian `Status` 的 Swift 解码 | **新建** |
| `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift` | AF_UNIX HTTP 客户端 | 改:加 `.up`/`.down` 端点 + 泛型解码 + 变更超时 |
| `apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift` | 开关的纯逻辑:进度文案、逾时阈值、失败指引 | **新建** |
| `apps/macos/BxMenu/Tests/GuardianStatusTests.swift` | 解码测试 | **新建** |
| `apps/macos/BxMenu/Tests/ToggleControllerTests.swift` | 纯逻辑测试 | **新建** |
| `scripts/test-macos-menu.sh` | Swift 测试运行器 | 改:注册两个新测试 |
| `apps/macos/BxMenu/Sources/BxMenu/main.swift` | AppKit 接线 | 改:开关走 socket、异步、进度 |

`main.swift` 不在测试脚本的编译单元里(它是 AppKit 应用),所以**一切可判定的逻辑必须落在
`ToggleController.swift`**,`main.swift` 只保留调用与 UI 更新。这与该目录既有的分工一致
(`StatusPresentation`/`UpdatePresentation`/`RecoveryPresentation` 都是这么切的)。

---

### Task 1: Guardian 放开 up/down 到 owner_uid

**Files:**
- Modify: `internal/guardian/localapi.go`(`authorizeRecoveryPeer` 约 :288、`mutationHandler` 约 :443、`NewLocalAPI` 里 `/v1/up` `/v1/down` 两处注册)
- Test: `internal/guardian/localapi_test.go`

**Interfaces:**
- Consumes: 无(本任务是起点)
- Produces: `/v1/up` 与 `/v1/down` 接受 `uid == options.OwnerUID` 的对端;
  `authorizeOwnerPeer(ctx context.Context, ownerUID uint32) bool` 为全包共用判据。

**关键背景(实施者必读):** `NewLocalAPI(controller Controller, provided ...LocalAPIOptions)`
是变参的。测试里大量存在 `NewLocalAPI(controller)` 的调用,此时 `options.OwnerUID` 为 **0**,
判据里 `ownerUID != 0` 为假 ⇒ 退化成 root-only ⇒ **既有测试全部照常通过,不要去改它们**。

- [ ] **Step 1: 写失败测试——配了 owner 时,owner 可以 up/down**

在 `internal/guardian/localapi_test.go` 末尾追加:

```go
// 菜单栏以普通用户身份连 Guardian socket 开关保护,不再弹管理员密码框。
// 该判据早已存在并守着 /v1/recoveries(路径恢复会重装路由,同样是改网络的操作),
// 此处把它推广到日常开关。装卸与迁移不在其列,见下一条测试。
func TestLocalAPIUpDownAcceptsConfiguredOwner(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		uid       uint32
		gotUID    bool
		wantCode  int
		wantUp    int
		wantDown  int
	}{
		{name: "owner up", path: "/v1/up", uid: 501, gotUID: true, wantCode: http.StatusOK, wantUp: 1},
		{name: "owner down", path: "/v1/down", uid: 501, gotUID: true, wantCode: http.StatusOK, wantDown: 1},
		{name: "root up", path: "/v1/up", uid: 0, gotUID: true, wantCode: http.StatusOK, wantUp: 1},
		{name: "other user up", path: "/v1/up", uid: 502, gotUID: true, wantCode: http.StatusForbidden},
		{name: "no credentials", path: "/v1/up", wantCode: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1, Desired: DesiredOn, Phase: PhaseCommitted, Protection: ProtectionProtected}}
			request := httptest.NewRequest(http.MethodPost, tt.path, nil)
			request = request.WithContext(withPeerCredentials(request.Context(), tt.uid, tt.gotUID))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", recorder.Code, tt.wantCode, recorder.Body.String())
			}
			if controller.upCalls != tt.wantUp {
				t.Fatalf("Up calls = %d, want %d", controller.upCalls, tt.wantUp)
			}
			if controller.downCalls != tt.wantDown {
				t.Fatalf("Down calls = %d, want %d", controller.downCalls, tt.wantDown)
			}
		})
	}
}

// 回归守卫:装卸与版本迁移**不**随开关一起放开。
// 没有这条,将来一次「顺手把判据统一一下」就会把 update/migrate 也交给普通用户,
// 而那两个动作会改磁盘上的二进制与服务定义,后果与开关完全不同量级。
func TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured(t *testing.T) {
	for _, path := range []string{"/v1/update", "/v1/migrate"} {
		t.Run(path, func(t *testing.T) {
			controller := &fakeController{status: Status{SchemaVersion: 1}}
			request := httptest.NewRequest(http.MethodPost, path, nil)
			request = request.WithContext(withPeerCredentials(request.Context(), 501, true))
			recorder := httptest.NewRecorder()
			NewLocalAPI(controller, LocalAPIOptions{OwnerUID: 501}).ServeHTTP(recorder, request)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("%s status = %d, want 403 (owner 不得装卸/迁移), body=%s",
					path, recorder.Code, recorder.Body.String())
			}
		})
	}
}
```

- [ ] **Step 2: 跑测试确认第一条红、第二条绿**

Run: `go test ./internal/guardian -run 'TestLocalAPIUpDownAcceptsConfiguredOwner|TestLocalAPIUpdateAndMigrateStayRootOnly' -count=1 -v`

Expected:
- `TestLocalAPIUpDownAcceptsConfiguredOwner/owner_up` 与 `/owner_down` **FAIL**(403,当前只认 root)
- `TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured` **PASS**(这条本来就该绿,它是守卫)

若第二条一开始就红,停下来查原因——那说明 update/migrate 的现状与设计不符。

- [ ] **Step 3: 把判据改名为共用名**

`internal/guardian/localapi.go`,把:

```go
func authorizeRecoveryPeer(ctx context.Context, ownerUID uint32) bool {
```

改为:

```go
// authorizeOwnerPeer 是「root 或 config 里配置的 owner」这条判据。
//
// ownerUID 为 0(未配置)时退化为 root-only —— 绝不因为「没配」就放宽。
// 守着 /v1/recoveries(路径恢复)与 /v1/up、/v1/down(日常开关)。
// 装卸(/v1/update)与迁移(/v1/migrate)刻意不用它,见
// TestLocalAPIUpdateAndMigrateStayRootOnlyEvenWithOwnerConfigured。
func authorizeOwnerPeer(ctx context.Context, ownerUID uint32) bool {
```

函数体一个字不改。然后把包内两处调用点(`recoveryRequestHandler` 与 `recoveryCurrentHandler`
内部)的 `authorizeRecoveryPeer(` 全部换成 `authorizeOwnerPeer(`。

Run: `grep -rn "authorizeRecoveryPeer" internal/` 确认结果为空。

- [ ] **Step 4: `mutationHandler` 收下 ownerUID 并改判据**

签名改为:

```go
func mutationHandler(controller Controller, mutate func(context.Context) error, mutations *acceptedMutations, ownerUID uint32) http.HandlerFunc {
```

把函数体里这一段:

```go
		credentials, _ := r.Context().Value(peerCredentialsKey{}).(peerCredentials)
		if !credentials.got || credentials.uid != 0 {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root peer"})
			return
		}
```

换成:

```go
		if !authorizeOwnerPeer(r.Context(), ownerUID) {
			writeGuardianJSON(w, http.StatusForbidden, map[string]string{"error": "mutation requires root or owner peer"})
			return
		}
```

`NewLocalAPI` 里两处注册补上参数:

```go
	mux.HandleFunc("/v1/up", mutationHandler(controller, controller.Up, mutations, options.OwnerUID))
	mux.HandleFunc("/v1/down", mutationHandler(controller, controller.Down, mutations, options.OwnerUID))
```

- [ ] **Step 5: 跑全部 guardian 测试**

Run: `go test ./internal/guardian -count=1`
Expected: PASS。**特别确认 `TestLocalAPIMutationsRequireRootPeer` 仍然绿**——它用的是
`NewLocalAPI(controller)`(OwnerUID=0),按设计应当保持 root-only,一个断言都不该动。

- [ ] **Step 6: 变异验证——证明新测试确实在守东西**

临时把 Step 4 的判据改回 `if !credentials.got || credentials.uid != 0`,跑:

Run: `go test ./internal/guardian -run TestLocalAPIUpDownAcceptsConfiguredOwner -count=1`
Expected: FAIL(owner 被拒)。确认后**把修改改回来**再继续。

再临时把 `updateHandler` 的判据换成 `authorizeOwnerPeer(r.Context(), ownerUID)`(需临时加参数),跑:

Run: `go test ./internal/guardian -run TestLocalAPIUpdateAndMigrateStayRootOnly -count=1`
Expected: FAIL。确认后**把修改改回来**。

- [ ] **Step 7: 全量验证并提交**

Run: `go build ./... && go vet ./... && go test ./... -count=1`

```bash
git add internal/guardian/localapi.go internal/guardian/localapi_test.go
git commit -m "$(cat <<'EOF'
feat(guardian): up/down 认 owner_uid,菜单栏开关不再需要管理员密码

判据 authorizeOwnerPeer(原 authorizeRecoveryPeer)早已存在并守着
/v1/recoveries —— 路径恢复会重装路由,同样是改网络的操作。此处把它推广到
/v1/up 与 /v1/down 这两个日常开关。

update 与 migrate 保持 root only:它们改磁盘上的二进制与服务定义,与开关
不是一个量级。回归守卫钉住这条,防止将来「顺手统一判据」把装卸也放开。

ownerUID 为 0(未配置)时判据退化为 root-only,故既有测试
TestLocalAPIMutationsRequireRootPeer 原样通过,未放宽任何断言。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Swift 侧解码 Guardian Status,GuardianClient 支持 up/down

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift`
- Create: `apps/macos/BxMenu/Tests/GuardianStatusTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`
- Modify: `scripts/test-macos-menu.sh`

**Interfaces:**
- Consumes: Task 1 放开的 `/v1/up`、`/v1/down`(POST,成功返回 200 + `Status` JSON)
- Produces:
  - `struct GuardianStatus: Decodable`,字段 `desired: String`、`phase: String`、
    `protectionState: String`、`lastError: String?`
  - `GuardianClient.turnOn() throws -> GuardianStatus`
  - `GuardianClient.turnOff() throws -> GuardianStatus`
  - `func decodeGuardianHTTPResponse<T: Decodable>(_ response: Data, expectedStatus: Int, as type: T.Type) throws -> T`

**关键背景:** Go 侧 `guardianMutationTimeout = time.Minute`,即服务端自己 60 秒后会返回错误。
客户端超时必须**大于**它(取 75 秒),否则拿到的是客户端超时而非服务端给的失败码。
既有 `guardianDefaultTimeout` 是 5 秒,只用于恢复相关端点,不要改它。

- [ ] **Step 1: 写失败测试**

新建 `apps/macos/BxMenu/Tests/GuardianStatusTests.swift`:

```swift
import Foundation

var failures = 0

func expect(_ condition: Bool, _ message: String) {
    if !condition {
        failures += 1
        FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
    }
}

// Guardian 的 Status JSON 字段远多于菜单需要的几个;解码必须容忍未知字段,
// 也必须容忍 last_error 缺席(omitempty:成功时它根本不出现)。
let fullBody = Data("""
{"schema_version":1,"desired":"on","phase":"committed","protection_state":"protected",
 "network_generation":"wifi-a","recovery":{},"dns_state":"managed","dns_managed":true,
 "guardian_version":"v0.2.9","runtime_version":"v0.2.9"}
""".utf8)

do {
    let status = try JSONDecoder().decode(GuardianStatus.self, from: fullBody)
    expect(status.desired == "on", "desired = \(status.desired)")
    expect(status.phase == "committed", "phase = \(status.phase)")
    expect(status.protectionState == "protected", "protectionState = \(status.protectionState)")
    expect(status.lastError == nil, "成功响应不该有 lastError,实际 \(String(describing: status.lastError))")
} catch {
    expect(false, "完整响应解码失败: \(error)")
}

// 失败响应带 last_error,菜单要拿它去查指引
let failureBody = Data(#"{"schema_version":1,"desired":"on","phase":"failed","protection_state":"needs_attention","last_error":"core_ownership_uncertain"}"#.utf8)
do {
    let status = try JSONDecoder().decode(GuardianStatus.self, from: failureBody)
    expect(status.lastError == "core_ownership_uncertain", "lastError = \(String(describing: status.lastError))")
} catch {
    expect(false, "失败响应解码失败: \(error)")
}

// 完整 HTTP 响应经泛型解码走通
let http = Data("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 76\r\n\r\n".utf8)
    + Data(#"{"schema_version":1,"desired":"off","phase":"committed","protection_state":"off"}"#.utf8.prefix(76))
do {
    let status = try decodeGuardianHTTPResponse(http, expectedStatus: 200, as: GuardianStatus.self)
    expect(status.desired == "off", "HTTP 解码 desired = \(status.desired)")
} catch {
    expect(false, "HTTP 泛型解码失败: \(error)")
}

if failures == 0 {
    print("GuardianStatusTests passed")
} else {
    exit(1)
}
```

- [ ] **Step 2: 注册到测试脚本并确认编译失败**

在 `scripts/test-macos-menu.sh` 的 `run_test install-presentation …` 之后追加:

```bash
run_test guardian-status \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianStatus.swift" \
  "$MENU/Sources/BxMenu/GuardianClient.swift" \
  "$MENU/Tests/GuardianStatusTests.swift"
```

Run: `bash scripts/test-macos-menu.sh`
Expected: 在 `guardian-status` 处失败,报 `GuardianStatus.swift` 不存在。

- [ ] **Step 3: 新建 GuardianStatus.swift**

```swift
import Foundation

/// Guardian `/v1/status`、`/v1/up`、`/v1/down` 响应体里菜单实际用到的字段。
///
/// 刻意只声明用得上的几个:Guardian 的 Status 还有 core_pid、dns_service 等十余个
/// 字段,`Decodable` 默认忽略未声明的键,新增字段不会让菜单解码失败。
struct GuardianStatus: Decodable {
    let desired: String
    let phase: String
    let protectionState: String
    /// 成功时服务端 omitempty 掉这个键,故为可选——不是「有但为空」。
    let lastError: String?

    enum CodingKeys: String, CodingKey {
        case desired
        case phase
        case protectionState = "protection_state"
        case lastError = "last_error"
    }
}
```

- [ ] **Step 4: GuardianClient 加泛型解码与 up/down**

在 `GuardianClient.swift` 顶部常量区,`guardianDefaultTimeout` 之后加:

```swift
// Go 侧 guardianMutationTimeout = 1 分钟。客户端必须比它长,否则拿到的是自己的
// 超时而不是服务端给的失败码(用户最需要的恰恰是那个码)。
private let guardianMutationTimeout: TimeInterval = 75
```

`GuardianEndpoint` 扩成:

```swift
private enum GuardianEndpoint {
    case requestRecovery
    case currentRecovery
    case turnOn
    case turnOff

    var expectedStatus: Int {
        switch self {
        case .requestRecovery: return 202
        case .currentRecovery, .turnOn, .turnOff: return 200
        }
    }

    var timeout: TimeInterval {
        switch self {
        case .requestRecovery, .currentRecovery: return guardianDefaultTimeout
        case .turnOn, .turnOff: return guardianMutationTimeout
        }
    }
}
```

`guardianRequest(for:)` 的 switch 补两个分支:

```swift
    case .turnOn:
        method = "POST"
        path = "/v1/up"
        body = Data("{}".utf8)
    case .turnOff:
        method = "POST"
        path = "/v1/down"
        body = Data("{}".utf8)
```

把现有 `decodeGuardianHTTPResponse` 改成泛型,并保留原签名作为薄包装
(**既有 `GuardianClientTests.swift` 用的是原签名,不得破坏**):

```swift
func decodeGuardianHTTPResponse<T: Decodable>(_ response: Data, expectedStatus: Int, as type: T.Type) throws -> T {
    let head = try parseGuardianHTTPHead(response)
    guard head.status == expectedStatus else {
        throw GuardianClientError.status(head.status)
    }
    let body = response[head.bodyOffset...]
    guard body.count == head.contentLength else {
        throw GuardianClientError.invalidResponse
    }
    do {
        return try JSONDecoder().decode(T.self, from: body)
    } catch {
        throw GuardianClientError.invalidResponse
    }
}

func decodeGuardianHTTPResponse(_ response: Data, expectedStatus: Int) throws -> RecoverySnapshot {
    try decodeGuardianHTTPResponse(response, expectedStatus: expectedStatus, as: RecoverySnapshot.self)
}
```

把 `perform` 泛型化,并改用 endpoint 自己的超时:

```swift
    func requestRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .requestRecovery, as: RecoverySnapshot.self)
    }

    func currentRecovery() throws -> RecoverySnapshot {
        try perform(endpoint: .currentRecovery, as: RecoverySnapshot.self)
    }

    func turnOn() throws -> GuardianStatus {
        try perform(endpoint: .turnOn, as: GuardianStatus.self)
    }

    func turnOff() throws -> GuardianStatus {
        try perform(endpoint: .turnOff, as: GuardianStatus.self)
    }

    private func perform<T: Decodable>(endpoint: GuardianEndpoint, as type: T.Type) throws -> T {
        let deadline = GuardianDeadline(timeout: overrideTimeout ?? endpoint.timeout, now: clock)
        let fd = try connectSocket(deadline)
        defer { close(fd) }
        try configureGuardianSocketTimeouts(fd, timeout: deadline.remaining())
        try setGuardianSocketNonBlocking(fd)
        var noSignal: Int32 = 1
        guard setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout.size(ofValue: noSignal))) == 0 else {
            throw GuardianClientError.socket(errno)
        }
        try writeGuardianRequest(guardianRequest(for: endpoint), to: fd, deadline: deadline)
        let response = try readGuardianHTTPResponse(from: fd, deadline: deadline)
        let decoded = try decodeGuardianHTTPResponse(response, expectedStatus: endpoint.expectedStatus, as: T.self)
        try deadline.checkpoint()
        return decoded
    }
```

`ioTimeout` 字段改名为 `overrideTimeout: TimeInterval?`:`init()` 里设 `nil`(让每个端点用
自己的超时),测试用的 `init(connectSocket:ioTimeout:clock:)` 里设为传入值(既有测试靠它注入
短超时,**行为不得变**)。

```swift
    private let overrideTimeout: TimeInterval?

    init() {
        overrideTimeout = nil
        clock = { ProcessInfo.processInfo.systemUptime }
        connectSocket = { deadline in try connectToGuardian(deadline: deadline) }
    }

    init(
        connectSocket: @escaping () throws -> Int32,
        ioTimeout: TimeInterval = guardianDefaultTimeout,
        clock: @escaping () -> TimeInterval = { ProcessInfo.processInfo.systemUptime }
    ) {
        self.connectSocket = { _ in try connectSocket() }
        self.overrideTimeout = ioTimeout
        self.clock = clock
    }
```

- [ ] **Step 5: 跑测试**

Run: `bash scripts/test-macos-menu.sh`
Expected: 全部通过,包含 `GuardianStatusTests passed`。**既有 `guardian-client` 那条必须仍然绿**
——它用的是原 `decodeGuardianHTTPResponse` 签名和注入超时的 init,两者都被刻意保留了。

- [ ] **Step 6: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/GuardianStatus.swift \
        apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift \
        apps/macos/BxMenu/Tests/GuardianStatusTests.swift \
        scripts/test-macos-menu.sh
git commit -m "$(cat <<'EOF'
feat(menu): GuardianClient 支持 up/down,解码 Guardian Status

客户端超时取 75 秒,必须长于 Go 侧的 guardianMutationTimeout(1 分钟),
否则拿到的是自己的超时而非服务端给的失败码 —— 而用户最需要的恰恰是那个码。

decodeGuardianHTTPResponse 泛型化后保留原 RecoverySnapshot 签名作为薄包装,
既有 GuardianClientTests 一个字未改。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: 开关的纯逻辑(进度文案、逾时阈值、失败指引)

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift`
- Create: `apps/macos/BxMenu/Tests/ToggleControllerTests.swift`
- Modify: `scripts/test-macos-menu.sh`

**Interfaces:**
- Consumes: `GuardianStatus.lastError`(Task 2)
- Produces:
  - `enum ToggleAction { case turnOn, turnOff }`,属性 `progressTitle: String`、`verb: String`
  - `func toggleProgressText(action: ToggleAction, elapsedSeconds: Int) -> String`
  - `func toggleSlowHint(action: ToggleAction, elapsedSeconds: Int) -> String?`
  - `func toggleFailureHint(code: String?) -> String?`
  - `let toggleSlowThresholdSeconds = 20`

- [ ] **Step 1: 写失败测试**

新建 `apps/macos/BxMenu/Tests/ToggleControllerTests.swift`:

```swift
import Foundation

var failures = 0

func expect(_ condition: Bool, _ message: String) {
    if !condition {
        failures += 1
        FileHandle.standardError.write(Data("FAIL: \(message)\n".utf8))
    }
}

// 进行中必须显示已用时。2026-08-04 事故里 bx down 卡了 71 分钟,
// 界面全程没有一个字 —— 秒数是这一期最核心的产出。
expect(toggleProgressText(action: .turnOff, elapsedSeconds: 0) == "正在断开… 0 秒",
       "0 秒文案 = \(toggleProgressText(action: .turnOff, elapsedSeconds: 0))")
expect(toggleProgressText(action: .turnOff, elapsedSeconds: 23) == "正在断开… 23 秒",
       "23 秒文案 = \(toggleProgressText(action: .turnOff, elapsedSeconds: 23))")
expect(toggleProgressText(action: .turnOn, elapsedSeconds: 2) == "正在连接… 2 秒",
       "连接文案 = \(toggleProgressText(action: .turnOn, elapsedSeconds: 2))")

// 逾时提示:阈值以下不出现,达到阈值才出现
expect(toggleSlowHint(action: .turnOff, elapsedSeconds: 19) == nil, "19 秒不该有逾时提示")
expect(toggleSlowHint(action: .turnOff, elapsedSeconds: 20) != nil, "20 秒必须有逾时提示")
expect(toggleSlowHint(action: .turnOff, elapsedSeconds: 60)?.contains("通常") == true,
       "逾时提示要说明正常耗时")

// 失败指引:必须与 Go 侧 guardianCodeHints 对齐。
// core_ownership_uncertain 是锁存的,唯一出路是 down 再 up —— 不说这句用户无从下手。
let uncertain = toggleFailureHint(code: "core_ownership_uncertain")
expect(uncertain != nil, "core_ownership_uncertain 必须有指引")
expect(uncertain?.contains("bx down") == true && uncertain?.contains("bx up") == true,
       "指引必须给出 down 再 up 这条唯一出路,实际 = \(String(describing: uncertain))")

// 未知码与无码都不能编造指引,但也不能崩
expect(toggleFailureHint(code: nil) == nil, "无码时不得编造指引")
expect(toggleFailureHint(code: "some_future_code") == nil, "未知码不得编造指引")

if failures == 0 {
    print("ToggleControllerTests passed")
} else {
    exit(1)
}
```

- [ ] **Step 2: 注册并确认失败**

`scripts/test-macos-menu.sh` 追加:

```bash
run_test toggle-controller \
  "$MENU/Sources/BxMenu/ToggleController.swift" \
  "$MENU/Tests/ToggleControllerTests.swift"
```

Run: `bash scripts/test-macos-menu.sh`
Expected: 在 `toggle-controller` 处失败,报 `ToggleController.swift` 不存在。

- [ ] **Step 3: 实现**

新建 `apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift`:

```swift
import Foundation

/// 超过这个秒数就在菜单里追加一行「比预期久」并给出日志入口。
///
/// 20 秒的依据:正常的 up/down 在 3 秒内完成,而 Go 侧 guardianMutationTimeout
/// 是 60 秒 —— 阈值必须落在两者之间,让用户在服务端还没放弃之前就拿到线索。
let toggleSlowThresholdSeconds = 20

enum ToggleAction {
    case turnOn
    case turnOff

    /// 进行中的动词。用「正在连接/正在断开」而非「启动中/停止中」,
    /// 与菜单其余文案(已保护/未保护)保持同一套说法。
    var progressVerb: String {
        switch self {
        case .turnOn: return "正在连接"
        case .turnOff: return "正在断开"
        }
    }
}

/// 进行中的状态行文案,永远带已用秒数。
func toggleProgressText(action: ToggleAction, elapsedSeconds: Int) -> String {
    "\(action.progressVerb)… \(max(0, elapsedSeconds)) 秒"
}

/// 逾时提示;未达阈值返回 nil(调用方据此决定要不要多画一行)。
func toggleSlowHint(action: ToggleAction, elapsedSeconds: Int) -> String? {
    guard elapsedSeconds >= toggleSlowThresholdSeconds else { return nil }
    return "比预期久,通常 3 秒内完成"
}

/// 失败码 → 用户能照做的下一步。
///
/// 与 Go 侧 internal/guardian/client.go 的 guardianCodeHints 对齐:Guardian 的
/// 响应体刻意只回传失败码、不外传原始错误串(可能含路径/链接/凭据),所以
/// 具体说法必须写在客户端。未知码返回 nil —— 宁可不给指引,也不编一句错的。
func toggleFailureHint(code: String?) -> String? {
    guard let code, !code.isEmpty else { return nil }
    switch code {
    case "core_ownership_uncertain":
        return "若确认没有第二个 bx 在跑,执行 sudo bx down 再 sudo bx up 可清除这条已锁存的判定"
    case "recovery_incomplete":
        return "上次网络恢复未完成。执行 sudo bx down 再 sudo bx up"
    case "guardian_busy":
        return "Guardian 正在处理上一个请求,稍候重试"
    default:
        return nil
    }
}
```

- [ ] **Step 4: 跑测试**

Run: `bash scripts/test-macos-menu.sh`
Expected: 全绿,含 `ToggleControllerTests passed`。

- [ ] **Step 5: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/ToggleController.swift \
        apps/macos/BxMenu/Tests/ToggleControllerTests.swift \
        scripts/test-macos-menu.sh
git commit -m "$(cat <<'EOF'
feat(menu): 开关进度与失败指引的纯逻辑

进行中永远显示已用秒数,20 秒后追加「比预期久」——2026-08-04 那次 down
卡了 71 分钟,界面全程没有一个字。阈值落在正常耗时(3 秒)与服务端
mutation 超时(60 秒)之间,让用户在服务端放弃之前就拿到线索。

失败指引与 Go 侧 guardianCodeHints 对齐;未知码返回 nil,宁可不给指引
也不编一句错的。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: main.swift 接线——开关走 socket、后台执行、菜单显示进度

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
  (`startBx` :443、`turnOffBx` :729、`quitBx` :713、`rebuildMenu` 的状态分支)

**Interfaces:**
- Consumes: `GuardianClient.turnOn()`/`turnOff()`(Task 2)、
  `toggleProgressText`/`toggleSlowHint`/`toggleFailureHint`/`ToggleAction`(Task 3)
- Produces: 无(终点任务)

**关键背景:** `main.swift` 不在 `scripts/test-macos-menu.sh` 的编译单元里(它是 AppKit 应用),
所以本任务**不新增单测**——一切可判定的逻辑已在 Task 3 里被测过,这里只做接线。
验收靠编译 + 真机。`reconnectBx()`(约 :517)已经是「后台队列调 GuardianClient、回主线程更新」
的写法,**照抄它的结构**,不要另发明一套。

- [ ] **Step 1: 加进行中状态**

在 `MenuController` 类的属性区(`private var timer: Timer?` 附近)加:

```swift
    /// 非 nil 表示有一个开关动作正在进行中。菜单据此显示进度而不是常规状态。
    private var toggleInFlight: (action: ToggleAction, startedAt: Date)?
    private var toggleTicker: Timer?
    /// 上一次开关失败留下的指引,下次动作开始时清掉。
    private var toggleFailureText: String?
```

- [ ] **Step 2: 写统一的异步开关**

在 `runPrivileged` 附近加:

```swift
    /// 开/关保护。经 Guardian socket 发起,全程不阻塞主线程。
    ///
    /// 不再走 AppleScript `with administrator privileges`:那条路既要每次输密码,
    /// 又是同步的 —— 2026-08-04 事故里 bx down 卡了 71 分钟,菜单跟着冻了 71 分钟。
    private func performToggle(_ action: ToggleAction, completion: (() -> Void)? = nil) {
        guard toggleInFlight == nil else { return }
        toggleFailureText = nil
        toggleInFlight = (action, Date())
        startToggleTicker()
        rebuildMenu()
        updateIcon()

        DispatchQueue.global(qos: .userInitiated).async { [weak self] in
            let client = GuardianClient()
            var failureCode: String?
            var transportError: String?
            do {
                let status = action == .turnOn ? try client.turnOn() : try client.turnOff()
                failureCode = status.lastError
            } catch {
                transportError = error.localizedDescription
            }
            DispatchQueue.main.async { [weak self] in
                guard let self else { return }
                self.toggleInFlight = nil
                self.stopToggleTicker()
                if let transportError {
                    self.toggleFailureText = transportError
                } else if let failureCode {
                    self.toggleFailureText = toggleFailureHint(code: failureCode) ?? "失败码 \(failureCode)"
                }
                self.refresh()
                completion?()
            }
        }
    }

    /// 每秒重画一次,好让已用秒数往前走。
    private func startToggleTicker() {
        stopToggleTicker()
        toggleTicker = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            guard let self, self.toggleInFlight != nil else { return }
            self.rebuildMenu()
            self.updateIcon()
        }
    }

    private func stopToggleTicker() {
        toggleTicker?.invalidate()
        toggleTicker = nil
    }
```

- [ ] **Step 3: 三个动作改用它**

`startBx` 改为:

```swift
    @objc private func startBx() {
        guard confirmStartProtection() else { return }
        performToggle(.turnOn)
    }
```

`turnOffBx` 改为(保留确认框,去掉 `runPrivileged` 与同步 `refresh`):

```swift
    @objc private func turnOffBx() {
        let alert = NSAlert()
        alert.messageText = "Turn off bx?"
        alert.informativeText = "bx will stop protecting system traffic and restore managed DNS settings. The menu stays open."
        alert.addButton(withTitle: "Turn Off")
        alert.addButton(withTitle: "Cancel")
        guard alert.runModal() == .alertFirstButtonReturn else { return }
        performToggle(.turnOff)
    }
```

`quitBx` 里那句 `if !runPrivileged("'\(bxPath)' down") { … }` 改为在**关闭完成后**才退出
——否则进程先没了,down 也就无人接收结果:

```swift
        performToggle(.turnOff) {
            NSApp.terminate(nil)
        }
```

(`quitBx` 原有的确认框逻辑保持不动,只替换执行那一段。)

- [ ] **Step 4: 菜单显示进度**

在 `rebuildMenu()` 里,紧跟 `let menu = NSMenu()` 之后、`if let snapshot = recoverySnapshot` **之前**插入:

```swift
        if let inFlight = toggleInFlight {
            let elapsed = Int(Date().timeIntervalSince(inFlight.startedAt))
            menu.addHeader("bx", subtitle: inFlight.action == .turnOn ? "Connecting" : "Disconnecting")
            menu.addInfo("Status", toggleProgressText(action: inFlight.action, elapsedSeconds: elapsed))
            if let hint = toggleSlowHint(action: inFlight.action, elapsedSeconds: elapsed) {
                menu.addInfo("", hint)
                menu.addAction("查看 Guardian 日志", symbol: "doc.text", target: self, action: #selector(openLogs))
            }
            menu.addItem(.separator())
            menu.addAction(quitBxActionTitle, symbol: "power", target: self, action: #selector(quitBx))
            statusItem.menu = menu
            return
        }
```

并在同一函数里、常规状态分支之后追加失败提示(紧接 `switch state { … }` 之后):

```swift
        if let failure = toggleFailureText {
            menu.addItem(.separator())
            menu.addInfo("上次操作失败", failure)
        }
```

- [ ] **Step 5: 编译验证**

Run:
```bash
cd apps/macos/BxMenu && swift build 2>&1 | tail -20
```
Expected: 编译通过。若本机无 Swift 工具链,退而求其次跑
`bash scripts/test-macos-menu.sh`(它覆盖不到 main.swift,此时**必须在报告里明确写出
main.swift 未经编译验证**,不得含糊)。

- [ ] **Step 6: 全量验证**

Run: `go build ./... && go vet ./... && go test ./... -count=1 && bash scripts/test-macos-menu.sh`
Expected: 全绿。

- [ ] **Step 7: 提交**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/main.swift
git commit -m "$(cat <<'EOF'
feat(menu): 开关改走 Guardian socket,后台执行并显示已用时

不再用 AppleScript `with administrator privileges`:那条路既每次弹密码,
又同步阻塞主线程 —— 2026-08-04 事故里 bx down 卡了 71 分钟,菜单跟着冻了
71 分钟,界面一个字都没有。

现在开关在后台队列发起,菜单每秒重画显示已用秒数,20 秒后追加「比预期久」
与日志入口;失败时就地展示失败码指引,而不是一句 "bx did not stop"。
quit 改为等关闭完成后才退出进程。

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
)"
```

---

## 真机验收(由用户执行,不在本计划的自动化范围)

本项目约定「绝不擅自启动 bx / 改路由」,以下步骤须由用户在自己的机器上跑:

1. 重装菜单与 Guardian(本轮改了 Guardian 二进制与 App)。
2. 点菜单里的 Turn Off:**不应弹管理员密码框**,菜单**保持可交互**,状态行秒数在走。
3. 关闭完成后点 Start Protection:同样免密、同样有秒数。
4. 检查 Guardian 日志能看到发起 uid 与时间。
5. 用另一个非 owner 账号(或 `sudo -u`)访问 socket 的 `/v1/up`,应得 403。

## 自查

**Spec 覆盖:** 授权模型变更 → Task 1;异步与进度 → Task 3 + 4;失败码指引 → Task 3 + 4;
「只放开 mutationHandler」的回归守卫 → Task 1 Step 1 第二条测试。
本计划**不含**图标形态、呼吸、菜单结构、数据行、轮询调频——那些属于阶段②③,
见 spec 的「拆分与顺序」。

**占位符:** 无 TBD/TODO;每个代码步骤都给了可直接粘贴的完整代码。

**类型一致:** `ToggleAction`(Task 3 定义)在 Task 4 使用;`GuardianStatus.lastError`
(Task 2 定义)在 Task 4 读取;`toggleFailureHint(code:)` 的入参类型 `String?` 与
`lastError` 的类型一致;`authorizeOwnerPeer` 在 Task 1 内部定义并使用,不跨任务。
