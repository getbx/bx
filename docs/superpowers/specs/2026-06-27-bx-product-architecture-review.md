# bx product architecture review — 2026-06-27

Status: RESEARCH-NOTE. This is a product/architecture positioning record, not an implementation plan.

## 0. One-line thesis

bx should not become another general-purpose proxy core like sing-box, mihomo, or Xray. bx should become a self-hosted, fail-closed, agent-operable network appliance: one binary that can be installed, diagnosed, repaired, verified, and safely rolled back by the owner's AI agent.

The product wedge is not "supports the most protocols" or "has the largest rules ecosystem." The wedge is: a normal developer can say "make this machine safely reach AI services without leaking," and their agent can do the entire loop without bricking the network.

## 1. Current bx strengths to preserve

- **Structural fail-closed posture.** Proxy-decided traffic blocks when the tunnel is unhealthy instead of silently falling back to direct. IPv6 is also treated as a leak vector and blocked unless explicitly carved out.
- **Single binary / low dependency surface.** This is a real product advantage against OpenWrt stacks that require a core, web UI, rule providers, kernel modules, and several moving config files.
- **Clear platform seam.** `supervisor.Run` is mostly platform-neutral, with `OpenTUN`, `DirectDialer`, and `Hijack` pushed into platform implementations. This is the right shape for Linux/macOS/Windows expansion.
- **Agent-control direction.** The MCP/control-socket specs already point toward an unusual and valuable product: a proxy that an AI agent can operate safely.
- **Dry-run and rollback thinking.** `router-plan`, `darwin-plan`, route snapshots, and commit-confirmed are stronger operational primitives than what most proxy products expose to users.

These are the core differentiators. Future work should amplify them, not dilute them.

## 2. Mature product comparison

### sing-box: universal proxy platform

sing-box is the broadest modern core. Its TUN inbound supports platform-specific automatic routing, strict route behavior, route exclusions, packet sniffing, and Linux-specific `auto_redirect`. Recent documentation also shows active churn around DNS/rule-set fields and a steady push toward richer route/rule APIs.

What bx should learn:
- Treat DNS, route rules, and TUN routing as first-class subsystems, not helper code.
- Expose route behavior as structured configuration and plan output.
- Prefer idempotent route/firewall reconciliation over one-off shell mutations.

What bx should not copy:
- The full protocol/config surface. That would make bx harder to use and harder for an agent to operate safely.
- Rapidly changing config semantics unless bx owns a stable compatibility layer.

### mihomo: rule ecosystem and dashboard product

mihomo's strength is not only the core. It has rule providers, proxy providers, fake-IP DNS, fake-IP filters, dashboard APIs, and a large client/config ecosystem. It is good at "power user control."

What bx should learn:
- Rule sources need provenance: where they came from, when they updated, whether they verified, and what rule matched.
- Users and agents need explainability: "why did this domain go proxy/direct/block?"
- Dashboard/menu status should be backed by stable machine-readable state.

What bx should not copy:
- Subscription complexity as the product center.
- Open-ended rule grammar in the first version of bx's rule-set feature.

### Xray: protocol and routing engine

Xray is strong at transport/protocol evolution: VLESS, REALITY, routing, observatory, balancer, FakeDNS, and transparent proxy recipes. It is an advanced engine rather than an appliance.

What bx should learn:
- Keep transport as a swappable layer with explicit capability metadata.
- REALITY/sing-box integration is useful as a transport option, not as a reason to abandon bx's simpler operational model.

What bx should not copy:
- Requiring the user to assemble transparent proxy, DNS, firewall, and service management manually.

### Tailscale: operations and user trust

Tailscale is the most relevant product reference for operational maturity. It wins through stable state, diagnostics, upgrades, clear control-plane boundaries, and predictable behavior across kernel/user-space modes.

What bx should learn:
- Status should be authoritative and boring.
- Diagnostics should produce actionable structured facts, not prose.
- A local control API can be the stable center while CLI/UI/agent frontends remain thin.

## 3. Product positioning

The right product sentence:

> bx is a self-hosted AI access appliance: it safely creates, verifies, and maintains a private network path to AI services, with fail-closed routing and agent-controlled recovery.

This positioning implies several choices:

- **AI-native means agent-operable, not chat UI.** bx does not need a natural-language interface. It needs safe machine actions and structured facts.
- **Safety beats configurability.** A smaller set of actions that are reversible and explainable is better than a large feature matrix.
- **Transport is a means, not the product.** brook, REALITY, and future transports are interchangeable runners. The product is the safe lifecycle around them.
- **Verification is part of the happy path.** A setup is not complete until `verify` passes and the change is committed.

## 4. Architecture direction

### 4.1 One control plane

All frontends should converge on one local control API over unix socket:

- CLI: thin client.
- macOS menu: thin client.
- MCP: thin client.
- Future UI: thin client.

The daemon should own:
- status
- diagnose
- verify
- plan/apply/commit/rollback
- transport lifecycle
- route/DNS/firewall mutation state

This avoids the current transitional split where MCP still owns an in-process guard while the daemon owns the real long-lived safety context.

### 4.2 Agent-readable facts instead of a user-facing explain command

Do not treat `bx explain` as a primary user-facing feature. A beginner will not think to run it, and the intended beginner path is likely "ask my own agent to check bx." That agent can run tests, inspect status, and understand structured output.

Therefore P1 should not add a standalone `bx explain` CLI command. It should add stable, agent-readable facts to existing surfaces:

```text
bx status --json
bx doctor --json
bx verify --json
```

Human output should stay short and non-expert:

```text
Protected
Tunnel: healthy, 42ms
Safety: kill-switch on
Next: no action needed
```

Structured output should include machine-friendly reason fields and recent decision facts where relevant. Agents should not need to parse prose or infer everything from raw logs.

Useful fields:
- current protection state
- tunnel health and latency
- kill-switch state
- DNS takeover state
- route hijack state
- recent errors with remediation and next actions
- verify result categories: egress IP, DNS leak, IPv6 leak, kill-switch, self-reach

Per-domain decision tracing can wait. It is only useful when debugging "why did this specific host go direct/proxy/block?", and it should be added later as an internal diagnostic endpoint if real use shows up.

### 4.3 Rule-set provider, but smaller than mihomo

bx should grow a rule-set system, but not a full mihomo clone.

Minimum useful shape:
- local file provider
- remote HTTP provider
- checksum and max-size guard
- update interval
- rule type: domain suffix, domain full, IP CIDR
- action: direct/proxy/block
- source/provenance in diagnostic output
- failed refresh keeps the old good set

This is enough for AI-service presets, internal corporate domains, user direct/proxy lists, and region lists.

### 4.4 DNS as a first-class subsystem

The current fake-IP design is directionally right. It should be formalized into a DNS engine with separate purposes:

- **server resolver:** resolves the proxy server itself and must never route through fake-IP.
- **route resolver:** resolves unknown domains for route decisions.
- **user DNS responder:** answers intercepted app queries.
- **split resolver:** resolves internal domains through internal DNS.

Each path should be visible in status, diagnose, or verify output. DNS leak checks should become part of verify.

### 4.5 Route mutation engine

Route/firewall/DNS mutations should move toward a common model:

```text
snapshot current state
build desired state
plan diff
apply
reconcile observed state
arm commit-confirmed
verify
commit or rollback
```

This should eventually cover:
- Linux host mode policy routes
- Linux router mode nft/fw4 rules
- IPv6 fail-closed routes
- macOS utun routes and DNS service settings
- systemd/launchd service state

The goal is not architectural purity. The goal is that every dangerous operation can be explained, verified, and undone.

### 4.6 Transport runner abstraction

Keep transport small:
- brook default
- REALITY via sing-box for harder networks
- later evaluate hysteria2/tuic only if a real use case appears

Each transport should expose capability metadata:
- TCP support
- UDP support
- supports HTTP proxy sidecar
- supports camouflage
- required external asset
- health probe method
- expected leak behavior

The agent can choose strategy; bx should safely execute the mechanical change.

## 5. Product roadmap

### P0 — Control-plane convergence

Goal: one daemon-owned truth.

Deliver:
- move MCP mutating tools to daemon unix socket
- add daemon-owned status/commit/rollback state to `bx status --json`
- make control socket startup mandatory
- keep peer-cred fail-closed for mutating APIs

Why first:
- It removes split-brain between short-lived MCP processes and the long-lived supervisor.
- It makes every later feature easier to expose consistently.

### P1 — Agent-readable status, diagnose, and verify

Goal: make bx debuggable by humans and agents.

Deliver:
- short "reason" lines in `bx status`, setup output, and menu-bar status details
- richer `status --json`, `doctor --json`, and `verify --json` fields for agents
- `bx verify --json` and `/v0/verify`
- structured health/safety/remediation facts
- redacted logs/events for recent decisions

Why second:
- Mature products are not only feature-rich; they tell users what is happening.
- Simple reason lines help beginners without requiring them to discover a command.
- Structured JSON gives agents a closed loop without requiring a separate explain feature.

### P2 — DNS engine and rule-set provider

Goal: graduate from static lists to safe, inspectable policy.

Deliver:
- provider config with source/checksum/refresh
- rule provenance in diagnostic output
- separate resolver roles
- failed refresh keeps last-known-good rule-set
- DNS leak checks integrated into verify

Why third:
- This is the minimum needed for AI-service presets and internal/corporate use without turning bx into mihomo.

### P3 — Route mutation engine

Goal: make route/firewall/DNS changes reconciled state, not scattered shell effects.

Deliver:
- common plan/apply/reconcile/rollback interface
- Linux host/router implementations first
- macOS DNS/route implementation second
- netns integration tests for Linux
- real-machine smoke for macOS and Mudi/OpenWrt

Why fourth:
- It is deeper engineering. It should land after the control API and status/diagnose/verify surfaces are stable.

## 6. What not to do yet

- Do not chase every transport protocol.
- Do not build a complex dashboard before the local API is stable.
- Do not make rule grammar as broad as mihomo in the first pass.
- Do not add automatic "smart repair" inside bx; let the agent choose based on structured diagnosis.
- Do not treat VM/CI leak tests as final proof. Real leak verification still needs a real network path on target hardware.

## 7. Concrete near-term backlog

1. Finish control-plane convergence:
   - MCP mutating tools call daemon unix socket.
   - daemon exposes commit/rollback state in status.
   - mutating endpoints remain peer-cred protected.

2. Expand status/diagnose/verify result models:
   - default human summary: one safety state and one next action
   - structured output: tunnel health, DNS takeover, route hijack, kill-switch, leak checks, remediation
   - no network mutation

3. Add `VerifyResult` model:
   - egress IP check
   - DNS leak check
   - IPv6 leak check
   - kill-switch check
   - self-reach/control-channel check

4. Refactor DNS roles:
   - server resolver
   - route resolver
   - user responder
   - split resolver

5. Draft rule-set provider spec:
   - intentionally small grammar
   - provenance and safe refresh as first-class requirements

## 8. Source notes

Checked around 2026-06-27:

- sing-box TUN inbound: https://sing-box.sagernet.org/configuration/inbound/tun/
- sing-box deprecations and rule-set direction: https://sing-box.sagernet.org/deprecated/
- mihomo DNS: https://wiki.metacubex.one/en/config/dns/
- mihomo rule providers: https://wiki.metacubex.one/en/config/rule-providers/
- mihomo TUN: https://wiki.metacubex.one/en/config/inbound/tun/
- Xray routing: https://xtls.github.io/en/config/routing.html
- Xray FakeDNS: https://xtls.github.io/en/config/fakedns.html
- Tailscale kernel vs userspace routers: https://tailscale.com/docs/reference/kernel-vs-userspace-routers

## 9. Decision record

- Product north star: bx as an AI-operable, fail-closed access appliance.
- Architecture north star: one daemon control plane; all frontends are thin clients.
- Next differentiating feature: agent-readable status/diagnose/verify, not another transport.
- Rule-set and DNS should grow, but stay smaller and safer than mature configurable cores.
- Route/firewall/DNS mutation should converge on plan/apply/reconcile/rollback.

## 10. Scope check

This note records product and architecture direction. It does not authorize implementation by itself. Each major item should still get a focused design/plan before code:

- control-plane convergence
- agent-readable status/diagnose/verify
- DNS engine
- rule-set provider
- route mutation engine
