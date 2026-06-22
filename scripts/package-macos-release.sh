#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${BX_ARCH:-arm64}"
VERSION="${BX_VERSION:-dev}"
RELEASE_NAME="bx-macos-$ARCH"
DIST_ROOT="${BX_RELEASE_DIR:-$ROOT/dist/release}"
RELEASE_DIR="$DIST_ROOT/$RELEASE_NAME"

if [[ "$(uname -s)" != "Darwin" ]]; then
  echo "macOS release packaging requires macOS." >&2
  exit 1
fi

case "$ARCH" in
  arm64|amd64) ;;
  *)
    echo "Unsupported BX_ARCH=$ARCH; use arm64 or amd64." >&2
    exit 2
    ;;
esac

rm -rf "$RELEASE_DIR"
mkdir -p "$RELEASE_DIR"

echo "Building bx for darwin/$ARCH..."
GOOS=darwin GOARCH="$ARCH" go build -trimpath -o "$RELEASE_DIR/bx" "$ROOT"

echo "Packaging menu bar app..."
BX_DIST_DIR="$ROOT/dist/macos-$ARCH" "$ROOT/scripts/package-macos-menu.sh" >/dev/null
ditto "$ROOT/dist/macos-$ARCH/Bx.app" "$RELEASE_DIR/Bx.app"

cat > "$RELEASE_DIR/install.sh" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
RELEASE_ARCH="__BX_RELEASE_ARCH__"
BX_DST="${BX_DST:-/usr/local/bin/bx}"
APP_DST="${BX_APP_DST:-$HOME/Applications/Bx.app}"
AGENT_ID="com.getbx.bx.menu"
AGENT_DIR="$HOME/Library/LaunchAgents"
AGENT_DST="$AGENT_DIR/$AGENT_ID.plist"
DOMAIN="gui/$(id -u)"
LOG_DIR="${BX_LOG_DIR:-$HOME/Library/Logs/bx}"

fail() {
  echo "install failed: $*" >&2
  exit 1
}

preflight() {
  [[ "$(uname -s)" == "Darwin" ]] || fail "this package is for macOS"
  local machine_arch
  machine_arch="$(uname -m)"
  case "$RELEASE_ARCH:$machine_arch" in
    arm64:arm64|amd64:x86_64) ;;
    *) fail "package architecture $RELEASE_ARCH does not match this Mac ($machine_arch)" ;;
  esac
  [[ -x "$DIR/bx" ]] || fail "missing bx executable"
  [[ -x "$DIR/Bx.app/Contents/MacOS/BxMenu" ]] || fail "missing Bx.app"
  command -v launchctl >/dev/null || fail "missing launchctl"
  command -v ditto >/dev/null || fail "missing ditto"
}

preflight

echo "Installing bx CLI to $BX_DST..."
sudo install -m 0755 "$DIR/bx" "$BX_DST"

echo "Installing bx menu bar app to $APP_DST..."
mkdir -p "$(dirname "$APP_DST")"
ditto "$DIR/Bx.app" "$APP_DST"
mkdir -p "$AGENT_DIR" "$LOG_DIR"
cat > "$AGENT_DST" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>$AGENT_ID</string>
  <key>ProgramArguments</key>
  <array>
    <string>$APP_DST/Contents/MacOS/BxMenu</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardOutPath</key>
  <string>$LOG_DIR/menu.log</string>
  <key>StandardErrorPath</key>
  <string>$LOG_DIR/menu.err.log</string>
</dict>
</plist>
PLIST

launchctl bootout "$DOMAIN" "$AGENT_DST" >/dev/null 2>&1 || true
launchctl bootstrap "$DOMAIN" "$AGENT_DST"
launchctl kickstart -k "$DOMAIN/$AGENT_ID"

cat <<MSG
bx installed.

Next:
  Open the bx menu bar icon and choose Set Up bx...

CLI fallback:
  sudo bx setup '<client-link>' && sudo bx up

The installer did not start bx or change DNS/routes.
MSG
SCRIPT

perl -0pi -e "s/__BX_RELEASE_ARCH__/$ARCH/g" "$RELEASE_DIR/install.sh"

cat > "$RELEASE_DIR/uninstall.sh" <<'SCRIPT'
#!/usr/bin/env bash
set -euo pipefail

BX_DST="${BX_DST:-/usr/local/bin/bx}"
APP_DST="${BX_APP_DST:-$HOME/Applications/Bx.app}"
AGENT_ID="com.getbx.bx.menu"
AGENT_DST="$HOME/Library/LaunchAgents/$AGENT_ID.plist"
DOMAIN="gui/$(id -u)"

launchctl bootout "$DOMAIN" "$AGENT_DST" >/dev/null 2>&1 || true
rm -f "$AGENT_DST"
rm -rf "$APP_DST"

echo "Removed bx menu bar app."
echo "CLI remains at $BX_DST. Remove it manually if desired:"
echo "  sudo rm -f '$BX_DST'"
echo
echo "This did not turn off protection or change DNS/routes."
SCRIPT

cat > "$RELEASE_DIR/README.txt" <<TXT
bx macOS $ARCH release ($VERSION)

Install:
  ./install.sh

After install:
  Open the bx menu bar icon and choose Set Up bx...

CLI fallback:
  sudo bx setup '<client-link>' && sudo bx up

Menu bar app:
  Installed to ~/Applications/Bx.app
  Login item: ~/Library/LaunchAgents/com.getbx.bx.menu.plist
  Logs: ~/Library/Logs/bx/menu.log and menu.err.log

Remove menu bar app:
  ./uninstall.sh

Notes:
  install.sh installs the bx CLI and menu bar app.
  install.sh does not run bx setup, does not run bx up, and does not change DNS/routes.
TXT

chmod +x "$RELEASE_DIR/install.sh" "$RELEASE_DIR/uninstall.sh" "$RELEASE_DIR/bx"

(
  cd "$DIST_ROOT"
  rm -f "$RELEASE_NAME.tar.gz"
  tar -czf "$RELEASE_NAME.tar.gz" "$RELEASE_NAME"
  shasum -a 256 "$RELEASE_NAME.tar.gz" > SHA256SUMS
)

echo "Built: $RELEASE_DIR"
echo "Archive: $DIST_ROOT/$RELEASE_NAME.tar.gz"
echo "Checksums: $DIST_ROOT/SHA256SUMS"
