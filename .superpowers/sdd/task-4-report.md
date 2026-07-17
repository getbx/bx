# Task 4 Report: Guardian Core Manager and LocalAPI

## Status

DONE_WITH_CONCERNS.

## Implementation

- Added the serialized Guardian manager with idempotent `Up`/`Down`, verified adoption, fail-closed down ordering, restoration recovery, and one bounded crash restart.
- Added `ExecCoreRunner` with argument-vector-only child launch, root-only process state, Darwin PID/UID/executable inspection, installed-inode verification, graceful stop, and adopted-process monitoring.
- Added `GET /v1/status`, root-only `POST /v1/up` and `POST /v1/down`, a Unix HTTP client, Darwin `LOCAL_PEERCRED`, and an owner-checked `/var/run/bx-guard.sock` daemon.
- Added the hidden root-only `bx guardian --config ... --listen-dns ...` command. Tests inject its daemon runner and do not execute live system actions.

## Red Evidence

Initial manager/process tests failed before production implementation:

```text
$ go test ./internal/guardian
internal/guardian/manager_test.go:33:26: undefined: ProtectionProtected
internal/guardian/manager_test.go:188:12: undefined: Manager
internal/guardian/manager_test.go:275:15: undefined: Process
FAIL github.com/getbx/bx/internal/guardian [build failed]
```

LocalAPI/CLI tests then failed on the intentionally absent interfaces:

```text
$ go test -count=1 ./internal/guardian ./internal/cli
internal/guardian/localapi_test.go:18:2: undefined: NewLocalAPI
internal/guardian/localapi_test.go:78:17: undefined: StartDaemon
internal/guardian/localapi_test.go:91:12: undefined: NewClient
internal/cli/guardian_test.go:14:13: undefined: guardianCommandWithDeps
FAIL github.com/getbx/bx/internal/guardian [build failed]
FAIL github.com/getbx/bx/internal/cli [build failed]
```

Self-review produced two behavioral red cycles:

```text
$ go test -count=1 ./internal/guardian -run TestLocalAPIMutationOutlivesClientCancellation
--- FAIL: TestLocalAPIMutationOutlivesClientCancellation
    accepted mutation inherited client cancellation: context canceled

$ go test -count=1 ./internal/guardian -run TestExecCoreRunnerAdoptedWatcherOutlivesInspectionContext
--- FAIL: TestExecCoreRunnerAdoptedWatcherOutlivesInspectionContext
    adopted Core exit was not observed after inspection context ended
```

## Green Evidence

Fresh verification after the final watcher fix:

```text
$ go test -count=1 ./internal/guardian ./internal/cli
ok github.com/getbx/bx/internal/guardian 0.813s
ok github.com/getbx/bx/internal/cli 0.638s

$ go test -race -count=1 ./internal/guardian ./internal/cli
ok github.com/getbx/bx/internal/guardian 1.802s
ok github.com/getbx/bx/internal/cli 1.739s

$ go vet ./internal/guardian ./internal/cli
(no output; exit 0)

$ GOOS=darwin GOARCH=arm64 go build ./...
(no output; exit 0)
```

The full repository suite passed immediately before the final adopted-watcher lifetime fix:

```text
$ go test -count=1 ./...
all packages passed; exit 0
```

## Self-Review

- Manager mutations use one mutex; read-only status uses a separate lock and remains available during lifecycle work.
- Adoption requires a live recorded PID, effective UID 0, the installed executable path/inode identity, and a matching healthy `/v0/runtime` PID/version. Verification failure never calls `Stop` or starts a second Core.
- `Down` orders barrier install, Core stop/route teardown, DNS restoration, desired-off persistence, then barrier removal. Restore plus recovery failure keeps desired On and the barrier held as `needs_attention`.
- Unexpected child exit marks attention, best-effort installs the barrier, performs one timeout-bounded restart through the complete health gate, and removes the barrier only after health succeeds.
- Accepted API mutations detach from client/SIGTERM cancellation and have a one-minute lifecycle bound. Guardian shutdown closes only its API socket; it does not call `Down` or restore direct networking.
- The daemon refuses non-root mutation peers, non-socket path replacement, active socket replacement, unsafe socket directories, and stale sockets not owned by the configured root UID.
- No `sudo`, `bx`, route, DNS, `networksetup`, `launchctl`, or live network mutation command was executed. Tests used fakes, temporary files, and local Unix sockets only.

## Concerns

- Per the stop request, the full `go test ./...` suite was not repeated after the final narrow watcher-lifetime change. The affected Guardian tests, Guardian/CLI race tests, vet, and full Darwin build all passed after that change.
- Real root-owned socket, process adoption, route barrier, DNS restoration, and SIGTERM behavior still require the explicitly authorized macOS integration pass. Task 5 remains responsible for launchd installation and legacy migration.
- Unexpected-exit recovery remains best-effort and does not claim a first-packet absolute guarantee, as required by the approved design.
