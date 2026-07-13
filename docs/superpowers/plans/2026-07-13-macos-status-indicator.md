# macOS Status Indicator Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Show a static green, yellow, red, or gray dot next to the bx menu-bar shield without changing bx networking behavior.

**Architecture:** Keep state-to-color policy in a small pure Swift type so it can be tested without AppKit or the executable entry point. `BxMenuApp` classifies its existing `BxState`, then uses that policy to render an attributed dot beside the existing template shield image. The existing five-second read-only refresh remains the only status source.

**Tech Stack:** Swift 5, AppKit, Swift Package Manager, zero-dependency `swiftc` contract tests.

## Global Constraints

- Keep the shield as a template SF Symbol and render one static dot immediately to its right.
- Do not add animation, notifications, network probes, privileged commands, DNS changes, route changes, or protection start/stop behavior.
- `connected` is green; `warning` and `updateNeeded` are yellow; `off`, `setupNeeded`, and `missing` are gray.
- Red is reserved for a future explicit failed-start or fail-closed status. Do not infer it from ordinary warning text.
- Tooltip and menu header remain the accessible textual source of status.

---

### Task 1: Define and test dot policy

**Files:**
- Create: `apps/macos/BxMenu/Sources/BxMenu/StatusIndicator.swift`
- Create: `apps/macos/BxMenu/Tests/StatusIndicatorTests.swift`

**Interfaces:**
- Produces: `enum StatusIndicator: Equatable { case green, yellow, red, gray }`
- Produces: `enum StatusIndicatorState { case connected, warning, updateNeeded, off, setupNeeded, missing, failed }`
- Produces: `func statusIndicator(for state: StatusIndicatorState) -> StatusIndicator`

- [ ] **Step 1: Write the failing test**

```swift
expect(statusIndicator(for: .off) == .gray, "off is gray")
expect(statusIndicator(for: .setupNeeded) == .gray, "setup is gray")
expect(statusIndicator(for: .missing) == .gray, "missing is gray")
expect(statusIndicator(for: .warning) == .yellow, "warning is yellow")
expect(statusIndicator(for: .updateNeeded) == .yellow, "update is yellow")
```

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
swiftc apps/macos/BxMenu/Sources/BxMenu/StatusIndicator.swift apps/macos/BxMenu/Tests/StatusIndicatorTests.swift -o /tmp/bx-status-indicator-tests
```

Expected: failure because the policy and test source do not yet exist.

- [ ] **Step 3: Implement the minimal policy**

```swift
enum StatusIndicator: Equatable {
    case green
    case yellow
    case red
    case gray
}

enum StatusIndicatorState {
    case connected
    case warning
    case updateNeeded
    case off
    case setupNeeded
    case missing
    case failed
}

func statusIndicator(for state: StatusIndicatorState) -> StatusIndicator {
    switch state {
    case .connected: return .green
    case .warning, .updateNeeded: return .yellow
    case .off, .setupNeeded, .missing: return .gray
    case .failed: return .red
    }
}
```

- [ ] **Step 4: Add connected and future red coverage, then verify the test passes**

Assert:

```swift
expect(statusIndicator(for: .connected) == .green, "connected is green")
expect(statusIndicator(for: .failed) == .red, "explicit failure is red")
```

Run:

```bash
swiftc apps/macos/BxMenu/Sources/BxMenu/StatusIndicator.swift apps/macos/BxMenu/Tests/StatusIndicatorTests.swift -o /tmp/bx-status-indicator-tests && /tmp/bx-status-indicator-tests
```

Expected: exit status 0 and no output.

- [ ] **Step 5: Commit the policy**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/StatusIndicator.swift apps/macos/BxMenu/Tests/StatusIndicatorTests.swift
git commit -m "feat(macos): add menu status indicator policy"
```

### Task 2: Render the dot next to the shield

**Files:**
- Modify: `apps/macos/BxMenu/Sources/BxMenu/main.swift:50-120`
- Modify: `apps/macos/BxMenu/Sources/BxMenu/StatusIndicator.swift`

**Interfaces:**
- Consumes: `func statusIndicator(for state: StatusIndicatorState) -> StatusIndicator`.
- Produces: `func statusDotTitle(for indicator: StatusIndicator) -> NSAttributedString`.

- [ ] **Step 1: Render shield and dot with a variable-width item**

Replace the square-length item with:

```swift
private let statusItem = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
```

Add a `statusIndicatorState()` method in `BxMenuApp` that maps the existing state cases to the pure categories. At the end of `updateIcon()`, set:

```swift
button.attributedTitle = statusDotTitle(for: statusIndicator(for: statusIndicatorState()))
```

Use this AppKit-only helper:

```swift
private func statusDotTitle(for indicator: StatusIndicator) -> NSAttributedString {
    let color: NSColor
    switch indicator {
    case .green: color = .systemGreen
    case .yellow: color = .systemYellow
    case .red: color = .systemRed
    case .gray: color = .secondaryLabelColor
    }
    return NSAttributedString(string: "●", attributes: [
        .font: NSFont.systemFont(ofSize: 10, weight: .semibold),
        .foregroundColor: color,
    ])
}
```

- [ ] **Step 2: Build and package the app**

Run:

```bash
swift build --package-path apps/macos/BxMenu -c release
scripts/package-macos-release.sh
scripts/verify-macos-release.sh
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 3: Install and visually inspect without changing bx networking**

Run:

```bash
scripts/install-macos-menu.sh install
screencapture -x /tmp/bx-menu-status-indicator.png
```

Expected: Bx.app runs with a white shield and stable dot to its right. These commands do not invoke `bx up`, `bx down`, DNS, or route operations.

- [ ] **Step 4: Commit the presentation**

```bash
git add apps/macos/BxMenu/Sources/BxMenu/main.swift apps/macos/BxMenu/Sources/BxMenu/StatusIndicator.swift
git commit -m "feat(macos): show protection state dot"
```

### Task 3: Document indicator meaning

**Files:**
- Modify: `apps/macos/BxMenu/README.md:3-10`
- Modify: `README.md:141-151`

- [ ] **Step 1: Document the four static states**

Add one sentence to both macOS menu descriptions: green means protected, yellow means bx is safely recovering or needs attention, red means protection is unavailable, and gray means it is off or not configured.

- [ ] **Step 2: Verify and commit documentation**

Run:

```bash
git diff --check
scripts/verify-macos-release.sh
git add README.md apps/macos/BxMenu/README.md
git commit -m "docs: explain macos protection indicator"
```

Expected: checks succeed and the commit contains only the two documentation files.
