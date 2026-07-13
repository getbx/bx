# App Presets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a simple `bx preset` command that applies curated app/CDN direct rules for common usability issues such as Steam updates and Apple services.

**Architecture:** Keep presets as a CLI layer over existing `rules.direct` editing and hot reload. Presets live in `internal/cli/preset.go`; they do not add a second routing system.

**Tech Stack:** Go, existing YAML rule editor, existing supervisor reload control socket.

## Global Constraints

- Presets are explicit opt-in.
- Applying a preset writes client config but does not directly change routes/DNS.
- Hot reload uses the existing `/v0/reload` path when bx is running.
- Do not include open cloud/storage top-level domains in direct presets.

---

### Task 1: Preset model and commands

**Files:**
- Create: `internal/cli/preset.go`
- Modify: `internal/cli/cli.go`
- Test: `internal/cli/preset_test.go`

**Steps:**
- [x] Add `bx preset ls/show/apply`.
- [x] Add built-in `gaming`, `apple`, and `china-cdn` presets.
- [x] Reuse `editYAMLRuleList`.
- [x] Run `go test ./internal/cli`.

### Task 2: Docs and capabilities

**Files:**
- Modify: `README.md`
- Modify: `internal/cli/cli.go`
- Test: `internal/cli/cli_test.go`

**Steps:**
- [x] Document preset usage.
- [x] Add capability metadata.
- [x] Run `go test ./...`.

