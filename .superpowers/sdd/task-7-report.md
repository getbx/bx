# Task 7 Report

## Summary

- Added `Manager.Update(context.Context, UpdateRequest)` and the root-only
  `POST /v1/update` Guardian LocalAPI operation, plus `Client.Update`.
- Preserved the Task 6 `update.PrepareMacOSInstall` transaction boundary and
  its `Activate`, `Restore`, and `Commit` API.
- Implemented the guarded activation order: verified preparation, prepared
  journal, maintenance barrier, old-Core stop, bypass reassertion, activation,
  target health gate, committed journal, barrier removal, receipt, and cleanup.
- Implemented rollback through the same complete Core health gate. A healthy
  previous Core produces `UpdateResult{RolledBack:true}`; double failure or
  uncertain process ownership retains the barrier and enters
  `needs_attention`.
- Kept one update operation externally serialized while moving package reads,
  extraction, snapshots, and recovery fingerprint preparation outside the
  lifecycle mutation lock. Lifecycle state is revalidated before routing
  mutation.
- Added startup recovery before LocalAPI serving. Recovery handles every Task 7
  persisted phase, reinstalls/reasserts the barrier before post-prepared Core
  work, and serves diagnostics while remaining fail-closed on recovery errors.
- Added versioned, root-only recovery descriptors with Guardian protocol,
  barrier handoff, destination metadata, and old/new artifact fingerprints.
- Kept errors, status, journals, receipts, API failures, and results free of
  package paths, client links, tokens, passwords, and injected raw errors.

## TDD Evidence

### Initial transaction and API RED

The transaction/API tests were written first. The focused Guardian test build
failed with undefined `UpdateRequest`, `Manager.Update`, and `Client.Update`.
After adding the exact request contract, Manager state machine, LocalAPI route,
and client, the focused transaction/API tests passed.

### Persisted-phase recovery RED

Fresh-Manager tests for `prepared`, `barrier_active`, `activating`,
`rolling_back`, and committed cleanup initially failed because `Recover`
ignored the update journal. Malformed-journal recovery also failed to retain a
barrier. Recovery was then wired ahead of desired-state startup and the focused
phase suite passed.

### On-disk recovery and daemon-order RED

Recovery descriptor and artifact tests initially failed to build because the
descriptor/recovered-install implementation did not exist. After the
descriptor and FD-relative recovery helpers were added, restore, substitution,
protocol compatibility, and cleanup tests passed.

The daemon-order test initially failed to build because
`startRecoveredDaemon` did not exist. It passed after startup was changed to
run journal recovery before constructing/serving the LocalAPI, including the
recovery-error diagnostics case.

### Self-review regression RED

Focused tests were added before each review fix. The combined review run
reported these expected failures:

- completed rollbacks returned `update_rolled_back` instead of a successful
  rolled-back result;
- `0755` transaction staging and `0644` packages were accepted;
- target cleanup inherited the expired health context;
- an incompatible prepared recovery did not call `Commit`;
- incomplete previous-artifact fingerprints and shared descriptor directories
  were accepted;
- a malformed descriptor beside a valid descriptor was silently skipped.

After minimal fixes, that focused suite passed. Additional RED/GREEN cycles
proved:

- malformed journal plus unavailable recovery metadata emits the IPv4/IPv6
  blocking route set instead of failing planner validation before install;
- uncertain target ownership cannot be crossed by restore or a previous-Core
  start;
- incompatible `prepared` recovery discards staging and continues the previous
  Core without a barrier;
- every snapshot artifact is fingerprint-verified before any destination is
  mutated;
- oversized descriptors are rejected;
- directory fingerprints frame file sizes so file content cannot impersonate
  following tree entries;
- unsafe persisted `last_error` values are rejected by `LoadTransaction` and
  cannot reach public status.

The final focused RED was:

`go test -count=1 ./internal/guardian -run TestManagerRecoveryRejectsSecretBearingJournal`

It failed with `secret-bearing journal was accepted`. After applying the same
`safeLastErrorPattern` validation on load as on save, and validating canonical
digest, snapshot path, and timestamps, the focused test and every persisted
phase test passed.

## Review-Fix Follow-Up (2026-07-18)

All eight Critical/Important findings were verified against the full source.
None were technically false, so there is no review pushback.

### 1. Proven recovery barrier and retry gate

RED: `TestManagerUnprovenRecoveryBarrierGatesLifecycleUntilRetry` showed a
failed route install being recorded as held/blocked, and
`TestDaemonRetriesRecoveryWhileServingDiagnostics` timed out because the
daemon discarded the recovery error.

GREEN: recovery records `barrierHeld` only after a complete successful
install. Failed startup recovery gates every lifecycle mutation, still serves
the root-only diagnostics API, and retries with bounded exponential backoff.
A successful retry clears the gate and resumes desired-state startup.

### 2. Preparation concurrency and unexpected Core exit

RED: a slow preparer held the lifecycle lock long enough for the bounded exit
context to expire, leaving stale protected status. A second regression test
showed failed preparation cleanup retaining the lifecycle token until
`Commit` returned.

GREEN: a separate update-operation token serializes updates, while package
verification, extraction, snapshots, and descriptor fingerprints run outside
the lifecycle token. The Manager reacquires and revalidates state, process
identity, runtime health, gateway, and handoff before mutation. Queued Core
exits wait for lifecycle ownership and receive a fresh bounded recovery
context; pre-network cleanup releases lifecycle ownership first.

### 3. Exact dual-stack block-only recovery

RED: `TestBlockOnlyRecoveryAlwaysPlansExactIPv4AndIPv6Routes` observed four
IPv4 routes instead of the required eight dual-stack routes.

GREEN: descriptor-less and malformed recovery always requests all four IPv4
`/2` rejects and all four IPv6 `/2` rejects, with exact reverse cleanup
commands asserted.

### 4. Explicit server-bypass ownership handoff

RED: the Guardian release planner and Core adoption helpers did not exist, so
the new ownership tests failed to build.

GREEN: Core treats only server-bypass routes as transferable, adopts an exact
preinstalled `/32` duplicate, records it as Core-owned, and removes it during
Core teardown. Guardian release-to-healthy-Core deletes only reject routes and
preserves the transferred bypass. Completed Down performs full removal;
update rollback, restart recovery, migration, and healthy update commit use
release-to-Core. Stateful route-table tests assert the add/adopt/delete
commands and ownership result.

### 5. Receipt-backed terminal cleanup

RED: receipt loading and directory-sync hooks were absent. The descriptor-gone
crash window failed recovery, and a matching receipt with remaining metadata
skipped pending cleanup.

GREEN: `LoadReceipt` validates terminal outcome, identifiers, versions,
canonical SHA-256, and completion time. Journal unlink fsyncs its parent
directory. A matching terminal receipt resumes `Commit` when recovery metadata
remains, or clears the journal without reactivation only when staging deletion
proves all earlier cleanup completed. Snapshot and artifact cleanup now precede
staging deletion.

### 6. Resumable atomic rename states

RED: real temporary-filesystem cases for app-old-moved-aside, app-new-moved-to-
discard, restore-promoted-with-residue, CLI restore staging, and cleanup
rename residue returned `update_restore_failed` or `update_cleanup_failed`.

GREEN: recovered activation/rollback classifies every destination and residue
by the persisted old/new fingerprint, resumes the corresponding rename, and
is idempotent when the destination is already old. Cleanup resumes an exact
`.guardian-cleanup` residue. Wrong fingerprints and duplicate ambiguous
residue are still refused as substitution.

### 7. Durable barrier-install intent

RED: the crash-window test could not express an install begun before
`barrier_active` because the journal had no intent field.

GREEN: `barrier_install_intent` is durably persisted in `prepared` before the
first route command and cleared by the durable `barrier_active` transition.
Recovery of `prepared + intent` reconciles the barrier, restores the previous
healthy Core without target activation, records rollback, transfers bypass
ownership, and completes receipt/journal cleanup.

### 8. Fresh update-boundary routing inputs

RED: the DHCP/runtime-change test had no gateway provider and continued using
daemon-start snapshots.

GREEN: after preparation and lifecycle revalidation, Manager obtains a fresh
complete runtime/health handoff for the exact current PID and rediscovers the
default gateway through an injected provider. Caller routing remains ignored;
the refreshed runtime bypass and gateway are bound into recovery metadata
before journaling or route mutation. Gateway failure cleans preparation before
either boundary.

### Minor last-error regression

RED: successful phase persistence converted an empty `last_error` to
`update_failed`. GREEN: `safeUpdateCode("")` now remains empty.

## Coverage

- Exact success and failure ordering for prepared journal, barrier install, old
  stop, bypass reassert, activation, target start, target health, receipt, and
  barrier cleanup.
- Healthy rollback, double health failure, unproven target cleanup, and
  uncertain launch ownership.
- Package path containment, sibling-prefix escape, wrong transaction, symlinked
  path components, directories, modes, ownership, package size, and SHA-256
  recomputation before barrier installation.
- Live runtime-derived gateway/bypass and rejection of caller-supplied unknown
  routing fields.
- Guardian protocol metadata default/current/newer behavior and prior recovery
  descriptor compatibility.
- Crash recovery for all phases required by the brief, committed cleanup
  without reactivation, malformed/ambiguous metadata, and fail-closed errors.
- Root-only strict JSON API, body limits, mutation lifetime, generic error
  responses, client result decoding, and no secret reflection.
- Daemon recovery-before-serve ordering on both success and recovery error.

## Changed Files

- `internal/guardian/barrier*.go` and `barrier_test.go`: dual-stack block-only
  recovery and release-to-Core planning/execution.
- `internal/guardian/daemon.go` and `daemon_test.go`: startup recovery retry
  while diagnostics remain available.
- `internal/guardian/manager.go` and `manager_test.go`: recovery gate, update
  operation token, fresh exit-recovery budget, gateway provider, and healthy
  bypass handoff.
- `internal/guardian/store.go`, `store_test.go`, and `types.go`: validated
  receipt loading, durable journal unlink, and barrier-install intent.
- `internal/guardian/update.go` and `update_test.go`: boundary revalidation,
  intent/receipt recovery, fingerprinted rename reconciliation, and all focused
  review regressions.
- `internal/supervisor/darwin_routes.go`, `platform_darwin.go`, and
  `darwin_routes_test.go`: Core server-bypass adoption and ownership cleanup.
- `internal/update/macos_install.go`: cleanup ordering that removes staging
  last, making a missing descriptor a durable terminal-cleanup signal.

No CLI update code was modified.

## Verification

- `gofmt` on every changed Go file
- `go test -count=1 ./internal/guardian ./internal/update ./internal/supervisor`
- `go test -race -count=1 ./internal/guardian ./internal/supervisor`
- `GOOS=darwin GOARCH=arm64 go build ./...`
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -exec=/usr/bin/true -count=1 ./internal/guardian ./internal/update ./internal/supervisor`
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -exec=/usr/bin/true -count=1 ./internal/guardian ./internal/update ./internal/supervisor`
- `go vet ./...`
- `git diff --check`

All commands passed. Tests use fakes and temporary directories only.

## Self-review and Concerns

Self-review replayed all eight scenarios: zero-route recovery failure, slow
prepare plus Core exit, descriptor-less IPv6 blocking, server-bypass transfer,
receipt-before-cleanup crash, every atomic rename state, route-install-before-
phase crash, and DHCP/runtime changes. It also rechecked one-update
serialization, fail-closed maintenance, root-only LocalAPI behavior, public
error redaction, protocol compatibility, terminal Guardian non-restart, and
full Down ownership removal.

No live network or system mutation was performed. In particular, no `sudo`,
`bx up/down/update/reconnect`, `launchctl`, `route`, or `networksetup` command
was run. Real route precedence and kill/power-loss behavior remain part of the
explicitly authorized macOS acceptance matrix in the design document and were
intentionally not exercised here.
