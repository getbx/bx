# Network Guard Status Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface runtime VPN/proxy coexistence warnings through `bx status` without adding commands or changing network state.

**Architecture:** Add `warnings` to `stats.Report`, maintain a small supervisor-side Network Guard cache, and render warnings in CLI status/up summary. macOS has read-only collectors; other platforms return no warnings for now.

**Tech Stack:** Go, existing supervisor control socket, read-only macOS commands.

## Global Constraints

- No routes, DNS, proxies, services, or Network Extension state are changed.
- Commands have short timeouts.
- If inspection fails, bx omits the warning instead of blocking status.
- Tailscale runtime self-healing remains limited to existing bootstrap/control-plane bypass behavior.

---

### Task 1: Status warning model

**Files:**
- Modify: `internal/stats/render.go`
- Modify: `internal/stats/render_test.go`

**Interfaces:**
- Produces: `stats.Warning` and `stats.Report.Warnings`.

- [x] Add warning model and JSON field.
- [x] Render a concise `提醒` block.
- [x] Test warning rendering.

### Task 2: Supervisor Network Guard

**Files:**
- Create: `internal/supervisor/network_guard.go`
- Create: `internal/supervisor/network_guard_darwin.go`
- Create: `internal/supervisor/network_guard_other.go`
- Modify: `internal/supervisor/control.go`
- Modify: `internal/supervisor/run.go`
- Test: `internal/supervisor/network_guard_darwin_test.go`

**Interfaces:**
- Produces: cached `[]stats.Warning` for `/v0/status`.

- [x] Add background cache with bounded refresh interval.
- [x] Add macOS read-only collectors.
- [x] Inject warnings into status report.
- [x] Test parser behavior without changing network state.

### Task 3: CLI and docs

**Files:**
- Modify: `internal/cli/cli.go`
- Modify: `README.md`
- Modify: `docs/leak-surfaces.md`

**Interfaces:**
- Consumes: `stats.Report.Warnings`.

- [x] Show `Needs Attention` on `bx up` summary when warnings exist.
- [x] Document runtime warnings.
- [x] Run `go test ./...`.

