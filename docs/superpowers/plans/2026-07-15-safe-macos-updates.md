# Safe macOS Updates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Deliver a verified, staged update foundation that can power a macOS menu-bar update action without interrupting active bx protection.

**Architecture:** `internal/update` owns a signed, versioned release manifest and asset verification. The CLI consumes that same manifest for update checks and replacement, while macOS release CI publishes complete packages. The active data-plane process is deliberately left running; the updater records that the new runtime is staged.

**Tech Stack:** Go standard library (`crypto/ed25519`, `encoding/json`, `net/http`), Swift/AppKit menu, GitHub Actions macOS runners, shell packaging scripts.

## Global Constraints

- Never run `bx up`, `bx down`, or `bx reconnect` during build or test.
- Updating must not change TUN, routes, managed DNS, client configuration, or transport settings.
- Release assets are accepted only after a pinned Ed25519 manifest signature and SHA-256 asset check.
- The signing private key never enters the repository or workflow logs.
- A running Core is staged, not restarted, after binary replacement.

---

### Task 1: Signed Release Manifest Library

**Files:**
- Create: `internal/update/manifest.go`
- Create: `internal/update/manifest_test.go`
- Modify: `internal/version/version.go`

**Interfaces:**
- Produces `Manifest`, `Asset`, `ParseAndVerify(manifest, signature []byte, key string) (Manifest, error)`.
- Produces `FindAsset(manifest Manifest, platform string) (Asset, error)`.
- `version.UpdatePublicKey` is injected at release build time and must contain base64 Ed25519 public-key bytes.

- [ ] **Step 1: Write failing verification tests**

```go
func TestParseAndVerifyAcceptsSignedManifest(t *testing.T) { /* sign canonical JSON with generated ed25519 key */ }
func TestParseAndVerifyRejectsTamperedManifest(t *testing.T) { /* change asset digest after signing */ }
func TestFindAssetRejectsWrongPlatform(t *testing.T) { /* no asset for darwin/arm64 */ }
```

- [ ] **Step 2: Run the package test and confirm the missing package fails**

Run: `go test ./internal/update -count=1`

- [ ] **Step 3: Implement strict JSON decoding and signature validation**

```go
type Manifest struct { Version string; Assets []Asset }
type Asset struct { Platform, Name, SHA256 string; Size int64 }
func ParseAndVerify(data, sig []byte, publicKeyBase64 string) (Manifest, error)
```

Reject empty keys, malformed base64, non-Ed25519 keys, invalid signatures,
unknown JSON fields, empty version, duplicate platforms, non-hex digests, and
non-positive sizes.

- [ ] **Step 4: Run package tests and commit**

Run: `go test ./internal/update -count=1`

Commit: `git commit -m "feat(update): verify signed release manifests"`

### Task 2: Manifest-Backed CLI Update Check

**Files:**
- Modify: `internal/cli/update.go`
- Modify: `internal/cli/update_test.go`
- Modify: `internal/cli/cli.go`

**Interfaces:**
- Produces `bx update --check --json` with `current`, `latest`, `available`, and `verified` fields.
- Existing `bx update` downloads only the selected verified asset and preserves the active service.

- [ ] **Step 1: Write failing CLI tests**

```go
func TestUpdateCheckJSONRequiresVerifiedManifest(t *testing.T) { /* invalid signature returns no update */ }
func TestUpdateDoesNotRestartProtection(t *testing.T) { /* retain existing source guard */ }
```

- [ ] **Step 2: Run targeted tests and confirm the new JSON path fails**

Run: `go test ./internal/cli -run 'TestUpdate(CheckJSONRequiresVerifiedManifest|DoesNotRestartProtection)' -count=1`

- [ ] **Step 3: Replace redirect/tag trust with manifest trust**

Fetch `bx-update.json` and `bx-update.json.sig` from the release channel,
verify through `internal/update`, select `runtime.GOOS + "/" + runtime.GOARCH`,
then verify the downloaded asset digest and size before `install.ReplaceBinary`.
Keep the current message that a live service remains running and the binary is
staged for the next safe start.

- [ ] **Step 4: Run targeted tests and commit**

Run: `go test ./internal/cli -run TestUpdate -count=1`

Commit: `git commit -m "feat(update): use verified release manifests"`

### Task 3: Complete macOS Release Assets

**Files:**
- Modify: `.github/workflows/release.yml`
- Modify: `scripts/package-macos-release.sh`
- Modify: `scripts/verify-macos-release.sh`
- Create: `scripts/sign-update-manifest.go`

**Interfaces:**
- Produces `bx-macos-arm64.tar.gz` and `bx-macos-amd64.tar.gz`, each containing `bx`, `Bx.app`, and installer scripts.
- Produces `bx-update.json` and detached `bx-update.json.sig` using `BX_UPDATE_PRIVATE_KEY` supplied only by release automation.

- [ ] **Step 1: Add failing package verification assertions**

Assert each archive contains `Bx.app/Contents/MacOS/BxMenu`, an architecture-matched
CLI, and that manifest platform entries match the archive hashes.

- [ ] **Step 2: Run local package verification and confirm it fails before manifest support**

Run: `scripts/package-macos-release.sh && scripts/verify-macos-release.sh`

- [ ] **Step 3: Add separate macOS runner matrix and signing step**

Use `macos-13` for amd64 and `macos-14` for arm64. Fail release publication
when the signing key is absent; do not publish an unsigned manifest. Upload
both packages, manifest, signature, and SHA256SUMS in the same release.

- [ ] **Step 4: Run local packaging verification and commit**

Run: `scripts/package-macos-release.sh && scripts/verify-macos-release.sh`

Commit: `git commit -m "build: publish verified macos update packages"`

### Task 4: Menu Update Discovery and Staged Installation

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift`
- Modify: `apps/macos/BxMenu/Tests/StatusPresentationTests.swift`
- Modify: `apps/macos/BxMenu/README.md`

**Interfaces:**
- Menu polls `bx update --check --json` at launch and every 24 hours.
- Menu shows `Update Available` and an explicit `Update bx…` action.
- Installation runs the verified CLI updater with administrator consent, restarts only the menu LaunchAgent, and reports `Updated. Protection stayed on.`

- [ ] **Step 1: Write a failing presentation test for update available**

```swift
func testUpdateAvailableUsesUpdateActionAndDoesNotOfferReconnect() { /* snapshot/menu model */ }
```

- [ ] **Step 2: Run Swift tests and confirm the new state is absent**

Run: `swift test --package-path apps/macos/BxMenu`

- [ ] **Step 3: Implement explicit update state and action**

Parse only the JSON CLI contract. Do not duplicate network download or signature
verification in Swift. The action invokes the updater, archives its sanitized
result under `~/Library/Logs/bx`, then relaunches the menu agent. It must not
invoke `up`, `down`, or `reconnect`.

- [ ] **Step 4: Build, test, and commit**

Run: `swift build --package-path apps/macos/BxMenu -c release && go test ./...`

Commit: `git commit -m "feat(macos): offer verified staged updates"`

### Task 5: Documentation and Failure Matrix

**Files:**
- Modify: `README.md`
- Modify: `docs/agent-tools.md`
- Modify: `docs/leak-surfaces.md`

- [ ] **Step 1: Document user-visible states**

Document no update, update available, update downloaded, verification failure,
and staged-runtime result. State clearly that update does not replace a running
Core or claim zero-leak runtime handover before Network Extension.

- [ ] **Step 2: Add agent contract**

Document update checking as read-only and installing as explicit user-authorized
maintenance, never an autonomous agent action.

- [ ] **Step 3: Verify and commit**

Run: `go test ./... && git diff --check`

Commit: `git commit -m "docs: explain safe staged updates"`
