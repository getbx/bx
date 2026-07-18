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
- Serialized update with `Up`, `Down`, migration, and other updates using the
  existing Manager mutation lock.
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

- `internal/guardian/update.go`: update transaction, validation, macOS
  preparation adapter, recovery descriptor, rollback, and crash recovery.
- `internal/guardian/update_test.go`: Task 7 transaction, recovery, protocol,
  filesystem, serialization, deadline, and secret-safety tests.
- `internal/guardian/manager.go`: update dependencies and recovery integration.
- `internal/guardian/daemon.go`, `daemon_test.go`: recovery-before-serve wiring.
- `internal/guardian/localapi.go`, `localapi_test.go`: root-only `/v1/update`.
- `internal/guardian/client.go`: Guardian update client.
- `internal/guardian/barrier.go`: internal recovery-only block-without-bypass
  planner mode; normal update/migration plans still require exact `/32` bypass.
- `internal/guardian/store.go`: reject unsafe persisted transaction errors.

`internal/cli/update.go` and `internal/update` were not modified.

## Verification

- `gofmt` on every changed Go file.
- `go test -count=1 ./internal/guardian ./internal/update`
- `go test -race -count=1 ./internal/guardian`
- `go vet ./internal/guardian ./internal/update`
- `GOOS=darwin GOARCH=arm64 go build ./...`
- `git diff --check`

All commands passed. Tests use fakes and temporary directories only.

## Self-review and Concerns

Self-review specifically checked event ordering, crash windows represented by
persisted phases, barrier retention, process ownership uncertainty, deadline
cleanup, recovery path substitution, descriptor ambiguity, fingerprint
framing, journal secret safety, and API redaction. The review findings above
were converted to RED tests and fixed.

No live network or system mutation was performed. In particular, no `sudo`,
`bx up/down/update/reconnect`, `launchctl`, `route`, or `networksetup` command
was run. Real route precedence and kill/power-loss behavior remain part of the
explicitly authorized macOS acceptance matrix in the design document and were
intentionally not exercised here.
