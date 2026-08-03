#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
MENU="$ROOT/apps/macos/BxMenu"
TMP="$(mktemp -d "${TMPDIR:-/tmp}/bx-menu-tests.XXXXXX")"
trap 'rm -rf "$TMP"' EXIT

run_test() {
  local name="$1"
  shift
  swiftc "$@" -o "$TMP/$name"
  "$TMP/$name"
}

run_test status-indicator \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Tests/StatusIndicatorTests.swift"
run_test status-presentation \
  "$MENU/Sources/BxMenu/StatusPresentation.swift" \
  "$MENU/Tests/StatusPresentationTests.swift"
run_test update-presentation \
  "$MENU/Sources/BxMenu/UpdatePresentation.swift" \
  "$MENU/Tests/UpdatePresentationTests.swift"
run_test recovery-presentation \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Tests/RecoveryPresentationTests.swift"
run_test guardian-client \
  "$MENU/Sources/BxMenu/StatusIndicator.swift" \
  "$MENU/Sources/BxMenu/RecoveryPresentation.swift" \
  "$MENU/Sources/BxMenu/GuardianClient.swift" \
  "$MENU/Tests/GuardianClientTests.swift"
run_test instance-gate \
  "$MENU/Sources/BxMenu/InstanceGate.swift" \
  "$MENU/Tests/InstanceGateTests.swift"

echo "macOS menu tests passed"
