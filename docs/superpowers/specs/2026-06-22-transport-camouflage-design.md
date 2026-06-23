# bx transport camouflage (anti-DPI) — design

Status: DRAFT (autonomous /loop 2026-06-22). Design only — NO VPS changes, NO deploy. Separate from router-mode. Addresses the user's "不被发现/乙": the *corp network* must not be able to fingerprint the Mudi as running a circumvention proxy.

## Problem / threat model
Router mode (separate spec) makes LAN clients leak-proof toward the *destination*. It does nothing about the *corp network* observing the Mudi's uplink. Today bx's transport is **brook over plain TCP/9999** to the VPS. A corporate DPI/ML middlebox sees a long-lived, high-entropy TCP flow to a foreign IP on a non-standard port — a classic proxy signature. 2026 research: GFW/enterprise classifiers flag exactly this (Shadowsocks/WG/plain-obfs detected by entropy + timing + port). The goal: make the uplink **indistinguishable from ordinary HTTPS to a plausible destination**, resistant to both passive classification and active probing.

## Key architectural fact (why this is cheap to add)
bx's data plane is already transport-agnostic. `internal/tunnel` consumes a `RunnerFactory` that starts *any* subprocess exposing a local socks5, plus a `HealthCheck`. `NewBrook` is just one factory (`brook connect … --socks5 addr`). The dialer/tun/dns/router never know what the transport is — they dial the local socks5. **So a new transport = a new RunnerFactory + a link format. No data-plane change.**

## Options
**A. brook `wssserver` behind the VPS nginx-443 SNI-demux.**
brook supports WSS (WebSocket-over-TLS). Put a brook wssserver behind the existing nginx that already SNI-demuxes public 443. To the corp it's "HTTPS to <your-domain>". Pros: brook-native, smallest change, reuses the 443 fronting; client = a brook wss link (still a brookFactory). Cons: SNI is *your* domain (not a famous site); a real cert needed; active probing of that host returns a non-website unless a decoy is served.

**B. A + decoy fallback.** nginx serves a believable site (or proxies a real one) for any non-bx request to that SNI/path, so active probing sees a normal website. Closes the active-probe gap of A. Still brook under the hood.

**C. VLESS-REALITY transport (SOTA).** Add a `realityFactory` RunnerFactory that runs sing-box (or xray) as the black-box subprocess exposing a local socks5 — exactly like brook today. REALITY borrows a real popular site's TLS-1.3 ClientHello (e.g. www.microsoft.com), so the flow is byte-indistinguishable from real HTTPS to that site and survives active probing (probes hit the real site). 2026 SOTA (~98% bypass). **The VPS already runs sing-box VLESS-REALITY (microsoft.com) for mihomo** — server side largely exists; bx just needs the client factory + link. Cons: bundles a second engine (sing-box ~big) or reuses the system one; larger than A.

**D. brook + TLS-fragment/obfs.** Marginal; doesn't fool ML classifiers. Rejected.

## Recommendation (phased)
1. **Quick win:** Option **A/B** — brook wssserver behind 443 with a decoy fallback. Reuses the existing nginx-443 SNI-demux; client stays a brook link. Defeats passive port/entropy fingerprinting and (with B) active probing. Low risk, no new engine.
2. **Max stealth (evaluate):** Option **C** — make bx transport pluggable (`transport: brook-wss | reality`), add a `realityFactory`. Since the VPS already serves REALITY and the data plane is transport-agnostic, the cost is a client factory + link parsing + embedding/locating the engine. This is the end-state for true indistinguishability.

## Proposed shape (when implemented — separate plan)
- `internal/tunnel`: add a transport selector. `bx://` link (or `config.transport`) chooses the factory: `brook` (today), `brook-wss`, `reality`. Each factory: start subprocess → local socks5 → same `Tunnel` supervision/health/reconnect.
- `internal/embedded`: optionally embed the chosen engine per-arch (already done for brook arm64/amd64); REALITY would add sing-box binaries (size tradeoff) or reuse a system one.
- `bx server`: extend to provision the chosen server mode (wssserver / reality) — VPS-side, user-gated.
- Health/anti-loop/killswitch/router-mode: unchanged (transport-agnostic).

## Out of scope here
- Any VPS change (provisioning wssserver/reality, nginx SNI map, certs) — user-gated, separate.
- Implementation plan — follow-up.
- Per-destination transport selection — YAGNI for now.

## Decision recorded
Transport is the last differentiator vs Reality-based stacks (OpenClash/sing-box/PassWall all do Reality). bx's pluggable RunnerFactory makes adoption cheap. Phase A/B first (brook-wss behind 443), evaluate C (Reality) for the end-state.
