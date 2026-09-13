# Leak surfaces

bx 的目标不是只让网页能打开,而是让流量路径可解释、可诊断、默认 fail-closed。下面是常见泄漏面和 bx 的处理边界。

## bx can test directly

### Network-path summary

Command:

```bash
bx leak-check --json
bx leak-check --network --json --expected-ip <proxy-or-vps-ip>
```

`leak-check` is the agent-friendly summary. It aggregates client service state, DNS takeover, UDP policy, and IPv6/QUIC risk notes. By default it only reads local state. With `--network`, it also sends outbound IPv4/IPv6/DNS probes and compares the observed IPv4 exit with `--expected-ip`. It stays scoped to network-path leakage; browser fingerprinting is intentionally outside bx.

**There is no browser flag here, and that is deliberate.** The browser half needs a person in front of the screen to click a button, which is not something an agent can stand in for; it lives in `bx leakcheck` (no hyphen) instead. The `webrtc` block in this command's JSON therefore reports posture only — its `browser_candidates` check says `not inspected by this command` and `leak_proof` stays `not_proven`, because nothing here ever looked at a browser.

`--network` classifies:

- `egress_ipv4`: the current HTTP IPv4 exit. It is `ok` only when it matches an expected proxy/VPS IP.
- `egress_ipv6`: public IPv6 egress. Any observed IPv6 public exit is high risk until bx IPv6 capture is verified.
- `dns_resolution`: whether system resolution looks like bx fake-IP DNS or ordinary resolver output.

### WebRTC public IP

Command:

```bash
bx leakcheck
```

This opens a local `127.0.0.1` page (token-gated, `Host`-checked), lists the third parties it is about to contact **before** contacting any of them, and only starts once you click *Run the check*. The browser gathers ICE candidates and fetches its own public exit; bx joins that with the local half (routes, DNS, who owns the default path) and judges both together. `bx leakcheck` takes no `--expected-ip`: it does not ask you what your exit should be, it **measures** it and compares the WebRTC address against that.

The report is ten conclusions in three sections, and the three counts are printed side by side and never summed:

- `WHERE YOUR TRAFFIC GOES` (5 conclusions) — who carries your traffic, WebRTC vs HTTP exit, IPv6 exposure, DNS path, routes around the tunnel. Only these feed the leak count.
- `CAN YOU BE SINGLED OUT` (4) — local addresses, clock vs exit country, language vs exit country, fingerprint defences. Counted separately: on an ordinary browser this is never zero, and folding it into one number would train you to ignore all of it.
- `WHAT SITES CAN READ` (1) — neither good nor bad, no verdict, counted nowhere.

Every conclusion is `ok`, `bad`, `not checked` or `info`. **`not checked` never silently becomes `ok`**: a run where the browser half never arrived prints `0 leak(s) in the traffic path, 0 identifying trait(s), 6 not checked.` rather than "no leaks found". Nothing is stored — `bx leakcheck` keeps no history of a check.

`bx leakcheck` refuses to run under `sudo`, does not read your config, and does not need bx to be installed or running — checking a machine that is *not* protected is the point.

Important: an unexpected public IP is not automatically the machine's real ISP IP. It may be another upstream proxy, router, or app tunnel — the report names which of those it can and cannot tell apart.

### DNS takeover

`bx doctor --json`, `bx dns status`, and `bx leak-check --json` report whether macOS system DNS is pointed at bx. During `bx up` on macOS, bx switches system DNS to `127.0.0.1` and restores it on `bx down`.

### UDP policy

`bx status --json`, `bx doctor --json`, and `bx leak-check --json` report whether non-DNS UDP is:

- `proxy`: relayed through bx, preferred for WebRTC/Meet.
- `block`: fail-closed; safer but realtime apps may degrade.
- `direct-realtime`: local real network path; high leak risk.

## bx can assess from local state

### IPv6

bx blocks global IPv6 paths in host-mode test plans so IPv6 cannot silently bypass the IPv4 tunnel. `bx leak-check --network --json` can also attempt an IPv6 egress probe; if it sees a public IPv6 exit, treat it as high risk.

### QUIC and HTTP/3

QUIC uses UDP. With `udp.mode: proxy`, bx should relay it; with `block`, it is stopped; with direct mode, it can expose the local path. `leak-check` reports the UDP policy; protocol-specific QUIC smoke tests can stay outside the default path unless needed.

### System proxy bypass

Apps that ignore system proxy settings are why bx uses TUN/DNS capture instead of only configuring a proxy. Still, browser extensions, app-level VPNs, or another network extension can create a different path. `bx leakcheck` helps catch this for browser UDP — it opens a local page and compares the WebRTC exit against the HTTP exit.

On macOS, overlay networks such as Tailscale and ZeroTier are treated as coexisting network extensions, not as traffic bx should own. bx keeps Tailscale control/MagicDNS domains out of fake-IP by default and routes Tailscale bootstrap/controlplane IPv4 addresses outside the bx tunnel, so Tailscale can build its own `100.64/10` overlay route. `bx leak-check` reports when Tailscale appears installed but that overlay route is missing, because in that state bx's split-default route can catch `100.x` traffic before Tailscale has recovered.

ZeroTier and similar overlays do not have one universal control-plane/route shape that bx can safely infer for every user. bx therefore starts with read-only coexistence checks: it can report that ZeroTier is running and whether a likely overlay interface is present, while leaving membership, ACLs, and managed routes to ZeroTier itself.

Other VPN/tunnel/proxy apps are treated the same way. On macOS, `bx leak-check` can surface common competing paths such as Cloudflare WARP, WireGuard, OpenVPN, Clash, Surge, mihomo, system proxy, and connected macOS VPN services. Process-only evidence is reported as `info`; active system proxy or connected VPN evidence is reported as `warn` because it may create a path outside bx. bx intentionally avoids flagging raw helper engine names that bx itself may run internally.

`bx status` is the runtime view. The macOS daemon refreshes a lightweight Network Guard snapshot in the background and exposes it as `warnings` in `/v0/status`. This catches changes that happen after `bx up`, such as a VPN service connecting or system proxy becoming enabled. The guard is read-only; it warns instead of disabling or reordering other software.

## Not IP leaks, but identity signals

These do not usually expose the local public IP, but they can correlate identity:

- browser timezone, locale, fonts, canvas/WebGL, device memory, media devices.
- logged-in accounts and cookies.
- LAN discovery names exposed through mDNS or app protocols.

bx should not pretend to solve browser fingerprinting. It can report network-path evidence and keep its JSON honest for agents.

If you want to inspect browser fingerprinting separately, bx provides a helper that only opens reference pages:

```bash
scripts/open-privacy-checks.sh
scripts/open-privacy-checks.sh --yes
```

The default run is a dry-run. With `--yes`, it opens third-party pages such as BrowserLeaks, EFF Cover Your Tracks, and CreepJS. bx does not collect, parse, upload, or judge those results.

## Testkit

For macOS real-machine testing:

```bash
scripts/darwin-testkit.sh ... --leak-network
scripts/darwin-testkit.sh --reconnect-check
```

The testkit has no browser flag: the browser half needs someone at the keyboard, so run `bx leakcheck` yourself alongside it.

When `--leak-network` is used, the testkit passes `--server-bypass` IPs as expected IPv4 exits for `bx leak-check --network`.

`--reconnect-check` is a separate dry-run-first smoke test for an already running bx service. With `--execute`, it performs only `bx reconnect`, then verifies that macOS split-default routes and bx-managed DNS remain in place and the replacement tunnel is healthy. It does not run `bx up` or `bx down`.
