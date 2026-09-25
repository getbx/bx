# Task 1 Report

## Status

Completed. The context-safe install DNS entry points and Guardian DNS lifecycle contract are implemented with fake-runner coverage only. Review fix round 1 normalizes all externally serialized DNS states to the contract values.

## Modified Files

- `internal/install/install.go`
- `internal/install/install_test.go`
- `internal/guardian/dns.go`
- `internal/guardian/dns_test.go`
- `internal/guardian/localapi_test.go`
- `internal/guardian/types.go`
- `.superpowers/sdd/2026-08-04-macos-guardian-dns-lifecycle/task-1-report.md`

## Tests

- Red: `go test ./internal/install ./internal/guardian -run 'TestEnableDNSDarwinContext|TestGuardianDNSStatus'` failed because the required context entry points and Guardian DNS model did not exist.
- Green: `go test ./internal/install ./internal/guardian -run 'TestEnableDNSDarwinContext|TestGuardianDNSStatus'` passed.
- `go test ./internal/install ./internal/guardian -run 'DNS'` passed.
- `go build ./...` passed.
- `go vet ./...` passed.
- `go test ./...` passed.
- `GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...` passed.
- `git diff --check` passed.
- Review fix red: `go test ./internal/guardian -run '^TestLocalAPIStatusNormalizesDNSState$' -count=1` failed because zero and invalid `DNSState` values serialized as `""` and `"invalid"`.
- Review fix green: `go test ./internal/guardian -run '^TestLocalAPIStatusNormalizesDNSState$' -count=1` passed.
- `go test ./internal/install ./internal/guardian -run 'DNS|TestEnableDNSDarwinContext|TestGuardianDNSStatus' -count=1` passed.
- `go test ./internal/guardian -count=1` passed.

## Commit

`feat(guardian): 定义受管 DNS 生命周期接口` (this report and implementation are included in that commit).

Review fix round 1: `fix(guardian): 规范化 DNS 状态 JSON 零值` (this report update and the P1 fix are included in that commit).

## Risks

- Real macOS DNS commands were intentionally not executed. Cancellation and command sequencing are covered through fake runners, and the macOS target compiles successfully.
- The adapter and status contract are defined here; wiring DNS lifecycle transitions into Guardian Manager belongs to later lifecycle tasks.
- `Status.MarshalJSON` is the single external serialization boundary for this default, so existing status construction paths do not require per-call-site changes.
