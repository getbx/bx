# Agent Tools

`bx mcp` is the stable agent surface. An agent must use these tools rather
than compose shell commands or call `bx down && bx up`.

## Read First

- `bx_capabilities`: discover this host and the available bx surface.
- `bx_check`: the default verification bundle. It inspects bx and samples
  local runtime counters for a short window without changing protection or
  issuing outbound probes. Egress/DNS probes and browser WebRTC verification
  are opt-in; browser use requires explicit user confirmation.
- `bx_inspect`: the default structured diagnosis.
- `bx_status`, `bx_diagnose`, `bx_logs`, `bx_observe`: focused read-only
  follow-ups.
- `bx_leak_check`: network or browser probes are opt-in; browser testing also
  requires explicit user confirmation.

## Controlled Changes

- `bx_reconnect`: safely rebuilds the active transport. It preserves TUN,
  routes, and managed DNS; a failed replacement keeps the existing path.
- `bx_set_transport`: changes transport under the daemon's 240-second
  commit-confirmed guard. Inspect or leak-check it, then call `bx_commit`;
  otherwise call `bx_rollback` or let the deadman revert it.
- `bx_rehijack`: re-applies route capture under the same commit-confirmed
  guard. It is for a diagnosed routing problem, not routine recovery.
- `bx_policy_apply`: makes a bounded, explicit domain-policy change. It only
  accepts `direct` or `proxy` plus add/remove lists, keeps the two modes
  exclusive, writes the config atomically, then reloads a running daemon. It
  never starts, stops, reconnects, or releases protection. A risky public
  cloud domain cannot become direct unless the caller explicitly sets
  `allow_risk` after user approval.

All controlled changes are marked destructive so the agent asks for user
approval before it runs them. `bx_commit` and `bx_rollback` only operate on a
currently armed daemon change.

## Deliberate Omissions

The MCP server does not expose stop/start protection, arbitrary shell access,
or placeholder tools. A tool is registered only when it has a real local
implementation and a defined failure behavior. This keeps global protection
fail-closed and makes an agent's authority small and auditable.
