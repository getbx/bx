# macOS Guardian Lifecycle and Safe Update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make one root-owned macOS Guardian the sole bx Core lifecycle owner so `bx up`, `bx down`, reconnect recovery, and running updates share one fail-closed transaction with automatic rollback and a unified BxMenu experience.

**Architecture:** Add `internal/guardian` as a small lifecycle state machine and versioned Unix LocalAPI, with platform route work behind a narrow barrier interface. Guardian launches the existing `bx run` Core as a child, persists desired state and update journals under `/var/lib/bx`, and uses a more-specific maintenance barrier while replacing or rolling back the Core. Refactor the existing verified macOS package updater into `internal/update` so CLI and Guardian use one parser/installer; BxMenu remains a thin consent and status surface.

**Tech Stack:** Go 1.26, Unix HTTP LocalAPI, `golang.org/x/sys/unix` peer credentials, macOS launchd/route/networksetup, Swift 5.9/AppKit, signed Ed25519 release manifests.

## Global Constraints

- Do not execute `bx up`, `bx down`, reconnect, update activation, `sudo`, DNS changes, route changes, or launchctl mutations during implementation unless the user explicitly authorizes that exact real-device step.
- All network-changing verification defaults to dry-run and prints the exact apply/cleanup plan before any privileged execution.
- A running update may pause Internet access for several seconds, but it must never restore ordinary public traffic to the physical default route.
- The no-direct proof covers planned transactions after Guardian installs the barrier; unexpected Core/Guardian kill or kernel failure is recovered immediately on a best-effort basis but needs a future Network Extension for a first-packet absolute guarantee.
- Install and verify the complete package before installing the maintenance barrier or stopping the old Core.
- Remove the maintenance barrier only after the target Core passes every health gate; failure restores the previous Core under the same barrier.
- If both target and previous Core fail, keep public traffic blocked and return `needs_attention`; never fall back to direct.
- BxMenu never edits DNS/routes or launches `down && up`; all mutations go through the privileged CLI and Guardian LocalAPI.
- Guardian does not parse client links or implement transport/data-plane behavior.
- Guardian and Core sockets are separate: `/var/run/bx-guard.sock` and `/var/run/bx.sock`.
- Guardian state paths are fixed by the spec: `/var/lib/bx/guardian-state.json` and `/var/lib/bx/update/{transaction.json,receipt.json,staging/,snapshots/}` with root-only permissions.
- Guardian LaunchDaemon is `com.getbx.bx.guard`; Core no longer has its own KeepAlive LaunchDaemon after migration.
- BxMenu LaunchAgent is `com.getbx.bx.menu`; legacy `com.ggshr9.bx.menu` is removed idempotently.
- Core and Guardian compatibility spans at least N-1, N, and N+1 lifecycle protocol versions.
- V1 guarded activation requires at least one resolved IPv4 `/32` server bypass; an IPv6-only server is rejected before barrier installation because the current Darwin Core route implementation has no verified IPv6 physical-gateway bypass.
- Follow repository TDD: add focused failing tests, confirm red, implement the minimum behavior, confirm green, then commit.

---

## Planned File Structure

```text
internal/guardian/
  types.go                    lifecycle phases, status, request/response contracts
  store.go                    atomic desired-state, transaction, and receipt persistence
  store_test.go
  barrier.go                  platform-neutral Barrier interface and route specs
  barrier_darwin.go           route command execution and gateway discovery
  barrier_other.go            explicit unsupported implementation
  barrier_test.go             pure maintenance route planning
  process.go                  Core child runner and identity validation
  manager.go                  desired-state and Core lifecycle state machine
  manager_test.go
  health.go                   complete Core health gate
  health_test.go
  localapi.go                 versioned HTTP-over-Unix Guardian server
  client.go                   CLI client
  localapi_test.go
  daemon.go                   listener, journal recovery, and signal lifecycle
  update.go                   guarded activation/rollback transaction
  update_test.go
internal/update/
  macos_package.go            strict archive parser and app/CLI payload
  macos_package_test.go
  macos_install.go            snapshot, atomic activation, restore, receipt
  macos_install_test.go
internal/install/
  guardian_darwin.go          Guardian plist/install/bootstrap/migration commands
  guardian_other.go           non-macOS no-op/unsupported boundary
  guardian_darwin_test.go
internal/supervisor/
  runtime_state.go            non-secret Core handoff/health metadata
  runtime_state_test.go
internal/cli/
  guardian.go                 hidden guardian daemon command and client adapters
  guardian_test.go
  update.go                   download/check front end and guarded activation routing
  update_package.go           thin compatibility wrappers only
apps/macos/BxMenu/Sources/BxMenu/
  GuardianPresentation.swift  three-color lifecycle/update presentation
  UpdatePresentation.swift    update confirmation/result copy
  main.swift                  Guardian-aware actions and polling
apps/macos/BxMenu/Tests/
  GuardianPresentationTests.swift
  UpdatePresentationTests.swift
scripts/
  darwin-guardian-testkit.sh  dry-run-first migration/update/failure smoke harness
```

### Task 1: Persisted Guardian state and update journal

**Files:**
- Create: `internal/guardian/types.go`
- Create: `internal/guardian/store.go`
- Create: `internal/guardian/store_test.go`

**Interfaces:**
- Produces: `type DesiredState string` with `DesiredOn` and `DesiredOff`.
- Produces: `type Phase string` with `idle`, `prepared`, `barrier_active`, `activating`, `rolling_back`, `committed`, `rolled_back`, and `needs_attention`.
- Produces: `type Transaction`, `Receipt`, `Status`, and `UpdateResult` JSON contracts.
- Produces: `OpenStore(Paths)`, `LoadDesired`, `SaveDesired`, `LoadTransaction`, `SaveTransaction`, `ClearTransaction`, and `SaveReceipt`.

- [ ] **Step 1: Write failing atomic-store tests**

Use `t.TempDir()` and exact path overrides:

```go
func TestStorePersistsDesiredStateAtomically(t *testing.T) {
    paths := TestPaths(t.TempDir())
    s := OpenStore(paths)
    if err := s.SaveDesired(DesiredOn); err != nil { t.Fatal(err) }
    got, err := s.LoadDesired()
    if err != nil || got != DesiredOn { t.Fatalf("desired = %q, %v", got, err) }
    info, err := os.Stat(paths.Desired)
    if err != nil || info.Mode().Perm() != 0o600 { t.Fatalf("mode = %v, %v", info.Mode(), err) }
}

func TestStoreRejectsInvalidTransactionPhase(t *testing.T) {
    s := OpenStore(TestPaths(t.TempDir()))
    err := s.SaveTransaction(Transaction{ID: "tx-1", Phase: Phase("unknown")})
    if err == nil { t.Fatal("invalid phase accepted") }
}

func TestTransactionJSONContainsNoClientSecrets(t *testing.T) {
    tx := Transaction{ID: "tx-1", FromVersion: "v1", ToVersion: "v2", Phase: PhasePrepared}
    b, err := json.Marshal(tx)
    if err != nil { t.Fatal(err) }
    for _, forbidden := range []string{"server_link", "client_link", "token", "password"} {
        if bytes.Contains(bytes.ToLower(b), []byte(forbidden)) { t.Fatalf("journal contains %q: %s", forbidden, b) }
    }
}
```

- [ ] **Step 2: Run tests and confirm red**

Run: `go test ./internal/guardian -run 'TestStore|TestTransaction'`

Expected: FAIL because `internal/guardian` does not exist.

- [ ] **Step 3: Implement finite state and atomic persistence**

Define the shared contracts exactly:

```go
type Paths struct {
    Desired, Transaction, Receipt, Staging, Snapshots string
}

type Transaction struct {
    ID           string    `json:"transaction_id"`
    FromVersion  string    `json:"from_version"`
    ToVersion    string    `json:"to_version"`
    Phase        Phase     `json:"phase"`
    AssetDigest  string    `json:"asset_digest"`
    SnapshotPath string    `json:"snapshot_path"`
    StartedAt    time.Time `json:"started_at"`
    UpdatedAt    time.Time `json:"updated_at"`
    LastError    string    `json:"last_error,omitempty"`
}

type UpdateResult struct {
    FromVersion    string `json:"from_version"`
    ToVersion      string `json:"to_version"`
    Phase          Phase  `json:"phase"`
    CoreActivated  bool   `json:"core_activated"`
    RolledBack     bool   `json:"rolled_back"`
    ProtectionState string `json:"protection_state"`
}
```

Use same-directory temp file, `Chmod(0600)`, `Sync`, `Close`, then `Rename`. Create state directories with `0700`. Missing desired state defaults to `DesiredOff`; malformed state fails closed with an error rather than guessing.

- [ ] **Step 4: Verify and commit**

Run: `go test ./internal/guardian && git diff --check`

Expected: PASS.

Commit:

```bash
git add internal/guardian/types.go internal/guardian/store.go internal/guardian/store_test.go
git commit -m "feat(macos): persist guardian lifecycle state"
```

### Task 2: Maintenance barrier planner and Darwin executor

**Files:**
- Create: `internal/guardian/barrier.go`
- Create: `internal/guardian/barrier_darwin.go`
- Create: `internal/guardian/barrier_other.go`
- Create: `internal/guardian/barrier_test.go`

**Interfaces:**
- Consumes: Task 1 state types.
- Produces: `type BarrierContext struct { Gateway string; ServerBypass []string; BlockIPv6 bool }`.
- Produces: `func PlanBarrier(BarrierContext) (apply, reassert, cleanup []Command, err error)`.
- Produces: `type Barrier interface { Install(context.Context, BarrierContext) error; ReassertBypass(context.Context, BarrierContext) error; Remove(context.Context, BarrierContext) error }`.
- Produces: `DiscoverDefaultGateway(context.Context) (string, error)` on Darwin.

- [ ] **Step 1: Write failing pure planner tests**

```go
func TestPlanBarrierBlocksPublicIPv4MoreSpecificallyThanSplitDefault(t *testing.T) {
    apply, reassert, cleanup, err := PlanBarrier(BarrierContext{
        Gateway: "192.168.50.2", ServerBypass: []string{"23.27.134.77/32"}, BlockIPv6: true,
    })
    if err != nil { t.Fatal(err) }
    requireCommands(t, apply,
        "route -n add -net 23.27.134.77/32 192.168.50.2",
        "route -n add -net 0.0.0.0/2 127.0.0.1 -reject",
        "route -n add -net 64.0.0.0/2 127.0.0.1 -reject",
        "route -n add -net 128.0.0.0/2 127.0.0.1 -reject",
        "route -n add -net 192.0.0.0/2 127.0.0.1 -reject",
        "route -n add -inet6 -net ::/2 ::1 -reject",
    )
    requireCommands(t, reassert, "route -n add -net 23.27.134.77/32 192.168.50.2")
    if cleanup[0].String() == apply[0].String() { t.Fatal("cleanup must delete in reverse order") }
}

func TestPlanBarrierRejectsBroadOrNonIPBypass(t *testing.T) {
    for _, bypass := range []string{"0.0.0.0/0", "23.27.134.0/24", "example.com"} {
        if _, _, _, err := PlanBarrier(BarrierContext{Gateway: "192.168.1.1", ServerBypass: []string{bypass}}); err == nil {
            t.Fatalf("unsafe bypass accepted: %s", bypass)
        }
    }
}
```

The IPv6 plan contains four `/2` reject routes: `::/2`, `4000::/2`, `8000::/2`, and `c000::/2`. Existing loopback, link-local, ULA, private, and overlay routes remain more specific and continue to win.

- [ ] **Step 2: Confirm red**

Run: `go test ./internal/guardian -run 'TestPlanBarrier'`

Expected: FAIL with undefined barrier symbols.

- [ ] **Step 3: Implement planner and command seam**

Use a command value rather than raw shell:

```go
type Command struct { Name string; Args []string }
func (c Command) String() string { return strings.Join(append([]string{c.Name}, c.Args...), " ") }

type CommandRunner interface {
    Run(context.Context, Command) error
}
```

Validate gateway with `netip.ParseAddr` and require every server bypass to be a single IPv4 `/32`; reject an IPv6-only handoff before running any command. Apply bypass first, then reject routes. Cleanup reject routes first and bypass last. `ReassertBypass` exists because stopping the old Core removes its identical bypass route; Guardian re-adds the exact route after old Core teardown and before starting the new Core.

- [ ] **Step 4: Implement Darwin discovery/execution and non-Darwin refusal**

Darwin gateway discovery runs `/sbin/route -n get default`, parses the `gateway:` field, and rejects empty/non-IP values. Executor runs only prevalidated argv through `/sbin/route`; no shell. Treat “already exists” as success during install/reassert and “not in table” as success during cleanup. `barrier_other.go` returns `ErrUnsupported`.

- [ ] **Step 5: Verify and commit**

Run:

```bash
go test ./internal/guardian
GOOS=darwin GOARCH=arm64 go test -c ./internal/guardian -o /tmp/bx-guardian-darwin.test
git diff --check
```

Expected: tests pass and Darwin package compiles. Do not run the Darwin test binary with route mutation.

Commit: `git commit -m "feat(macos): plan fail-closed maintenance barrier"`

### Task 3: Core handoff metadata and complete health gate

**Files:**
- Create: `internal/supervisor/runtime_state.go`
- Create: `internal/supervisor/runtime_state_test.go`
- Create: `internal/guardian/health.go`
- Create: `internal/guardian/health_test.go`
- Modify: `internal/supervisor/run.go`
- Modify: `internal/supervisor/control.go`
- Modify: `internal/supervisor/control_client.go`
- Modify: `internal/stats/render.go`
- Modify: `internal/stats/stats.go`

**Interfaces:**
- Produces from Core: `GET /v0/runtime` returning non-secret `RuntimeState`.
- Produces: `supervisor.FetchRuntimeState(sockPath string) (RuntimeState, error)`.
- Produces: `guardian.HealthChecker.Wait(ctx, HealthTarget) (RuntimeState, error)`.

```go
type HealthTarget struct {
    Version string
    PID     int
    Timeout time.Duration
}
```

- [ ] **Step 1: Write failing runtime-state redaction tests**

```go
func TestRuntimeStateContainsOnlyHandoffMetadata(t *testing.T) {
    state := RuntimeState{
        Version: "v0.3.0", TunName: "utun7", ServerBypass: []string{"23.27.134.77/32"},
        TunnelHealthy: true, DNSListening: true, RoutesInstalled: true, UDPRequired: true, UDPReady: true,
    }
    b, _ := json.Marshal(state)
    for _, forbidden := range []string{"vless://", "hysteria2://", "password", "token", "uuid"} {
        if bytes.Contains(bytes.ToLower(b), []byte(forbidden)) { t.Fatalf("runtime state leaked %q", forbidden) }
    }
}
```

Define exactly:

```go
type RuntimeState struct {
    Version       string   `json:"version"`
    PID           int      `json:"pid"`
    TunName       string   `json:"tun_name"`
    SocksAddr     string   `json:"socks_addr"`
    ServerBypass  []string `json:"server_bypass"`
    TunnelHealthy bool     `json:"tunnel_healthy"`
    DNSListening  bool     `json:"dns_listening"`
    RoutesInstalled bool   `json:"routes_installed"`
    UDPRequired   bool     `json:"udp_required"`
    UDPReady      bool     `json:"udp_ready"`
}
```

- [ ] **Step 2: Add failing health-combination tests**

Table-test that wrong version, unhealthy tunnel, DNS false, routes false, required UDP false, PID mismatch, and failed local proxy probe each prevent success. Include one complete passing row.

- [ ] **Step 3: Implement Core runtime endpoint**

Populate state from values already known inside `supervisor.Run`: resolved server IPs, actual TUN name, DNS listener success, hijack success, primary/UDP tunnel health, `os.Getpid()`, and `version.Version`. Do not expose raw links or config. Add read-only `/v0/runtime`; it does not require mutating peer authorization.

- [ ] **Step 4: Implement Guardian health gate**

`HealthChecker` polls `/v0/runtime`, verifies the expected version/PID, and then probes the current Core's local SOCKS address through the Core transport. The probe must use loopback and must not rely on the system default route while the barrier is active. Return the last concrete failed condition on timeout.

- [ ] **Step 5: Verify and commit**

Run:

```bash
go test ./internal/supervisor ./internal/stats ./internal/guardian
git diff --check
```

Commit: `git commit -m "feat: expose non-secret core handoff state"`

### Task 4: Guardian Core manager and LocalAPI

**Files:**
- Create: `internal/guardian/process.go`
- Create: `internal/guardian/manager.go`
- Create: `internal/guardian/manager_test.go`
- Create: `internal/guardian/localapi.go`
- Create: `internal/guardian/client.go`
- Create: `internal/guardian/localapi_test.go`
- Create: `internal/guardian/daemon.go`
- Modify: `internal/cli/cli.go`
- Create: `internal/cli/guardian.go`
- Create: `internal/cli/guardian_test.go`

**Interfaces:**
- Produces: hidden `bx guardian --config /etc/bx/config.yaml --listen-dns 127.0.0.1:53` command.
- Produces: `GET /v1/status`, `POST /v1/up`, `POST /v1/down` over `/var/run/bx-guard.sock`.
- Produces: `guardian.Client.Status`, `Up`, and `Down`.
- Consumes: Tasks 1-3 store, barrier, and health checker.

- [ ] **Step 1: Write failing manager tests with fakes**

Use interfaces `CoreRunner`, `Barrier`, `HealthGate`, and `NetworkRestorer`. Cover:

```go
func TestManagerUpStartsOneCoreAndPersistsOn(t *testing.T) {
    env := newManagerTestEnv(t)
    if err := env.manager.Up(context.Background()); err != nil { t.Fatal(err) }
    if err := env.manager.Up(context.Background()); err != nil { t.Fatal(err) }
    if env.runner.starts != 1 { t.Fatalf("starts = %d", env.runner.starts) }
    if got, _ := env.store.LoadDesired(); got != DesiredOn { t.Fatalf("desired = %q", got) }
}

func TestManagerDownTransitionsBehindBarrier(t *testing.T) {
    env := newProtectedManagerTestEnv(t)
    if err := env.manager.Down(context.Background()); err != nil { t.Fatal(err) }
    want := []string{"barrier.install", "core.stop", "network.restore", "desired.off", "barrier.remove"}
    if !reflect.DeepEqual(want, env.events) { t.Fatalf("events = %#v, want %#v", env.events, want) }
}

func TestManagerAdoptsMatchingHealthyCore(t *testing.T) {
    env := newManagerTestEnv(t)
    env.runner.existing = Process{PID: 42, Executable: install.BinPath, UID: 0}
    env.health.runtime = supervisor.RuntimeState{PID: 42, Version: version.Version, TunnelHealthy: true, DNSListening: true, RoutesInstalled: true}
    if err := env.manager.Up(context.Background()); err != nil { t.Fatal(err) }
    if env.runner.starts != 0 { t.Fatalf("unexpected starts = %d", env.runner.starts) }
}

func TestManagerRejectsUnverifiableExistingPID(t *testing.T) {
    env := newManagerTestEnv(t)
    env.runner.existing = Process{PID: 42, Executable: "/tmp/not-bx", UID: 501}
    if err := env.manager.Up(context.Background()); err == nil { t.Fatal("unverifiable process adopted") }
    if env.runner.signals != 0 { t.Fatal("unrelated process was signalled") }
}

func TestManagerSerializesMutations(t *testing.T) {
    env := newManagerTestEnv(t)
    env.runner.blockStart = make(chan struct{})
    firstDone := make(chan error, 1)
    go func() { firstDone <- env.manager.Up(context.Background()) }()
    <-env.runner.startEntered
    secondDone := make(chan error, 1)
    go func() { secondDone <- env.manager.Down(context.Background()) }()
    select { case <-secondDone: t.Fatal("Down overlapped Up"); case <-time.After(50 * time.Millisecond): }
    close(env.runner.blockStart)
    if err := <-firstDone; err != nil { t.Fatal(err) }
    if err := <-secondDone; err != nil { t.Fatal(err) }
}
```

Implement `newManagerTestEnv` and `newProtectedManagerTestEnv` with in-memory fakes that append every interface call to one shared `events` slice. Keep those fakes in `manager_test.go`.

- [ ] **Step 2: Implement Core process ownership**

`ExecCoreRunner` starts the installed binary with argument-vector construction only:

```go
[]string{"run", "-c", configPath, "--listen-dns", dnsListen}
```

Record PID and executable identity in root state. On Guardian restart, adoption requires: PID alive, effective UID 0, executable resolves to the installed bx inode/path, and `/v0/runtime` reports the same PID. If any check fails, do not signal an unrelated process.

Monitor the child exit channel. If Core exits while desired state is On and no planned mutation owns the lock, install the maintenance barrier immediately, mark `needs_attention`, and attempt one bounded restart through the complete health gate. This is best-effort crash recovery, not part of the planned-update no-direct proof.

- [ ] **Step 3: Implement manager desired-state behavior**

`Up` writes `DesiredOn` before starting, starts/adopts one Core, waits for health, then reports Protected. Fresh normal startup does not use the update barrier unless recovering a transaction. `Down` obtains the current runtime handoff, installs the maintenance barrier, stops Core, restores managed DNS/routes, writes `DesiredOff`, and only then removes the barrier to permit intentional direct networking. If restoration fails, restore the previous protected Core under the barrier; if that also fails, retain the barrier and report `needs_attention`. Make repeated `Up` and `Down` successful.

- [ ] **Step 4: Add LocalAPI and root peer authorization**

Reuse the repository's Darwin `LOCAL_PEERCRED` pattern. Read-only status is available to the logged-in user; `/v1/up` and `/v1/down` require UID 0. Replies use:

```go
type Status struct {
    SchemaVersion int          `json:"schema_version"`
    Desired       DesiredState `json:"desired"`
    Phase         Phase        `json:"phase"`
    CorePID       int          `json:"core_pid,omitempty"`
    CoreVersion   string       `json:"core_version,omitempty"`
    Protection    string       `json:"protection_state"`
    LastError     string       `json:"last_error,omitempty"`
}
```

- [ ] **Step 5: Wire hidden daemon command and verify**

The command refuses non-root execution, creates the socket directory safely, removes only a stale socket owned by root, starts journal recovery, and handles SIGTERM without restoring direct network unless a requested `down` transaction is in progress.

Run:

```bash
go test ./internal/guardian ./internal/cli
GOOS=darwin GOARCH=arm64 go build ./...
git diff --check
```

Commit: `git commit -m "feat(macos): add root guardian lifecycle daemon"`

### Task 5: Guardian launchd installation, legacy migration, and `up/down`

**Files:**
- Create: `internal/install/guardian_darwin.go`
- Create: `internal/install/guardian_other.go`
- Create: `internal/install/guardian_darwin_test.go`
- Modify: `internal/install/install.go`
- Modify: `internal/install/install_test.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Modify: `scripts/install-macos-menu.sh`

**Interfaces:**
- Produces: `install.WriteGuardianUnit`, `EnableGuardian`, `GuardianInstalled`, `GuardianActive`, and `RemoveLegacyCoreUnit` on Darwin.
- Changes macOS `upAction`/`downAction` to call Guardian; Linux and Windows behavior stays unchanged.
- Produces: `ensureMacOSMenuRunning(uid int) error` as a best-effort UI helper.

- [ ] **Step 1: Write failing plist and migration tests**

Require Guardian plist arguments and ownership contract:

```xml
<string>/usr/local/bin/bx</string>
<string>guardian</string>
<string>--config</string>
<string>/etc/bx/config.yaml</string>
<string>--listen-dns</string>
<string>127.0.0.1:53</string>
```

Require label `com.getbx.bx.guard`, `RunAtLoad=true`, `KeepAlive=true`, and root log paths. Migration command tests must treat absent/disabled `com.getbx.bx` and `com.ggshr9.bx` as success.

- [ ] **Step 2: Implement fresh install and setup integration**

On macOS, `setup` writes the Guardian plist rather than a direct Core plist. It does not start protection. Preserve current Linux systemd and Windows service output exactly.

- [ ] **Step 3: Implement guarded legacy migration**

When a direct Core service is loaded, CLI obtains non-secret handoff metadata. For the first v0.2.x migration where `/v0/runtime` is unavailable, a narrowly scoped CLI fallback resolves only server host IPs from the existing config and discovers the physical gateway; raw links never enter Guardian state or logs. Then:

1. bootstrap Guardian without starting a second Core;
2. send a root `migrate` request with validated gateway and `/32`/`/128` bypasses;
3. Guardian installs barrier;
4. bootout old direct label;
5. reassert bypass routes;
6. start/health-check child Core;
7. remove barrier;
8. delete old plist only after success.

Failure before step 3 leaves the old Core untouched. Failure after step 3 follows the same rollback/needs-attention rules as an update.

- [ ] **Step 4: Route macOS `up/down` through Guardian**

`up` ensures Guardian is installed/active, calls `guardian.Client.Up`, waits for Protected, then best-effort starts the menu LaunchAgent for the console user. Menu failure prints one warning and leaves Core running. `down` calls Guardian, which restores DNS/routes and persists Off; CLI no longer bootouts Core directly.

- [ ] **Step 5: Make menu bootstrap singular and idempotent**

Remove `~/Library/LaunchAgents/com.ggshr9.bx.menu.plist`, bootout a loaded legacy label if present, bootstrap/kickstart only `com.getbx.bx.menu`, and verify only one matching menu process via launchd label rather than `pgrep` name matching.

- [ ] **Step 6: Verify and commit**

Run:

```bash
go test ./internal/install ./internal/cli ./internal/guardian
bash -n scripts/install-macos-menu.sh
GOOS=darwin GOARCH=arm64 go build ./...
git diff --check
```

Commit: `git commit -m "feat(macos): make guardian own bx lifecycle"`

### Task 6: Move macOS package activation into a rollback-capable update library

**Files:**
- Create: `internal/update/macos_package.go`
- Create: `internal/update/macos_package_test.go`
- Create: `internal/update/macos_install.go`
- Create: `internal/update/macos_install_test.go`
- Modify: `internal/cli/update_package.go`
- Modify: `internal/cli/update_package_test.go`

**Interfaces:**
- Produces: `update.ExtractMacOSPackage(data []byte, arch string) (MacOSPayload, error)`.
- Produces: `update.PrepareMacOSInstall(InstallOptions, MacOSPayload) (*PreparedInstall, error)`.
- Produces methods `Activate() error`, `Restore() error`, `Commit() error`.
- Preserves the existing strict archive path, size, regular-file, CLI, Info.plist, and BxMenu checks.

- [ ] **Step 1: Move existing parser tests first and confirm red**

Copy the existing archive tests into package `update`, change calls to exported names, and add traversal, symlink, duplicate, oversized, missing CLI, and missing menu cases. Run:

`go test ./internal/update -run 'TestExtractMacOSPackage'`

Expected: FAIL until parser moves.

- [ ] **Step 2: Implement parser without behavior drift**

Move parser logic from `internal/cli/update_package.go`; leave temporary CLI wrappers so existing callers compile. Keep the 128 MiB cap and fixed `bx-macos-<arch>/` root.

- [ ] **Step 3: Write failing snapshot/rollback tests**

```go
func TestPreparedInstallRestoresCLIAndAppAfterActivationFailure(t *testing.T) {
    env := newInstallTestEnv(t, "old-cli", "old-menu")
    prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
    if err != nil { t.Fatal(err) }
    env.ops.FailRenameTo(env.appPath)
    if err := prepared.Activate(); err == nil { t.Fatal("activation unexpectedly succeeded") }
    if err := prepared.Restore(); err != nil { t.Fatal(err) }
    requireFileContents(t, env.cliPath, "old-cli")
    requireFileContents(t, filepath.Join(env.appPath, "Contents/MacOS/BxMenu"), "old-menu")
}

func TestPreparedInstallCommitDeletesSnapshot(t *testing.T) {
    env := newInstallTestEnv(t, "old-cli", "old-menu")
    prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
    if err != nil { t.Fatal(err) }
    if err := prepared.Activate(); err != nil { t.Fatal(err) }
    if err := prepared.Commit(); err != nil { t.Fatal(err) }
    if _, err := os.Stat(env.snapshotPath); !errors.Is(err, os.ErrNotExist) { t.Fatalf("snapshot still exists: %v", err) }
}

func TestPreparedInstallNeverCopiesConfigOrState(t *testing.T) {
    env := newInstallTestEnv(t, "old-cli", "old-menu")
    writeTestFile(t, filepath.Join(env.root, "etc/bx/config.yaml"), "server: secret-link", 0o600)
    prepared, err := PrepareMacOSInstall(env.options(), testPayload("new-cli", "new-menu"))
    if err != nil { t.Fatal(err) }
    entries, err := listRelativeFiles(prepared.SnapshotPath())
    if err != nil { t.Fatal(err) }
    want := []string{"Bx.app/Contents/Info.plist", "Bx.app/Contents/MacOS/BxMenu", "bx"}
    if !reflect.DeepEqual(want, entries) { t.Fatalf("entries = %#v, want %#v", entries, want) }
}
```

Implement `newInstallTestEnv`, `testPayload`, `requireFileContents`, and sorted `listRelativeFiles` in `macos_install_test.go`; the fake `FileOps` fails only the configured destination rename.

Inject filesystem operations through a small `FileOps` interface; do not require root.

- [ ] **Step 4: Implement transactional installer**

Snapshot `/usr/local/bin/bx` and the selected `Bx.app` into the transaction snapshot directory. Stage replacements beside each destination, preserve app owner, and rename atomically. `Restore` is idempotent and restores both destinations. `Commit` removes snapshot/staging only after Guardian has removed the barrier and written receipt.

`InstallOptions` contains only validated destinations and ownership:

```go
type InstallOptions struct {
    CLIDestination string
    AppDestination string
    AppUID          int
    AppGID          int
    SnapshotDir     string
    StagingDir      string
}
```

Require the CLI destination to equal `install.BinPath`. App destination must be an absolute path ending in `Bx.app`, either the console user's `~/Applications/Bx.app` or `/Applications/Bx.app`; its UID/GID must match the discovered console user unless it is the system-wide root-owned app.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/update ./internal/cli && git diff --check`

Commit: `git commit -m "refactor(update): add rollback-capable macOS package install"`

### Task 7: Guarded update activation, rollback, and crash recovery

**Files:**
- Create: `internal/guardian/update.go`
- Create: `internal/guardian/update_test.go`
- Modify: `internal/guardian/manager.go`
- Modify: `internal/guardian/daemon.go`
- Modify: `internal/guardian/localapi.go`
- Modify: `internal/guardian/client.go`

**Interfaces:**
- Produces: `POST /v1/update` accepting a root-only, already verified `PreparedUpdate` reference under Guardian-owned staging.
- Produces: `Manager.Update(context.Context, UpdateRequest) (UpdateResult, error)`.
- Produces: `Manager.Recover(context.Context) error` called before desired-state startup.

Define the activation request exactly:

```go
type UpdateRequest struct {
    TransactionID string `json:"transaction_id"`
    FromVersion   string `json:"from_version"`
    ToVersion     string `json:"to_version"`
    AssetSHA256   string `json:"asset_sha256"`
    PackagePath   string `json:"package_path"`
    AppPath       string `json:"app_path,omitempty"`
    AppUID        int    `json:"app_uid,omitempty"`
    AppGID        int    `json:"app_gid,omitempty"`
}
```

Require `PackagePath` to resolve beneath `/var/lib/bx/update/staging/<transaction-id>/`, reject symlinks, and recompute SHA-256 before barrier installation. Guardian derives gateway and bypass from the live Core runtime state; callers cannot provide arbitrary bypass routes.

- [ ] **Step 1: Write table-driven transaction tests**

Inject failures after each exact event: `prepared`, barrier install, old stop, bypass reassert, activate, new start, health, receipt, and barrier cleanup. Assert ordered calls and final state. The key rows are:

```go
{name: "success", wantPhase: PhaseCommitted, wantBarrier: false, wantVersion: "v2"},
{name: "new unhealthy old healthy", failAt: "new-health", wantPhase: PhaseRolledBack, wantBarrier: false, wantVersion: "v1"},
{name: "new and old unhealthy", failAt: "old-health", wantPhase: PhaseNeedsAttention, wantBarrier: true},
{name: "prepare failure", failAt: "prepare", wantOldRunning: true, wantBarrierNeverInstalled: true},
```

- [ ] **Step 2: Implement the update state machine**

The only valid event order is:

```text
prepare+verify -> journal(prepared) -> barrier install -> journal(barrier_active)
-> stop old -> reassert bypass -> activate files -> start new -> complete health gate
-> journal(committed) -> barrier remove -> receipt -> cleanup
```

On target failure, keep barrier, stop target, restore snapshot, start previous, run the same complete health gate, then remove barrier only on success. Serialize update against `Up`, `Down`, migration, and another update with the manager mutation lock.

- [ ] **Step 3: Write crash-recovery tests for every persisted phase**

Restart a fresh Manager against the same temp store for `prepared`, `barrier_active`, `activating`, and `rolling_back`. Require:

- `prepared`: discard unactivated staging and continue prior Core;
- later phases: install/reassert barrier before any Core start;
- committed receipt: cleanup only, no second activation;
- malformed journal: keep barrier and return needs_attention.

- [ ] **Step 4: Implement startup recovery and Guardian self-version rule**

The in-memory Guardian completing the transaction remains running even though `/usr/local/bin/bx` changed. Do not kickstart Guardian during update. A later Guardian restart may load the new binary and must read the prior protocol/journal version. Reject packages requiring a Guardian protocol newer than the running Guardian before barrier installation.

- [ ] **Step 5: Verify and commit**

Run:

```bash
go test ./internal/guardian ./internal/update -count=1
go test -race ./internal/guardian
git diff --check
```

Commit: `git commit -m "feat(macos): activate updates behind guardian barrier"`

### Task 8: Simplify `bx update`, machine output, capabilities, and MCP

**Files:**
- Modify: `internal/cli/update.go`
- Modify: `internal/cli/update_test.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Modify: `internal/mcp/liveops.go`
- Modify: `internal/mcp/liveops_test.go`
- Modify: `README.md`

**Interfaces:**
- User command remains `sudo bx update`; macOS complete-package behavior becomes the default.
- `bx update --check --json` remains read-only.
- Agent update result exposes Task 1 `UpdateResult` and never emits links/secrets.

- [ ] **Step 1: Replace the obsolete no-restart source contract test**

Delete `TestUpdateDoesNotRestartProtection`. Add tests requiring that active macOS protection calls Guardian update, inactive protection installs without starting Core, and `--check` makes no mutation call. Assert JSON keys exactly:

```json
{"from_version":"v1","to_version":"v2","phase":"committed","core_activated":true,"rolled_back":false,"protection_state":"protected"}
```

- [ ] **Step 2: Make full macOS package the default**

Keep `--package` hidden for compatibility. On Darwin, download/verify the complete package, copy it into Guardian-owned staging, and call `/v1/update` when desired/running state is On. When Off, use the same prepared installer without barrier and do not start Core. Linux/Windows continue their existing binary update path until platform-specific guardians exist.

Discover the active console UID/GID from `/dev/console`, resolve its home with `os/user.LookupId`, then select `~/Applications/Bx.app` when installed, otherwise `/Applications/Bx.app` when installed. If neither exists, update CLI/Core only and leave menu installation to the normal installer. Pass the validated app target in `UpdateRequest`; after commit or successful rollback, best-effort kickstart exactly `gui/<uid>/com.getbx.bx.menu` so the in-memory menu matches the installed app. Menu restart failure is a warning and never changes protection state.

- [ ] **Step 3: Use four concise human phases**

Print only:

```text
• Prepare    Verify update
✓ Prepare    Ready
• Update     Install vX
• Reconnect  Restore protection
✓ Done       bx is up to date
```

Rollback result: `Update couldn't be completed. Previous version restored.` Double failure: `Protection needs attention. Run bx doctor.`

- [ ] **Step 4: Update capabilities and MCP semantics**

Change update safety notes to: signed/verified before switching, Internet may pause briefly, maintenance remains fail-closed, automatic previous-Core rollback. Agent must call one update operation, never synthesize `down/up`. MCP reports unavailable/permission errors structurally.

- [ ] **Step 5: Verify and commit**

Run: `go test ./internal/cli ./internal/mcp ./internal/update ./internal/guardian && git diff --check`

Commit: `git commit -m "feat: make guarded package update the default"`

### Task 9: Guardian-aware BxMenu lifecycle and update UX

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/GuardianPresentation.swift`
- Create: `apps/macos/BxMenu/Tests/GuardianPresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/UpdatePresentation.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `apps/macos/BxMenu/Tests/UpdatePresentationTests.swift`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/StatusIndicator.swift`
- Modify: `apps/macos/BxMenu/README.md`

**Interfaces:**
- Consumes: `bx guardian-status --json` or equivalent thin CLI read adapter for `/v1/status`.
- Produces three user colors only: green Protected, yellow Working/Needs Attention, red Protection needs attention.

- [ ] **Step 1: Write failing pure presentation tests**

```swift
expect(presentation(for: status(phase: "committed", protection: "protected")).color == .green)
expect(presentation(for: status(phase: "activating", protection: "blocked")).title == "Updating")
expect(presentation(for: status(phase: "rolled_back", protection: "protected")).color == .green)
expect(presentation(for: status(phase: "needs_attention", protection: "blocked")).color == .red)
```

Also assert exact confirmation copy:

```text
Update bx?
Internet access may pause briefly. bx will reconnect automatically.
[Not Now] [Update]
```

- [ ] **Step 2: Implement Guardian polling and action routing**

Poll read-only Guardian status alongside Core status. During `preparing`, `barrier_active`, `activating`, or `rolling_back`, show yellow Updating and suppress conflicting Turn Off/Reconnect actions. Invoke only `sudo bx update`; remove menu construction of `--package`, app path, owner, and old “Protection stays on” copy.

- [ ] **Step 3: Implement results and exit semantics**

Success returns green. Rollback returns green plus `Update couldn't be completed. Previous version restored.` Double failure returns red with **Run Doctor**. Keep **Turn Off bx** as Guardian down and **Quit Menu** as UI-only; menu never exits Core implicitly.

- [ ] **Step 4: Verify and commit**

Run:

```bash
swift test --package-path apps/macos/BxMenu
swift build --package-path apps/macos/BxMenu -c release
git diff --check
```

Commit: `git commit -m "feat(macos): show guardian update lifecycle in menu"`

### Task 10: Release integration, docs, and dry-run-first macOS acceptance harness

**Files:**
- Create: `scripts/darwin-guardian-testkit.sh`
- Create: `scripts/verify-guardian-source-contracts.sh`
- Modify: `scripts/package-macos-release.sh`
- Modify: `scripts/verify-macos-release.sh`
- Modify: `.github/workflows/release.yml`
- Modify: `README.md`
- Modify: `apps/macos/BxMenu/README.md`
- Modify: `CLAUDE.md`

**Interfaces:**
- Produces dry-run scenarios: `migration`, `update-success`, `new-core-failure`, `double-failure`, and `journal-recovery`.
- Produces no privileged execution unless both `--execute` and a scenario-specific confirmation are present.

- [ ] **Step 1: Write source/release contract checks**

The script must fail unless:

- package contains `bx` and `Bx.app`, and both assets match signed manifest entries;
- source/install contracts generate `com.getbx.bx.guard` with the exact hidden `bx guardian` argv;
- old direct Core plist is not installed for fresh setups;
- BxMenu copy contains the brief interruption message;
- docs do not claim the running Core remains old after update;
- docs state that double failure remains safely offline.

- [ ] **Step 2: Build the dry-run harness**

Default output prints exact Guardian, launchctl, route, Core, DNS, and cleanup phases without executing them. `--execute` requires root, an automatic rollback deadline, log directory under `.bx-test-logs`, and explicit `--gateway`, `--server-bypass`, and `--dns-service` when discovery is uncertain. It must not infer permission from environment variables alone.

- [ ] **Step 3: Add passive leak evidence collection**

During an explicitly authorized real run, capture route snapshots, DNS state, Core/Guardian PIDs and versions, status JSON, physical-interface packet evidence, and expected egress. A result passes only when traffic during the barrier either uses the expected protected path or fails; any public physical-interface flow fails the run.

- [ ] **Step 4: Update user and maintainer docs**

Document the simple experience:

```text
sudo bx up       # starts protection and the installed menu app
sudo bx update   # verifies, updates, reconnects, and rolls back automatically
sudo bx down     # turns protection off and restores managed network state
```

Explain that update may pause Internet briefly, never intentionally falls back direct, and first-packet-at-boot absolute enforcement remains a future Network Extension property. Keep Apple signing/notarization as a later release gate, not a blocker for Guardian semantics.

- [ ] **Step 5: Run automated verification without changing network**

Run:

```bash
go test ./...
go test -race ./internal/guardian ./internal/update
swift test --package-path apps/macos/BxMenu
swift build --package-path apps/macos/BxMenu -c release
bash -n scripts/darwin-guardian-testkit.sh
scripts/darwin-guardian-testkit.sh --scenario update-success --gateway 192.0.2.1 --server-bypass 198.51.100.10/32 --dns-service Wi-Fi
scripts/package-macos-release.sh
scripts/verify-macos-release.sh
scripts/verify-guardian-source-contracts.sh
git diff --check
```

Expected: all tests pass; testkit prints `dry-run only` and performs no route/DNS/launchctl mutation.

- [ ] **Step 6: Commit**

```bash
git add scripts .github/workflows/release.yml README.md CLAUDE.md apps/macos/BxMenu/README.md
git commit -m "docs: ship macOS guardian update workflow"
```

### Task 11: Explicitly authorized real-macOS acceptance

**Files:**
- Modify only if evidence exposes a defect: files owned by the failing task above.
- Evidence output: `.bx-test-logs/` (never commit).

**Interfaces:**
- Consumes: Task 10 harness and a user-provided real server bypass/gateway/DNS service.
- Produces: an archived evidence directory and a written pass/fail matrix.

- [ ] **Step 1: Stop and ask for exact execution authorization**

Show the complete dry-run command and generated apply/cleanup plan. Do not continue until the user explicitly authorizes this real-device run.

- [ ] **Step 2: Run migration and successful update with watchdog**

Execute only the approved scenario. Require a rollback deadline, keep agent connectivity expectations explicit, and wait until the harness has fully restored or committed before ending the session.

- [ ] **Step 3: Run injected target failure and double failure**

Verify target failure restores the previous Core and green protection. Verify double failure retains the barrier and blocks public traffic. The harness must include a separately approved recovery command before testing double failure.

- [ ] **Step 4: Verify sleep/wake, Wi-Fi transition, reboot recovery, and Tailscale ordering**

Collect evidence for Tailscale-before-bx, Tailscale-after-bx, update-during-Tailscale-reconnect, sleep/wake, and network service change. Do not change Tailscale configuration; observe and report its overlay readiness.

- [ ] **Step 5: Publish only after evidence passes**

Run the complete automated suite again, tag the next release, wait for signed manifest/package verification, then test menu-driven update from N-1 to N. If any no-direct assertion fails, do not publish the no-leak update claim.

Commit only defect fixes and evidence-derived test improvements; never commit `.bx-test-logs`.
