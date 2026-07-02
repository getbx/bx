# Leak surfaces

bx 的目标不是只让网页能打开,而是让流量路径可解释、可诊断、默认 fail-closed。下面是常见泄漏面和 bx 的处理边界。

## bx can test directly

### Network-path summary

Command:

```bash
bx leak-check --json
bx leak-check --network --json --expected-ip <proxy-or-vps-ip>
bx leak-check --browser --json --expected-ip <proxy-or-vps-ip>
```

`leak-check` is the agent-friendly summary. It aggregates client service state, DNS takeover, UDP policy, WebRTC posture, and IPv6/QUIC risk notes. By default it only reads local state. With `--network`, it also sends outbound IPv4/IPv6/DNS probes and compares the observed IPv4 exit with `--expected-ip`. It stays scoped to network-path leakage; browser fingerprinting is intentionally outside bx.

`--network` classifies:

- `egress_ipv4`: the current HTTP IPv4 exit. It is `ok` only when it matches an expected proxy/VPS IP.
- `egress_ipv6`: public IPv6 egress. Any observed IPv6 public exit is high risk until bx IPv6 capture is verified.
- `dns_resolution`: whether system resolution looks like bx fake-IP DNS or ordinary resolver output.

### WebRTC public IP

Command:

```bash
bx webrtc-check --browser --json --expected-ip <proxy-or-vps-ip>
```

This opens a local `127.0.0.1` page, asks the browser to gather ICE candidates, and returns the result to bx. It can distinguish:

- `no_public_leak_detected`: browser candidates only contain expected public IPs, ignored placeholders, or no public IP.
- `unexpected_public_ip_detected`: a public IP appeared that is not in `--expected-ip`.
- `public_ip_detected_without_expected`: a public IP appeared, but bx was not given an expected proxy/VPS IP, so it cannot classify whether it is acceptable.
- `local_network_candidate_detected`: a LAN candidate such as `192.168.x.x` appeared.

Important: an unexpected public IP is not automatically the machine's real ISP IP. It may be another upstream proxy, router, or app tunnel. Pass every acceptable exit with `--expected-ip`.

### DNS takeover

`bx doctor --json`, `bx dns status`, and `bx webrtc-check --json` report whether macOS system DNS is pointed at bx. During `bx up` on macOS, bx switches system DNS to `127.0.0.1` and restores it on `bx down`.

### UDP policy

`bx status --json`, `bx doctor --json`, and `bx webrtc-check --json` report whether non-DNS UDP is:

- `proxy`: relayed through bx, preferred for WebRTC/Meet.
- `block`: fail-closed; safer but realtime apps may degrade.
- `direct-realtime`: local real network path; high leak risk.

## bx can assess from local state

### IPv6

bx blocks global IPv6 paths in host-mode test plans so IPv6 cannot silently bypass the IPv4 tunnel. `bx leak-check --network --json` can also attempt an IPv6 egress probe; if it sees a public IPv6 exit, treat it as high risk.

### QUIC and HTTP/3

QUIC uses UDP. With `udp.mode: proxy`, bx should relay it; with `block`, it is stopped; with direct mode, it can expose the local path. `leak-check` reports the UDP policy; protocol-specific QUIC smoke tests can stay outside the default path unless needed.

### System proxy bypass

Apps that ignore system proxy settings are why bx uses TUN/DNS capture instead of only configuring a proxy. Still, browser extensions, app-level VPNs, or another network extension can create a different path. `bx webrtc-check --browser` helps catch this for browser UDP.

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
scripts/darwin-testkit.sh ... --webrtc-browser
scripts/darwin-testkit.sh ... --leak-network
```

When `--webrtc-browser` is used, the testkit passes `--server-bypass` IPs as expected WebRTC public IPs, so the browser result is compared against the intended bx exit.

When `--leak-network` is used, the testkit passes `--server-bypass` IPs as expected IPv4 exits for `bx leak-check --network`.
