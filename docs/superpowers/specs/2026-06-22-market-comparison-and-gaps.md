# bx vs market gateways — comparison & optimization backlog

Authored 2026-06-22 (autonomous /loop). Purpose: position bx (host mode + new router mode) against the mainstream OpenWrt transparent-proxy stacks, and derive a prioritized backlog. Sources: OpenClash/Mihomo, sing-box/HomeProxy, PassWall(2), plus 2026 anti-DPI research (VLESS-REALITY, ShadowTLS).

## Feature comparison

| Capability | bx (host + router mode) | OpenClash / Mihomo | sing-box / HomeProxy | PassWall2 |
|---|---|---|---|---|
| LAN transparent proxy | ✅ router mode (TUN, source-routed) | ✅ TPROXY/redirect or TUN | ✅ Redirect TCP + TProxy UDP | ✅ (Xray/sing-box) |
| fake-IP DNS, resolve-in-core | ✅ 198.18/15, no DNS leak | ✅ | ✅ | ✅ |
| China-direct split (geoip/geosite) | ✅ auto-updated thru tunnel | ✅ | ✅ | ✅ |
| Corp split-DNS (internal zones) | ✅ `dns.split` | ⚠️ via custom rules | ✅ | ⚠️ |
| Kill-switch / fail-closed | ✅ data-plane + route blackhole | ⚠️ partial / config | ⚠️ config | ⚠️ config |
| DNS-leak prevention | ✅ forced-thru + GL anti-leak | ✅ | ✅ hijack-dns | ✅ |
| IPv6-leak prevention | ✅ block LAN v6 fwd (router) / unreachable (host) | ⚠️ user-config | ⚠️ user-config | ⚠️ |
| WebRTC/STUN UDP leak | ✅ udp.mode=proxy (or block); no direct | depends on TProxy-UDP enabled | ✅ TProxy UDP | depends |
| Router's own traffic untouched | ✅ **structural** (source rule only) | ⚠️ usually all-traffic; needs excludes | ⚠️ | ⚠️ |
| Tailscale coexistence on the gateway | ✅ (router mode never hijacks router-own) | ⚠️ needs manual excludes | ⚠️ | ⚠️ |
| Single static binary, no deps | ✅ (embeds brook) | ❌ luci+core+kmods | ❌ | ❌ |
| Dry-run / pre-deploy review | ✅ `router-plan` / `darwin-plan` | ❌ | ❌ | ❌ |
| **Transport anti-DPI** | ⚠️ **brook (plain TCP / wss)** | ✅ any (Reality/Vision via core) | ✅ Reality/Vision | ✅ Reality/Vision |
| GUI / per-client policy | ❌ CLI + menubar (mac) | ✅ luci | ✅ luci | ✅ luci |

## Where bx is now competitive or better
- **Fail-closed is structural, not configured.** Route blackhole + data-plane kill-switch + (router mode) no-direct-WAN path. Most competitors leave leak-prevention to user config; bx makes it the default.
- **Router-own traffic is never hijacked** in router mode (source-based rule). This is the cleanest answer to the Tailscale/management-breakage that bit us — most stacks proxy all traffic and require hand-maintained excludes.
- **fakeip-everything** → resolution never depends on Chinese DNS → china-blocked domains (google/youtube) always resolve+proxy. Validated.
- **Operational ergonomics**: one static binary (no luci/kmod sprawl), and a `router-plan` dry-run nobody else ships.

## Gaps → optimization backlog (priority order)
1. **Transport anti-DPI (highest, = your "不被发现/乙").** brook plain TCP/9999 is fingerprintable by corp DPI; brook-wss is only "HTTPS to *your* domain". 2026 SOTA is **VLESS-REALITY** (borrows a real popular site's TLS ClientHello — the "microsoft.com" camouflage; ~98% bypass) — already running on this VPS for mihomo. Options for the separate **transport-camouflage spec**: (a) brook **wssserver behind the VPS nginx-443 SNI-demux** (TLS, looks like HTTPS to a real-ish domain; small change), (b) front brook with a REALITY-capable layer, or (c) adopt sing-box/REALITY as bx's transport (large; bx is brook-centric). Recommend (a) first, evaluate (c).
2. **netns integration test** — verify real forwarded-client egress + the fail-closed + leak assertions OFF-device (root needed; gate behind a build tag / CI). Closes the "declared done without a true LAN-client test" risk.
3. **Leak-test tooling** — a `bx leak-test` helper (or doc) running the browserleaks/dnsleaktest equivalents (IP, DNS, WebRTC/STUN, IPv6) so deploy verification is one command, not ad hoc.
4. **LAN-CIDR auto-detect** — when `router.lan_cidrs` empty, derive from the LAN bridge subnets (br-*). Minor ergonomics.
5. **Per-client / per-CIDR policy** — route some LAN sources direct and others proxied (the source-rule design already supports this; expose in config).
6. **UDP fakeip + QUIC** — confirm QUIC (UDP/443) over brook UDP-associate behaves under load; consider QUIC-block option if it destabilizes (sing-box offers this).

## Performance (TUN vs TProxy — a real tradeoff)
bx forwards via a **TUN + gVisor userspace netstack**. On routers this is CPU-bound: community/sing-box data put `auto_route` TUN throughput at ~100–220 Mbit even on strong routers (Redmi AX6000 ~200 Mbit; mid-range ~100–120 Mbit), because routing + userspace TCP termination is expensive. TProxy (sing-box/PassWall default) is lighter on the CPU but has OpenWrt-24.10 quirks; sing-box's `auto_redirect` (TUN + nft redirect hybrid) is the fastest and auto-inserts fw4 rules.

- **Impact on the Mudi now:** likely negligible — the WAN is corp Wi-Fi (`wlan4`, well under the TUN ceiling), so gVisor-TUN throughput is not the bottleneck. Document, don't block.
- **Backlog item #7 (throughput):** if a faster uplink ever matters, add a **TProxy forward mode** for router mode (kernel-path TCP/UDP, nft tproxy + `kmod-nft-tproxy`/`kmod-nft-socket`) as an alternative to the gVisor-TUN path. The data-plane decision logic (dialer/route/dns) can be reused; only the ingress (TUN → tproxy) changes. Keep TUN as the default (simpler, no extra kmods, works everywhere).

## Decision recorded
- Router-mode leak model = market-leading once shipped; keep it the default.
- Transport stealth is the real remaining differentiator vs Reality-based stacks → its own spec, evaluate brook-wss-behind-443 vs Reality.
- gVisor-TUN throughput ceiling is a known limit; a TProxy mode is the future high-throughput path, but not needed at corp-Wi-Fi link speeds.
