# macOS Network Transition Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make bx automatically and safely recover after macOS changes physical network, while exposing one truthful asynchronous recovery operation to Guardian, Bx.app, CLI, and agents.

**Architecture:** Core owns TUN, capture routes, underlay route rebinding, and transport replacement. Guardian owns the asynchronous recovery transaction, event deduplication, lifecycle serialization, and public LocalAPI. BxMenu and CLI submit or observe that Guardian transaction; the legacy direct-Core path remains a bounded compatibility fallback until the unified App migration completes.

**Tech Stack:** Go 1.26, HTTP over Unix sockets, macOS routing socket (`AF_ROUTE`), `golang.org/x/net/route`, AppKit/Swift, launchd Guardian, existing gVisor/sing-box transport layer.

## Global Constraints

- Implement on `codex/macos-guardian-lifecycle` after merging current `master`; do not develop Guardian lifecycle again on `master`.
- Never run `bx up`, `bx down`, route/DNS mutation, or Wi-Fi switching from an agent session without explicit user authorization.
- Recovery may temporarily block networking but must never fall back to the physical public default route.
- `0.0.0.0/1`, `128.0.0.0/1`, `::/1`, and `8000::/1` are immutable capture routes during recovery.
- Network-change recovery may change only underlay-dependent server, local-network, user bypass, and recognized coexistence routes.
- Guardian is the only public recovery coordinator; Core exposes a root/owner-authenticated mechanical operation.
- Use `path recovery` / `network recovery` names for this feature. Existing Guardian `recoveryLifecycle` and `Recover` refer to unexpected Core/update recovery and must not be conflated or renamed casually.
- POST acceptance is asynchronous: `POST /v1/recoveries` returns `202 Accepted` before route or transport health work completes.
- The `3s` generic Core HTTP timeout must not govern a transport health operation that can take `20s`.
- Logs and status must not contain client links, UUIDs, passwords, tokens, full configs, or user request destinations.
- All implementation follows red-green-refactor TDD and each task ends in an independently reviewable commit.

## File Structure

- `internal/supervisor/path_recovery.go`: Core recovery types, stage reporting, and one serialized mechanical recovery operation.
- `internal/supervisor/underlay.go`: platform-neutral underlay snapshot, generation identity, capture validator/rebinder interfaces.
- `internal/supervisor/underlay_darwin.go`: macOS observation, capture validation, and exact-route rebinding execution.
- `internal/supervisor/underlay_other.go`: explicit unsupported implementation for non-macOS builds.
- `internal/supervisor/darwin_underlay_plan.go`: pure, cross-platform-testable route diff construction; never executes commands.
- `internal/supervisor/transportset.go`: prepare/commit/abort transaction for main and optional UDP transports.
- `internal/guardian/path_recovery.go`: asynchronous transaction coordinator and current snapshot.
- `internal/guardian/network_observer.go`: event/debounce interface and platform-neutral generation coalescing.
- `internal/guardian/network_observer_darwin.go`: `AF_ROUTE` event source.
- `internal/guardian/network_observer_other.go`: disabled observer for other platforms.
- `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`: versioned HTTP-over-Unix-socket client.
- `apps/macos/BxMenu/Sources/BxMenu/RecoveryPresentation.swift`: recovery-state-to-menu presentation.

---

### Task 1: Fix the Proven Legacy Reconnect Timeout Contract

**Files:**
- Modify: `internal/supervisor/control_client.go`
- Modify: `internal/supervisor/control_client_test.go`
- Modify: `internal/cli/cli.go`
- Test: `internal/cli/cli_test.go`

**Interfaces:**
- Produces: `ReconnectControlContext(ctx context.Context, sockPath string) (string, error)`.
- Preserves: `ReconnectControl(sockPath string) (string, error)` as a compatibility wrapper with a `25s` operation deadline.
- Consumed later by: CLI fallback when Guardian is not installed.

- [ ] **Step 1: Write a failing delayed-response client test**

Add a Unix-socket test server whose `/v0/reconnect` handler sleeps longer than the generic `3s` client timeout only when driven by a short injected context. Refactor the test helper so production timing is not required:

```go
func TestReconnectControlUsesCallerDeadline(t *testing.T) {
    sock := startControlSocket(t, func(w http.ResponseWriter, r *http.Request) {
        time.Sleep(75 * time.Millisecond)
        writeJSON(w, http.StatusOK, controlResponse{Status: "ok", State: "reconnected"})
    })
    ctx, cancel := context.WithTimeout(context.Background(), time.Second)
    defer cancel()
    state, err := ReconnectControlContext(ctx, sock)
    if err != nil || state != "reconnected" {
        t.Fatalf("ReconnectControlContext = %q, %v", state, err)
    }
}
```

- [ ] **Step 2: Run the focused test and verify red**

Run: `go test ./internal/supervisor -run TestReconnectControlUsesCallerDeadline -count=1`

Expected: FAIL because `ReconnectControlContext` does not exist.

- [ ] **Step 3: Add context-aware POST plumbing**

Implement request construction with the caller context and no fixed total timeout layered above it:

```go
func ReconnectControlContext(ctx context.Context, sockPath string) (string, error) {
    client := controlHTTPClientForOperation(sockPath)
    defer client.CloseIdleConnections()
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local/v0/reconnect", nil)
    if err != nil { return "", err }
    return doControlRequest(client, req, "/v0/reconnect")
}

func ReconnectControl(sockPath string) (string, error) {
    ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
    defer cancel()
    return ReconnectControlContext(ctx, sockPath)
}
```

Keep the generic status/capabilities client at `3s`; only long-running operations use caller deadlines.

- [ ] **Step 4: Make CLI diagnostics preserve the concrete timeout error**

Use the CLI context for reconnect and ensure `autoArchiveAfterClientCommand` records the exact returned error. Add an assertion that `context deadline exceeded` is not rewritten as a transport failure.

- [ ] **Step 5: Run focused and package tests**

Run: `go test ./internal/supervisor ./internal/cli -run 'Reconnect|MacMenu' -count=1`

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/supervisor/control_client.go internal/supervisor/control_client_test.go internal/cli/cli.go internal/cli/cli_test.go
git commit -m "fix(reconnect): align client deadline with health gate"
```

### Task 2: Add a Structured Core Path-Recovery Operation

**Files:**
- Create: `internal/supervisor/path_recovery.go`
- Create: `internal/supervisor/path_recovery_test.go`
- Modify: `internal/supervisor/control.go`
- Modify: `internal/supervisor/control_test.go`
- Modify: `internal/supervisor/control_client.go`
- Modify: `internal/supervisor/control_client_test.go`

**Interfaces:**
- Produces:

```go
type PathRecoveryRequest struct {
    Reason     string `json:"reason"`
    Generation string `json:"generation,omitempty"`
}

type PathRecoverySnapshot struct {
    ID         string    `json:"recovery_id"`
    State      string    `json:"state"`
    Stage      string    `json:"stage"`
    Reason     string    `json:"reason"`
    Generation string    `json:"generation,omitempty"`
    Attempt    int       `json:"attempt"`
    ErrorCode  string    `json:"last_error_code,omitempty"`
    Detail     string    `json:"detail,omitempty"`
    StartedAt  time.Time `json:"started_at"`
    UpdatedAt  time.Time `json:"updated_at"`
}

type pathRecoverer interface {
    RecoverPath(context.Context, PathRecoveryRequest, func(PathRecoverySnapshot)) (PathRecoverySnapshot, error)
}
```

- Produces internal Core endpoints: `POST /v0/path-recovery`, `GET /v0/path-recovery`.
- Consumed by: Guardian `CorePathClient` in Task 4.

- [ ] **Step 1: Write failing stage and serialization tests**

Test one successful fake recoverer emits `observe -> validate_capture -> rebind_underlay -> transport_health -> verify -> succeeded`; test failures preserve a stable error code and redact detail supplied by the fake.

- [ ] **Step 2: Run tests to verify red**

Run: `go test ./internal/supervisor -run 'PathRecovery|ControlPathRecovery' -count=1`

Expected: FAIL because the types and routes do not exist.

- [ ] **Step 3: Implement the serialized Core operation**

Use a one-slot mutex/channel so two calls cannot mutate routes or transport slots concurrently. The operation callback updates an atomic snapshot; `GET` remains responsive while POST runs.

Return structured errors through a typed error:

```go
type PathRecoveryError struct {
    Code   string
    Detail string
}

func (e *PathRecoveryError) Error() string { return e.Code + ": " + e.Detail }
```

Map only stable codes to LocalAPI; redact free-form detail before storage.

- [ ] **Step 4: Add root/owner-authenticated Core endpoints and client**

`POST /v0/path-recovery` is mechanical and synchronous inside Core; Guardian supplies a context longer than the transport health gate. `GET` is read-only. Add:

```go
func RecoverPathControl(ctx context.Context, sockPath string, in PathRecoveryRequest) (PathRecoverySnapshot, error)
func FetchPathRecovery(ctx context.Context, sockPath string) (PathRecoverySnapshot, error)
```

- [ ] **Step 5: Run package tests and race test**

Run: `go test -race ./internal/supervisor -run 'PathRecovery|ControlPathRecovery' -count=1`

Expected: PASS with no races.

- [ ] **Step 6: Commit**

```bash
git add internal/supervisor/path_recovery.go internal/supervisor/path_recovery_test.go internal/supervisor/control.go internal/supervisor/control_test.go internal/supervisor/control_client.go internal/supervisor/control_client_test.go
git commit -m "feat(core): expose structured path recovery"
```

### Task 3: Build a Capture-Safe macOS Underlay Rebinding Plan

**Files:**
- Create: `internal/supervisor/underlay.go`
- Create: `internal/supervisor/darwin_underlay_plan.go`
- Create: `internal/supervisor/darwin_underlay_plan_test.go`
- Create: `internal/supervisor/underlay_darwin.go`
- Create: `internal/supervisor/underlay_other.go`
- Modify: `internal/supervisor/platform_darwin.go`
- Create: `internal/supervisor/platform_darwin_test.go`

**Interfaces:**
- Produces:

```go
type UnderlaySnapshot struct {
    Generation string
    Interface  string
    Gateway    netip.Addr
    LocalCIDRs []netip.Prefix
}

type underlayManager interface {
    Observe(context.Context) (UnderlaySnapshot, error)
    ValidateCapture(context.Context, tunHandle) error
    Rebind(context.Context, tunHandle, UnderlaySnapshot, UnderlaySnapshot, []string, []string) error
}
```

- Consumed by: `livePathRecoverer` in Task 5.

- [ ] **Step 1: Write failing pure route-plan tests**

Cover gateway `192.168.50.2 -> 192.168.1.1`, unchanged generation, missing old exact route, duplicate bypasses, malformed prefixes, and forbidden capture prefixes.

The central invariant test must inspect every command:

```go
func TestDarwinUnderlayPlanNeverTouchesCaptureRoutes(t *testing.T) {
    plan, err := darwinUnderlayPlan(old, next, serverBypass, userBypass)
    if err != nil { t.Fatal(err) }
    for _, cmd := range plan {
        text := strings.Join(cmd.Args, " ")
        for _, forbidden := range []string{"0.0.0.0/1", "128.0.0.0/1", "::/1", "8000::/1"} {
            if strings.Contains(text, forbidden) { t.Fatalf("capture mutation: %s", text) }
        }
    }
}
```

- [ ] **Step 2: Run pure tests to verify red**

Run: `go test ./internal/supervisor -run DarwinUnderlayPlan -count=1`

Expected: FAIL because the planner does not exist.

- [ ] **Step 3: Implement typed snapshots and deterministic generation**

Canonicalize interface, unmap IPv4 gateway, sort/deduplicate local prefixes, then hash the canonical tuple with SHA-256 and expose the first 16 hex characters. Reject loopback, utun, empty interface, and non-IPv4 gateway as physical underlay.

- [ ] **Step 4: Implement a pure exact-route plan**

Use `route -n change -net <prefix> <new-gateway>` for existing exact bypass routes. Represent fallback as an explicit fail-closed pair that removes only that exact bypass before adding it to the new gateway; absence routes the server into TUN and blocks/loops rather than exposing public traffic. Never call or include `darwinRouteSpecs`, which also owns capture routes.

- [ ] **Step 5: Implement macOS observation and capture validation**

Observation may reuse `defaultRouteDarwin()` but must also capture interface addresses with `net.Interfaces`. Validation queries representative addresses from both IPv4 halves and verifies their selected interface equals the active bx utun. Verify IPv6 reject routes when IPv6 is enabled. Return `capture_missing`, never attempt automatic full rehijack.

- [ ] **Step 6: Implement command execution behind an injectable runner**

Use an interface so tests prove exact ordering and injected failure behavior:

```go
type commandRunner interface {
    Run(context.Context, string, ...string) error
}
```

Stop on first failed route update and return `underlay_rebind_failed`.

- [ ] **Step 7: Run tests and cross-platform compile**

Run: `go test ./internal/supervisor -run 'Underlay|Capture' -count=1`

Run: `GOOS=darwin GOARCH=arm64 go test ./internal/supervisor -run 'Underlay|Capture' -count=1`

Run: `GOOS=linux GOARCH=amd64 go build ./internal/supervisor`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/supervisor/underlay.go internal/supervisor/darwin_underlay_plan.go internal/supervisor/darwin_underlay_plan_test.go internal/supervisor/underlay_darwin.go internal/supervisor/underlay_other.go internal/supervisor/platform_darwin.go internal/supervisor/platform_darwin_test.go
git commit -m "feat(macos): rebind underlay without releasing capture"
```

### Task 4: Add Guardian's Asynchronous Recovery Transaction

**Files:**
- Create: `internal/guardian/path_recovery.go`
- Create: `internal/guardian/path_recovery_test.go`
- Modify: `internal/guardian/types.go`
- Modify: `internal/guardian/manager.go`
- Modify: `internal/guardian/manager_test.go`
- Modify: `internal/guardian/localapi.go`
- Modify: `internal/guardian/localapi_test.go`
- Modify: `internal/guardian/client.go`

**Interfaces:**
- Produces:

```go
type RecoveryRequest struct {
    Reason     string `json:"reason"`
    Generation string `json:"generation,omitempty"`
}

type RecoverySnapshot struct {
    ID         string    `json:"recovery_id"`
    State      string    `json:"state"`
    Stage      string    `json:"stage"`
    Reason     string    `json:"reason"`
    Generation string    `json:"generation,omitempty"`
    ErrorCode  string    `json:"last_error_code,omitempty"`
    Detail     string    `json:"detail,omitempty"`
    Attempt    int       `json:"attempt"`
    StartedAt  time.Time `json:"started_at"`
    UpdatedAt  time.Time `json:"updated_at"`
}

type CorePathClient interface {
    RecoverPath(context.Context, supervisor.PathRecoveryRequest) (supervisor.PathRecoverySnapshot, error)
}
```

- Produces public Guardian endpoints: `POST /v1/recoveries` and `GET /v1/recoveries/current`.
- Consumed by: CLI and BxMenu in Task 7; observer in Task 6.

- [ ] **Step 1: Write failing LocalAPI acceptance tests**

Assert POST returns `202` in under `100ms` while a fake Core operation remains blocked, response contains a recovery ID, GET reports `running`, and later reports `succeeded` or a stable failure code.

- [ ] **Step 2: Write failing coordinator concurrency tests**

Cover same-generation deduplication, newer-generation coalescing, Off-state ignore, Down cancellation, update/mutation serialization, shutdown drain, and no collision with existing unexpected-Core `recoveryLifecycle` counters.

- [ ] **Step 3: Run tests to verify red**

Run: `go test ./internal/guardian -run 'PathRecovery|RecoveryLocalAPI' -count=1`

Expected: FAIL because coordinator and routes do not exist.

- [ ] **Step 4: Implement coordinator with its own names and lifecycle**

Name fields `pathRecoveryMu`, `pathRecoveryCurrent`, `pathRecoveryPending`, and `pathRecoveryCancel`; do not reuse existing `recoveryMu` fields. POST records acceptance and starts one goroutine. The goroutine acquires the Manager mutation gate, calls Core, publishes stages, and then consumes the newest pending generation.

- [ ] **Step 5: Add owner authorization only for recovery**

Extend LocalAPI options with configured `OwnerUID`. `/v1/recoveries` accepts peer UID `0` or exactly `OwnerUID`; up/down/update retain root-only authorization. Unknown or missing peer credentials fail closed.

- [ ] **Step 6: Add typed Guardian client methods**

```go
func (c *Client) RequestRecovery(ctx context.Context, in RecoveryRequest) (RecoverySnapshot, error)
func (c *Client) CurrentRecovery(ctx context.Context) (RecoverySnapshot, error)
```

Accept HTTP 202 only for POST and HTTP 200 only for GET.

- [ ] **Step 7: Run race tests**

Run: `go test -race ./internal/guardian -run 'PathRecovery|RecoveryLocalAPI' -count=1`

Expected: PASS with no races or leaked goroutines.

- [ ] **Step 8: Commit**

```bash
git add internal/guardian/path_recovery.go internal/guardian/path_recovery_test.go internal/guardian/types.go internal/guardian/manager.go internal/guardian/manager_test.go internal/guardian/localapi.go internal/guardian/localapi_test.go internal/guardian/client.go
git commit -m "feat(guardian): coordinate asynchronous path recovery"
```

### Task 5: Recover Main and UDP Transports as One Generation

**Files:**
- Create: `internal/supervisor/transportset.go`
- Create: `internal/supervisor/transportset_test.go`
- Modify: `internal/supervisor/transportswap.go`
- Create: `internal/supervisor/transportswap_test.go`
- Modify: `internal/supervisor/run.go`
- Modify: `internal/supervisor/path_recovery.go`
- Test: `internal/supervisor/path_recovery_test.go`

**Interfaces:**
- Produces:

```go
type transportCandidate interface {
    Healthy() bool
    Stop()
}

type transportSet interface {
    Recover(context.Context) error
}
```

- Consumes: `underlayManager` from Task 3.
- Produces: complete `livePathRecoverer` consumed by Core LocalAPI from Task 2.

- [ ] **Step 1: Write failing prepare/commit/abort tests**

Cover main-only success, main+UDP success, main failure, UDP failure, cancellation, candidate cleanup, old slot preservation, and a second recovery waiting on the same serialization lock.

- [ ] **Step 2: Write a regression test for candidate config-file collisions**

Assert each candidate receives a transaction-specific config path such as `sing-box.<recovery-id>.json`; the active process config file is never overwritten before commit.

- [ ] **Step 3: Run tests to verify red**

Run: `go test ./internal/supervisor -run 'TransportSet|PathRecovery' -count=1`

Expected: FAIL because `transportSet` does not exist.

- [ ] **Step 4: Separate prepare from commit**

Refactor `transportSwapper` so health waiting accepts a caller context and candidate creation does not mutate the active dialer:

```go
func (s *transportSwapper) prepare(ctx context.Context, link, recoveryID string) (*tunnel.Tunnel, error)
func (s *transportSwapper) commit(candidate *tunnel.Tunnel, link string)
func (s *transportSwapper) abort(candidate *tunnel.Tunnel)
```

Keep existing automatic failover behavior by wrapping prepare+commit under the same swap lock.

- [ ] **Step 5: Add an optional UDP transport slot**

The UDP slot activates through `Dialer.SetUDPTransport`; it has independent health but participates in the same recovery generation. If UDP is configured and fails, main may remain active but the recovery result is `needs_attention` and UDP stays fail-closed.

- [ ] **Step 6: Wire the full Core operation**

Order must be: observe -> validate capture -> rebind exact underlay routes -> prepare main and UDP candidates -> commit slots -> verify runtime state. Do not call `RehijackRoutes`.

- [ ] **Step 7: Run focused race tests and existing failover tests**

Run: `go test -race ./internal/supervisor -run 'TransportSet|PathRecovery|Failover|Swap' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/supervisor/transportset.go internal/supervisor/transportset_test.go internal/supervisor/transportswap.go internal/supervisor/transportswap_test.go internal/supervisor/run.go internal/supervisor/path_recovery.go internal/supervisor/path_recovery_test.go
git commit -m "feat(core): recover the complete transport set"
```

### Task 6: Observe macOS Network Changes and Coalesce Generations

**Files:**
- Create: `internal/guardian/network_observer.go`
- Create: `internal/guardian/network_observer_test.go`
- Create: `internal/guardian/network_observer_darwin.go`
- Create: `internal/guardian/network_observer_other.go`
- Modify: `internal/guardian/daemon.go`
- Modify: `internal/guardian/daemon_test.go`

**Interfaces:**
- Produces:

```go
type NetworkEventSource interface {
    Events(context.Context) (<-chan struct{}, error)
}

type UnderlayGenerationSource interface {
    Current(context.Context) (string, error)
}
```

- Consumes: `Manager.RequestPathRecovery(RecoveryRequest)` from Task 4.

- [ ] **Step 1: Write failing debounce tests with a fake clock/source**

Assert bursts collapse after a 1-second quiet window, unchanged generation is ignored, a new generation during active recovery becomes one pending request, observer shutdown closes promptly, and event-source errors retry with bounded backoff.

- [ ] **Step 2: Run tests to verify red**

Run: `go test ./internal/guardian -run NetworkObserver -count=1`

Expected: FAIL because observer types do not exist.

- [ ] **Step 3: Implement platform-neutral debounce loop**

Keep the event payload empty; after debounce call `Current`, compare the opaque generation, then submit `Reason: "underlay_changed"`. Also perform a low-frequency 60-second generation check to recover from dropped route-socket events.

- [ ] **Step 4: Implement Darwin `AF_ROUTE` event source**

Open a route socket with `unix.Socket(AF_ROUTE, SOCK_RAW, AF_UNSPEC)`, read messages until context cancellation, and emit a non-blocking signal for route/interface/address message classes. Closing the descriptor must unblock read. Do not parse an event into trusted gateway data; Core re-observes before mutation.

- [ ] **Step 5: Wire observer lifetime to Guardian daemon**

Start only when desired state is On. App exit must not affect it. Daemon shutdown cancels and drains it before removing the Guardian socket.

- [ ] **Step 6: Run tests and Darwin compile**

Run: `go test -race ./internal/guardian -run 'NetworkObserver|Daemon' -count=1`

Run: `GOOS=darwin GOARCH=arm64 go build ./...`

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/guardian/network_observer.go internal/guardian/network_observer_test.go internal/guardian/network_observer_darwin.go internal/guardian/network_observer_other.go internal/guardian/daemon.go internal/guardian/daemon_test.go
git commit -m "feat(macos): recover on underlay generation changes"
```

### Task 7: Move CLI and BxMenu Reconnect to Guardian

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Create: `apps/macos/BxMenu/Sources/BxMenu/GuardianClient.swift`
- Create: `apps/macos/BxMenu/Sources/BxMenu/RecoveryPresentation.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Create: `apps/macos/BxMenu/Tests/RecoveryPresentationTests.swift`
- Modify: `apps/macos/BxMenu/Package.swift`

**Interfaces:**
- Consumes: Guardian POST/GET recovery API from Task 4.
- Preserves: legacy direct-Core reconnect fallback from Task 1 when Guardian is not installed.

- [ ] **Step 1: Write failing CLI behavior tests**

Inject a fake Guardian client. Assert `bx reconnect` submits once, prints `Reconnecting`, polls to terminal state, reports stable error code, and falls back to Core only on a typed `guardian unavailable` error rather than any server failure.

- [ ] **Step 2: Write failing Swift presentation tests**

Cover `accepted/running/succeeded/failed`, menu icon color, title, short reason, and absence of a modal success alert.

- [ ] **Step 3: Run tests to verify red**

Run: `go test ./internal/cli -run Reconnect -count=1`

Run: `swift test --package-path apps/macos/BxMenu --filter RecoveryPresentationTests`

Expected: FAIL.

- [ ] **Step 4: Implement the CLI Guardian-first flow**

Default human output:

```text
• Protection  Reconnecting
✓ Protection  Reconnected
```

JSON output returns the final `RecoverySnapshot`. Poll at 250ms then 500ms, bounded by the command context; timing out observation must say recovery is still running and include the recovery ID, not claim failure.

- [ ] **Step 5: Implement a focused Swift Unix HTTP client**

Use a POSIX `AF_UNIX` socket to send fixed HTTP requests to Guardian. The client accepts only the two fixed paths, caps headers at 32 KiB and body at 1 MiB, requires `Content-Type: application/json`, and decodes `RecoverySnapshot`. No arbitrary command, shell, path, or argument input is accepted.

- [ ] **Step 6: Replace menu AppleScript reconnect**

Remove `runPrivileged("'\(bxPath)' reconnect")`. Submit recovery directly, immediately show yellow `Reconnecting`, poll current status off the main thread, then refresh. On failure show the structured short reason with Details/Doctor as a secondary action. Repeated clicks while running are disabled or deduplicated by Guardian.

- [ ] **Step 7: Run CLI and Swift tests**

Run: `go test ./internal/cli -run 'Reconnect|MacMenu' -count=1`

Run: `swift test --package-path apps/macos/BxMenu`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/cli/cli.go internal/cli/cli_test.go apps/macos/BxMenu
git commit -m "feat(macos): unify reconnect through Guardian"
```

### Task 8: Integrate Status, Doctor, Packaging, and Safe Verification

**Files:**
- Modify: `internal/guardian/types.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `README.md`
- Modify: `scripts/darwin-testkit.sh`
- Create: `docs/macos-network-transition-validation.md`

**Interfaces:**
- Consumes all previous tasks.
- Produces user/agent-visible recovery status and a user-gated true-machine acceptance harness.

- [ ] **Step 1: Write failing status and Doctor tests**

Assert status JSON contains `protection_state`, `network_generation`, and nested recovery fields; Doctor reports the latest stage/error code and never recommends direct fallback. Human output must distinguish `Reconnecting`, `Blocked`, and `Repair Required`.

- [ ] **Step 2: Run tests to verify red**

Run: `go test ./internal/cli ./internal/guardian -run 'Recovery|Doctor|Status' -count=1`

Expected: FAIL until rendering is wired.

- [ ] **Step 3: Wire truthful status and diagnostic archives**

Each transaction logs `recovery_id`, generation, reason, stage, attempt, duration, and stable error code. Add latest snapshot to archives as `recovery.json`. Redact free-form transport errors before persistence.

- [ ] **Step 4: Update menu and README copy**

Document one normal behavior: bx reconnects automatically after network changes. Keep `bx reconnect` as a troubleshooting action, not a required workflow. Explain that recovery may briefly block networking and never falls back to direct.

- [ ] **Step 5: Extend darwin-testkit without making it self-executing**

Add `--network-transition-check` dry-run output that records the exact user-operated sequence and snapshots. It must refuse execution unless bx is already healthy, `--execute` is explicit, and the user supplies/acknowledges the physical network change. The script itself must not invoke Wi-Fi switching.

- [ ] **Step 6: Run full verification**

Run: `go test ./...`

Run: `go test -race ./internal/supervisor ./internal/guardian ./internal/cli`

Run: `go vet ./...`

Run: `go build ./...`

Run: `GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...`

Run: `swift test --package-path apps/macos/BxMenu`

Expected: all PASS.

- [ ] **Step 7: Run package verification without installation**

Run: `STAGING="$(mktemp -d)"; BX_VERSION=dev BX_RELEASE_DIR="$STAGING" scripts/package-macos-release.sh; BX_RELEASE_DIR="$STAGING" scripts/verify-macos-release.sh`

Expected: package assets and versions agree; current network session remains untouched.

- [ ] **Step 8: Commit**

```bash
git add internal/guardian/types.go internal/cli/cli.go internal/cli/cli_test.go apps/macos/BxMenu/Sources/BxMenu/main.swift README.md scripts/darwin-testkit.sh docs/macos-network-transition-validation.md
git commit -m "docs(macos): ship observable network recovery"
```

## Manual macOS Acceptance Gate

Do not run this section automatically. After all automated checks pass, present the exact staged package and ask the user for explicit authorization.

The user-controlled acceptance must verify:

1. Existing Protected session remains unchanged before installation.
2. Package update uses Guardian maintenance barrier and rolls back on injected Core failure.
3. Company Wi-Fi -> home Wi-Fi and home Wi-Fi -> hotspot each show `Reconnecting` then `Protected`.
4. During transition, continuous exit sampling observes only the configured proxy IP or no connectivity, never the physical public IP.
5. Main REALITY and UDP Hysteria2 both recover; UDP failure is shown as Needs Attention and remains fail-closed.
6. Menu Reconnect and `sudo bx reconnect` return the same recovery ID/state and do not time out at 3 seconds.
7. Quit Menu does not stop Guardian observation or protection.
8. Logs contain structured recovery evidence and no secret links.

## Follow-On Plan Boundary

After this plan is complete, write and execute a separate plan for
`docs/superpowers/specs/2026-07-20-macos-unified-app-design.md`. That plan will package Bx.app,
CLI bridge, Guardian, and Core as one release transaction. It consumes the Guardian recovery API built here;
it must not reintroduce AppleScript reconnect or a second CLI lifecycle.
