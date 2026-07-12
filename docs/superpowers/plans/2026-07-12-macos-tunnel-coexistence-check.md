# macOS Tunnel Coexistence Check Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `bx check` and `bx leak-check` show other VPN/tunnel/proxy paths on macOS without adding new commands or changing network state.

**Architecture:** Extend the existing `internal/cli` platform check layer with small read-only detectors. Each detector returns the existing `checkReport` shape, and risk escalation stays centralized in `applyPlatformRisk`.

**Tech Stack:** Go, macOS read-only shell commands (`pgrep`, `ifconfig`, `netstat`, `scutil`), existing `urfave/cli` command surface.

## Global Constraints

- No new user command.
- Read-only detection only; do not change DNS, routes, services, proxies, or Network Extension state.
- False positives should be `info`, not `warn`.
- Raise `leak-check` risk only for `warn` or `fail` tunnel/overlay checks.
- Keep Tailscale as the only stronger coexistence adapter in this phase.

---

### Task 1: Add detector helpers and macOS checks

**Files:**
- Modify: `internal/cli/platform_check_darwin.go`
- Test: `internal/cli/platform_check_darwin_test.go`

**Interfaces:**
- Consumes: `checkReport`, `darwinCommand(ctx, name, args...)`.
- Produces: additional `checkReport` entries named `warp`, `wireguard`, `openvpn`, `local_proxy`, and `packet_tunnel`. `local_proxy` should target external app names such as Clash, Surge, and mihomo; do not flag raw `brook` or `sing-box` process names because bx may run those engines internally.

- [ ] **Step 1: Write tests for detector helpers**

Add tests for process matching, interface detection, local proxy process detection, and packet tunnel classification.

- [ ] **Step 2: Implement helper functions**

Add small pure helpers for matching process output and interface output. Keep command execution inside `collectPlatformChecks` path.

- [ ] **Step 3: Run targeted tests**

Run: `go test ./internal/cli`

### Task 2: Risk classification and docs

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `README.md`
- Modify: `docs/leak-surfaces.md`

**Interfaces:**
- Consumes: `checkReport.Name` from Task 1.
- Produces: medium risk for active competing tunnels.

- [ ] **Step 1: Extend risk names**

Add the new tunnel check names to `applyPlatformRisk` where `warn/fail` should raise risk.

- [ ] **Step 2: Update docs**

Describe that bx detects common VPN/tunnel/proxy coexistence risks but does not manage those tools.

- [ ] **Step 3: Run full tests**

Run: `go test ./...`
