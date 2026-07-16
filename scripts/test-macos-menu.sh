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

echo "macOS menu tests passed"
