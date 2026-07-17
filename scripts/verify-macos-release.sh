#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${BX_ARCH:-arm64}"
RELEASE_NAME="bx-macos-$ARCH"
DIST_ROOT="${BX_RELEASE_DIR:-$ROOT/dist/release}"
RELEASE_DIR="$DIST_ROOT/$RELEASE_NAME"
ARCHIVE="$DIST_ROOT/$RELEASE_NAME.tar.gz"
SUMS="$DIST_ROOT/SHA256SUMS"

fail() {
  echo "verify failed: $*" >&2
  exit 1
}

[[ -x "$RELEASE_DIR/bx" ]] || fail "missing executable bx"
[[ -d "$RELEASE_DIR/Bx.app" ]] || fail "missing Bx.app"
[[ -x "$RELEASE_DIR/install.sh" ]] || fail "missing executable install.sh"
[[ -x "$RELEASE_DIR/uninstall.sh" ]] || fail "missing executable uninstall.sh"
[[ -f "$RELEASE_DIR/README.txt" ]] || fail "missing README.txt"
[[ -f "$ARCHIVE" ]] || fail "missing archive $ARCHIVE"
[[ -f "$SUMS" ]] || fail "missing SHA256SUMS"

case "$ARCH" in
  arm64) MACH_ARCH="arm64" ;;
  amd64) MACH_ARCH="x86_64" ;;
  *) fail "unsupported architecture $ARCH" ;;
esac
file "$RELEASE_DIR/bx" | grep -q "Mach-O.*$MACH_ARCH" || fail "bx is not $MACH_ARCH"
file "$RELEASE_DIR/Bx.app/Contents/MacOS/BxMenu" | grep -q "Mach-O.*$MACH_ARCH" || fail "BxMenu is not $MACH_ARCH"

plutil -lint "$RELEASE_DIR/Bx.app/Contents/Info.plist" >/dev/null
plutil -extract NSAppleEventsUsageDescription raw "$RELEASE_DIR/Bx.app/Contents/Info.plist" >/dev/null || fail "Info.plist missing Apple Events usage description"
tar -tzf "$ARCHIVE" >/dev/null

grep -q "does not run bx setup" "$RELEASE_DIR/README.txt" || fail "README missing no-setup note"
grep -q "does not run bx up" "$RELEASE_DIR/README.txt" || fail "README missing no-up note"
grep -q "does not change DNS/routes" "$RELEASE_DIR/README.txt" || fail "README missing network safety note"
grep -q "preserves /etc/bx/config.yaml" "$RELEASE_DIR/README.txt" || fail "README missing upgrade config preservation note"
grep -q "Reconnect only replaces the transport safely" "$RELEASE_DIR/README.txt" || fail "README missing safe reconnect upgrade note"
grep -q "menu bar app is installed and running" "$RELEASE_DIR/README.txt" || fail "README missing running menu note"
grep -q "Set Up bx" "$RELEASE_DIR/README.txt" || fail "README missing menu setup note"
grep -q "menu bar app is installed and running" "$RELEASE_DIR/install.sh" || fail "install.sh missing running menu note"
grep -q "The installer did not start bx or change DNS/routes." "$RELEASE_DIR/install.sh" || fail "install.sh missing safety note"
grep -q "Existing client config will be preserved" "$RELEASE_DIR/install.sh" || fail "install.sh missing upgrade config preservation note"
grep -q "Reconnect only replaces the transport safely" "$RELEASE_DIR/install.sh" || fail "install.sh missing safe reconnect upgrade note"
grep -q "Set Up bx" "$RELEASE_DIR/install.sh" || fail "install.sh missing menu setup note"
grep -q "package architecture" "$RELEASE_DIR/install.sh" || fail "install.sh missing architecture preflight"
grep -q "missing bx executable" "$RELEASE_DIR/install.sh" || fail "install.sh missing bx preflight"
grep -q "missing Bx.app" "$RELEASE_DIR/install.sh" || fail "install.sh missing app preflight"
grep -q "normal macOS user" "$RELEASE_DIR/install.sh" || fail "install.sh missing non-root install guard"
grep -q "not with sudo" "$RELEASE_DIR/README.txt" || fail "README missing non-root install note"
grep -q "Library/Logs/bx" "$RELEASE_DIR/install.sh" || fail "install.sh missing menu log directory"
grep -q 'LOG_DIR/menu.log' "$RELEASE_DIR/install.sh" || fail "install.sh missing menu stdout log path"
grep -q 'LOG_DIR/menu.err.log' "$RELEASE_DIR/install.sh" || fail "install.sh missing menu stderr log path"
grep -q 'LEGACY_AGENT_ID="com.ggshr9.bx.menu"' "$RELEASE_DIR/install.sh" || fail "install.sh missing legacy menu migration"
grep -q 'rm -f "$LEGACY_AGENT_DST"' "$RELEASE_DIR/install.sh" || fail "install.sh missing legacy menu cleanup"
grep -q "Library/Logs/bx/menu.log" "$RELEASE_DIR/README.txt" || fail "README missing menu log path"
grep -q "does not turn off protection" "$RELEASE_DIR/README.txt" || fail "README missing uninstall protection safety note"
grep -q "did not turn off protection" "$RELEASE_DIR/uninstall.sh" || fail "uninstall.sh missing protection safety note"
grep -q "normal macOS user" "$RELEASE_DIR/uninstall.sh" || fail "uninstall.sh missing non-root uninstall guard"
grep -q "Run uninstall.sh as your normal macOS user" "$RELEASE_DIR/README.txt" || fail "README missing non-root uninstall note"

(
  cd "$DIST_ROOT"
  shasum -a 256 -c SHA256SUMS >/dev/null
)

echo "macOS release verified: $RELEASE_DIR"
