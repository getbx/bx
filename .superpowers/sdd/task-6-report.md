# Task 6 Report

## Summary

- Moved strict macOS package extraction into `internal/update` behind
  `ExtractMacOSPackage` and `MacOSPayload`.
- Added `PrepareMacOSInstall`, `PreparedInstall.Activate`,
  `PreparedInstall.Restore`, and `PreparedInstall.Commit`.
- Snapshot scope is limited to the installed CLI and selected `Bx.app`.
  Configuration and bx state paths are never traversed or copied.
- Staged replacements are created beside each destination. CLI activation uses
  rename replacement; app activation retains the previous bundle until commit.
- Restore is idempotent and reconstructs both destinations from the retained
  snapshot. Commit removes snapshot, staging, and adjacent transaction paths.
- Kept the CLI extraction wrapper for existing callers and moved parser tests
  from `internal/cli` to `internal/update`.

## TDD Evidence

1. Parser tests were added first and run with:

   `go test ./internal/update -run 'TestExtractMacOSPackage'`

   The expected red build failed because `ExtractMacOSPackage` and the package
   size limit did not exist in `internal/update`.

2. Transaction tests were added before installer implementation and run with:

   `go test ./internal/update -run 'TestPreparedInstall|TestPrepareMacOSInstall'`

   The expected red build failed because `PrepareMacOSInstall`, `InstallOptions`,
   and `PreparedInstall` did not exist.

3. After implementation, both focused suites passed. The required package tests
   and whitespace verification also passed.

## Preserved Parser Checks

- Fixed `bx-macos-<arch>/` package root.
- Canonical relative paths only; absolute, traversal, and backslash paths fail.
- Directories are allowed, while symlinks and other non-regular files fail.
- Duplicate selected files fail.
- Total selected payload size remains capped at 128 MiB.
- `bx`, `Bx.app/Contents/MacOS/BxMenu`, and
  `Bx.app/Contents/Info.plist` remain mandatory and non-empty.

## Filesystem Safety

- Tests use temporary directories and an injected `FileOps` fake that maps the
  validated production destinations into the temporary root.
- The fake injects a one-shot app destination rename failure to verify rollback
  after partial activation.
- The default production Darwin path no longer uses the path-based `FileOps`.
  It opens each destination and transaction parent by walking from `/` with
  `openat(O_DIRECTORY|O_NOFOLLOW)` and `fstat`, then retains those parent FDs for
  the transaction lifetime.
- Public transaction paths remain `/var/lib/bx/update/{snapshots,staging}`. The
  Darwin opener canonicalizes only macOS's trusted `/var` alias to
  `/private/var`; it never resolves arbitrary user-controlled symlinks.
- Transaction directories and staged entries use exclusive `mkdirat`/`openat`.
  Tree copy, mode and owner changes, recursive removal, and activation are all
  FD-relative. App and CLI activation use `renameat`/`renameatx_np` between
  retained parent FDs.
- Preparation records device/inode/type identities for destinations, snapshots,
  and staged entries. Activation reopens the absolute parent walk and verifies
  all recorded identities before the first rename, then verifies each activated
  name before ownership changes. Cleanup refuses to remove a substituted name.
- A staged user app stays owned by the privileged installer until it has been
  renamed and its identity rechecked. Recursive `fchown` runs through the
  retained app FD, with the app root changed last.
- No `sudo`, `bx up`, `bx down`, `bx update`, `launchctl`, route, DNS, or network
  mutation command was run.

## Verification

- `go test -count=1 ./internal/update ./internal/cli`
- `go test -race -count=1 ./internal/update ./internal/cli`
- `go test -count=1 ./...`
- `go vet ./...`
- `GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...`
- `GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/bx-update-linux.test ./internal/update`
- `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/bx-update-windows.test.exe ./internal/update`
- `git diff --check`

## Review follow-up

Independent review found unsafe transaction-directory cleanup, crash-residue
destruction, an aggregate package-size gap, and umask-dependent modes. Follow-up
tests now require:

- matching transaction IDs directly beneath
  `/var/lib/bx/update/{snapshots,staging}/`;
- no overlap among install destinations and every derived transaction path;
- an existing transaction is preserved and rejected before any mutation;
- ignored archive entries count toward the 128 MiB aggregate limit;
- explicit modes survive a restrictive write mask.

The current production CLI activation path remains a temporary compatibility
path. Task 7/8 will route activation through Guardian and `PreparedInstall`;
the overall guarded update feature is not complete until that wiring lands.

### Directory-FD follow-up evidence

The Darwin tampering and console-user tests were written before the secure
backend. The focused RED command was:

`go test -count=1 ./internal/update -run 'TestDarwinPreparedInstall|TestValidateInstallOptions'`

It failed to build with three `undefined: newDarwinPreparedInstall` errors from
the new ancestor and stage-substitution tests. After implementation, the same
command passed. The Darwin suite now covers:

- rejection of a symlinked `Applications` ancestor during preparation;
- rejection when the retained app parent is renamed and replaced before
  activation, before the CLI is changed;
- rejection when the staged app name is replaced before activation;
- activation, mode enforcement, idempotent restore, and commit cleanup using the
  real descriptor backend;
- refusal to recursively clean a substituted stage name;
- acceptance of the exact console user's UID/GID/home and rejection of another
  local user's UID, GID, or home.

The original independent reviewer re-reviewed the complete working-tree diff
and returned `APPROVED`: the prior HIGH symlink/TOCTOU finding and console-user
finding are closed, with no new Critical or Important defect.
