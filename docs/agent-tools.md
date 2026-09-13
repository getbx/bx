# Agent Tools

`bx mcp` is the stable agent surface. An agent must use these tools rather
than compose shell commands or call `bx down && bx up`.

## Read First

- `bx_capabilities`: discover this host and the available bx surface.
- `bx_check`: the default verification bundle. It inspects bx and samples
  local runtime counters for a short window without changing protection or
  issuing outbound probes. Egress/DNS probes are opt-in via `network=true`.
- `bx_inspect`: the default structured diagnosis.
- `bx_status`, `bx_diagnose`, `bx_logs`, `bx_observe`, `bx_protection`,
  `bx_apps`, `bx_explain`: focused read-only follow-ups.
- `bx_leak_check`: the network-path half only. Outbound probes are opt-in via
  `network=true`.

**No tool here has a browser option, and none of them asks for a browser
confirmation.** `webrtc-check` and `leak-check --browser` were removed on
purpose, and with them the `browser` / `browser_confirmed` / `browser_timeout`
fields and that confirmation gate. The reason is structural rather than
cautious: the browser half only produces data when a person clicks a button on
a local page, so an agent cannot stand in for it, and a confirmation prompt is
not stronger than simply not having the capability. A human runs `bx leakcheck`
(no hyphen) for that half; it joins the browser facts to the local ones and
judges both together.

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

## Updates

`bx update --check --json` is read-only and may be used to report a verified
release. Installing an update is maintenance, not autonomous recovery: an
agent must obtain explicit user approval first. On macOS, the normal install
path is the menu bar's `Update bx…` action; it updates the app, the CLI and the
runtime together.

**It is not untouched-in-place.** With protection on it runs as a Guardian
transaction: install a blocking barrier, stop the running Core, activate the new
files, start a new Core (new TUN, freshly installed routes), re-verify DNS
takeover, then release the barrier. Network access can pause for a few seconds
and the whole window is fail-closed — a new Core that fails its health check is
rolled back to the previous one with protection intact. With protection off it
is a plain file swap with no network effect. Either way this is maintenance, not
something to schedule behind the user's back.

## Deliberate Omissions

The MCP server does not expose stop/start protection, arbitrary shell access,
or placeholder tools. A tool is registered only when it has a real local
implementation and a defined failure behavior. This keeps global protection
fail-closed and makes an agent's authority small and auditable.
