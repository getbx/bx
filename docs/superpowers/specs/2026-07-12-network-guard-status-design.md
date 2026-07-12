# Network Guard status design

## Goal

bx should detect when another VPN, tunnel, or system proxy appears after bx is already running, and surface that in `bx status` so users and agents do not rely on a stale `bx check` snapshot.

## Scope

This phase is read-only. It does not disable, reorder, or take ownership of other tools.

Network Guard reports:

- Tailscale is running but its overlay route is missing.
- macOS system proxy is enabled while bx is running.
- macOS Network Service reports a VPN as Connected or Connecting.

Network Guard does not report process-only evidence as a warning. Process-only detection remains in `bx check`.

## Architecture

The daemon owns a small background watcher. It refreshes a `[]stats.Warning` snapshot periodically with read-only macOS commands and injects that snapshot into the existing `/v0/status` report. Non-macOS builds return no warnings in this phase.

`bx status --json` gets a new `warnings` field. Human `bx status` renders a short `提醒` block and `sudo bx up` summary uses `Needs Attention` if warnings exist.

## Safety

- No routes, DNS, proxies, services, or Network Extension state are changed.
- Commands have short timeouts.
- If inspection fails, bx omits the warning instead of blocking status.
- Tailscale runtime self-healing remains limited to existing bootstrap/control-plane bypass behavior.

## Tests

Pure tests cover warning rendering and platform parser behavior. No test changes local network state.

