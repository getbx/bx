# macOS tunnel coexistence check

## Goal

`bx check` should tell the user when the Mac already has another VPN, overlay, packet tunnel, or local proxy that may create a second network path. This keeps bx honest: if another program can build its own tunnel, bx should surface that fact instead of pretending it owns the whole machine.

The feature is read-only in this phase. bx detects and explains; it does not manage other products' accounts, peers, ACLs, nodes, or routes.

## Product boundary

bx owns:

- its own protection state.
- its own DNS/TUN/UDP policy.
- leakage and coexistence reporting.
- narrow compatibility adapters when the route/control-plane shape is known and safe, such as Tailscale bootstrap coexistence.

bx does not own:

- Tailscale, ZeroTier, WARP, WireGuard, OpenVPN, NetBird, Nebula, Clash, Surge, mihomo, sing-box, or enterprise VPN configuration.
- user identity, access control, remote peers, or vendor-specific policy.
- automatic approval of a newly detected tunnel.

## User experience

No new command is added. The signal appears in existing checks:

```text
bx check
  结论    安全可用
  ...
  ✓ Tailscale overlay route present
  ? WARP running, may create another tunnel
  ? WireGuard tunnel interface detected
```

The JSON form uses normal `checkReport` entries, so agents can reason from the same output:

- `name`: stable identifier such as `tailscale`, `zerotier`, `warp`, `wireguard`, `openvpn`, `local_proxy`.
- `status`: `ok`, `info`, `warn`, or `fail`.
- `detail`: concise human-readable evidence.
- `hint`: next action when useful.

## Detection scope

macOS first version:

- Tailscale: existing stronger adapter remains. Warn when running but its overlay route is missing.
- ZeroTier: detect process and likely overlay interface. Do not infer managed routes globally.
- WARP: detect Cloudflare WARP process or known interface hints. Warn because it may create a separate tunnel.
- WireGuard/OpenVPN: detect processes and utun-style tunnel/interface evidence. Warn only when a tunnel appears active.
- Clash/Surge/mihomo/sing-box/brook local proxy apps: detect common processes and system proxy/env proxy evidence. Report `info` unless they appear to own system proxy or a tunnel.
- macOS Network Extension / Packet Tunnel: if discoverable through read-only system commands, report a generic `packet_tunnel` warning.

The first implementation should prefer conservative detection. False positives should be `info`, not `warn`.

## Risk rules

- `ok`: coexistence looks normal, such as Tailscale route present.
- `info`: something is present, but bx cannot prove conflict.
- `warn`: another active tunnel/proxy may bypass bx or fight bx.
- `fail`: a known coexistence dependency is broken.

`leak-check` raises risk to at least `medium` for `warn` or `fail` on tunnel/overlay checks. `info` does not raise risk.

## Implementation shape

Keep this as a small platform layer under `internal/cli`:

- extend `collectPlatformChecks(ctx)`.
- keep pure helpers for process/interface/route matching.
- keep command execution read-only.
- do not add setup/up/down side effects.

The implementation should be easy to extend with another detector without changing CLI command shape.

## Tests

Add pure tests for:

- ZeroTier interface detection.
- process-output matching helpers where possible.
- WARP/WireGuard/OpenVPN detector classification from sample command output.
- risk escalation for `warn/fail` tunnel checks.

No test should require changing local network state.

