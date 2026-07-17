# macOS Update Architecture

> Superseded on 2026-07-16 by
> `2026-07-16-macos-guardian-lifecycle-update-design.md`. This document records
> the original staged-update design; the newer specification adds a root-owned
> guardian and a fail-closed runtime activation transaction.

## Goal

Make bx updates discoverable and one-click on macOS without changing the active
protection session. The design must remain useful when bx supports additional
transports and when its delivery moves from unsigned development builds to a
signed product.

## Product Contract

- The menu bar may check for a release and present an update action. It never
  installs an update without explicit user confirmation.
- Every update package is authenticated and hash-verified before installation.
- Installing an update does not stop bx, release TUN, change routes, or restore
  DNS.
- A running data-plane process is not restarted just to load a new binary.
  The UI reports that protection remained on and that the new runtime is staged
  for a later safe start.
- bx does not claim a zero-leak runtime replacement guarantee on macOS until a
  Network Extension owns the traffic boundary.

## Boundaries

### Menu

`Bx.app` is a non-networking status and consent surface. It observes the local
CLI and release manifest, shows an available update, and launches the updater.
It must not synthesize `bx down && bx up` or edit proxy/network settings.

### Updater

The updater is a self-contained transaction with these phases:

1. Fetch the release manifest over HTTPS.
2. Verify its detached Ed25519 signature against an update public key embedded
   in bx.
3. Select the macOS/architecture asset and download it to a private staging
   directory.
4. Verify its SHA-256 from the authenticated manifest.
5. Validate the archive layout and version before replacing anything.
6. Atomically install the CLI and application bundle, preserving client config
   and the live bx service.
7. Relaunch only the menu LaunchAgent and write an update receipt containing
   the release version, asset digest, timestamp, and outcome.

An interrupted or failed transaction leaves the installed application and CLI
unchanged. Staging data is disposable and contains no client link or secret.

### Core

The running bx daemon remains the owner of the current protected path. Replacing
`/usr/local/bin/bx` does not replace that in-memory process. The receipt marks
the runtime as staged rather than claiming it is already active. A user can
later start a fresh protection session; no updater action forces that change.

### Release Authority

GitHub Release is distribution storage, not the authority for update contents.
Each release publishes a signed manifest describing:

- release version and publication date;
- per-platform assets, SHA-256, size, and minimum updater version;
- optional severity and release notes URL;
- revocation or minimum-safe-version policy when needed.

The signing private key is kept outside the repository and CI. The public key
is versioned in the updater. Key rotation uses a manifest signed by an already
trusted key and carries the successor public key.

## macOS Delivery

Release CI needs a macOS runner to create the complete architecture-specific
bundle: `bx`, `Bx.app`, installer/uninstaller, and `SHA256SUMS`. The signed
manifest references that archive. Until Apple signing and notarization are
available, installation keeps the existing explicit administrator consent and
unsigned-app guidance; it must not weaken artifact verification.

When signing is available, the menu app and privileged installer helper use the
same Developer ID identity. Notarization and hardened runtime become release
gates. This changes trust presentation in macOS, but does not alter the bx
release-manifest verification model.

## Network-Extension End State

macOS has no general process replacement primitive that can prove every packet
remains blocked while a root TUN daemon exits and another starts. A PF rule set
would be an unreliable substitute and is explicitly out of scope.

The product-grade answer is a signed `NEPacketTunnelProvider` with the required
Network Extension entitlement. The extension owns the traffic boundary and
applies an explicit kill-switch policy while bx Core is unavailable. Only after
that architecture is delivered may bx promise no direct-traffic window across a
runtime upgrade, crash, sleep transition, or daemon restart.

## Delivery Plan

### Phase 1: Safe staged updates

- Add a signed release manifest format and verification library.
- Publish complete macOS packages from a macOS release workflow.
- Add a user-confirmed menu update flow with staging, receipt, and clear
  outcomes.
- Extend `bx update` to use the same manifest and package transaction rather
  than a separate GitHub-latest path.
- Test success, bad signature, bad checksum, partial download, downgrade
  rejection, and active-protection preservation.

### Phase 2: Signed distribution

- Add Developer ID signing, notarization, and a signed privileged installer
  helper.
- Make the updater reject unsigned bundles in production channels.

### Phase 3: Durable traffic ownership

- Move macOS traffic ownership to Network Extension.
- Introduce versioned Core handover under the extension's fail-closed policy.
- Add real-device crash, update, sleep/wake, and network-transition tests before
  making a no-leak runtime-update claim.

## Non-goals

- Silent background installation.
- Automatic daemon restart after update.
- Trusting unverified redirect targets or release assets.
- Updating user client links, routing mode, DNS policy, or transport settings.
- Advertising a packet-perfect runtime handover before Network Extension exists.
