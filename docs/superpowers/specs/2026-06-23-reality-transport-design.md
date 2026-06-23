# bx REALITY transport (pluggable, user-selectable) — design

Status: APPROVED-FOR-PLANNING (brainstormed 2026-06-23). Implementation follows a separate plan.

## Goal & priority (explicit)
bx should **focus on 不泄漏 (no leak) + 无风险/不被发现 (undetectable, un-blockable)**. **Latency is NOT a goal** — the user has stated a bit slower is fine. So this work is about *camouflage strength and leak-proofness*, not speed.

Today's transport is **brook-wss** (WebSocket-over-TLS to `vps.example.com:443`, behind the VPS nginx that SNI-demuxes 443). It defeats passive port/entropy fingerprinting but has one real weakness: **active probing** of `vps.example.com` returns a 301 redirect (not a believable website), and brook-over-ws has subtler tells. brook's plain `:9999` was already GFW-blackholed once — evidence the environment has active detection. **REALITY** is the SOTA answer: it borrows a real popular site's (www.microsoft.com) TLS-1.3 ClientHello, and active probes hit the *real* site, so the uplink is byte-indistinguishable from ordinary HTTPS to microsoft.com.

## Key architectural fact (why this is cheap)
bx's data plane is **transport-agnostic**. `internal/tunnel` consumes a `RunnerFactory` (`func(socksAddr string) (Runner, error)` — start any subprocess that exposes a local socks5) plus a `HealthCheck`. The TUN/DNS/router/dialer never know the transport; they dial the local socks5. **A new transport = a new RunnerFactory + link parsing. No data-plane change.** bx does NOT implement protocols itself — it already ships the brook *binary* and runs it as a subprocess; REALITY is the same pattern with a different engine.

## Scope
**In scope**
1. Pluggable transport selection driven by the `server:` link scheme.
2. A **REALITY runner**: trimmed/full sing-box, **downloaded on-demand** to `/usrdata`, run as a subprocess exposing a local socks5, configured from a `vless://` link.
3. Keep **brook-wss** as a selectable fallback.
4. **Decoy site** on the VPS nginx (hardens the brook-wss fallback) — near-term, VPS-only.
5. **Adversarial leak-proof audit** — verification, no code.

**Out of scope (YAGNI)**: per-destination transport selection; in-process REALITY (rejected — importing xray/sing-box internals is fragile and ~same size); QUIC transport; multiplexing.

## Architecture

### 1. Transport selection (by link scheme)
`config.server` already holds a link. Extend the dispatch:
- `brook://…`  → existing `brookFactory` (brook plain / wss, unchanged).
- `vless://…`  → new `realityFactory` (sing-box engine).

A small `internal/tunnel` (or `internal/transport`) selector inspects the scheme and returns the right `RunnerFactory` + `HealthCheck`. `vless://` is the ecosystem-standard share link (sing-box/xray/mihomo all emit it) and encodes everything: `vless://<uuid>@<host>:<port>?security=reality&pbk=<pubkey>&sid=<shortid>&sni=www.microsoft.com&flow=xtls-rprx-vision&fp=chrome&type=tcp#<name>`. User pastes one link → bx routes to REALITY. No new config fields, no separate param block.

### 2. The REALITY runner (sing-box subprocess)
- **Engine**: sing-box (the VPS server *is* sing-box → guaranteed client↔server compatibility, same config dialect).
- **Shipping**: **downloaded on-demand** to `/usrdata/proxy/data/sing-box` (mirrors `EnsureBrook`). Because it's downloaded — not embedded — **the engine size never bloats the bx binary** (stays ~47MB); users who don't pick REALITY pay nothing. Trimming sing-box (build tags → only vless+reality+socks, ~10–15MB) is a *nice-to-have* for download/disk size only; if sing-box's build system doesn't make trimming clean, **the full official sing-box (~30MB) download is acceptable** (it lives on /usrdata, not in bx). Decision deferred to implementation; correctness first, size second.
- **Download source**: hosted on the **user's VPS** (e.g. `https://vps.example.com/dl/sing-box-<arch>`), pinned by SHA-256 in bx. User-controlled, no third-party dependency. Falls back to a build+push path (like the bx binary) if the URL is unset/unreachable.
- **Config generation**: from the parsed `vless://` link, bx writes a minimal sing-box client config to `/usrdata/proxy/data/sing-box.json`: one `socks` inbound on `127.0.0.1:<port>` + one `vless` outbound with the reality+utls+vision settings. Runs `sing-box run -c <config>`. Same `Tunnel` supervision/health/reconnect as brook.
- **Engine-managed binary on /usrdata** → survives firmware (no re-download after an OTA); `provision`/`recover.sh` need no change beyond knowing the path.

### 3. Anti-loop / server-bypass for vless
`serverHostFromLink` must learn to parse `vless://` (extract `<host>` from the authority) in addition to `brook://`. Everything downstream (the pref-6580 server-bypass route that keeps the engine's connection to the VPS from looping into the tun) is unchanged — it just needs the right host→IP. For a domain host, the existing `/etc/hosts` pin + `hostToCIDRs` resolution applies (same as the wss domain pin).

### 4. Health check (unchanged)
The existing `socks5Health` probe (TCP dial to `1.1.1.1:443` through the local socks5, `maxFails=3` tolerance) works for any transport — it only talks to the local socks5. No change.

### 5. brook-wss retained as fallback
The brook factory stays. Transport is whatever link the user puts in `server:`. Keeping brook-wss selectable means REALITY having any issue is not a lockout — fall back by swapping the link. Lower operational risk.

### 6. Decoy site (VPS nginx, near-term, separate from bx)
On the VPS, make `vps.example.com` return a believable website (a static site, or `proxy_pass` to a real innocuous site) for any non-`/ws` request, instead of the current 301. Closes the active-probe gap of the brook-wss fallback. Pure nginx config; no bx change, no engine, low risk. Do this first (an afternoon) — it protects the fallback while REALITY is built.

### 7. Adversarial leak-proof audit (verification)
Confirm 不泄漏 has no gap, from a **real LAN client**: browserleaks.com (/ip = VPS, no DNS leak, **no WebRTC IP**, no IPv6), exit IP = VPS, China-direct still direct, and the kill-switch test (stop bx → LAN loses internet, no leak; start → restored). This is the test that was historically skipped; do it on whichever transport is active.

## Data flow (REALITY)
```
LAN client → br-lan → fw4 bxr accept → ip rule 6600 → table 441 → bx0 (TUN, gVisor)
  → bx dialer → local socks5 (127.0.0.1:N) → sing-box subprocess
  → VLESS-REALITY (ClientHello mimics www.microsoft.com) → VPS :443 SNI-demux → sing-box reality :8444
  → destination
bx's own server-bypass (pref 6580, VPS IP → main) keeps sing-box's uplink direct (no tun loop).
```

## Error handling
- **Download fails / no URL**: REALITY runner returns an error at startup; bx logs it and (per existing supervise loop) retries with backoff. Fail-closed routing means no leak while down. Document the build+push fallback for the binary.
- **sing-box crashes**: same as brook — `Tunnel.supervise` respawns; tun stays up → traffic blackholed (fail-closed) during the gap, never leaks.
- **Bad/expired REALITY params** (uuid/pbk/sid): handshake fails → health probe fails → tunnel marked unhealthy; surfaced in `bx status`. No silent direct fallback.
- **SHA-256 mismatch on the downloaded engine**: refuse to run it (supply-chain guard), log loudly.

## Testing
- **Unit**: `vless://` link parsing → correct sing-box outbound config (table-driven); scheme→factory selection; `serverHostFromLink` for vless. Pure, no network.
- **Engine integration** (gated/manual): generated config actually starts sing-box + serves socks5 + a probe succeeds.
- **End-to-end on the Mudi**: select REALITY → tunnel healthy, exit IP = VPS, github 200, China-direct works, Tailscale online; then the full leak audit (§7).
- **Regression**: brook-wss path still works (fallback intact); router-mode/fail-closed/fw4 unchanged.

## Firmware survival
sing-box binary + its generated config live under `/usrdata/proxy/data/` (survives OTA, like brook). `provision-mudi.sh`: `mudi.env` already carries the transport via the `server:` link in `bx.yaml`; add the sing-box download URL + SHA to the env. `recover.sh` re-runs provision; the engine is already on /usrdata so no re-download needed.

## Phasing
1. **Decoy site** (VPS nginx) — immediate, hardens the brook-wss fallback. *(VPS task, not bx code.)*
2. **Leak-proof audit** — confirm 不泄漏 airtight. *(Verification.)*
3. **REALITY runner** (this spec's core) — the real build: scheme dispatch → `realityFactory` → on-demand sing-box download → vless link→config → `serverHostFromLink` for vless → tests → Mudi deploy → re-run audit on REALITY. brook-wss kept selectable.

## Decisions recorded
- Engine = sing-box (matches server), **downloaded on-demand** (binary size never touches bx); trim if cheap, else full.
- Transport selected by `server:` link scheme (`brook://` | `vless://`), no new config fields.
- brook-wss stays as fallback. REALITY is the primary undetectable transport.
- Decoy + audit are cheap near-term wins, NOT substitutes for REALITY.
- Latency explicitly de-prioritized.
