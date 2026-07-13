# Safe Reconnect Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace service-stop restart paths with an authenticated in-daemon transport reconnect that leaves TUN, routes, and DNS in place.

**Architecture:** Add a direct `Reconnect` mutator that calls `transportSwapper.swapTo` with its current link. The swapper already builds and health-checks the replacement before atomically changing the dialer, so it remains fail closed. Expose it through a privileged local control route, authorize Darwin peers with `LOCAL_PEERCRED`, then make CLI and macOS menu use it.

**Tech Stack:** Go, Unix HTTP control socket, `golang.org/x/sys/unix`, Swift/AppKit menu app.

## Global Constraints

- Reconnect must never call `bx down`, `bx up`, `launchctl bootout`, `launchctl kickstart`, DNS mutation, route teardown, or TUN teardown.
- A fresh transport must be healthy before it replaces the active transport.
- If replacement startup fails, the old active transport and all system network state remain unchanged.
- New flows while no transport is healthy must fail closed through the existing dialer health gate.
- Mutating control routes require a verified root or configured-owner peer identity; unknown platforms remain denied.
- This phase does not claim forced-process-death or binary-update kill-switch coverage.

---

### Task 1: Enable Darwin local control authorization

**Files:**
- Create: `internal/supervisor/peercred_darwin.go`
- Create: `internal/supervisor/peercred_darwin_test.go`
- Modify: `internal/supervisor/peercred_other.go`

**Interfaces:**
- Produces: `const peerCredSupported = true` on Darwin.
- Produces: `func peerCredUID(conn net.Conn) (uint32, bool)` using `unix.GetsockoptXucred(fd, unix.SOL_LOCAL, unix.LOCAL_PEERCRED)` and `cred.Uid`.

- [ ] **Step 1: Write the Darwin Unix-socket test**

```go
func TestPeerCredUIDDarwin(t *testing.T) {
    path := filepath.Join(t.TempDir(), "peer.sock")
    ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
    // Dial the listener, accept a *net.UnixConn, call peerCredUID on the
    // accepted connection, and require ok plus uint32(os.Geteuid()).
}
```

- [ ] **Step 2: Run the Darwin test before implementation**

Run: `go test ./internal/supervisor -run TestPeerCredUIDDarwin`

Expected: compile failure because the Darwin implementation does not exist.

- [ ] **Step 3: Implement Darwin peer credentials**

```go
//go:build darwin

func peerCredUID(conn net.Conn) (uint32, bool) {
    uc, ok := conn.(*net.UnixConn)
    if !ok { return 0, false }
    raw, err := uc.SyscallConn()
    if err != nil { return 0, false }
    var uid uint32
    var got bool
    err = raw.Control(func(fd uintptr) {
        cred, e := unix.GetsockoptXucred(int(fd), unix.SOL_LOCAL, unix.LOCAL_PEERCRED)
        if e == nil { uid, got = cred.Uid, true }
    })
    return uid, err == nil && got
}
```

Change the fallback build tag to `!linux && !darwin`.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/supervisor`

Commit: `git commit -m "feat(macos): authorize local control peers"`

### Task 2: Add the in-daemon reconnect control path

**Files:**
- Modify: `internal/supervisor/mutator.go`
- Modify: `internal/supervisor/transportswap.go`
- Modify: `internal/supervisor/control.go`
- Modify: `internal/supervisor/control_client.go`
- Modify: `internal/supervisor/mutator_test.go`
- Modify: `internal/supervisor/control_test.go`
- Modify: `internal/supervisor/control_client_test.go`

**Interfaces:**
- Add: `Reconnect() error` to `mutator`.
- Add: `func (m *liveMutator) Reconnect() error` that calls `m.swap.swapTo(m.swap.currentLink())`.
- Add: `POST /v0/reconnect`, guarded by `requireOwnerOrRoot`, returning `state: "reconnected"` only after `Reconnect` returns success.
- Add: `func ReconnectControl(sockPath string) (string, error)`.

- [ ] **Step 1: Write failing mutator tests**

```go
func TestLiveMutatorReconnectUsesCurrentLink(t *testing.T) {
    fs := &fakeSwapper{cur: "reality://current"}
    err := (&liveMutator{swap: fs}).Reconnect()
    // Require exactly one swap call to reality://current.
}

func TestLiveMutatorReconnectFailureKeepsCurrentLink(t *testing.T) {
    fs := &fakeSwapper{cur: "reality://current", swapErr: errors.New("unhealthy")}
    // Require returned error and fs.cur still reality://current.
}
```

- [ ] **Step 2: Run focused tests and confirm red**

Run: `go test ./internal/supervisor -run 'TestLiveMutatorReconnect|TestControlReconnect|TestReconnectControl'`

Expected: compile failure because reconnect symbols do not exist.

- [ ] **Step 3: Implement direct reconnect without the mutation engine**

Do not call `eng.Arm`: reconnect changes only the transport object and has no route/config rollback. In `handleReconnect`, require authenticated peer identity, call `cs.mut.Reconnect()` outside any long-held control mutex, return 500 on error, and return:

```go
writeJSON(w, http.StatusOK, controlResponse{Status: "ok", State: "reconnected"})
```

Keep `transportSwapper.swapTo` ordering: build -> start -> health check -> install new dialer transport -> stop old. Do not add a stop-first path.

- [ ] **Step 4: Add control/client coverage and verify green**

Test `POST /v0/reconnect` success, authentication denial, failure propagation, and client request path. Run:

`go test ./internal/supervisor`

- [ ] **Step 5: Commit**

`git commit -m "feat: add fail-closed transport reconnect"`

### Task 3: Make CLI and macOS menu use safe reconnect

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `apps/macos/BxMenu/README.md`
- Modify: `README.md`

**Interfaces:**
- Add the `reconnect` command with `Action: reconnectAction`.
- Make `restartAction` call `reconnectAction` so existing `sudo bx restart` becomes safe.
- `reconnectAction` calls `supervisor.ReconnectControl(supervisor.SockPath)` and never calls `install.Restart`.
- Menu action invokes `bx reconnect`; it is labelled `Reconnect`.

- [ ] **Step 1: Write failing CLI and source-contract tests**

Add `var reconnectControl = supervisor.ReconnectControl` beside the CLI action so a CLI test can inject a reconnect error. Assert `restartAction` delegates to that seam and never calls the service restart seam. Add a source-contract test that reads `main.swift` and requires `bx reconnect`, while rejecting `down &&` and `&& ... up` in the reconnect action.

- [ ] **Step 2: Implement CLI and menu routing**

```go
func reconnectAction(c *cli.Context) error {
    state, err := supervisor.ReconnectControl(supervisor.SockPath)
    if err != nil { return err }
    fmt.Println("✅ bx 已安全重连。")
    return nil
}

Use the existing service-installed precondition and diagnostics archive convention. Change command usage to `安全重连传输(保留 TUN、路由和 DNS)`.

In Swift, replace the shell command in the menu action with `"'\(bxPath)' reconnect"`; do not use `down`, `up`, `restart`, or launchctl.

- [ ] **Step 3: Verify release and source contracts**

Run:

```bash
go test ./...
swift build --package-path apps/macos/BxMenu -c release
scripts/package-macos-release.sh
scripts/verify-macos-release.sh
git diff --check
```

- [ ] **Step 4: Commit**

`git commit -m "feat(macos): reconnect without stopping protection"`

### Task 4: Verify on macOS without changing normal-network state

**Files:**
- Modify: `scripts/darwin-testkit.sh`
- Modify: `README.md`

- [ ] **Step 1: Add a reconnect-only dry-run mode**

Add `--reconnect-check` that prints the exact read-only route/DNS snapshots and the one privileged `bx reconnect` operation. It must require explicit `--execute`; default is dry run.

- [ ] **Step 2: Add post-reconnect assertions**

After reconnect returns, compare before/after snapshots: split-default `/1` routes remain attached to utun, server bypass remains on the physical gateway, and system DNS remains `127.0.0.1`. Attempting egress during the operation must either use the expected proxy IP or fail.

- [ ] **Step 3: Document and verify**

Document the explicit smoke command and its rollback-free behavior. Run `go test ./...`, `bash -n scripts/darwin-testkit.sh`, release verification, and `git diff --check`.

- [ ] **Step 4: Commit**

`git commit -m "test(macos): verify fail-closed reconnect"`
