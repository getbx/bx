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
- No `sudo`, `bx up`, `bx down`, `bx update`, `launchctl`, route, DNS, or network
  mutation command was run.

## Verification

- `go test ./internal/update ./internal/cli`
- `git diff --check`
