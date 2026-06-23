# bx router-mode (gateway transparent proxy) — design

Status: DRAFT (authored autonomously 2026-06-22 under a /loop mandate; decisions delegated by the user "你来做决定". User to review.)

## Problem

bx today is a **host** transparent proxy: it hijacks the *host's own* outbound traffic (`ip rule from all → table 100 → tun`, OUTPUT path) and re-originates it Direct/Proxy/Block. That is correct for a Linux/macOS host.

On the **GL.iNet Mudi 7 router** (the target), this fails in two ways (observed 2026-06-22, caused a LAN outage):

1. **LAN clients get no internet.** bx never sets up forwarding for *forwarded* LAN-client traffic. `bx0` is in no fw4 zone, so `LAN → bx0` forwarding is dropped by fw4's default. (Router-side curls passed because that is the host's own OUTPUT path — exactly what bx already handles. True LAN-client egress was never exercised.)
2. **It breaks the router's own services.** Hijacking the *router's own* default route pulled the Mudi's own management traffic (Tailscale DERP/control, GL cloud) into the tunnel and killed it (Tailscale went offline).

mihomo works on the Mudi because it is built for the gateway role (forwarding + careful exclusions + GL integration).

## Goal

Add a **router mode** to bx: proxy **only LAN-client forwarded traffic**, leak-proof, while leaving the **router's own traffic completely untouched** (direct, as today, so Tailscale/management/GL never break). Match the leak-prevention bar of mainstream OpenWrt gateways (OpenClash/Mihomo, sing-box/HomeProxy, PassWall).

Non-goals (separate specs):
- **Transport camouflage / anti-DPI** (brook plain TCP → brook-wss or REALITY-style, to evade *corp-network* discovery). Orthogonal to forwarding; touches the VPS + server config. Tracked separately.
- Touching the live Mudi. This spec is design + local implementation + local tests only. Deployment is a later, user-gated step with mihomo as the live fallback.

## Core design

### Mode selection
New config field `mode: router` (default `host`, preserving current behavior). In `host` mode nothing changes. In `router` mode bx hijacks **forwarded** traffic by source, not the host's OUTPUT.

```yaml
mode: router            # host (default) | router
router:
  lan_cidrs:            # source nets whose forwarded traffic is proxied; empty = auto-detect from LAN ifaces
    - 192.168.8.0/24
  # bypass (existing top-level) still applies as always-direct destinations
```

### Routing (Linux, router mode)
Instead of `ip rule from all → table 100` (which also catches the router's own OUTPUT), use **source-based** rules so only LAN-client traffic is hijacked:

```
ip rule add from <LAN_CIDR> lookup <bx_table> pref <P>     # per lan_cidr
# bx_table: default → bx0 ; bypass dsts → main (direct)
```
The router's own packets (source = a router IP, not in lan_cidr) never match → normal routing → direct. **This is the single change that fixes the Tailscale breakage** — the router's own traffic is structurally never hijacked.

This mirrors mihomo's 99-vpn-mode hook (`ip rule from $LAN_NET lookup 1001`).

### Firewall (fail-closed) — the leak-prevention core
On OpenWrt (fw4/nftables); on generic Linux, equivalent nft rules. bx (router mode) installs and on teardown removes:

1. **Allow** `LAN → bx0` forward (so forwarded traffic reaches the gVisor tun).
2. **Block** `LAN → WAN` *direct* forward for everything NOT in the bypass set. LAN clients have **no direct egress path** — only via `bx0`. If bx/the tunnel is down, LAN traffic is dropped (fail-closed, no real-IP leak), clients just lose internet.
3. **Allow direct** the bypass set: LAN → {LAN, corp/private, Tailscale 100.64/10, WG subnet, the brook server IP}. These are intentionally direct.
4. **DNS**: rely on GL's existing `lan_drop_leaked_dns` (drops LAN `udp/53` not marked for the router resolver) + wire both dnsmasq instances (`cfg01411c`, `wgclient1`) upstream → bx `--listen-dns`. LAN clients can only resolve via the router → bx fake-ip. No DNS leak. (Generic Linux: add an nft redirect of LAN `udp/53` → bx DNS.)

### Leak matrix (every vector closed)
| Vector | Closed by |
|---|---|
| Direct WAN (client bypasses proxy) | fw4: LAN→WAN direct DROP; only LAN→bx0 allowed |
| Tunnel down → fall to direct | bx killswitch (Proxy decision → Block); + fw4 has no direct LAN→WAN path anyway |
| DNS leak | GL lan_drop_leaked_dns + dnsmasq→bx fakeip; clients can't use external DNS |
| IPv6 leak (globally-unique v6 via ICE/WebRTC) | fw4: DROP LAN IPv6 forward (or disable LAN v6). Clients fall back to v4 → bx |
| WebRTC/STUN UDP (3478/5349) revealing real IP | UDP forced into bx0 like all forwarded traffic; bx `udp.mode=proxy` relays via brook (exit = VPS IP) or `block`. Never direct. |
| App sniffing real IP via any direct socket | no direct LAN→WAN path exists; all egress re-originates from bx as VPS (proxy) or router (CN-direct) |

UDP mode decision: **`udp.mode: proxy`** in router mode (WebRTC/QUIC work, exit via VPS, no real-IP leak), with killswitch → block when the tunnel is unhealthy. `block` remains available for maximum stealth.

### What re-originates where
- LAN client → fake-ip (foreign) → bx0 → gVisor terminates → dialer: **Proxy** → brook → VPS. Real client IP never leaves; destination sees VPS.
- LAN client → fake-ip (CN domain) or CN IP → bx0 → dialer: **Direct** → router OUTPUT to the real CN IP (NAT). CN traffic doesn't transit the VPS (perf), and CN sites only see the router's WAN IP — no privacy concern.
- LAN client → bypass dst (private/tailscale/server) → direct, never enters bx0.
- Router's own traffic → never matched by the source rule → direct (Tailscale etc. unaffected).

## Platform shape

bx's `platform interface` (`OpenTUN`/`DirectDialer`/`Hijack`) stays. Router mode adds a hijack variant:
- `platform_linux.go`: `Hijack` gains router behavior — source-based `ip rule` from `lan_cidrs` + the fw4/nft fail-closed rules, instead of the `from all` host rule. Teardown removes both rule sets + restores firewall.
- Keep the core `run.go` OS-agnostic; mode is an `Options`/config flag threaded to the platform `Hijack`.
- Firewall management abstracted: a small `firewall` seam with an OpenWrt (fw4 nft) impl and a generic-nft impl, so the leak rules are testable and platform-specific bits are isolated.

## Testing (close the gap that caused the outage)

Pure-logic, root-free where possible (`t.TempDir()`, table-driven), plus a gated integration check:
1. **Rule generation unit tests**: given `lan_cidrs` + `bypass`, assert the exact `ip rule` set and nft rule set (fail-closed: a LAN→WAN-direct DROP exists; LAN→bx0 ACCEPT exists; bypass dsts ACCEPT-direct; IPv6 forward DROP).
2. **Leak assertions**: a test that the generated ruleset has NO path for a LAN source to reach a non-bypass WAN dst except via bx0 (parse the rules, assert).
3. **Dialer/router**: existing tests cover proxy/direct/block + fakeip; add cases for the UDP/STUN path (udp.mode=proxy → Proxy decision; killswitch down → Block).
4. **Integration (gated, not on the live Mudi)**: a netns-based harness — create a fake "LAN client" netns + a veth to a bx tun, run bx router-mode, assert (a) client egress works and exits via the proxy, (b) with the tunnel killed, client egress is BLOCKED (no direct leak), (c) client UDP/STUN does not reveal a non-proxy IP. This reproduces real forwarded-client behavior off-device.
5. **Deployment verification (later, user-gated, mihomo as fallback)**: real LAN-client egress from an actual device + a leak test (browserleaks-style: IP, DNS, WebRTC, IPv6) BEFORE declaring done. Never again "declared done" without a true LAN-client test.

## Rollout
1. Implement behind `mode: router` (host mode untouched → zero risk to existing host users / the corp Linux bx).
2. Land + test locally (units + netns integration).
3. User-gated Mudi deploy with mihomo fallback + real leak test. Update `provision-mudi.sh` (in the `mudi7-smart-gateway` repo) only after verified.

## Market alignment (why this matches best practice)
- fake-ip + in-core resolution (no DNS leak): OpenClash/Mihomo, sing-box fakeip.
- TProxy/redirect or TUN delivering fakeip traffic to the core: sing-box "Redirect TCP + TProxy UDP"; bx uses a TUN (gVisor) which is equivalent and simpler to reason about.
- Firewall-enforced no-direct-path + UDP(STUN)/IPv6 blocking: the documented requirement for WebRTC/IPv6 leak prevention at a gateway (proxy-level alone is insufficient; the firewall must block direct UDP/v6).
