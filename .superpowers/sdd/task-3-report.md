# Task 3 Report: Capture-Safe macOS Underlay Rebinding

## Status

DONE_WITH_CONCERNS

## Commit

`65a4d96 feat(macos): rebind underlay without releasing capture`

The commit contains only the seven Task 3 source and test files. This report is intentionally outside that source-only commit.

## Red Evidence

1. `go test ./internal/supervisor -run DarwinUnderlayPlan -count=1`
   failed before implementation with undefined `darwinUnderlayPlan`, `executeDarwinUnderlayPlan`, `UnderlaySnapshot`, and `darwinUnderlayCommand` symbols.
2. `go test ./internal/supervisor -run 'DarwinUnderlay|ParseDarwinRoute' -count=1`
   failed before the Darwin manager implementation with undefined `darwinUnderlayManager`, `darwinRouteSelection`, and `parseDarwinRouteSelection` symbols.
3. `go test ./internal/supervisor -run TestDarwinUnderlayPlanHonorsAnExplicitUnchangedGeneration -count=1`
   failed before the generation guard because it planned changes despite identical explicit generations.
4. `go test ./internal/supervisor -run TestDarwinUnderlayValidateCaptureRejectsWrongIPv6RejectGateway -count=1`
   failed before the IPv6 guard because a reject via `fe80::1` was accepted.

## Green Evidence

- `go test ./internal/supervisor -run 'Underlay|Capture' -count=1` - PASS
- `go test ./internal/supervisor -count=1` - PASS
- `go test -race ./internal/supervisor -run 'Underlay|Capture' -count=1` - PASS
- `GOOS=darwin GOARCH=arm64 go test ./internal/supervisor -run 'Underlay|Capture' -count=1` - PASS
- `GOOS=linux GOARCH=amd64 go build ./internal/supervisor` - PASS
- `go vet ./internal/supervisor` - PASS

All tests use injected command/route-selection fakes. No `route`, `networksetup`, `launchctl`, `bx`, Wi-Fi, or other live network operation was executed.

## Route Safety Assertions

- `darwinUnderlayPlan` is independent of the full `darwinRouteSpecs` builder and does not call it.
- Every planned primary command is `route -n change -net <validated-bypass> <new-ipv4-gateway>`.
- The missing-route fallback is an explicit fail-closed sequence for the same exact prefix only: `change`, `delete -net <prefix>` (missing tolerated), then `add -net <prefix> <new-gateway>`. While absent, traffic is captured by the existing TUN split-default instead of falling back to the physical default route.
- Input rejects malformed/non-canonical prefixes, non-IPv4 bypasses, `/0`, `0.0.0.0/1`, and `128.0.0.0/1`. IPv6 capture prefixes cannot enter an IPv4 bypass plan.
- Tests inspect every primary and fallback command and assert none contains `0.0.0.0/1`, `128.0.0.0/1`, `::/1`, or `8000::/1`.
- Capture validation queries both IPv4 halves (`1.1.1.1` and `129.1.1.1`) and requires the active bx utun. With IPv6 enabled, it queries both halves and requires the bx reject signature (`gateway ::1` plus `REJECT`). Failure returns `capture_missing`; it never invokes a full rehijack.
- The injected runner stops on the first non-recoverable route update failure and returns `underlay_rebind_failed`.

## Files

- `internal/supervisor/underlay.go`
- `internal/supervisor/darwin_underlay_plan.go`
- `internal/supervisor/darwin_underlay_plan_test.go`
- `internal/supervisor/underlay_darwin.go`
- `internal/supervisor/underlay_other.go`
- `internal/supervisor/platform_darwin.go`
- `internal/supervisor/platform_darwin_test.go`

## Concerns

- macOS `route` command syntax and route-output behavior remain fake-tested only, by explicit instruction. A user-authorized real macOS validation is still needed before claiming end-to-end network-transition behavior.
- This task intentionally exposes but does not yet wire the manager into the recovery coordinator. Task 5 must call `Observe -> ValidateCapture -> Rebind` and must not call `RehijackRoutes`.

## Fix Review

### Fixed Findings

1. `Rebind` now calls `ValidateCapture` before generating or executing a route plan. A failed validation returns `capture_missing` and the fake runner records no route command.
2. Darwin underlay validation now uses an injectable `(bool, error)` IPv6 capability probe. `net.InterfaceAddrs` errors propagate through `ipv6HostEnabledWithError`; an uncertain result returns `capture_missing` instead of skipping IPv6 capture checks.
3. Darwin interface-address conversion now unmapped Go's 16-byte IPv4-mapped `net.IP` before constructing a `netip.Prefix`. The regression test verifies ordinary `192.168.50.27/24` conversion.
4. The underlay planner no longer trusts supplied `Generation` text. It compares only canonical interface, gateway, and local CIDRs, so a forged matching generation cannot suppress a required rebind.
5. `capture_missing` and `underlay_rebind_failed` are allowlisted structured path-recovery codes. The Core HTTP response and Unix-socket client preserve those codes while redacting diagnostic detail.

### Additional Red Evidence

1. `go test ./internal/supervisor -run TestDarwinUnderlayRebindValidatesCaptureBeforeExecutingRoutes -count=1` failed because `Rebind` returned nil after route execution without capture validation.
2. `go test ./internal/supervisor -run TestDarwinUnderlayValidateCaptureFailsClosedWhenIPv6CapabilityErrors -count=1` failed before the error-bearing capability API existed.
3. `go test ./internal/supervisor -run TestDarwinPrefixFromIPNetUnmapsOrdinaryIPv4 -count=1` failed before the conversion helper existed.
4. `go test ./internal/supervisor -run TestDarwinUnderlayPlanDoesNotTrustForgedEqualGeneration -count=1` failed because matching caller-supplied generations suppressed the plan.
5. `go test ./internal/supervisor -run 'TestControlPathRecoveryMapsTypedErrorsToSafeCodes|TestPathRecoveryControlPreservesAllowlistedSafetyCodesWithoutDetail' -count=1` failed because both safety codes normalized to `recovery_failed`.

### Fix Verification

- `go test ./internal/supervisor -run 'Underlay|Capture|PathRecovery' -count=1` - PASS
- `go test -race ./internal/supervisor -run 'Underlay|Capture|PathRecovery' -count=1` - PASS
- `go test ./internal/supervisor -count=1` - PASS
- `GOOS=darwin GOARCH=arm64 go test ./internal/supervisor -count=1` - PASS
- `GOOS=linux GOARCH=amd64 go build ./internal/supervisor` - PASS
- `go vet ./internal/supervisor` - PASS

The capture-route invariant remains unchanged: the pure underlay plan and its fallbacks never contain `0.0.0.0/1`, `128.0.0.0/1`, `::/1`, or `8000::/1`. No live bx, route, network, launchctl, Wi-Fi, or DNS operation was executed.
