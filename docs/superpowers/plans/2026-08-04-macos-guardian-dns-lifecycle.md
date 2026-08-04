# macOS Guardian DNS Lifecycle Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Make macOS Guardian own system DNS for the complete bx lifecycle, so bx up only reaches green Protected after DNS is verified as managed and BxMenu turns yellow whenever DNS is unmanaged or unknown.

**Architecture:** Add a context-aware DNSManager port to Guardian and a production adapter over internal/install. Gate every Core activation at the existing acceptHealthy convergence point, explicitly gate in-place path recovery, and make every Protected status commit require a cached managed DNS state. Publish that authoritative state through Guardian -> CLI -> BxMenu instead of running a second bx dns status subprocess.

**Tech Stack:** Go 1.26, macOS networksetup, Guardian HTTP-over-Unix LocalAPI, Swift 5.9/AppKit, launchd.

## Global Constraints

- sudo bx up on macOS must manage the active network service DNS and verify its only configured server is 127.0.0.1.
- Guardian must not publish ProtectionProtected unless DNS state is managed.
- DNS takeover or verification failure must return a non-zero mutation result, publish needs_attention or blocked, and never release a held fail-closed barrier.
- Startup recovery, in-place network recovery, reconnect, update activation, rollback, and unexpected Core restart must all re-verify DNS.
- bx down must restore DNS while the maintenance barrier is held; restoration failure must not commit Off.
- Status exposes only dns_state, dns_managed, and optional dns_service; original resolver values remain root-only.
- BxMenu uses Guardian-derived status only and must not execute bx dns status.
- Linux and Windows behavior remain unchanged.
- Tests must not invoke networksetup or modify the development machine's network.
- Follow TDD for every behavior: red test, verify red, minimal implementation, verify green, commit.

---

## Planned File Structure

~~~text
internal/install/install.go                    context-aware DNS inspect/enable
internal/install/install_test.go               canceled and verified DNS tests
internal/guardian/dns.go                       DNSManager port and system adapter
internal/guardian/dns_test.go                  adapter and redaction tests
internal/guardian/types.go                     authoritative DNS status fields
internal/guardian/manager.go                   activation and shutdown DNS gate
internal/guardian/manager_test.go              Up/Down/order/failure tests
internal/guardian/path_recovery.go             in-place recovery DNS verification
internal/guardian/path_recovery_test.go        recovery contract tests
internal/guardian/update.go                    guarded update status commits
internal/guardian/update_test.go               update/rollback sequencing tests
internal/guardian/daemon.go                    production adapter injection
internal/cli/cli.go                            status, Doctor, and summary
internal/cli/cli_test.go                       CLI contract tests
apps/macos/BxMenu/Sources/BxMenu/main.swift
apps/macos/BxMenu/Sources/BxMenu/StatusPresentation.swift
apps/macos/BxMenu/Tests/StatusPresentationTests.swift
README.md
~~~

### Task 1: Context-safe DNS adapter and status contract

**Files:**
- Modify: internal/install/install.go
- Modify: internal/install/install_test.go
- Create: internal/guardian/dns.go
- Create: internal/guardian/dns_test.go
- Modify: internal/guardian/types.go

**Interfaces:**
- Produces: install.InspectDNSContext(ctx, service).
- Produces: install.EnableDNSContext(ctx, service).
- Produces: DNSState values unknown, managed, unmanaged.
- Produces: DNSManager.EnsureManaged, Inspect, Restore.
- Produces: Status.DNSState, Status.DNSManaged, Status.DNSService.

- [ ] **Step 1: Write failing context tests**

Add a canceled-context test around a scripted dnsCommandRunner:

~~~go
func TestEnableDNSDarwinContextStopsBeforeCommandsWhenCanceled(t *testing.T) {
    ctx, cancel := context.WithCancel(context.Background())
    cancel()
    runner := &scriptedDNSCommandRunner{
        combinedOutput: func(context.Context, string, ...string) ([]byte, error) {
            t.Fatal("canceled enable executed a command")
            return nil, nil
        },
    }
    _, err := enableDNSDarwinContextWithRunner(ctx, runner, statePath, "Wi-Fi")
    if !errors.Is(err, context.Canceled) {
        t.Fatalf("error = %v, want context canceled", err)
    }
}
~~~

- [ ] **Step 2: Write failing Guardian model tests**

Assert managed/unmanaged mapping and that marshaled Guardian status cannot contain resolver addresses or a servers field.

~~~go
func TestGuardianDNSStatusJSONOmitsResolverAddresses(t *testing.T) {
    status := Status{DNSState: DNSManaged, DNSManaged: true, DNSService: "Wi-Fi"}
    data, err := json.Marshal(status)
    if err != nil { t.Fatal(err) }
    for _, forbidden := range []string{"1.1.1.1", "8.8.8.8", "servers"} {
        if bytes.Contains(data, []byte(forbidden)) {
            t.Fatalf("status leaked %q: %s", forbidden, data)
        }
    }
}
~~~

- [ ] **Step 3: Run tests and confirm red**

~~~bash
go test ./internal/install ./internal/guardian -run 'TestEnableDNSDarwinContext|TestGuardianDNSStatus'
~~~

Expected: FAIL because the context entry points and Guardian DNS model do not exist.

- [ ] **Step 4: Implement context-aware install functions**

Make non-context APIs delegate to new context APIs. Thread ctx through service discovery, resolver reads, setdnsservers, cache flush, and final inspection. Preserve the existing root-only state file and idempotence.

~~~go
func InspectDNS(service string) (DNSStatus, error) {
    return InspectDNSContext(context.Background(), service)
}

func InspectDNSContext(ctx context.Context, service string) (DNSStatus, error) {
    return inspectDNSContextWithRunner(ctx, execDNSCommandRunner{}, service)
}

func EnableDNS(service string) (DNSStatus, error) {
    return EnableDNSContext(context.Background(), service)
}
~~~

- [ ] **Step 5: Implement Guardian port and status fields**

Use these exact package types:

~~~go
type DNSState string

const (
    DNSUnknown   DNSState = "unknown"
    DNSManaged   DNSState = "managed"
    DNSUnmanaged DNSState = "unmanaged"
)

type DNSStatus struct {
    State   DNSState
    Service string
}

type DNSManager interface {
    EnsureManaged(context.Context) (DNSStatus, error)
    Inspect(context.Context) (DNSStatus, error)
    Restore(context.Context) (DNSStatus, error)
}
~~~

The production adapter maps install.DNSStatus.Enabled and never copies Servers. Add dns_state, dns_managed, and optional dns_service to guardian.Status.

- [ ] **Step 6: Verify and commit**

~~~bash
go test ./internal/install ./internal/guardian -run 'DNS'
git diff --check
git add internal/install/install.go internal/install/install_test.go internal/guardian/dns.go internal/guardian/dns_test.go internal/guardian/types.go
git commit -m "feat(guardian): 定义受管 DNS 生命周期接口"
~~~

### Task 2: Gate Up, adoption, restart, migration, and Down

**Files:**
- Modify: internal/guardian/manager.go
- Modify: internal/guardian/manager_test.go
- Modify: internal/guardian/migration_test.go
- Modify: internal/guardian/daemon.go
- Modify: internal/guardian/daemon_test.go

**Interfaces:**
- Consumes: Task 1 DNSManager and DNSStatus.
- Produces: required ManagerOptions.DNS.
- Produces: ensureDNSManaged(ctx, runtimeState).
- Produces: setProtectedStatus(phase, pid, version, lastError).

- [ ] **Step 1: Add fake DNS manager and failing Up tests**

Extend managerTestEnv with a fake that supports queued Ensure, Inspect, and Restore results. It should record events only when record is true so unrelated order tests remain stable.

~~~go
func TestManagerUpVerifiesDNSBeforeProtected(t *testing.T) {
    env := newManagerTestEnv(t)
    env.dns.record = true
    if err := env.manager.Up(context.Background()); err != nil { t.Fatal(err) }
    want := []string{"desired.on", "core.start", "dns.ensure", "dns.inspect"}
    if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
        t.Fatalf("events = %#v, want %#v", got, want)
    }
    status := env.manager.Status()
    if status.Protection != ProtectionProtected || status.DNSState != DNSManaged {
        t.Fatalf("status = %+v", status)
    }
}

func TestManagerUpDNSFailureCannotClaimProtected(t *testing.T) {
    env := newManagerTestEnv(t)
    env.dns.ensureErr = errors.New("resolver change failed")
    if err := env.manager.Up(context.Background()); err == nil { t.Fatal("Up succeeded") }
    status := env.manager.Status()
    if status.Protection == ProtectionProtected || status.LastError != "dns_takeover_failed" {
        t.Fatalf("status = %+v", status)
    }
}
~~~

Also test Inspect returning unmanaged and adoption of an existing healthy Core.

- [ ] **Step 2: Confirm activation tests red**

~~~bash
go test ./internal/guardian -run 'TestManager(Up.*DNS|Adopts.*DNS)'
~~~

Expected: FAIL because Manager has no DNS dependency or gate.

- [ ] **Step 3: Implement shared activation gate**

Require ManagerOptions.DNS and cache only non-secret DNSStatus. In acceptHealthy, after recording the healthy Core but before barrier release or Protected status, call EnsureManaged then Inspect.

On failure: retain a proven barrier or attempt installBarrierForRecovery; publish dns_takeover_failed or dns_verification_failed; return without releasing the barrier.

Create setProtectedStatus and replace direct Protected literals in manager.go. The helper must reject any cached state other than managed.

- [ ] **Step 4: Write failing Down tests**

~~~go
func TestManagerDownRestoresDNSBeforeBarrierRelease(t *testing.T) {
    env := newProtectedManagerTestEnv(t)
    env.dns.record = true
    env.events.reset()
    if err := env.manager.Down(context.Background()); err != nil { t.Fatal(err) }
    want := []string{"barrier.install", "core.stop", "dns.restore", "desired.off", "barrier.remove"}
    if got := env.events.snapshot(); !reflect.DeepEqual(got, want) {
        t.Fatalf("events = %#v, want %#v", got, want)
    }
    status := env.manager.Status()
    if status.Protection != ProtectionOff || status.DNSState != DNSUnmanaged {
        t.Fatalf("status = %+v", status)
    }
}
~~~

Retain the existing restoration-failure recovery guarantee, but require the restarted Core to re-run Ensure and Inspect before it can be Protected.

- [ ] **Step 5: Implement Down and production injection**

Replace the implicit NetworkRestorer dependency with DNSManager.Restore. Even if desired is already off and no Core exists, restore stale managed DNS before returning Off. Inject systemDNSManager in RunDaemon and pass fakes from every Manager test constructor.

- [ ] **Step 6: Verify and commit**

~~~bash
go test ./internal/guardian -run 'TestManager|TestSystemDNS'
go test ./internal/guardian
git diff --check
git add internal/guardian/manager.go internal/guardian/manager_test.go internal/guardian/migration_test.go internal/guardian/daemon.go internal/guardian/daemon_test.go
git commit -m "fix(guardian): 将 DNS 纳入保护生命周期"
~~~

### Task 3: Re-verify DNS during path recovery and updates

**Files:**
- Modify: internal/guardian/path_recovery.go
- Modify: internal/guardian/path_recovery_test.go
- Modify: internal/guardian/update.go
- Modify: internal/guardian/update_test.go

**Interfaces:**
- Consumes: Task 2 ensureDNSManaged and setProtectedStatus.
- Produces: stable recovery codes dns_takeover_failed and dns_verification_failed.
- Preserves: update rollback and barrier semantics.

- [ ] **Step 1: Write failing in-place recovery tests**

After a successful Core RecoverPath, require DNS verification before a succeeded Guardian snapshot.

~~~go
func TestPathRecoveryDNSFailureIsNotPublishedAsSuccess(t *testing.T) {
    env := newPathRecoveryTestEnv(t)
    env.dns.inspectStatus = DNSStatus{State: DNSUnmanaged, Service: "Wi-Fi"}
    snapshot := runPathRecoveryAndWait(t, env.manager, RecoveryRequest{
        Reason: "underlay_changed", Generation: "wifi-b",
    })
    if snapshot.State != "failed" || snapshot.ErrorCode != "dns_verification_failed" {
        t.Fatalf("snapshot = %+v", snapshot)
    }
    if env.manager.Status().Protection == ProtectionProtected {
        t.Fatalf("status = %+v", env.manager.Status())
    }
}
~~~

- [ ] **Step 2: Confirm recovery red**

~~~bash
go test ./internal/guardian -run 'TestPathRecovery.*DNS'
~~~

Expected: FAIL because in-place recovery bypasses Guardian DNS verification.

- [ ] **Step 3: Add in-place DNS gate**

In executePathRecovery, after Core success and before completedPathRecoverySnapshot, call ensureDNSManaged with m.runtime. Convert its typed error to a stable path-recovery code and allow the existing retry loop to retry the complete transaction.

- [ ] **Step 4: Write failing update tests**

Assert successful update ordering:

~~~text
barrier.install -> old core stop -> new core start -> dns.ensure -> dns.inspect -> barrier.release -> Protected
~~~

Add a queued fake response where new-Core verification fails once and old-Core rollback verification succeeds. Assert rollback returns an update error and only reports Protected after old-Core DNS is managed. Add a second case where both verification attempts fail and the barrier remains held.

- [ ] **Step 5: Guard every update Protected commit**

Replace all production ProtectionProtected literals in update.go with setProtectedStatus, including live commit, rollback, recovered rollback, committed recovery, and terminal recovery.

- [ ] **Step 6: Verify and commit**

~~~bash
go test ./internal/guardian -run 'TestPathRecovery|TestManagerUpdate|TestManagerRecovery'
go test ./internal/guardian
git diff --check
git add internal/guardian/path_recovery.go internal/guardian/path_recovery_test.go internal/guardian/update.go internal/guardian/update_test.go
git commit -m "fix(guardian): 恢复和更新前复验 DNS"
~~~

### Task 4: Publish authoritative DNS through CLI and Doctor

**Files:**
- Modify: internal/cli/cli.go
- Modify: internal/cli/cli_test.go
- Modify: internal/cli/guardian.go
- Modify: internal/cli/macos_lifecycle_test.go

**Interfaces:**
- Consumes: Guardian DNS state fields.
- Produces: matching clientStatusReport JSON fields.
- Produces: guardianDNSDoctorCheck(status).

- [ ] **Step 1: Write failing status tests**

~~~go
func TestStatusReportIncludesGuardianDNSState(t *testing.T) {
    rep := assembleClientStatusReport(stats.Report{TunnelHealthy: true}, guardian.Status{
        Protection: guardian.ProtectionProtected,
        DNSState: guardian.DNSManaged, DNSManaged: true, DNSService: "Wi-Fi",
    })
    data, _ := json.Marshal(rep)
    for _, want := range []string{
        "\"dns_state\":\"managed\"",
        "\"dns_managed\":true",
        "\"dns_service\":\"Wi-Fi\"",
    } {
        if !bytes.Contains(data, []byte(want)) { t.Fatalf("missing %s: %s", want, data) }
    }
}
~~~

Add human output assertions for DNS Wi-Fi managed and prove Darwin Protected is defensively downgraded when DNS is unmanaged or unknown.

- [ ] **Step 2: Confirm status red**

~~~bash
go test ./internal/cli -run 'Test(StatusReportIncludesGuardianDNS|Status.*DNS)'
~~~

Expected: FAIL because client status lacks DNS fields.

- [ ] **Step 3: Implement status propagation**

Add DNSState, DNSManaged, and DNSService to clientStatusReport and copy them in assembleClientStatusReportWithCore. On Darwin, downgrade an impossible Protected plus non-managed DNS combination to Needs Attention. Render a DNS line in human status and macOS up summary.

- [ ] **Step 4: Write and implement Doctor tests**

~~~go
func TestGuardianDNSDoctorCheck(t *testing.T) {
    managed := guardianDNSDoctorCheck(guardian.Status{
        DNSState: guardian.DNSManaged, DNSManaged: true, DNSService: "Wi-Fi",
    })
    if managed.Status != "ok" { t.Fatalf("managed = %+v", managed) }
    unmanaged := guardianDNSDoctorCheck(guardian.Status{DNSState: guardian.DNSUnmanaged})
    if unmanaged.Status != "fail" || unmanaged.Hint == "" {
        t.Fatalf("unmanaged = %+v", unmanaged)
    }
}
~~~

Append this check beside network_recovery in macOS Doctor. Do not call install.InspectDNS when Guardian status is available.

- [ ] **Step 5: Verify and commit**

~~~bash
go test ./internal/cli
git diff --check
git add internal/cli/cli.go internal/cli/cli_test.go internal/cli/guardian.go internal/cli/macos_lifecycle_test.go
git commit -m "fix(cli): 统一展示 Guardian DNS 保护状态"
~~~

### Task 5: Make BxMenu yellow unless DNS is managed

**Files:**
- Modify: apps/macos/BxMenu/Sources/BxMenu/main.swift
- Modify: apps/macos/BxMenu/Sources/BxMenu/StatusPresentation.swift
- Modify: apps/macos/BxMenu/Tests/StatusPresentationTests.swift

**Interfaces:**
- Consumes: bx status JSON dns_state, dns_managed, dns_service.
- Produces: pure dnsPresentation(state:managed:service:).

- [ ] **Step 1: Write failing Swift tests**

~~~swift
let managed = dnsPresentation(state: "managed", managed: true, service: "Wi-Fi")
expect(managed.allowsProtected, "managed allows Protected")
expect(managed.label == "Wi-Fi managed", "managed label")

let unmanaged = dnsPresentation(state: "unmanaged", managed: false, service: nil)
expect(!unmanaged.allowsProtected, "unmanaged is attention")
expect(unmanaged.label == "Not managed", "unmanaged label")

let unknown = dnsPresentation(state: nil, managed: false, service: nil)
expect(!unknown.allowsProtected, "missing old status is attention")
expect(unknown.label == "Status unavailable", "unknown label")
~~~

- [ ] **Step 2: Confirm Swift red**

~~~bash
scripts/test-macos-menu.sh
~~~

Expected: FAIL because dnsPresentation does not exist.

- [ ] **Step 3: Implement Guardian-only menu state**

Decode DNS fields in BxReport. loadState may return connected only when the tunnel is healthy, Guardian protection is Protected, and dnsPresentation allows Protected. Return warning DNS not managed for unmanaged and DNS status unavailable for unknown/missing.

Delete loadDNSStatus and its line parser. The warning state already drives the yellow shield and Needs Attention header.

- [ ] **Step 4: Add source regression guard**

Add an automated assertion that main.swift contains neither a runBx dns status invocation nor loadDNSStatus. This prevents a second status source from returning.

- [ ] **Step 5: Verify and commit**

~~~bash
scripts/test-macos-menu.sh
swift build --package-path apps/macos/BxMenu
git diff --check
git add apps/macos/BxMenu/Sources/BxMenu/main.swift apps/macos/BxMenu/Sources/BxMenu/StatusPresentation.swift apps/macos/BxMenu/Tests/StatusPresentationTests.swift
git commit -m "fix(macos): DNS 未接管时菜单显示黄色"
~~~

### Task 6: Documentation and non-invasive verification

**Files:**
- Modify: README.md
- Modify: docs/superpowers/plans/2026-08-04-macos-guardian-dns-lifecycle.md only to check completed boxes during execution.

**Interfaces:**
- Documents exact green/yellow semantics and automatic bx up behavior.
- Verifies all build targets without starting bx.

- [ ] **Step 1: Update user-facing language**

Document:

- bx up automatically manages macOS DNS; bx dns on is repair tooling, not a normal startup step.
- Green means DNS is verified managed in addition to tunnel and route protection.
- Unmanaged or unknown DNS is yellow Needs Attention and bx up returns an error.
- Recovery, reconnect, and update re-verify DNS before returning green.

- [ ] **Step 2: Run race tests**

~~~bash
go test -race ./internal/guardian ./internal/cli
~~~

- [ ] **Step 3: Run complete Go verification**

~~~bash
go test ./... -count=1
go vet ./...
go build ./...
~~~

- [ ] **Step 4: Run cross-platform builds**

~~~bash
GOOS=darwin GOARCH=arm64 go build -o /tmp/bx-dns-darwin-arm64 .
GOOS=linux GOARCH=amd64 go build -o /tmp/bx-dns-linux-amd64 .
GOOS=windows GOARCH=amd64 go build -o /tmp/bx-dns-windows-amd64.exe .
~~~

- [ ] **Step 5: Run menu and static checks**

~~~bash
scripts/test-macos-menu.sh
swift build --package-path apps/macos/BxMenu
bash -n scripts/darwin-testkit.sh
git diff --check
~~~

- [ ] **Step 6: Commit documentation**

~~~bash
git add README.md docs/superpowers/plans/2026-08-04-macos-guardian-dns-lifecycle.md
git commit -m "docs(macos): 说明 DNS 完整保护语义"
~~~

- [ ] **Step 7: Stop before real network testing**

Report automated results and give the user a separate smoke-test command. Do not run bx up, bx down, bx reconnect, bx dns on/off, networksetup -setdnsservers, route, or ifconfig during implementation.
